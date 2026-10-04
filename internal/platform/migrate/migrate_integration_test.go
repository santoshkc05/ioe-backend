//go:build integration

package migrate_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/santoshkc2200/ioe-backend/internal/platform/migrate"
	"github.com/santoshkc2200/ioe-backend/internal/platform/postgres/pgtest"
)

func tableExists(t *testing.T, pool *pgxpool.Pool, name string) bool {
	t.Helper()
	var exists bool
	if err := pool.QueryRow(context.Background(), "SELECT to_regclass($1) IS NOT NULL", name).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	return exists
}

func TestMigrationsUpDownUp(t *testing.T) {
	ctx := context.Background()
	pool := pgtest.New(t)
	for _, tbl := range []string{"platform.outbox_messages", "platform.outbox_offsets", "identity.users", "identity.refresh_tokens"} {
		if !tableExists(t, pool, tbl) {
			t.Fatalf("%s missing after up", tbl)
		}
	}

	p, err := migrate.NewProvider(stdlib.OpenDBFromPool(pool))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.DownTo(ctx, 0); err != nil {
		t.Fatalf("down to 0: %v", err)
	}
	if tableExists(t, pool, "identity.users") || tableExists(t, pool, "platform.outbox_messages") {
		t.Fatal("tables remain after full down")
	}
	if _, err := p.Up(ctx); err != nil {
		t.Fatalf("up again: %v", err)
	}
	if !tableExists(t, pool, "identity.refresh_tokens") {
		t.Fatal("identity.refresh_tokens missing after re-up")
	}
}
