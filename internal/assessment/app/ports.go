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

// LockMode is the row lock ExamRepository.Find takes on the exam.
type LockMode int

const (
	LockNone   LockMode = iota
	LockShare           // FOR SHARE: blocks edits, not other starts
	LockUpdate          // FOR UPDATE: serializes edits with starts and other edits
)

// ExamRepository reads and writes exams and their attempts in the current transaction.
type ExamRepository interface {
	// Find returns ErrNotFound when the exam does not exist.
	Find(ctx context.Context, examID id.ID, lock LockMode) (domain.Exam, error)
	// ListByCourse orders by position, then ID.
	ListByCourse(ctx context.Context, courseID id.ID, publishedOnly bool) ([]domain.Exam, error)
	Insert(ctx context.Context, e domain.Exam) error
	// Replace overwrites every mutable column; ErrNotFound when absent.
	Replace(ctx context.Context, e domain.Exam) error
	// Delete removes the exam, a no-op when absent; ErrExamHasAttempts when an attempt exists.
	Delete(ctx context.Context, examID id.ID) error
	// NextPosition returns one past the highest exam position in the course, or 0.
	NextPosition(ctx context.Context, courseID id.ID) (int, error)
	// SetPositions sets each listed exam of the course to its index in examIDs.
	SetPositions(ctx context.Context, courseID id.ID, examIDs []id.ID) error

	Locks(ctx context.Context, examID id.ID) (domain.Locks, error)
	// FindAttempt returns ErrNotFound when the attempt does not exist.
	FindAttempt(ctx context.Context, attemptID id.ID, forUpdate bool) (domain.ExamAttempt, error)
	// FindOpenAttempt returns ErrNotFound when the user has no open attempt on the exam.
	FindOpenAttempt(ctx context.Context, examID, userID id.ID) (domain.ExamAttempt, error)
	HasSubmitted(ctx context.Context, examID, userID id.ID) (bool, error)
	// InsertAttempt returns ErrOpenAttemptExists when the user already has an open attempt.
	InsertAttempt(ctx context.Context, a domain.ExamAttempt) error
	// MergeAnswer sets one answer on an open attempt; domain.ErrAttemptSubmitted when closed.
	MergeAnswer(ctx context.Context, attemptID id.ID, a domain.ExamAnswer) error
	// SaveResult writes an open attempt's grading fields and answers; domain.ErrAttemptSubmitted
	// when it was closed meanwhile.
	SaveResult(ctx context.Context, a domain.ExamAttempt) error
	// ListAttempts orders by start time, then ID.
	ListAttempts(ctx context.Context, examID id.ID) ([]domain.ExamAttempt, error)
	// ListUserAttempts returns the user's attempts on every exam of the course, by start time.
	ListUserAttempts(ctx context.Context, courseID, userID id.ID) ([]domain.ExamAttempt, error)
}

// Repos are the repositories of one transaction.
type Repos struct {
	Quizzes QuizRepository
	Exams   ExamRepository
}

// TxRunner commits when fn returns nil and rolls back otherwise.
type TxRunner interface {
	RunInTx(ctx context.Context, fn func(Repos) error) error
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
	// CanManageCourse returns nil when p manages the course and it is not archived; otherwise
	// ErrNotFound, ErrForbidden, or ErrCourseNotEditable.
	CanManageCourse(ctx context.Context, p auth.Principal, courseID id.ID) error
	// CanReadAsManager returns nil when p manages the course, archived included; otherwise
	// ErrNotFound or ErrForbidden.
	CanReadAsManager(ctx context.Context, p auth.Principal, courseID id.ID) error
	// CanReadCourse returns nil when the course is visible to p (published, or p manages it);
	// otherwise ErrNotFound.
	CanReadCourse(ctx context.Context, p auth.Principal, courseID id.ID) error
}

// EnrollmentQuery is backed by enrollment.
type EnrollmentQuery interface {
	IsActivelyEnrolled(ctx context.Context, courseID, userID id.ID) (bool, error)
}
