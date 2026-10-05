package domain

import (
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

// Event is a domain event written to the outbox under its EventName.
type Event interface {
	EventName() string
}

// EnrollmentActivated is emitted when an enrollment is created or reactivated.
type EnrollmentActivated struct {
	EnrollmentID id.ID     `json:"enrollment_id"`
	CourseID     id.ID     `json:"course_id"`
	UserID       id.ID     `json:"user_id"`
	OccurredAt   time.Time `json:"occurred_at"`
}

func (EnrollmentActivated) EventName() string { return "enrollment.enrollment.activated" }

// EnrollmentCanceled is emitted when an active enrollment is canceled.
type EnrollmentCanceled struct {
	EnrollmentID id.ID     `json:"enrollment_id"`
	CourseID     id.ID     `json:"course_id"`
	UserID       id.ID     `json:"user_id"`
	Reason       string    `json:"reason"`
	OccurredAt   time.Time `json:"occurred_at"`
}

func (EnrollmentCanceled) EventName() string { return "enrollment.enrollment.canceled" }
