package main

import (
	"context"
	"errors"
	"log/slog"

	courseauthoringapp "github.com/santoshkc2200/ioe-backend/internal/courseauthoring/app"
	enrollmenthttp "github.com/santoshkc2200/ioe-backend/internal/enrollment/adapters/httpapi"
	enrollmentapp "github.com/santoshkc2200/ioe-backend/internal/enrollment/app"
	enrollmentdomain "github.com/santoshkc2200/ioe-backend/internal/enrollment/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/clock"
	"github.com/santoshkc2200/ioe-backend/internal/platform/httpserver"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

// courseCatalog lets enrollment read course facts from course authoring.
type courseCatalog struct {
	courses *courseauthoringapp.CourseService
}

func (c courseCatalog) CourseFacts(ctx context.Context, courseID id.ID) (enrollmentdomain.CourseFacts, error) {
	f, err := c.courses.Facts(ctx, courseID)
	if errors.Is(err, courseauthoringapp.ErrNotFound) {
		return enrollmentdomain.CourseFacts{}, enrollmentapp.ErrNotFound
	}
	if err != nil {
		return enrollmentdomain.CourseFacts{}, err
	}
	return enrollmentdomain.CourseFacts{Published: f.Published, Free: f.Free, OwnerID: f.OwnerID}, nil
}

func registerEnrollment(r *httpserver.Router, tx enrollmentapp.TxRunner, courses *courseauthoringapp.CourseService, ids *id.Generator, clk clock.Clock, requireAuth httpserver.Middleware, logger *slog.Logger) {
	enrollmenthttp.New(
		enrollmentapp.NewService(tx, courseCatalog{courses: courses}, ids, clk),
		enrollmenthttp.Config{RequireAuth: requireAuth, Logger: logger},
	).Register(r)
}
