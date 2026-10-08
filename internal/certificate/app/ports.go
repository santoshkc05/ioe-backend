// Package app contains the certificate use cases and the ports they depend on.
package app

import (
	"context"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/certificate/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

// Repository reads and writes certificates and policies in the current transaction.
type Repository interface {
	FindPolicy(ctx context.Context, courseID id.ID) (domain.Policy, bool, error)
	UpsertPolicy(ctx context.Context, p domain.Policy, now time.Time) error
	// FindValid returns the user's unrevoked certificate for the course.
	FindValid(ctx context.Context, courseID, userID id.ID) (domain.Certificate, bool, error)
	// Insert returns false, and stores nothing, when the user already holds a valid
	// certificate for the course.
	Insert(ctx context.Context, c domain.Certificate) (bool, error)
	FindByCode(ctx context.Context, code string) (domain.Certificate, bool, error)
	// ListByUser returns every certificate of the user, newest first.
	ListByUser(ctx context.Context, userID id.ID) ([]domain.Certificate, error)
	// RevokeValid revokes the user's valid certificate for the course when it was issued at or
	// before issuedBy; a no-op otherwise.
	RevokeValid(ctx context.Context, courseID, userID id.ID, issuedBy, now time.Time) error
}

// TxRunner commits when fn returns nil and rolls back otherwise.
type TxRunner interface {
	RunInTx(ctx context.Context, fn func(Repository) error) error
}

// CourseManagement is backed by courseauthoring.
type CourseManagement interface {
	// CanManage returns nil when p manages the course; otherwise ErrNotFound or ErrForbidden.
	CanManage(ctx context.Context, p auth.Principal, courseID id.ID) error
}

// Enrollments is backed by enrollment.
type Enrollments interface {
	IsActivelyEnrolled(ctx context.Context, courseID, userID id.ID) (bool, error)
}

// Progress is backed by progress.
type Progress interface {
	// IsComplete reports whether the user completed every lecture of the course.
	IsComplete(ctx context.Context, courseID, userID id.ID) (bool, error)
}

// Exams is backed by assessment.
type Exams interface {
	ExamInCourse(ctx context.Context, courseID, examID id.ID) (bool, error)
	HasPassed(ctx context.Context, courseID, userID, examID id.ID) (bool, error)
}

// Directory supplies the names a certificate snapshots, backed by identity and courseauthoring.
type Directory interface {
	// StudentName returns ErrNotFound when the user is unknown.
	StudentName(ctx context.Context, userID id.ID) (string, error)
	// CourseTitle returns ErrNotFound when the course is unknown.
	CourseTitle(ctx context.Context, courseID id.ID) (string, error)
}
