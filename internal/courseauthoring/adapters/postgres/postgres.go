// Package postgres implements course authoring persistence on the courseauthoring schema.
package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/adapters/postgres/sqlcgen"
	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/app"
	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/clock"
	"github.com/santoshkc2200/ioe-backend/internal/platform/outbox"
)

// TxRunner runs course authoring use cases in one PostgreSQL transaction.
type TxRunner struct {
	pool  *pgxpool.Pool
	clock clock.Clock
}

func NewTxRunner(pool *pgxpool.Pool, c clock.Clock) *TxRunner {
	return &TxRunner{pool: pool, clock: c}
}

func (r *TxRunner) RunInTx(ctx context.Context, fn func(app.Repos) error) error {
	return pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		q := sqlcgen.New(tx)
		return fn(app.Repos{
			Courses:    courses{q: q},
			Contents:   contents{q: q, clock: r.clock},
			Events:     events{tx: tx},
			Categories: categories{q: q},
		})
	})
}

type events struct{ tx pgx.Tx }

// Publish writes each event to the outbox in the current transaction, topic = event name.
func (e events) Publish(ctx context.Context, evs ...domain.Event) error {
	for _, ev := range evs {
		payload, err := json.Marshal(ev)
		if err != nil {
			return err
		}
		msg := message.NewMessage(uuid.NewString(), payload)
		msg.Metadata.Set("event_name", ev.EventName())
		if err := outbox.Publish(ctx, e.tx, ev.EventName(), msg); err != nil {
			return err
		}
	}
	return nil
}

const (
	uniqueViolation     = "23505"
	foreignKeyViolation = "23503"
)

// pgCode returns the PostgreSQL error code of err, or "" when err is not a PostgreSQL error.
func pgCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}
