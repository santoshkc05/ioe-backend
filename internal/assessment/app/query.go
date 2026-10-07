package app

import (
	"context"

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
