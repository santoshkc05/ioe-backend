// Package app contains the progress use cases and the ports they depend on.
package app

import (
	"context"

	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
	"github.com/santoshkc2200/ioe-backend/internal/progress/domain"
)

// Repository reads and writes progress in the current transaction.
type Repository interface {
	// RecordLecture upserts the lecture row and, when that row changed, sets the course's
	// last lecture and updated time. An in_progress write over a completed row changes nothing.
	RecordLecture(ctx context.Context, courseID, userID id.ID, p domain.LectureProgress) error
	// FindCourse returns false when the user has no progress in the course.
	FindCourse(ctx context.Context, courseID, userID id.ID) (domain.CourseProgress, bool, error)
	// FindByUser returns every course with progress, most recently updated first.
	FindByUser(ctx context.Context, userID id.ID) ([]domain.CourseProgress, error)
	// ActivityByUser returns UTC days with activity, newest first.
	ActivityByUser(ctx context.Context, userID id.ID) ([]domain.ActivityDay, error)
}

// TxRunner commits when fn returns nil and rolls back otherwise.
type TxRunner interface {
	RunInTx(ctx context.Context, fn func(Repository) error) error
}

// CourseCatalog returns ErrNotFound when the course does not exist.
type CourseCatalog interface {
	CourseFacts(ctx context.Context, courseID id.ID) (domain.CourseFacts, error)
}

// EnrollmentQuery reports whether a user is actively enrolled in a course.
type EnrollmentQuery interface {
	IsActivelyEnrolled(ctx context.Context, courseID, userID id.ID) (bool, error)
}
