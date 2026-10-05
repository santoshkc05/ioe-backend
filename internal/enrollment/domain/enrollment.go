package domain

import (
	"strings"
	"time"
	"unicode/utf8"

	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

type Status string

const (
	StatusActive   Status = "active"
	StatusCanceled Status = "canceled"
)

// MaxReasonRunes bounds a cancellation reason after trimming.
const MaxReasonRunes = 500

// Enrollment is one user's access to one course. There is at most one per (course, user);
// re-enrolling reactivates it.
type Enrollment struct {
	ID           id.ID
	CourseID     id.ID
	UserID       id.ID
	Status       Status
	CancelReason string
	EnrolledAt   time.Time
	CanceledAt   time.Time // zero unless canceled
	Version      int64
}

func NewEnrollment(enrollmentID, courseID, userID id.ID, now time.Time) (Enrollment, Event) {
	e := Enrollment{ID: enrollmentID, CourseID: courseID, UserID: userID, Status: StatusActive, EnrolledAt: now}
	return e, e.activated(now)
}

func (e *Enrollment) IsActive() bool { return e.Status == StatusActive }

// Cancel ends an active enrollment. Canceling a canceled enrollment changes nothing and
// returns no event.
func (e *Enrollment) Cancel(reason string, now time.Time) (Event, error) {
	reason = strings.TrimSpace(reason)
	if utf8.RuneCountInString(reason) > MaxReasonRunes {
		return nil, ErrInvalidReason
	}
	if !e.IsActive() {
		return nil, nil
	}
	e.Status, e.CancelReason, e.CanceledAt = StatusCanceled, reason, now
	return EnrollmentCanceled{EnrollmentID: e.ID, CourseID: e.CourseID, UserID: e.UserID, Reason: reason, OccurredAt: now}, nil
}

// Reactivate restores a canceled enrollment. Reactivating an active enrollment changes
// nothing and returns no event.
func (e *Enrollment) Reactivate(now time.Time) Event {
	if e.IsActive() {
		return nil
	}
	e.Status, e.CancelReason, e.CanceledAt, e.EnrolledAt = StatusActive, "", time.Time{}, now
	return e.activated(now)
}

func (e *Enrollment) activated(now time.Time) Event {
	return EnrollmentActivated{EnrollmentID: e.ID, CourseID: e.CourseID, UserID: e.UserID, OccurredAt: now}
}
