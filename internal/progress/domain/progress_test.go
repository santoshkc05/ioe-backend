package domain_test

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
	"github.com/santoshkc2200/ioe-backend/internal/progress/domain"
)

func TestNewLectureProgress(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.FixedZone("NPT", 20700))
	p, err := domain.NewLectureProgress(5, domain.LectureStateInProgress, 900, now)
	if err != nil || p.LectureID != 5 || p.State != domain.LectureStateInProgress || p.PositionMs != 900 || !p.UpdatedAt.Equal(now) || p.UpdatedAt.Location() != time.UTC {
		t.Fatalf("p=%+v err=%v", p, err)
	}
	if _, err := domain.NewLectureProgress(5, domain.LectureStateCompleted, 0, now); err != nil {
		t.Fatalf("completed at 0: %v", err)
	}
	for _, s := range []domain.LectureState{"", "done", "COMPLETED"} {
		if _, err := domain.NewLectureProgress(5, s, 0, now); !errors.Is(err, domain.ErrInvalidLectureState) {
			t.Fatalf("state %q err = %v", s, err)
		}
	}
	if _, err := domain.NewLectureProgress(5, domain.LectureStateInProgress, -1, now); !errors.Is(err, domain.ErrNegativePosition) {
		t.Fatalf("negative err = %v", err)
	}
}

func TestCompletedLectureIDs(t *testing.T) {
	p := domain.CourseProgress{Lectures: []domain.LectureProgress{
		{LectureID: 3, State: domain.LectureStateCompleted},
		{LectureID: 7, State: domain.LectureStateInProgress},
		{LectureID: 9, State: domain.LectureStateCompleted},
	}}
	if got := p.CompletedLectureIDs(); !slices.Equal(got, []id.ID{3, 9}) {
		t.Fatalf("completed = %v", got)
	}
	if got := (domain.CourseProgress{}).CompletedLectureIDs(); got == nil || len(got) != 0 {
		t.Fatalf("empty completed = %#v", got)
	}
}
