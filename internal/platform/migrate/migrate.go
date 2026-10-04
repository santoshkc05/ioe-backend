// Package migrate applies the embedded goose migrations.
package migrate

import (
	"context"
	"database/sql"
	"fmt"
	"io"

	"github.com/pressly/goose/v3"

	"github.com/santoshkc2200/ioe-backend/migrations"
)

// NewProvider returns a goose provider over the embedded migrations.
func NewProvider(db *sql.DB) (*goose.Provider, error) {
	return goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
}

// Run executes "up", "down" (one step), or "status", writing results to w.
func Run(ctx context.Context, db *sql.DB, command string, w io.Writer) error {
	p, err := NewProvider(db)
	if err != nil {
		return err
	}
	switch command {
	case "up":
		results, err := p.Up(ctx)
		for _, r := range results {
			fmt.Fprintln(w, r)
		}
		return err
	case "down":
		r, err := p.Down(ctx)
		if r != nil {
			fmt.Fprintln(w, r)
		}
		return err
	case "status":
		statuses, err := p.Status(ctx)
		if err != nil {
			return err
		}
		for _, s := range statuses {
			fmt.Fprintf(w, "%-10s %s\n", s.State, s.Source.Path)
		}
		return nil
	default:
		return fmt.Errorf("unknown migrate command %q (want up, down, or status)", command)
	}
}
