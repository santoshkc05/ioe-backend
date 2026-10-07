package domain

import (
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

// MaxReviewNoteRunes bounds reviewer and approval notes.
const MaxReviewNoteRunes = 4000

type ReviewDecision string

const (
	DecisionSubmitted        ReviewDecision = "submitted"
	DecisionApproved         ReviewDecision = "approved"
	DecisionChangesRequested ReviewDecision = "changes_requested"
	DecisionUnpublished      ReviewDecision = "unpublished"
)

// Review is one immutable entry in a course's review trail. ActorID is the author for
// a submission and the reviewer otherwise.
type Review struct {
	ID        id.ID
	CourseID  id.ID
	ActorID   id.ID
	Decision  ReviewDecision
	Note      string
	CreatedAt time.Time
}
