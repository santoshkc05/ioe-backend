// Package outbox implements the transactional outbox: messages are written in the caller's
// PostgreSQL transaction and delivered to in-process handlers after commit.
package outbox

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/ThreeDotsLabs/watermill"
	wsql "github.com/ThreeDotsLabs/watermill-sql/v4/pkg/sql"
	"github.com/ThreeDotsLabs/watermill/components/forwarder"
	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/ThreeDotsLabs/watermill/message/router/middleware"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

const (
	forwarderTopic = "outbox"
	consumerGroup  = "forwarder"
	// resendInterval is the pause before a nacked message starts a new retry cycle.
	resendInterval = 5 * time.Second
)

func schemaAdapter() wsql.DefaultPostgreSQLSchema {
	return wsql.DefaultPostgreSQLSchema{
		GenerateMessagesTableName: func(string) string { return "platform.outbox_messages" },
	}
}

func offsetsAdapter() wsql.DefaultPostgreSQLOffsetsAdapter {
	return wsql.DefaultPostgreSQLOffsetsAdapter{
		GenerateMessagesOffsetsTableName: func(string) string { return "platform.outbox_offsets" },
	}
}

// Publish stores msgs for topic inside tx. They are delivered only if tx commits.
// The trace context of ctx is stored in each message's metadata.
func Publish(ctx context.Context, tx pgx.Tx, topic string, msgs ...*message.Message) error {
	pub, err := wsql.NewPublisher(wsql.TxFromPgx(tx), wsql.PublisherConfig{SchemaAdapter: schemaAdapter()}, watermill.NopLogger{})
	if err != nil {
		return err
	}
	prop := otel.GetTextMapPropagator()
	for _, m := range msgs {
		m.SetContext(ctx)
		prop.Inject(ctx, propagation.MapCarrier(m.Metadata))
	}
	return forwarder.NewPublisher(pub, forwarder.PublisherConfig{ForwarderTopic: forwarderTopic}).Publish(topic, msgs...)
}

// Handler processes one forwarded message. Returning an error makes the forwarder retry the
// message with backoff; returning nil acknowledges it. Delivery is at least once, and a
// failure in one handler of a topic re-runs every handler of that topic, so handlers must be
// idempotent. The message carries a background context: handlers must bound their own work.
type Handler func(*message.Message) error

// dispatcher is the forwarder's output. It calls handlers synchronously, so the forwarder
// acknowledges a SQL message only after every handler for its topic has succeeded.
type dispatcher struct{ handlers map[string][]Handler }

func (d *dispatcher) Publish(topic string, msgs ...*message.Message) error {
	for _, m := range msgs {
		for _, h := range d.handlers[topic] {
			if err := h(m); err != nil {
				return err
			}
		}
	}
	return nil
}

func (*dispatcher) Close() error { return nil }

// Forwarder moves committed outbox messages to the registered handlers.
type Forwarder struct {
	fw *forwarder.Forwarder
	d  *dispatcher
}

func NewForwarder(pool *pgxpool.Pool, logger *slog.Logger) (*Forwarder, error) {
	wl := watermill.NewSlogLogger(logger)
	sub, err := wsql.NewSubscriber(wsql.BeginnerFromPgx(pool), wsql.SubscriberConfig{
		ConsumerGroup:  consumerGroup,
		SchemaAdapter:  schemaAdapter(),
		OffsetsAdapter: offsetsAdapter(),
		ResendInterval: resendInterval,
	}, wl)
	if err != nil {
		return nil, err
	}
	d := &dispatcher{handlers: map[string][]Handler{}}
	fw, err := forwarder.NewForwarder(sub, d, wl, forwarder.Config{
		ForwarderTopic: forwarderTopic,
		// Retry wraps Recoverer so that a handler panic becomes an error that is retried.
		Middlewares: []message.HandlerMiddleware{
			middleware.Retry{
				MaxRetries:      5,
				InitialInterval: time.Second,
				MaxInterval:     time.Minute,
				Multiplier:      2,
				Logger:          wl,
			}.Middleware,
			middleware.Recoverer,
		},
	})
	if err != nil {
		return nil, errors.Join(err, sub.Close())
	}
	return &Forwarder{fw: fw, d: d}, nil
}

// Handle registers h for messages published to topic. Call it before Run.
func (f *Forwarder) Handle(topic string, h Handler) {
	f.d.handlers[topic] = append(f.d.handlers[topic], h)
}

// Run forwards messages until ctx is cancelled or Close is called.
func (f *Forwarder) Run(ctx context.Context) error { return f.fw.Run(ctx) }

func (f *Forwarder) Close() error { return f.fw.Close() }
