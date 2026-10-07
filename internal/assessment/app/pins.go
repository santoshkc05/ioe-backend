package app

import (
	"context"
	"errors"

	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

// ids returns the pinned revision of each pinned assessment of kind.
func (p Pins) ids(kind Kind) map[id.ID]int {
	out := map[id.ID]int{}
	for ref, rev := range p {
		if ref.Kind == kind {
			out[ref.ID] = rev
		}
	}
	return out
}

// readPins picks what a read serves. version > 0 returns that version's pins (managers only).
// Otherwise canRead must pass; then a manager gets nil pins (the working copy) and everyone else
// the live pins, empty when the course is not live.
func readPins(ctx context.Context, courses CourseAccess, p auth.Principal, courseID id.ID, version int, canRead func() error) (Pins, error) {
	if version > 0 {
		return courses.VersionPins(ctx, p, courseID, version)
	}
	if err := canRead(); err != nil {
		return nil, err
	}
	switch err := courses.CanReadAsManager(ctx, p, courseID); {
	case err == nil:
		return nil, nil
	case !errors.Is(err, ErrForbidden) && !errors.Is(err, ErrNotFound):
		return nil, err
	}
	pins, _, err := courses.LivePins(ctx, courseID)
	if pins == nil && err == nil {
		pins = Pins{}
	}
	return pins, err
}
