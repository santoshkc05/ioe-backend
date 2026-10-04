package domain

import (
	"time"

	"github.com/google/uuid"
)

// Event is a domain event published through the outbox.
type Event interface {
	EventName() string
}

// UserRegistered is emitted once, when a Google account first signs in.
type UserRegistered struct {
	UserID     uuid.UUID `json:"user_id"`
	Email      string    `json:"email"`
	OccurredAt time.Time `json:"occurred_at"`
}

func (UserRegistered) EventName() string { return "identity.user_registered" }
