package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel"

	courseauthoringhttp "github.com/santoshkc2200/ioe-backend/internal/courseauthoring/adapters/httpapi"
	courseauthoringpg "github.com/santoshkc2200/ioe-backend/internal/courseauthoring/adapters/postgres"
	courseauthoringapp "github.com/santoshkc2200/ioe-backend/internal/courseauthoring/app"
	enrollmentpg "github.com/santoshkc2200/ioe-backend/internal/enrollment/adapters/postgres"
	enrollmentapp "github.com/santoshkc2200/ioe-backend/internal/enrollment/app"
	"github.com/santoshkc2200/ioe-backend/internal/identity/adapters/google"
	"github.com/santoshkc2200/ioe-backend/internal/identity/adapters/httpapi"
	"github.com/santoshkc2200/ioe-backend/internal/identity/adapters/jwt"
	identitypg "github.com/santoshkc2200/ioe-backend/internal/identity/adapters/postgres"
	identityapp "github.com/santoshkc2200/ioe-backend/internal/identity/app"
	identitydomain "github.com/santoshkc2200/ioe-backend/internal/identity/domain"
	notificationevents "github.com/santoshkc2200/ioe-backend/internal/notification/adapters/events"
	"github.com/santoshkc2200/ioe-backend/internal/notification/adapters/notifysvc"
	"github.com/santoshkc2200/ioe-backend/internal/notification/adapters/templates"
	notificationapp "github.com/santoshkc2200/ioe-backend/internal/notification/app"
	"github.com/santoshkc2200/ioe-backend/internal/platform/clock"
	"github.com/santoshkc2200/ioe-backend/internal/platform/config"
	"github.com/santoshkc2200/ioe-backend/internal/platform/httpserver"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
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

	ids, err := id.NewGenerator(cfg.SnowflakeNodeID)
	if err != nil {
		return nil, err
	}

	keys, err := jwt.ParseKeys(cfg.JWTSigningKeyPEM, cfg.JWTSigningKeyID, cfg.JWTVerifyKeys)
	if err != nil {
		return nil, err
	}
	tokens := jwt.New(keys, cfg.JWTIssuer, cfg.JWTAudience, clk)
	googleVerifier, err := google.NewVerifier(ctx, cfg.GoogleJWKSURL, cfg.GoogleClientIDs, clk)
	if err != nil {
		return nil, err
	}
	identityTx := identitypg.NewTxRunner(pool)
	identity := identityapp.NewService(identityTx, googleVerifier, tokens, ids, clk, cfg.BootstrapRootAdminEmails)
	identityAdmin := identityapp.NewAdminService(identityTx, clk)

	router, handler := httpserver.NewRouter(httpserver.Options{
		Logger: logger, AllowedOrigins: cfg.AllowedOrigins, ServiceName: serviceName,
	})
	httpserver.MountHealth(router, pool.Ping)
	ips := httpserver.NewIPResolver(cfg.TrustedProxies())
	identityHandler, err := registerIdentity(router, identity, identityAdmin, tokens, ips, cfg, logger)
	if err != nil {
		return nil, err
	}
	enrollmentTx := enrollmentpg.NewTxRunner(pool)
	courses := registerCourseAuthoring(router, pool, ids, clk, enrollmentapp.NewAccessQuery(enrollmentTx), identityHandler, ips, logger)
	registerEnrollment(router, enrollmentTx, courses, ids, clk, identityHandler.RequireAuth, logger)

	fw, err := outbox.NewForwarder(pool, logger)
	if err != nil {
		return nil, err
	}
	if err := registerNotifications(fw, cfg, logger); err != nil {
		return nil, errors.Join(err, fw.Close())
	}
	return &application{handler: handler, forwarder: fw}, nil
}

// registerNotifications subscribes the notification context to the events it consumes.
func registerNotifications(fw *outbox.Forwarder, cfg config.Config, logger *slog.Logger) error {
	if !cfg.NotificationsEnabled() {
		logger.Warn("notifications disabled: NOTIFICATION_SERVICE_BASE_URL and NOTIFICATION_SERVICE_SEND_API_KEY are not set")
		return nil
	}
	renderer, err := templates.New()
	if err != nil {
		return err
	}
	mailer := notifysvc.New(cfg.NotificationServiceBaseURL, cfg.NotificationServiceSendAPIKey)
	handlers, err := notificationevents.New(notificationapp.NewService(mailer, renderer), logger,
		otel.Meter("github.com/santoshkc2200/ioe-backend/internal/notification"))
	if err != nil {
		return err
	}
	fw.Handle(identitydomain.UserRegistered{}.EventName(), handlers.Welcome)
	return nil
}

func registerIdentity(r *httpserver.Router, svc *identityapp.Service, admin *identityapp.AdminService, tokens *jwt.Tokens, ips httpserver.IPResolver, cfg config.Config, logger *slog.Logger) (*httpapi.Handler, error) {
	h, err := httpapi.New(svc, admin, tokens, httpapi.Config{
		CookieSecure:   cfg.CookieSecure,
		AllowedOrigins: cfg.AllowedOrigins,
		Logger:         logger,
		IPs:            ips,
		AuthLimiter:    httpserver.NewRateLimiter(cfg.AuthRateLimitPerMinute),
	})
	if err != nil {
		return nil, err
	}
	h.Register(r)
	return h, nil
}

// registerCourseAuthoring mounts course authoring and returns its course service for
// contexts that read course facts.
func registerCourseAuthoring(r *httpserver.Router, pool *pgxpool.Pool, ids *id.Generator, clk clock.Clock, enrollments courseauthoringapp.EnrollmentQuery, authn *httpapi.Handler, ips httpserver.IPResolver, logger *slog.Logger) *courseauthoringapp.CourseService {
	tx := courseauthoringpg.NewTxRunner(pool, clk)
	courses := courseauthoringapp.NewCourseService(tx, ids, clk)
	courseauthoringhttp.New(
		courses,
		courseauthoringapp.NewContentService(tx, ids, enrollments),
		courseauthoringhttp.Config{
			RequireAuth: authn.RequireAuth, OptionalAuth: authn.OptionalAuth, IPs: ips,
			ContentLimiter: httpserver.NewRateLimiter(60), CatalogLimiter: httpserver.NewRateLimiter(120),
			Logger: logger,
		},
	).Register(r)
	return courses
}
