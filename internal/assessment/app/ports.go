// Package app contains the assessment use cases and the ports they depend on.
package app

import (
	"context"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/assessment/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

// Kind names the assessment kinds a course version pins.
type Kind string

const (
	KindQuiz Kind = "quiz"
	KindExam Kind = "exam"
)

// Ref names one quiz or exam.
type Ref struct {
	Kind Kind
	ID   id.ID
}

// Pins maps each pinned quiz or exam to its revision.
type Pins map[Ref]int

// Head is a quiz's or exam's current revision, deleted ones included.
type Head struct {
	Ref      Ref
	Revision int
	Deleted  bool
}

// QuizRepository reads and writes quizzes in the current transaction. Find, FindForUpdate,
// ListByLecture and LecturesOf see only quizzes that are not deleted, at their head revision.
type QuizRepository interface {
	// Find returns ErrNotFound when the quiz does not exist or is deleted.
	Find(ctx context.Context, quizID id.ID) (domain.Quiz, error)
	// FindForUpdate is Find holding the quiz row lock until the transaction ends.
	FindForUpdate(ctx context.Context, quizID id.ID) (domain.Quiz, error)
	// FindRevisions returns the given revision of each quiz, deleted quizzes included, ordered
	// by position, then ID. Unknown pairs are absent.
	FindRevisions(ctx context.Context, revs map[id.ID]int) ([]domain.Quiz, error)
	// ListByLecture orders by position, then ID.
	ListByLecture(ctx context.Context, courseID, lectureID id.ID) ([]domain.Quiz, error)
	// Insert stores q as revision 1; q.Revision must be 1.
	Insert(ctx context.Context, q domain.Quiz, by id.ID) error
	// AppendRevision stores q as revision q.Revision and moves the head to it. It returns
	// ErrNotFound unless the head is q.Revision-1 and the quiz is not deleted.
	AppendRevision(ctx context.Context, q domain.Quiz, by id.ID) error
	// Delete marks the quiz deleted; a no-op when absent or already deleted.
	Delete(ctx context.Context, quizID id.ID, now time.Time) error
	// Undelete clears the deleted mark; a no-op when absent or not deleted.
	Undelete(ctx context.Context, quizID id.ID, now time.Time) error
	// Heads returns every quiz of the course, deleted ones included.
	Heads(ctx context.Context, courseID id.ID) ([]Head, error)
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
	LockUpdate          // FOR UPDATE: serializes edits
	LockShare  = LockNone
)

// ExamRepository reads and writes exams and their attempts in the current transaction. Find
// and ListByCourse see only exams that are not deleted, at their head revision.
type ExamRepository interface {
	// Find returns ErrNotFound when the exam does not exist or is deleted.
	Find(ctx context.Context, examID id.ID, lock LockMode) (domain.Exam, error)
	// FindRevisions has QuizRepository.FindRevisions' contract.
	FindRevisions(ctx context.Context, revs map[id.ID]int) ([]domain.Exam, error)
	// ListByCourse orders by position, then ID.
	ListByCourse(ctx context.Context, courseID id.ID) ([]domain.Exam, error)
	Insert(ctx context.Context, e domain.Exam, by id.ID) error
	AppendRevision(ctx context.Context, e domain.Exam, by id.ID) error
	Delete(ctx context.Context, examID id.ID, now time.Time) error
	Undelete(ctx context.Context, examID id.ID, now time.Time) error
	Heads(ctx context.Context, courseID id.ID) ([]Head, error)
	// NextPosition returns one past the highest head position of the course's exams, or 0.
	NextPosition(ctx context.Context, courseID id.ID) (int, error)

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
	// BeginEdit authorizes p to change the course's quizzes and exams and applies the course
	// edit rule: ErrNotFound, ErrForbidden, or ErrCourseNotEditable while archived, in review or
	// approved; a published course moves to draft. lectureID zero skips the lecture check;
	// otherwise ErrNotFound when the lecture is not in the course's working copy.
	BeginEdit(ctx context.Context, p auth.Principal, courseID, lectureID id.ID) error
	// CanReadLecture applies the lecture content read rule: ErrNotFound or
	// ErrEnrollmentRequired.
	CanReadLecture(ctx context.Context, p auth.Principal, courseID, lectureID id.ID) error
	// CanReadAsManager returns nil when p manages the course, archived included; otherwise
	// ErrNotFound or ErrForbidden.
	CanReadAsManager(ctx context.Context, p auth.Principal, courseID id.ID) error
	// CanReadCourse returns nil when the course is visible to p (published, or p manages it);
	// otherwise ErrNotFound.
	CanReadCourse(ctx context.Context, p auth.Principal, courseID id.ID) error
	// LivePins returns the live version's pins, and false when the course is not live.
	LivePins(ctx context.Context, courseID id.ID) (Pins, bool, error)
	// VersionPins returns version number's pins to a manager: ErrNotFound or ErrForbidden.
	VersionPins(ctx context.Context, p auth.Principal, courseID id.ID, number int) (Pins, error)
}

// EnrollmentQuery is backed by enrollment.
type EnrollmentQuery interface {
	IsActivelyEnrolled(ctx context.Context, courseID, userID id.ID) (bool, error)
}
