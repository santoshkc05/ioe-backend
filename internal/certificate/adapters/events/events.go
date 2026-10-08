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

// PurchaseRefundedTopic is payment's PurchaseRefunded event name.
const PurchaseRefundedTopic = "payment.purchase.refunded"

const handlerTimeout = 30 * time.Second

// Revoker revokes a user's certificate for a course after a refund.
type Revoker interface {
	RevokeForRefund(ctx context.Context, userID, courseID id.ID) error
}

// Handlers process the events certificate subscribes to.
type Handlers struct {
	revoker Revoker
	logger  *slog.Logger
}

func New(revoker Revoker, logger *slog.Logger) *Handlers {
	return &Handlers{revoker: revoker, logger: logger}
}

// purchaseRefunded mirrors the payment.purchase.refunded fields this context relies on.
type purchaseRefunded struct {
	UserID        id.ID `json:"user_id"`
	CourseID      id.ID `json:"course_id"`
	AccessRevoked bool  `json:"access_revoked"`
}

// PurchaseRefunded revokes the buyer's certificate when the refund also ended their access.
// A malformed payload is logged and acknowledged: retrying it can never succeed. Only a
// storage failure is returned, so the outbox redelivers it; revoking is idempotent.
func (h *Handlers) PurchaseRefunded(msg *message.Message) error {
	var ev purchaseRefunded
	if err := json.Unmarshal(msg.Payload, &ev); err != nil || ev.UserID.IsZero() || ev.CourseID.IsZero() {
		h.logger.WarnContext(msg.Context(), "certificate: dropping malformed purchase.refunded event",
			"message_id", msg.UUID, "error", err)
		return nil
	}
	if !ev.AccessRevoked {
		return nil
	}
	ctx, cancel := context.WithTimeout(msg.Context(), handlerTimeout)
	defer cancel()
	return h.revoker.RevokeForRefund(ctx, ev.UserID, ev.CourseID)
}
