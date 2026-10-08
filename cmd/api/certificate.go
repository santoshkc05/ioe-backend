package main

import (
	"context"
	"errors"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	certificatehttp "github.com/santoshkc2200/ioe-backend/internal/certificate/adapters/httpapi"
	certificatepg "github.com/santoshkc2200/ioe-backend/internal/certificate/adapters/postgres"
	certificateapp "github.com/santoshkc2200/ioe-backend/internal/certificate/app"
	courseauthoringapp "github.com/santoshkc2200/ioe-backend/internal/courseauthoring/app"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/clock"
	"github.com/santoshkc2200/ioe-backend/internal/platform/httpserver"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
	progressapp "github.com/santoshkc2200/ioe-backend/internal/progress/app"
)

// certificateDirectory lets certificate ask course authoring who manages a course and what it
// is called, and identity what a student is called.
type certificateDirectory struct {
	courses *courseauthoringapp.CourseService
	users   identityUsers
}

func (d certificateDirectory) CanManage(ctx context.Context, p auth.Principal, courseID id.ID) error {
	err := d.courses.CheckManagerRead(ctx, p, courseID)
	switch {
	case errors.Is(err, courseauthoringapp.ErrNotFound):
		return certificateapp.ErrNotFound
	case errors.Is(err, courseauthoringapp.ErrForbidden):
		return certificateapp.ErrForbidden
	}
	return err
}

func (d certificateDirectory) CourseTitle(ctx context.Context, courseID id.ID) (string, error) {
	f, err := d.courses.Facts(ctx, courseID)
	if errors.Is(err, courseauthoringapp.ErrNotFound) {
		return "", certificateapp.ErrNotFound
	}
	return f.Title, err
}

func (d certificateDirectory) StudentName(ctx context.Context, userID id.ID) (string, error) {
	names, err := d.users.Names(ctx, []id.ID{userID})
	if err != nil {
		return "", err
	}
	name, ok := names[userID]
	if !ok {
		return "", certificateapp.ErrNotFound
	}
	return name, nil
}

// registerCertificate mounts certificates and returns the service the refund consumer calls.
// progress and exams already have the methods certificate's ports ask for.
func registerCertificate(r *httpserver.Router, pool *pgxpool.Pool, courses *courseauthoringapp.CourseService, users identityUsers, enrollments certificateapp.Enrollments, progress certificateapp.Progress, exams certificateapp.Exams, ids *id.Generator, clk clock.Clock, ips httpserver.IPResolver, requireAuth httpserver.Middleware, logger *slog.Logger) *certificateapp.Service {
	dir := certificateDirectory{courses: courses, users: users}
	svc := certificateapp.NewService(certificatepg.NewTxRunner(pool), dir, enrollments, progress, exams, dir, ids, clk)
	certificatehttp.New(svc, certificatehttp.Config{
		RequireAuth: requireAuth, VerifyLimiter: httpserver.NewRateLimiter(120), IPs: ips, Logger: logger,
	}).Register(r)
	return svc
}

var _ certificateapp.Progress = (*progressapp.Service)(nil)
