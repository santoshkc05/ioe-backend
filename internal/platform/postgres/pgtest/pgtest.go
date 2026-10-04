//go:build integration

// Package pgtest starts a disposable, fully migrated PostgreSQL for integration tests.
package pgtest

import (
	"context"
	"io"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/santoshkc2200/ioe-backend/internal/platform/migrate"
)

// New starts PostgreSQL 17, applies all migrations, and returns a pool closed at test end.
func New(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	ctr, err := tcpostgres.Run(ctx, "postgres:17-alpine",
		tcpostgres.WithDatabase("ioe"),
		tcpostgres.WithUsername("ioe"),
		tcpostgres.WithPassword("ioe"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(ctr) })

	url, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	db := stdlib.OpenDBFromPool(pool)
	t.Cleanup(func() { _ = db.Close() })
	if err := migrate.Run(ctx, db, "up", io.Discard); err != nil {
		t.Fatalf("migrate up: %v", err)
	}
	return pool
}
