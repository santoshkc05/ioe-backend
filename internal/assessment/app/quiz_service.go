package app

import (
	"context"
	"fmt"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/assessment/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/clock"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

const maxIdempotencyKeyLen = 128

type OptionInput struct {
	ID        string // empty for a new option
	Label     string
	IsCorrect bool
}

type QuestionInput struct {
	ID                 string // empty for a new question
	Prompt             string
	Type               string
	Explanation        string
	Points             int // zero means 1
	ReferenceLectureID string
	Options            []OptionInput
}

type QuizInput struct {
	Position  int
	Questions []QuestionInput
}

type AnswerInput struct {
	QuestionID string
	OptionIDs  []string
}

// QuizService implements quiz authoring, listing, and attempt recording. Course access and
// enrollment checks run before the assessment transaction opens: each takes its own pool
// connection.
type QuizService struct {
	tx          TxRunner
	courses     CourseAccess
	enrollments EnrollmentQuery
	ids         *id.Generator
	clock       clock.Clock
}

func NewQuizService(tx TxRunner, courses CourseAccess, enrollments EnrollmentQuery, ids *id.Generator, clk clock.Clock) *QuizService {
	return &QuizService{tx: tx, courses: courses, enrollments: enrollments, ids: ids, clock: clk}
}

// List returns the lecture's quizzes, answer keys included, to anyone who may read the lecture.
func (s *QuizService) List(ctx context.Context, p auth.Principal, courseID, lectureID id.ID) ([]domain.Quiz, error) {
	if err := s.courses.CanReadLecture(ctx, p, courseID, lectureID); err != nil {
		return nil, err
	}
	var out []domain.Quiz
	err := s.tx.RunInTx(ctx, func(r Repos) error {
		var err error
		out, err = r.Quizzes.ListByLecture(ctx, courseID, lectureID)
		return err
	})
	return out, err
}

func (s *QuizService) Create(ctx context.Context, p auth.Principal, courseID, lectureID id.ID, in QuizInput) (domain.Quiz, error) {
	if err := s.courses.CanManageLecture(ctx, p, courseID, lectureID); err != nil {
		return domain.Quiz{}, err
	}
	now := s.clock.Now()
	q, err := s.buildQuiz(s.ids.New(), courseID, lectureID, in, nil, now, now)
	if err != nil {
		return domain.Quiz{}, err
	}
	return q, s.tx.RunInTx(ctx, func(r Repos) error { return r.Quizzes.Insert(ctx, q) })
}

// Update replaces a quiz's position and questions. The quiz must belong to the path's course
// and lecture; supplied question and option IDs must already belong to the quiz.
func (s *QuizService) Update(ctx context.Context, p auth.Principal, courseID, lectureID, quizID id.ID, in QuizInput) (domain.Quiz, error) {
	if err := s.courses.CanManageLecture(ctx, p, courseID, lectureID); err != nil {
		return domain.Quiz{}, err
	}
	var out domain.Quiz
	err := s.tx.RunInTx(ctx, func(r Repos) error {
		cur, err := r.Quizzes.Find(ctx, quizID)
		if err != nil {
			return err
		}
		if cur.CourseID != courseID || cur.LectureID != lectureID {
			return ErrNotFound
		}
		out, err = s.buildQuiz(quizID, courseID, lectureID, in, cur.IDs(), cur.CreatedAt, s.clock.Now())
		if err != nil {
			return err
		}
		return r.Quizzes.Replace(ctx, out)
	})
	return out, err
}

// Delete removes a quiz and its attempts. Lecture blocks that reference it are left alone.
func (s *QuizService) Delete(ctx context.Context, p auth.Principal, quizID id.ID) error {
	q, err := s.find(ctx, quizID)
	if err != nil {
		return err
	}
	if err := s.courses.CanManageLecture(ctx, p, q.CourseID, q.LectureID); err != nil {
		return err
	}
	return s.tx.RunInTx(ctx, func(r Repos) error { return r.Quizzes.Delete(ctx, quizID) })
}

// RecordAttempt stores userID's answers. Only the user themselves, with an active enrollment,
// may record. A repeated non-empty key returns the first attempt's ID.
func (s *QuizService) RecordAttempt(ctx context.Context, p auth.Principal, quizID, userID id.ID, answers []AnswerInput, key string) (id.ID, error) {
	if userID != p.UserID {
		return 0, ErrForbidden
	}
	if len(key) > maxIdempotencyKeyLen {
		return 0, fmt.Errorf("%w: Idempotency-Key exceeds %d characters", ErrInvalidInput, maxIdempotencyKeyLen)
	}
	parsed, err := parseAnswers(answers)
	if err != nil {
		return 0, err
	}
	q, err := s.find(ctx, quizID)
	if err != nil {
		return 0, err
	}
	if err := s.courses.CanReadLecture(ctx, p, q.CourseID, q.LectureID); err != nil {
		return 0, err
	}
	active, err := s.enrollments.IsActivelyEnrolled(ctx, q.CourseID, userID)
	if err != nil {
		return 0, err
	}
	if !active {
		return 0, ErrEnrollmentRequired
	}
	if err := q.CheckAnswers(parsed); err != nil {
		return 0, fmt.Errorf("%w: %w", ErrInvalidInput, err)
	}
	attempt := domain.QuizAttempt{ID: s.ids.New(), QuizID: quizID, UserID: userID, Answers: parsed,
		IdempotencyKey: key, SubmittedAt: s.clock.Now()}
	var out id.ID
	err = s.tx.RunInTx(ctx, func(r Repos) error {
		var err error
		out, err = r.Quizzes.RecordAttempt(ctx, attempt)
		return err
	})
	return out, err
}

func (s *QuizService) find(ctx context.Context, quizID id.ID) (domain.Quiz, error) {
	var q domain.Quiz
	err := s.tx.RunInTx(ctx, func(r Repos) error {
		var err error
		q, err = r.Quizzes.Find(ctx, quizID)
		return err
	})
	return q, err
}

// buildQuiz validates in. owned is nil on create and the quiz's current IDs on update.
func (s *QuizService) buildQuiz(quizID, courseID, lectureID id.ID, in QuizInput, owned map[id.ID]struct{}, createdAt, updatedAt time.Time) (domain.Quiz, error) {
	questions, err := buildQuestions(s.ids, in.Questions, owned)
	if err != nil {
		return domain.Quiz{}, err
	}
	q, err := domain.NewQuiz(quizID, courseID, lectureID, in.Position, questions, createdAt, updatedAt)
	if err != nil {
		return domain.Quiz{}, fmt.Errorf("%w: %w", ErrInvalidInput, err)
	}
	return q, nil
}

func parseAnswers(in []AnswerInput) ([]domain.Answer, error) {
	out := make([]domain.Answer, len(in))
	for i, a := range in {
		qid, err := id.Parse(a.QuestionID)
		if err != nil {
			return nil, fmt.Errorf("%w: question_id: %w", ErrInvalidInput, err)
		}
		opts := make([]id.ID, len(a.OptionIDs))
		for j, raw := range a.OptionIDs {
			if opts[j], err = id.Parse(raw); err != nil {
				return nil, fmt.Errorf("%w: option_ids: %w", ErrInvalidInput, err)
			}
		}
		out[i] = domain.Answer{QuestionID: qid, OptionIDs: opts}
	}
	return out, nil
}
