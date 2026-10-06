// Package app contains the assessment use cases and the ports they depend on.
package app

import (
	"context"

	"github.com/santoshkc2200/ioe-backend/internal/assessment/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

// QuizRepository reads and writes quizzes in the current transaction.
type QuizRepository interface {
	// Find returns ErrNotFound when the quiz does not exist.
	Find(ctx context.Context, quizID id.ID) (domain.Quiz, error)
	// ListByLecture orders by position, then ID.
	ListByLecture(ctx context.Context, courseID, lectureID id.ID) ([]domain.Quiz, error)
	Insert(ctx context.Context, q domain.Quiz) error
	// Replace overwrites position, questions and updated time; ErrNotFound when absent, so a
	// concurrent delete is never undone.
	Replace(ctx context.Context, q domain.Quiz) error
	// Delete removes the quiz and its attempts; a no-op when absent.
	Delete(ctx context.Context, quizID id.ID) error
	// LecturesOf returns the lecture of each given quiz that belongs to courseID.
	LecturesOf(ctx context.Context, courseID id.ID, quizIDs []id.ID) (map[id.ID]id.ID, error)
	// RecordAttempt inserts the attempt and returns its ID, or returns the ID of the existing
	// attempt with the same quiz, user, and non-empty idempotency key.
	RecordAttempt(ctx context.Context, a domain.QuizAttempt) (id.ID, error)
}

// TxRunner commits when fn returns nil and rolls back otherwise.
type TxRunner interface {
	RunInTx(ctx context.Context, fn func(QuizRepository) error) error
}

// CourseAccess is backed by courseauthoring.
type CourseAccess interface {
	// CanManageLecture returns nil when p manages the course, the course is not archived, and
	// the lecture is in the course; otherwise ErrNotFound, ErrForbidden, or
	// ErrCourseNotEditable.
	CanManageLecture(ctx context.Context, p auth.Principal, courseID, lectureID id.ID) error
	// CanReadLecture applies the lecture content read rule: ErrNotFound or
	// ErrEnrollmentRequired.
	CanReadLecture(ctx context.Context, p auth.Principal, courseID, lectureID id.ID) error
}

// EnrollmentQuery is backed by enrollment.
type EnrollmentQuery interface {
	IsActivelyEnrolled(ctx context.Context, courseID, userID id.ID) (bool, error)
}
