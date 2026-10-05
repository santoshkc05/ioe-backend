package domain_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/enrollment/domain"
)

var (
	t0 = time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	t1 = t0.Add(time.Hour)
	t2 = t1.Add(time.Hour)
)

func TestNewEnrollment(t *testing.T) {
	e, ev := domain.NewEnrollment(1, 10, 20, t0)
	if e.ID != 1 || e.CourseID != 10 || e.UserID != 20 || !e.IsActive() || !e.EnrolledAt.Equal(t0) || !e.CanceledAt.IsZero() {
		t.Fatalf("enrollment = %+v", e)
	}
	want := domain.EnrollmentActivated{EnrollmentID: 1, CourseID: 10, UserID: 20, OccurredAt: t0}
	if ev != want || ev.EventName() != "enrollment.enrollment.activated" {
		t.Fatalf("event = %#v", ev)
	}
}

func TestCancel(t *testing.T) {
	e, _ := domain.NewEnrollment(1, 10, 20, t0)
	ev, err := e.Cancel("  moving on  ", t1)
	if err != nil {
		t.Fatal(err)
	}
	if e.Status != domain.StatusCanceled || e.CancelReason != "moving on" || !e.CanceledAt.Equal(t1) {
		t.Fatalf("enrollment = %+v", e)
	}
	want := domain.EnrollmentCanceled{EnrollmentID: 1, CourseID: 10, UserID: 20, Reason: "moving on", OccurredAt: t1}
	if ev != want || ev.EventName() != "enrollment.enrollment.canceled" {
		t.Fatalf("event = %#v", ev)
	}

	ev, err = e.Cancel("again", t2)
	if err != nil || ev != nil || e.CancelReason != "moving on" || !e.CanceledAt.Equal(t1) {
		t.Fatalf("second cancel: ev=%v err=%v e=%+v", ev, err, e)
	}
}

func TestCancelReasonLimit(t *testing.T) {
	e, _ := domain.NewEnrollment(1, 10, 20, t0)
	if _, err := e.Cancel(strings.Repeat("é", domain.MaxReasonRunes+1), t1); !errors.Is(err, domain.ErrInvalidReason) {
		t.Fatalf("err = %v", err)
	}
	if !e.IsActive() {
		t.Fatal("rejected cancel changed state")
	}
	if _, err := e.Cancel(strings.Repeat("é", domain.MaxReasonRunes), t1); err != nil {
		t.Fatalf("500 runes rejected: %v", err)
	}
}

func TestReactivate(t *testing.T) {
	e, _ := domain.NewEnrollment(1, 10, 20, t0)
	if ev := e.Reactivate(t1); ev != nil {
		t.Fatalf("reactivating active emitted %v", ev)
	}
	_, _ = e.Cancel("r", t1)
	ev := e.Reactivate(t2)
	if !e.IsActive() || e.CancelReason != "" || !e.CanceledAt.IsZero() || !e.EnrolledAt.Equal(t2) {
		t.Fatalf("enrollment = %+v", e)
	}
	if ev != (domain.EnrollmentActivated{EnrollmentID: 1, CourseID: 10, UserID: 20, OccurredAt: t2}) {
		t.Fatalf("event = %#v", ev)
	}
}
