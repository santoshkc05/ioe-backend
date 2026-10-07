package app

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ErrUnknownUser is returned by a UserDirectory for a user that does not exist.
var ErrUnknownUser = errors.New("unknown user")

// UserDirectory is backed by identity.
type UserDirectory interface {
	Contact(ctx context.Context, userID string) (email, name string, err error)
}

// PurchasePaidInput is the data of one payment.purchase.paid event.
type PurchasePaidInput struct {
	EventID      string // outbox message UUID; stable across redeliveries
	UserID       string
	CourseID     string
	CourseTitle  string
	AmountMinor  int64
	Currency     string
	Gateway      string
	ManualMethod string
	PaidAt       time.Time
}

// PurchasePaidEmail is the display data of the payment-received email. Every field is
// already formatted; CourseURL and Name may be empty.
type PurchasePaidEmail struct {
	Name        string
	CourseTitle string
	Amount      string
	Method      string
	PaidOn      string
	CourseURL   string
}

// nepalTime is Nepal Standard Time (UTC+05:45, no daylight saving), fixed so the binary needs no tzdata.
var nepalTime = time.FixedZone("NPT", 5*3600+45*60)

// SendPurchasePaid emails the buyer that their payment was received, exactly once per event.
func (s *Service) SendPurchasePaid(ctx context.Context, in PurchasePaidInput) error {
	if in.EventID == "" || in.UserID == "" {
		return fmt.Errorf("%w: purchase paid requires an event ID and a user ID", ErrPermanent)
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
	e := PurchasePaidEmail{
		Name: strings.TrimSpace(name), CourseTitle: in.CourseTitle, Amount: formatMoney(in.AmountMinor, in.Currency),
		Method: methodLabel(in.Gateway, in.ManualMethod), PaidOn: in.PaidAt.In(nepalTime).Format("2 January 2006, 15:04 MST"),
	}
	if s.courseURLBase != "" && in.CourseID != "" {
		e.CourseURL = s.courseURLBase + "/courses/" + in.CourseID
	}
	subject, text, html, err := s.renderer.PurchasePaid(e)
	if err != nil {
		return fmt.Errorf("%w: render purchase paid: %w", ErrPermanent, err)
	}
	return s.mailer.Enqueue(ctx, Email{To: email, Subject: subject, Text: text, HTML: html}, PurchasePaidKey(in.EventID))
}

// PurchasePaidKey is the idempotency key for the email of one paid event.
// Changing the email's content requires a new version suffix.
func PurchasePaidKey(eventID string) string {
	return "ioe:payment.purchase.paid:" + eventID + ":paid-v1"
}

// formatMoney renders minor units with two decimals and thousands separators: "NPR 1,500.00".
func formatMoney(minor int64, currency string) string {
	whole := strconv.FormatInt(minor/100, 10)
	var b strings.Builder
	for i, r := range whole {
		if i > 0 && (len(whole)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	return fmt.Sprintf("%s %s.%02d", currency, b.String(), minor%100)
}

func methodLabel(gateway, manual string) string {
	switch {
	case gateway == "esewa":
		return "eSewa"
	case manual == "bank_transfer":
		return "Bank transfer"
	case manual == "cash":
		return "Cash"
	case manual == "other":
		return "Other"
	}
	return gateway
}
