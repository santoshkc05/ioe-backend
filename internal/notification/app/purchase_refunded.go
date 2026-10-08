package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// PurchaseRefundedInput is the data of one payment.purchase.refunded event.
type PurchaseRefundedInput struct {
	EventID       string // outbox message UUID; stable across redeliveries
	UserID        string
	CourseID      string
	CourseTitle   string
	AmountMinor   int64
	Currency      string
	Gateway       string
	ManualMethod  string
	Reference     string
	AccessRevoked bool
	RefundedAt    time.Time
}

// PurchaseRefundedEmail is the display data of the refund-issued email. Every field is already
// formatted; Name may be empty.
type PurchaseRefundedEmail struct {
	Name          string
	CourseTitle   string
	Amount        string
	Method        string
	Reference     string
	RefundedOn    string
	AccessRevoked bool
}

// SendPurchaseRefunded emails the buyer that their payment was refunded, exactly once per event.
func (s *Service) SendPurchaseRefunded(ctx context.Context, in PurchaseRefundedInput) error {
	if in.EventID == "" || in.UserID == "" {
		return fmt.Errorf("%w: purchase refunded requires an event ID and a user ID", ErrPermanent)
	}
	email, name, err := s.users.Contact(ctx, in.UserID)
	if errors.Is(err, ErrUnknownUser) {
		return fmt.Errorf("%w: %w", ErrPermanent, err)
	}
	if err != nil {
		return err
	}
	if email == "" {
		return fmt.Errorf("%w: user %s has no email", ErrPermanent, in.UserID)
	}
	e := PurchaseRefundedEmail{
		Name: strings.TrimSpace(name), CourseTitle: in.CourseTitle, Amount: formatMoney(in.AmountMinor, in.Currency),
		Method: methodLabel(in.Gateway, in.ManualMethod), Reference: in.Reference,
		RefundedOn: in.RefundedAt.In(nepalTime).Format("2 January 2006, 15:04 MST"), AccessRevoked: in.AccessRevoked,
	}
	subject, text, html, err := s.renderer.PurchaseRefunded(e)
	if err != nil {
		return fmt.Errorf("%w: render purchase refunded: %w", ErrPermanent, err)
	}
	return s.mailer.Enqueue(ctx, Email{To: email, Subject: subject, Text: text, HTML: html}, PurchaseRefundedKey(in.EventID))
}

// PurchaseRefundedKey is the idempotency key for the email of one refunded event.
// Changing the email's content requires a new version suffix.
func PurchaseRefundedKey(eventID string) string {
	return "ioe:payment.purchase.refunded:" + eventID + ":refunded-v1"
}
