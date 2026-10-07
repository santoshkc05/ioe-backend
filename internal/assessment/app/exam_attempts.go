package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/assessment/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

// StudentExam is a published exam with the caller's standing on it.
type StudentExam struct {
	Exam          domain.Exam
	Availability  domain.Availability
	OpenAttemptID id.ID // zero when the caller has no open attempt
	BestScore     *int  // nil until an attempt is submitted
	BestPassed    *bool
	AttemptCount  int
}

// add counts a toward the summary. The best attempt has the highest score; attempts arrive in
// start order, and a user has one open attempt at a time, so a tie keeps the earliest.
func (se *StudentExam) add(a domain.ExamAttempt) {
	se.AttemptCount++
	if a.Open() {
		se.OpenAttemptID = a.ID
		return
	}
	if se.BestScore == nil || *a.Score > *se.BestScore {
		se.BestScore, se.BestPassed = a.Score, a.Passed
	}
}

// AttemptDetail is an attempt with its exam, which its deadline and review are read against.
type AttemptDetail struct {
	Attempt         domain.ExamAttempt
	Exam            domain.Exam
	RevealPermitted bool
}

// List returns the course's published exams with the caller's standing, settling the
// caller's expired attempts.
func (s *ExamService) List(ctx context.Context, p auth.Principal, courseID id.ID) ([]StudentExam, error) {
	if err := s.canTake(ctx, p, courseID); err != nil {
		return nil, err
	}
	var out []StudentExam
	err := s.tx.RunInTx(ctx, func(r Repos) error {
		exams, err := r.Exams.ListByCourse(ctx, courseID, true)
		if err != nil {
			return err
		}
		attempts, err := r.Exams.ListUserAttempts(ctx, courseID, p.UserID)
		if err != nil {
			return err
		}
		now := s.clock.Now()
		out = make([]StudentExam, len(exams))
		for i, e := range exams {
			se := StudentExam{Exam: e, Availability: e.Availability(now)}
			for _, a := range attempts {
				if a.ExamID != e.ID {
					continue
				}
				if err := settle(ctx, r, e, &a, now); err != nil {
					return err
				}
				se.add(a)
			}
			out[i] = se
		}
		return nil
	})
	return out, err
}

// Get returns a published exam to an enrolled student once its window opens. Drafts are ErrNotFound.
func (s *ExamService) Get(ctx context.Context, p auth.Principal, examID id.ID) (domain.Exam, error) {
	e, err := s.find(ctx, examID)
	if err != nil {
		return domain.Exam{}, err
	}
	if err := s.canTake(ctx, p, e.CourseID); err != nil {
		return domain.Exam{}, err
	}
	if e.Status != domain.ExamPublished {
		return domain.Exam{}, ErrNotFound
	}
	if e.Availability(s.clock.Now()) == domain.AvailabilityNotOpen {
		return domain.Exam{}, &WindowError{Err: ErrExamNotOpen, At: *e.OpensAt}
	}
	return e, nil
}

// Start opens a new attempt for the caller inside the exam's window.
func (s *ExamService) Start(ctx context.Context, p auth.Principal, examID id.ID) (AttemptDetail, error) {
	e, err := s.find(ctx, examID)
	if err != nil {
		return AttemptDetail{}, err
	}
	if err := s.canTake(ctx, p, e.CourseID); err != nil {
		return AttemptDetail{}, err
	}
	var out AttemptDetail
	err = s.tx.RunInTx(ctx, func(r Repos) error {
		e, err := r.Exams.Find(ctx, examID, LockShare)
		if err != nil {
			return err
		}
		if e.Status != domain.ExamPublished {
			return ErrNotFound
		}
		now := s.clock.Now()
		switch e.Availability(now) {
		case domain.AvailabilityNotOpen:
			return &WindowError{Err: ErrExamNotOpen, At: *e.OpensAt}
		case domain.AvailabilityClosed:
			return &WindowError{Err: ErrExamClosed, At: *e.ClosesAt}
		case domain.AvailabilityOpen:
		}
		open, err := r.Exams.FindOpenAttempt(ctx, examID, p.UserID)
		switch {
		case err == nil:
			if err := settle(ctx, r, e, &open, now); err != nil {
				return err
			}
			if open.Open() {
				return ErrOpenAttemptExists
			}
		case !errors.Is(err, ErrNotFound):
			return err
		}
		if !e.RetakesAllowed {
			submitted, err := r.Exams.HasSubmitted(ctx, examID, p.UserID)
			if err != nil {
				return err
			}
			if submitted {
				return ErrRetakesNotAllowed
			}
		}
		a := domain.NewExamAttempt(s.ids.New(), e, p.UserID, now)
		out = AttemptDetail{Attempt: a, Exam: e}
		return r.Exams.InsertAttempt(ctx, a)
	})
	return out, err
}

// SaveAnswer records the caller's answer to one question of their open attempt.
func (s *ExamService) SaveAnswer(ctx context.Context, p auth.Principal, attemptID id.ID, in AnswerInput) error {
	parsed, err := parseAnswers([]AnswerInput{in})
	if err != nil {
		return err
	}
	answer := domain.ExamAnswer{QuestionID: parsed[0].QuestionID, OptionIDs: parsed[0].OptionIDs}
	return s.tx.RunInTx(ctx, func(r Repos) error {
		a, e, err := ownAttempt(ctx, r, p, attemptID, false)
		if err != nil {
			return err
		}
		if err := a.CheckWritable(e, s.clock.Now()); err != nil {
			return err
		}
		if err := e.CheckAnswer(answer); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidInput, err)
		}
		return r.Exams.MergeAnswer(ctx, attemptID, answer)
	})
}

// Submit grades the caller's open attempt now.
func (s *ExamService) Submit(ctx context.Context, p auth.Principal, attemptID id.ID) (AttemptDetail, error) {
	var out AttemptDetail
	err := s.tx.RunInTx(ctx, func(r Repos) error {
		a, e, err := ownAttempt(ctx, r, p, attemptID, true)
		if err != nil {
			return err
		}
		now := s.clock.Now()
		if err := a.CheckWritable(e, now); err != nil {
			return err
		}
		a.Grade(e, now)
		out = AttemptDetail{
			Attempt:         a,
			Exam:            e,
			RevealPermitted: a.CheckReveal(e, now) == nil,
		}
		return r.Exams.SaveResult(ctx, a)
	})
	return out, err
}

// GetAttempt returns the caller's attempt, settled if it has expired.
func (s *ExamService) GetAttempt(ctx context.Context, p auth.Principal, attemptID id.ID) (AttemptDetail, error) {
	var out AttemptDetail
	err := s.tx.RunInTx(ctx, func(r Repos) error {
		a, e, err := ownAttempt(ctx, r, p, attemptID, false)
		if err != nil {
			return err
		}
		now := s.clock.Now()
		if err := settle(ctx, r, e, &a, now); err != nil {
			return err
		}
		out = AttemptDetail{
			Attempt:         a,
			Exam:            e,
			RevealPermitted: a.CheckReveal(e, now) == nil,
		}
		return nil
	})
	return out, err
}

// Review returns a graded attempt with the answer key. The owner is subject to the exam's
// reveal policy; a manager of the course is not. Anyone else gets ErrNotFound.
func (s *ExamService) Review(ctx context.Context, p auth.Principal, attemptID id.ID) (AttemptDetail, error) {
	var owner, courseID id.ID
	err := s.tx.RunInTx(ctx, func(r Repos) error {
		a, err := r.Exams.FindAttempt(ctx, attemptID, false)
		owner, courseID = a.UserID, a.CourseID
		return err
	})
	if err != nil {
		return AttemptDetail{}, err
	}
	manager := owner != p.UserID
	if manager {
		if err := s.courses.CanReadAsManager(ctx, p, courseID); err != nil {
			if errors.Is(err, ErrForbidden) || errors.Is(err, ErrNotFound) {
				return AttemptDetail{}, ErrNotFound
			}
			return AttemptDetail{}, err
		}
	}
	var out AttemptDetail
	err = s.tx.RunInTx(ctx, func(r Repos) error {
		a, err := r.Exams.FindAttempt(ctx, attemptID, false)
		if err != nil {
			return err
		}
		e, err := r.Exams.Find(ctx, a.ExamID, LockNone)
		if err != nil {
			return err
		}
		now := s.clock.Now()
		if err := settle(ctx, r, e, &a, now); err != nil {
			return err
		}
		switch {
		case !manager:
			if err := a.CheckReveal(e, now); err != nil {
				return err
			}
		case a.Open():
			return domain.ErrRevealAttemptOpen
		}
		out = AttemptDetail{Attempt: a, Exam: e, RevealPermitted: true}
		return nil
	})
	return out, err
}

// ListAttempts returns every attempt on an exam to a manager of its course, settling expired
// ones.
func (s *ExamService) ListAttempts(ctx context.Context, p auth.Principal, examID id.ID) ([]domain.ExamAttempt, error) {
	if err := s.authorize(ctx, p, examID, s.courses.CanReadAsManager); err != nil {
		return nil, err
	}
	var out []domain.ExamAttempt
	err := s.tx.RunInTx(ctx, func(r Repos) error {
		e, err := r.Exams.Find(ctx, examID, LockNone)
		if err != nil {
			return err
		}
		if out, err = r.Exams.ListAttempts(ctx, examID); err != nil {
			return err
		}
		now := s.clock.Now()
		for i := range out {
			if err := settle(ctx, r, e, &out[i], now); err != nil {
				return err
			}
		}
		return nil
	})
	return out, err
}

// canTake requires a visible course and an active enrollment.
func (s *ExamService) canTake(ctx context.Context, p auth.Principal, courseID id.ID) error {
	if err := s.courses.CanReadCourse(ctx, p, courseID); err != nil {
		return err
	}
	active, err := s.enrollments.IsActivelyEnrolled(ctx, courseID, p.UserID)
	if err != nil {
		return err
	}
	if !active {
		return ErrEnrollmentRequired
	}
	return nil
}

// ownAttempt loads p's attempt and its exam. Another user's attempt is ErrNotFound.
func ownAttempt(ctx context.Context, r Repos, p auth.Principal, attemptID id.ID, forUpdate bool) (domain.ExamAttempt, domain.Exam, error) {
	a, err := r.Exams.FindAttempt(ctx, attemptID, forUpdate)
	if err != nil {
		return domain.ExamAttempt{}, domain.Exam{}, err
	}
	if a.UserID != p.UserID {
		return domain.ExamAttempt{}, domain.Exam{}, ErrNotFound
	}
	e, err := r.Exams.Find(ctx, a.ExamID, LockNone)
	return a, e, err
}

// settleExamAttempts settles all expired attempts for an exam.
func settleExamAttempts(ctx context.Context, r Repos, e domain.Exam, now time.Time) error {
	attempts, err := r.Exams.ListAttempts(ctx, e.ID)
	if err != nil {
		return err
	}
	for i := range attempts {
		if err := settle(ctx, r, e, &attempts[i], now); err != nil {
			return err
		}
	}
	return nil
}

// settle grades a in place as of its deadline when it has expired and stores the result.
// It locks the fresh attempt row before grading and saving so concurrent answers are not lost.
// When a concurrent submit or settlement wins, a is reloaded instead.
func settle(ctx context.Context, r Repos, e domain.Exam, a *domain.ExamAttempt, now time.Time) error {
	d := a.Deadline(e)
	if !a.Open() || d == nil || !now.After(*d) {
		return nil
	}
	fresh, err := r.Exams.FindAttempt(ctx, a.ID, true)
	if err != nil {
		return err
	}
	if !fresh.Settle(e, now) {
		*a = fresh
		return nil
	}
	err = r.Exams.SaveResult(ctx, fresh)
	if errors.Is(err, domain.ErrAttemptSubmitted) {
		fresh, err = r.Exams.FindAttempt(ctx, a.ID, false)
	}
	if err != nil {
		return err
	}
	*a = fresh
	return nil
}
