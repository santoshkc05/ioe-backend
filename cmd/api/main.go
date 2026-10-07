// Command api runs the IOE backend HTTP server and its database migrations.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"golang.org/x/sync/errgroup"

	"github.com/santoshkc2200/ioe-backend/internal/platform/config"
	"github.com/santoshkc2200/ioe-backend/internal/platform/httpserver"
	"github.com/santoshkc2200/ioe-backend/internal/platform/logging"
	"github.com/santoshkc2200/ioe-backend/internal/platform/migrate"
	"github.com/santoshkc2200/ioe-backend/internal/platform/postgres"
	"github.com/santoshkc2200/ioe-backend/internal/platform/telemetry"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout io.Writer) error {
	switch {
	case len(args) == 0:
		return serve(ctx)
	case args[0] == "migrate":
		return runMigrate(ctx, args[1:], stdout)
	default:
		return fmt.Errorf("unknown command %q (usage: api [migrate up|down|status])", args[0])
	}
}

func runMigrate(ctx context.Context, args []string, w io.Writer) error {
	if len(args) != 1 {
		return errors.New("usage: api migrate up|down|status")
	}
	url, err := config.LoadDatabaseURL()
	if err != nil {
		return err
	}
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return err
	}
	defer pool.Close()
	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()
	return migrate.Run(ctx, db, args[0], w)
}

func serve(parent context.Context) error {
	ctx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	level, _ := cfg.SlogLevel()

	tel, err := telemetry.Setup(ctx, version)
	if err != nil {
		return fmt.Errorf("telemetry: %w", err)
	}
	defer func() {
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = tel.Shutdown(sctx)
	}()
	logger := logging.New(level, os.Stdout, tel.LogHandler)
	slog.SetDefault(logger)

	pool, err := postgres.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	defer pool.Close()

	a, err := buildApp(ctx, cfg, logger, pool)
	if err != nil {
		return err
	}
	defer a.forwarder.Close()

	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.HTTPAddr)
	if err != nil {
		return err
	}
	logger.InfoContext(ctx, "server started", "addr", ln.Addr().String(), "version", version)

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return httpserver.Serve(gctx, ln, a.handler, logger) })
	g.Go(func() error {
		if err := a.forwarder.Run(gctx); err != nil && gctx.Err() == nil {
			return fmt.Errorf("outbox forwarder: %w", err)
		}
		return nil
	})
	g.Go(func() error {
		runReconciler(gctx, a.payments, logger)
		return nil
	})
	err = g.Wait()
	logger.InfoContext(context.WithoutCancel(ctx), "server stopped")
	return err
}
