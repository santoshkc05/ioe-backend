package app

import (
	"context"
	"errors"

	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

// AssessmentQuery answers internal questions about quizzes and exams. It applies no
// authorization and must not be exposed over HTTP. It is separate from the services so
// courseauthoring can depend on it while the services depend on courseauthoring.
type AssessmentQuery struct{ tx TxRunner }

func NewAssessmentQuery(tx TxRunner) *AssessmentQuery { return &AssessmentQuery{tx: tx} }

// Lectures returns the lecture of each given quiz that belongs to courseID; other IDs are absent.
func (q *AssessmentQuery) Lectures(ctx context.Context, courseID id.ID, quizIDs []id.ID) (map[id.ID]id.ID, error) {
	if len(quizIDs) == 0 {
		return map[id.ID]id.ID{}, nil
	}
	var out map[id.ID]id.ID
	err := q.tx.RunInTx(ctx, func(r Repos) error {
		var err error
		out, err = r.Quizzes.LecturesOf(ctx, courseID, quizIDs)
		return err
	})
	return out, err
}

// Heads returns the head revision of every quiz and exam of the course that is not deleted:
// quizzes first, then exams, each by ID.
func (q *AssessmentQuery) Heads(ctx context.Context, courseID id.ID) ([]Head, error) {
	var out []Head
	err := q.tx.RunInTx(ctx, func(r Repos) error {
		quizzes, err := r.Quizzes.Heads(ctx, courseID)
		if err != nil {
			return err
		}
		exams, err := r.Exams.Heads(ctx, courseID)
		if err != nil {
			return err
		}
		for _, h := range append(quizzes, exams...) {
			if !h.Deleted {
				out = append(out, h)
			}
		}
		return nil
	})
	return out, err
}

// ExamInCourse reports whether the exam exists, is not deleted and belongs to courseID.
func (q *AssessmentQuery) ExamInCourse(ctx context.Context, courseID, examID id.ID) (bool, error) {
	var ok bool
	err := q.tx.RunInTx(ctx, func(r Repos) error {
		e, err := r.Exams.Find(ctx, examID, LockNone)
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		ok = e.CourseID == courseID
		return nil
	})
	return ok, err
}

// HasPassed reports whether userID has a submitted, passing attempt on the exam. Attempts
// are graded against the revision they were taken on, so later edits never change the answer.
func (q *AssessmentQuery) HasPassed(ctx context.Context, courseID, userID, examID id.ID) (bool, error) {
	var passed bool
	err := q.tx.RunInTx(ctx, func(r Repos) error {
		attempts, err := r.Exams.ListUserAttempts(ctx, courseID, userID)
		if err != nil {
			return err
		}
		for _, a := range attempts {
			if a.ExamID == examID && a.SubmittedAt != nil && a.Passed != nil && *a.Passed {
				passed = true
				return nil
			}
		}
		return nil
	})
	return passed, err
}
