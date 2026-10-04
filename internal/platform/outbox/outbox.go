// Package outbox implements the transactional outbox: messages are written in the caller's
// PostgreSQL transaction and forwarded to in-process subscribers after commit.
package outbox

import (
	"context"
	"errors"
	"log/slog"

	"github.com/ThreeDotsLabs/watermill"
	wsql "github.com/ThreeDotsLabs/watermill-sql/v4/pkg/sql"
	"github.com/ThreeDotsLabs/watermill/components/forwarder"
	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/ThreeDotsLabs/watermill/pubsub/gochannel"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

const (
	forwarderTopic = "outbox"
	consumerGroup  = "forwarder"
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

// Forwarder moves committed outbox messages to an in-process pub/sub.
type Forwarder struct {
	fw  *forwarder.Forwarder
	out *gochannel.GoChannel
}

func NewForwarder(pool *pgxpool.Pool, logger *slog.Logger) (*Forwarder, error) {
	wl := watermill.NewSlogLogger(logger)
	sub, err := wsql.NewSubscriber(wsql.BeginnerFromPgx(pool), wsql.SubscriberConfig{
		ConsumerGroup:  consumerGroup,
		SchemaAdapter:  schemaAdapter(),
		OffsetsAdapter: offsetsAdapter(),
	}, wl)
	if err != nil {
		return nil, err
	}
	out := gochannel.NewGoChannel(gochannel.Config{}, wl)
	fw, err := forwarder.NewForwarder(sub, out, wl, forwarder.Config{ForwarderTopic: forwarderTopic})
	if err != nil {
		return nil, errors.Join(err, out.Close())
	}
	return &Forwarder{fw: fw, out: out}, nil
}

// Run forwards messages until ctx is cancelled or Close is called.
func (f *Forwarder) Run(ctx context.Context) error { return f.fw.Run(ctx) }

func (f *Forwarder) Close() error { return errors.Join(f.fw.Close(), f.out.Close()) }

// Subscriber exposes forwarded messages to in-process consumers.
func (f *Forwarder) Subscriber() message.Subscriber { return f.out }
