package main

import (
	"context"
	"errors"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	courseauthoringapp "github.com/santoshkc2200/ioe-backend/internal/courseauthoring/app"
	"github.com/santoshkc2200/ioe-backend/internal/platform/clock"
	"github.com/santoshkc2200/ioe-backend/internal/platform/httpserver"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
	progresshttp "github.com/santoshkc2200/ioe-backend/internal/progress/adapters/httpapi"
	progresspg "github.com/santoshkc2200/ioe-backend/internal/progress/adapters/postgres"
	progressapp "github.com/santoshkc2200/ioe-backend/internal/progress/app"
	progressdomain "github.com/santoshkc2200/ioe-backend/internal/progress/domain"
)

// progressCourseCatalog lets progress read course facts from course authoring.
type progressCourseCatalog struct {
	courses *courseauthoringapp.CourseService
}

func (c progressCourseCatalog) CourseFacts(ctx context.Context, courseID id.ID) (progressdomain.CourseFacts, error) {
	f, err := c.courses.Facts(ctx, courseID)
	if errors.Is(err, courseauthoringapp.ErrNotFound) {
		return progressdomain.CourseFacts{}, progressapp.ErrNotFound
	}
	if err != nil {
		return progressdomain.CourseFacts{}, err
	}
	return progressdomain.CourseFacts{Published: f.Published, OwnerID: f.OwnerID, LectureIDs: f.LectureIDs}, nil
}

func registerProgress(r *httpserver.Router, pool *pgxpool.Pool, courses *courseauthoringapp.CourseService, enrollments progressapp.EnrollmentQuery, clk clock.Clock, requireAuth httpserver.Middleware, logger *slog.Logger) {
	progresshttp.New(
		progressapp.NewService(progresspg.NewTxRunner(pool), progressCourseCatalog{courses: courses}, enrollments, clk),
		progresshttp.Config{RequireAuth: requireAuth, Logger: logger},
	).Register(r)
}
