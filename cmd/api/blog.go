package main

import (
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	bloghttp "github.com/santoshkc2200/ioe-backend/internal/blog/adapters/httpapi"
	blogpg "github.com/santoshkc2200/ioe-backend/internal/blog/adapters/postgres"
	blogapp "github.com/santoshkc2200/ioe-backend/internal/blog/app"
	"github.com/santoshkc2200/ioe-backend/internal/platform/clock"
	"github.com/santoshkc2200/ioe-backend/internal/platform/httpserver"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

// registerBlog mounts the blog context.
func registerBlog(r *httpserver.Router, pool *pgxpool.Pool, ids *id.Generator, clk clock.Clock, users blogapp.UserDirectory, requireAuth httpserver.Middleware, ips httpserver.IPResolver, logger *slog.Logger) {
	tx := blogpg.NewTxRunner(pool, clk)
	bloghttp.New(
		blogapp.NewPostService(tx, ids, clk),
		blogapp.NewContentService(tx, ids),
		blogapp.NewPublicService(tx, users),
		bloghttp.Config{
			RequireAuth: requireAuth, IPs: ips,
			ContentLimiter: httpserver.NewRateLimiter(60), PublicLimiter: httpserver.NewRateLimiter(120),
			Logger: logger,
		},
	).Register(r)
}
