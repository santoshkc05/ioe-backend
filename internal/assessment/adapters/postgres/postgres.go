// Package postgres implements assessment persistence on the assessment schema.
package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/santoshkc2200/ioe-backend/internal/assessment/adapters/postgres/sqlcgen"
	"github.com/santoshkc2200/ioe-backend/internal/assessment/app"
)

// TxRunner runs assessment use cases in one PostgreSQL transaction.
type TxRunner struct{ pool *pgxpool.Pool }

func NewTxRunner(pool *pgxpool.Pool) *TxRunner { return &TxRunner{pool: pool} }

func (r *TxRunner) RunInTx(ctx context.Context, fn func(app.Repos) error) error {
	return pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		return fn(app.Repos{Quizzes: quizzes{q: sqlcgen.New(tx)}})
	})
}
