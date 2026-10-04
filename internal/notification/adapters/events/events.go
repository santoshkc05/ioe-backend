// Package events turns outbox events into notification use-case calls.
package events

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"

	"github.com/ThreeDotsLabs/watermill/message"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/santoshkc2200/ioe-backend/internal/notification/app"
)

// WelcomeSender is the use case behind the welcome handler.
type WelcomeSender interface {
	SendWelcome(ctx context.Context, in app.WelcomeInput) error
}

// Handlers holds one outbox handler per consumed event.
type Handlers struct {
	welcome WelcomeSender
	logger  *slog.Logger
	dropped metric.Int64Counter
}

func New(welcome WelcomeSender, logger *slog.Logger, meter metric.Meter) (*Handlers, error) {
	dropped, err := meter.Int64Counter("notification.events.dropped",
		metric.WithDescription("Events acknowledged without sending a notification."))
	if err != nil {
		return nil, err
	}
	return &Handlers{welcome: welcome, logger: logger, dropped: dropped}, nil
}

// userRegistered mirrors the identity.user_registered payload this context relies on.
type userRegistered struct {
	Email string `json:"email"`
	Name  string `json:"name"`
}

// Welcome handles identity.user_registered. It returns an error only when the message
// should be retried; malformed events and permanent failures are logged and acknowledged.
func (h *Handlers) Welcome(msg *message.Message) error {
	var ev userRegistered
	if err := json.Unmarshal(msg.Payload, &ev); err != nil {
		h.drop(msg, "invalid_payload", err)
		return nil
	}
	err := h.welcome.SendWelcome(msg.Context(), app.WelcomeInput{EventID: msg.UUID, Email: ev.Email, Name: ev.Name})
	if errors.Is(err, app.ErrPermanent) {
		h.drop(msg, "permanent", err)
		return nil
	}
	return err
}

func (h *Handlers) drop(msg *message.Message, reason string, cause error) {
	ctx := msg.Context()
	h.logger.ErrorContext(ctx, "notification event dropped", "event_id", msg.UUID, "reason", reason, "error", cause.Error())
	h.dropped.Add(ctx, 1, metric.WithAttributes(attribute.String("reason", reason)))
}
