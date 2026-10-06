package app

import (
	"context"

	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

// QuizQuery answers internal questions about quizzes. It applies no authorization and must not
// be exposed over HTTP. It is separate from QuizService so courseauthoring can depend on it
// while QuizService depends on courseauthoring.
type QuizQuery struct{ tx TxRunner }

func NewQuizQuery(tx TxRunner) *QuizQuery { return &QuizQuery{tx: tx} }

// Lectures returns the lecture of each given quiz that belongs to courseID; other IDs are absent.
func (q *QuizQuery) Lectures(ctx context.Context, courseID id.ID, quizIDs []id.ID) (map[id.ID]id.ID, error) {
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
