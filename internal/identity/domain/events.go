package domain

import (
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

// Event is a domain event published through the outbox.
type Event interface {
	EventName() string
}

// UserRegistered is emitted once, when a Google account first signs in.
type UserRegistered struct {
	UserID     id.ID     `json:"user_id"`
	Email      string    `json:"email"`
	Name       string    `json:"name"`
	OccurredAt time.Time `json:"occurred_at"`
}

func (UserRegistered) EventName() string { return "identity.user_registered" }

// UserRoleChanged records a root admin changing a user's role.
type UserRoleChanged struct {
	UserID       id.ID     `json:"user_id"`
	PreviousRole auth.Role `json:"previous_role"`
	Role         auth.Role `json:"role"`
	ChangedBy    id.ID     `json:"changed_by"`
	OccurredAt   time.Time `json:"occurred_at"`
}

func (UserRoleChanged) EventName() string { return "identity.user_role_changed" }
