package domain

import (
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

// Event is a domain event written to the outbox under its EventName.
type Event interface {
	EventName() string
}

type CoursePublished struct {
	CourseID         id.ID     `json:"course_id"`
	OwnerID          id.ID     `json:"owner_id"`
	PriceAmountMinor int64     `json:"price_amount_minor"`
	PriceCurrency    string    `json:"price_currency"`
	OccurredAt       time.Time `json:"occurred_at"`
}

func (CoursePublished) EventName() string { return "courseauthoring.course.published" }

type CourseArchived struct {
	CourseID   id.ID     `json:"course_id"`
	OwnerID    id.ID     `json:"owner_id"`
	OccurredAt time.Time `json:"occurred_at"`
}

func (CourseArchived) EventName() string { return "courseauthoring.course.archived" }

type CourseUnpublished struct {
	CourseID   id.ID     `json:"course_id"`
	OwnerID    id.ID     `json:"owner_id"`
	OccurredAt time.Time `json:"occurred_at"`
}

func (CourseUnpublished) EventName() string { return "courseauthoring.course.unpublished" }

// DraftDiscarded carries the live version's pins so assessment can reset its working copy.
type DraftDiscarded struct {
	CourseID   id.ID           `json:"course_id"`
	ActorID    id.ID           `json:"actor_id"`
	Pins       []AssessmentPin `json:"pins"`
	OccurredAt time.Time       `json:"occurred_at"`
}

func (DraftDiscarded) EventName() string { return "courseauthoring.course.draft_discarded" }
