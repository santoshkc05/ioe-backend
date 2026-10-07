package app

import (
	"cmp"
	"context"
	"fmt"
	"reflect"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/platform/clock"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

// RestoreService resets a course's working-copy quizzes and exams to a published version's pins.
type RestoreService struct {
	tx    TxRunner
	clock clock.Clock
}

func NewRestoreService(tx TxRunner, clk clock.Clock) *RestoreService {
	return &RestoreService{tx: tx, clock: clk}
}

// RestorePins makes each pinned revision the head again, undeleting as needed, and deletes the
// course's quizzes and exams that pins does not name. It is idempotent: an assessment already
// at its pin is left alone. actorID is recorded as the author of copied revisions.
func (s *RestoreService) RestorePins(ctx context.Context, courseID, actorID id.ID, pins Pins) error {
	now := s.clock.Now()
	return s.tx.RunInTx(ctx, func(r Repos) error {
		quizzes, err := r.Quizzes.Heads(ctx, courseID)
		if err != nil {
			return err
		}
		exams, err := r.Exams.Heads(ctx, courseID)
		if err != nil {
			return err
		}
		for _, h := range append(quizzes, exams...) {
			if err := s.restore(ctx, r, h, pins, actorID, now); err != nil {
				return err
			}
		}
		return nil
	})
}

func deleteRef(ctx context.Context, r Repos, ref Ref, now time.Time) error {
	if ref.Kind == KindQuiz {
		return r.Quizzes.Delete(ctx, ref.ID, now)
	}
	return r.Exams.Delete(ctx, ref.ID, now)
}

func undeleteRef(ctx context.Context, r Repos, ref Ref, now time.Time) error {
	if ref.Kind == KindQuiz {
		return r.Quizzes.Undelete(ctx, ref.ID, now)
	}
	return r.Exams.Undelete(ctx, ref.ID, now)
}

// copyRevision appends a copy of revision pin as the head's next revision.
func copyRevision(ctx context.Context, r Repos, h Head, pin int, actorID id.ID, now time.Time) error {
	if h.Ref.Kind == KindQuiz {
		got, err := r.Quizzes.FindRevisions(ctx, map[id.ID]int{h.Ref.ID: pin})
		if err != nil || len(got) == 0 {
			return cmp.Or(err, fmt.Errorf("quiz %s has no revision %d", h.Ref.ID, pin))
		}
		q := got[0]
		q.Revision, q.UpdatedAt = h.Revision+1, now
		return r.Quizzes.AppendRevision(ctx, q, actorID)
	}
	got, err := r.Exams.FindRevisions(ctx, map[id.ID]int{h.Ref.ID: pin})
	if err != nil || len(got) == 0 {
		return cmp.Or(err, fmt.Errorf("exam %s has no revision %d", h.Ref.ID, pin))
	}
	e := got[0]
	e.Revision, e.UpdatedAt = h.Revision+1, now
	return r.Exams.AppendRevision(ctx, e, actorID)
}

// samePinned reports whether the head's content equals revision pin's.
func samePinned(ctx context.Context, r Repos, h Head, pin int) (bool, error) {
	if h.Revision == pin {
		return true, nil
	}
	revs := map[id.ID]int{h.Ref.ID: h.Revision}
	pinned := map[id.ID]int{h.Ref.ID: pin}
	if h.Ref.Kind == KindQuiz {
		a, err := r.Quizzes.FindRevisions(ctx, revs)
		if err != nil {
			return false, err
		}
		b, err := r.Quizzes.FindRevisions(ctx, pinned)
		if err != nil || len(a) == 0 || len(b) == 0 {
			return false, err
		}
		x, y := a[0], b[0]
		x.Revision, x.UpdatedAt, y.Revision, y.UpdatedAt = 0, time.Time{}, 0, time.Time{}
		return reflect.DeepEqual(x, y), nil
	}
	a, err := r.Exams.FindRevisions(ctx, revs)
	if err != nil {
		return false, err
	}
	b, err := r.Exams.FindRevisions(ctx, pinned)
	if err != nil || len(a) == 0 || len(b) == 0 {
		return false, err
	}
	x, y := a[0], b[0]
	x.Revision, x.UpdatedAt, y.Revision, y.UpdatedAt = 0, time.Time{}, 0, time.Time{}
	return reflect.DeepEqual(x, y), nil
}

func (s *RestoreService) restore(ctx context.Context, r Repos, h Head, pins Pins, actorID id.ID, now time.Time) error {
	pin, pinned := pins[h.Ref]
	if !pinned {
		if h.Deleted {
			return nil
		}
		return deleteRef(ctx, r, h.Ref, now)
	}
	if h.Deleted {
		if err := undeleteRef(ctx, r, h.Ref, now); err != nil {
			return err
		}
	}
	same, err := samePinned(ctx, r, h, pin)
	if err != nil || same {
		return err
	}
	return copyRevision(ctx, r, h, pin, actorID, now)
}
