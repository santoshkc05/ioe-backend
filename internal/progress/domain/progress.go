package domain

import (
	"slices"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

type LectureState string

const (
	LectureStateInProgress LectureState = "in_progress"
	LectureStateCompleted  LectureState = "completed"
)

func (s LectureState) Valid() bool {
	return s == LectureStateInProgress || s == LectureStateCompleted
}

// LectureProgress is one user's progress on one lecture. A completed lecture never returns
// to in_progress; persistence enforces that so concurrent writes cannot race it.
type LectureProgress struct {
	LectureID  id.ID
	State      LectureState
	PositionMs int64 // video resume offset; 0 for lectures without video
	UpdatedAt  time.Time
}

func NewLectureProgress(lectureID id.ID, state LectureState, positionMs int64, now time.Time) (LectureProgress, error) {
	if !state.Valid() {
		return LectureProgress{}, ErrInvalidLectureState
	}
	if positionMs < 0 {
		return LectureProgress{}, ErrNegativePosition
	}
	return LectureProgress{LectureID: lectureID, State: state, PositionMs: positionMs, UpdatedAt: now.UTC()}, nil
}

// CourseProgress is one user's progress in one course. The zero LastLectureID and
// UpdatedAt mean nothing was recorded.
type CourseProgress struct {
	CourseID      id.ID
	UserID        id.ID
	LastLectureID id.ID
	Lectures      []LectureProgress // ordered by LectureID
	UpdatedAt     time.Time
}

// CompletedLectureIDs returns the completed lectures in Lectures order.
func (p CourseProgress) CompletedLectureIDs() []id.ID {
	out := []id.ID{}
	for _, l := range p.Lectures {
		if l.State == LectureStateCompleted {
			out = append(out, l.LectureID)
		}
	}
	return out
}

// CompletedAll reports whether every lecture in lectureIDs is completed. A course without
// lectures is never complete.
func (p CourseProgress) CompletedAll(lectureIDs []id.ID) bool {
	if len(lectureIDs) == 0 {
		return false
	}
	done := p.CompletedLectureIDs()
	for _, l := range lectureIDs {
		if !slices.Contains(done, l) {
			return false
		}
	}
	return true
}

// ActivityDay counts the distinct lectures whose latest write fell on Date (UTC midnight).
type ActivityDay struct {
	Date         time.Time
	LectureCount int
}
