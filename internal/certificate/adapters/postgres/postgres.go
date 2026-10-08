// Package postgres implements certificate persistence on the certificate schema.
package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/santoshkc2200/ioe-backend/internal/certificate/adapters/postgres/sqlcgen"
	"github.com/santoshkc2200/ioe-backend/internal/certificate/app"
)

// TxRunner runs certificate use cases in one PostgreSQL transaction.
type TxRunner struct{ pool *pgxpool.Pool }

func NewTxRunner(pool *pgxpool.Pool) *TxRunner { return &TxRunner{pool: pool} }

func (r *TxRunner) RunInTx(ctx context.Context, fn func(app.Repository) error) error {
	return pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		return fn(repo{q: sqlcgen.New(tx)})
	})
}
