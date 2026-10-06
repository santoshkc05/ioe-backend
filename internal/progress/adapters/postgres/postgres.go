// Package postgres implements progress persistence on the progress schema.
package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/santoshkc2200/ioe-backend/internal/progress/adapters/postgres/sqlcgen"
	"github.com/santoshkc2200/ioe-backend/internal/progress/app"
)

// TxRunner runs progress use cases in one PostgreSQL transaction.
type TxRunner struct{ pool *pgxpool.Pool }

func NewTxRunner(pool *pgxpool.Pool) *TxRunner { return &TxRunner{pool: pool} }

func (r *TxRunner) RunInTx(ctx context.Context, fn func(app.Repository) error) error {
	return pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		return fn(progress{q: sqlcgen.New(tx)})
	})
}
