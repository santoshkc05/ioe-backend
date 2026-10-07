package main

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	courseauthoringapp "github.com/santoshkc2200/ioe-backend/internal/courseauthoring/app"
	enrollmentapp "github.com/santoshkc2200/ioe-backend/internal/enrollment/app"
	"github.com/santoshkc2200/ioe-backend/internal/payment/adapters/esewa"
	paymenthttp "github.com/santoshkc2200/ioe-backend/internal/payment/adapters/httpapi"
	paymentpg "github.com/santoshkc2200/ioe-backend/internal/payment/adapters/postgres"
	paymentapp "github.com/santoshkc2200/ioe-backend/internal/payment/app"
	paymentdomain "github.com/santoshkc2200/ioe-backend/internal/payment/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/clock"
	"github.com/santoshkc2200/ioe-backend/internal/platform/config"
	"github.com/santoshkc2200/ioe-backend/internal/platform/httpserver"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

// reconcileInterval is how often unconfirmed purchases are settled with their gateway.
const reconcileInterval = 5 * time.Minute

// paymentCourseCatalog lets payment read course publication and price from course authoring.
type paymentCourseCatalog struct {
	courses *courseauthoringapp.CourseService
}

func (c paymentCourseCatalog) CourseFacts(ctx context.Context, courseID id.ID) (paymentapp.CourseFacts, error) {
	f, err := c.courses.Facts(ctx, courseID)
	if errors.Is(err, courseauthoringapp.ErrNotFound) {
		return paymentapp.CourseFacts{}, paymentapp.ErrNotFound
	}
	if err != nil {
		return paymentapp.CourseFacts{}, err
	}
	return paymentapp.CourseFacts{
		Published: f.Published,
		Title:     f.Title,
		Price:     paymentdomain.Money{AmountMinor: f.Price.AmountMinor, Currency: f.Price.Currency},
	}, nil
}

// paymentEnrollments lets payment check and grant enrollment.
type paymentEnrollments struct {
	access *enrollmentapp.AccessQuery
	svc    *enrollmentapp.Service
}

func (e paymentEnrollments) IsEnrolled(ctx context.Context, courseID, userID id.ID) (bool, error) {
	return e.access.IsActivelyEnrolled(ctx, courseID, userID)
}

func (e paymentEnrollments) GrantPurchased(ctx context.Context, courseID, userID id.ID) error {
	return e.svc.EnrollPurchased(ctx, courseID, userID)
}

// registerPayment mounts payment routes and returns the service the reconciler runs. Without
// eSewa settings the routes still exist and checkout answers 503 payment_unavailable.
func registerPayment(r *httpserver.Router, pool *pgxpool.Pool, courses *courseauthoringapp.CourseService, access *enrollmentapp.AccessQuery, enrollments *enrollmentapp.Service, ids *id.Generator, clk clock.Clock, cfg config.Config, requireAuth httpserver.Middleware, logger *slog.Logger) *paymentapp.Service {
	gateways := map[string]paymentapp.Gateway{}
	if cfg.EsewaEnabled() {
		gateways[esewa.Name] = esewa.New(esewa.Config{
			ProductCode: cfg.EsewaProductCode,
			SecretKey:   cfg.EsewaSecretKey,
			FormURL:     cfg.EsewaFormURL,
			StatusURL:   cfg.EsewaStatusURL,
			ReturnURL:   cfg.PaymentReturnURL,
		}, logger)
	} else {
		logger.Warn("payments disabled: ESEWA_PRODUCT_CODE, ESEWA_SECRET_KEY, ESEWA_FORM_URL, ESEWA_STATUS_URL and PAYMENT_RETURN_URL are not set")
	}
	svc := paymentapp.NewService(paymentpg.NewTxRunner(pool), paymentCourseCatalog{courses: courses},
		paymentEnrollments{access: access, svc: enrollments}, gateways, ids, clk, logger)
	paymenthttp.New(svc, paymenthttp.Config{RequireAuth: requireAuth, Logger: logger}).Register(r)
	return svc
}

// runReconciler settles unconfirmed purchases now and every reconcileInterval until ctx ends.
func runReconciler(ctx context.Context, svc *paymentapp.Service, logger *slog.Logger) {
	t := time.NewTicker(reconcileInterval)
	defer t.Stop()
	for {
		if err := svc.Reconcile(ctx); err != nil && ctx.Err() == nil {
			logger.ErrorContext(ctx, "purchase reconcile failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
