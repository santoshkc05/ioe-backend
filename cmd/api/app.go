package main

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/gorilla/mux"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/santoshkc2200/ioe-backend/internal/identity/adapters/google"
	"github.com/santoshkc2200/ioe-backend/internal/identity/adapters/httpapi"
	"github.com/santoshkc2200/ioe-backend/internal/identity/adapters/jwt"
	identitypg "github.com/santoshkc2200/ioe-backend/internal/identity/adapters/postgres"
	identityapp "github.com/santoshkc2200/ioe-backend/internal/identity/app"
	"github.com/santoshkc2200/ioe-backend/internal/platform/clock"
	"github.com/santoshkc2200/ioe-backend/internal/platform/config"
	"github.com/santoshkc2200/ioe-backend/internal/platform/httpserver"
	"github.com/santoshkc2200/ioe-backend/internal/platform/outbox"
)

const serviceName = "ioe-backend"

type application struct {
	handler   http.Handler
	forwarder *outbox.Forwarder
}

// buildApp wires every bounded context. ctx bounds background work such as JWKS refresh.
func buildApp(ctx context.Context, cfg config.Config, logger *slog.Logger, pool *pgxpool.Pool) (*application, error) {
	clk := clock.System{}

	keys, err := jwt.ParseKeys(cfg.JWTSigningKeyPEM, cfg.JWTSigningKeyID, cfg.JWTVerifyKeys)
	if err != nil {
		return nil, err
	}
	tokens := jwt.New(keys, cfg.JWTIssuer, cfg.JWTAudience, clk)
	googleVerifier, err := google.NewVerifier(ctx, cfg.GoogleJWKSURL, cfg.GoogleClientIDs, clk)
	if err != nil {
		return nil, err
	}
	identity := identityapp.NewService(identitypg.NewTxRunner(pool), googleVerifier, tokens, clk, cfg.BootstrapRootAdminEmails)

	router, handler := httpserver.NewRouter(httpserver.Options{
		Logger: logger, AllowedOrigins: cfg.AllowedOrigins, ServiceName: serviceName,
	})
	httpserver.MountHealth(router, pool.Ping)
	if err := registerIdentity(router, identity, tokens, cfg, logger); err != nil {
		return nil, err
	}

	fw, err := outbox.NewForwarder(pool, logger)
	if err != nil {
		return nil, err
	}
	return &application{handler: handler, forwarder: fw}, nil
}

func registerIdentity(r *mux.Router, svc *identityapp.Service, tokens *jwt.Tokens, cfg config.Config, logger *slog.Logger) error {
	h, err := httpapi.New(svc, tokens, httpapi.Config{
		CookieSecure:   cfg.CookieSecure,
		AllowedOrigins: cfg.AllowedOrigins,
		Logger:         logger,
		IPs:            httpserver.NewIPResolver(cfg.TrustedProxies()),
		AuthLimiter:    httpserver.NewRateLimiter(cfg.AuthRateLimitPerMinute),
	})
	if err != nil {
		return err
	}
	h.Register(r)
	return nil
}
