package app

import (
	"context"

	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

// AccessQuery answers enrollment questions for other contexts. It depends only on the
// repository so it can be built before the contexts it serves.
type AccessQuery struct{ tx TxRunner }

func NewAccessQuery(tx TxRunner) *AccessQuery { return &AccessQuery{tx: tx} }

func (q *AccessQuery) IsActivelyEnrolled(ctx context.Context, courseID, userID id.ID) (bool, error) {
	var active bool
	err := q.tx.RunInTx(ctx, func(r Repos) error {
		e, found, err := r.Enrollments.FindByCourseAndUser(ctx, courseID, userID)
		active = found && e.IsActive()
		return err
	})
	return active, err
}
