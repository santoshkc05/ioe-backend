// Package events subscribes certificate to the domain events it consumes.
package events

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/ThreeDotsLabs/watermill/message"

	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

// EnrollmentCanceledTopic is enrollment's EnrollmentCanceled event name.
const EnrollmentCanceledTopic = "enrollment.enrollment.canceled"

// refundedReason is enrollment's cancel reason for an enrollment ended by a refund.
const refundedReason = "refunded"

const handlerTimeout = 30 * time.Second

// Revoker revokes a user's certificate for a course after a refund.
type Revoker interface {
	RevokeForRefund(ctx context.Context, userID, courseID id.ID, refundedAt time.Time) error
}

// Handlers process the events certificate subscribes to.
type Handlers struct {
	revoker Revoker
	logger  *slog.Logger
}

func New(revoker Revoker, logger *slog.Logger) *Handlers {
	return &Handlers{revoker: revoker, logger: logger}
}

// enrollmentCanceled mirrors the enrollment.enrollment.canceled fields this context relies on.
type enrollmentCanceled struct {
	UserID     id.ID     `json:"user_id"`
	CourseID   id.ID     `json:"course_id"`
	Reason     string    `json:"reason"`
	OccurredAt time.Time `json:"occurred_at"`
}

// EnrollmentCanceled revokes the student's certificate when a refund ended their enrollment.
// It listens to enrollment rather than payment because payment announces a refund before the
// enrollment is canceled, and a claim in between would keep its certificate. Only certificates
// issued by the time of the cancel are revoked, so a late redelivery never revokes one the
// student earned again. A malformed payload is logged and acknowledged: retrying it can never
// succeed. Only a storage failure is returned, so the outbox redelivers it; revoking is idempotent.
func (h *Handlers) EnrollmentCanceled(msg *message.Message) error {
	var ev enrollmentCanceled
	if err := json.Unmarshal(msg.Payload, &ev); err != nil || ev.UserID.IsZero() || ev.CourseID.IsZero() || ev.OccurredAt.IsZero() {
		h.logger.WarnContext(msg.Context(), "certificate: dropping malformed enrollment.canceled event",
			"message_id", msg.UUID, "error", err)
		return nil
	}
	if ev.Reason != refundedReason {
		return nil
	}
	ctx, cancel := context.WithTimeout(msg.Context(), handlerTimeout)
	defer cancel()
	return h.revoker.RevokeForRefund(ctx, ev.UserID, ev.CourseID, ev.OccurredAt)
}
