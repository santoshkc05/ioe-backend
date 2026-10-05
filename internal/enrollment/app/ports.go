// Package app contains the enrollment use cases and the ports they depend on.
package app

import (
	"context"

	"github.com/santoshkc2200/ioe-backend/internal/enrollment/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

// Repository reads and writes enrollments in the current transaction. Insert returns
// ErrDuplicate on the (course_id, user_id) unique constraint and sets e.Version to 1.
// Update returns ErrConcurrentModification when e.Version is stale and increments it on
// success. List methods return active enrollments only.
type Repository interface {
	FindByCourseAndUser(ctx context.Context, courseID, userID id.ID) (domain.Enrollment, bool, error)
	ListActiveByCourse(ctx context.Context, courseID id.ID, limit, offset int) ([]domain.Enrollment, int, error)
	ListActiveByUser(ctx context.Context, userID id.ID) ([]domain.Enrollment, error)
	Insert(ctx context.Context, e *domain.Enrollment) error
	Update(ctx context.Context, e *domain.Enrollment) error
}

// EventPublisher records events in the current transaction.
type EventPublisher interface {
	Publish(ctx context.Context, events ...domain.Event) error
}

// Repos are bound to one transaction.
type Repos struct {
	Enrollments Repository
	Events      EventPublisher
}

// TxRunner commits when fn returns nil and rolls back otherwise.
type TxRunner interface {
	RunInTx(ctx context.Context, fn func(Repos) error) error
}

// CourseCatalog returns ErrNotFound when the course does not exist.
type CourseCatalog interface {
	CourseFacts(ctx context.Context, courseID id.ID) (domain.CourseFacts, error)
}
