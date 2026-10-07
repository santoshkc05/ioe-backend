// Package app holds the notification use cases and the ports they depend on.
package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ErrPermanent marks a failure that retrying the same request cannot fix.
var ErrPermanent = errors.New("permanent notification failure")

// Email is one fully rendered message.
type Email struct {
	To      string
	Subject string
	Text    string
	HTML    string
}

// Mailer enqueues one rendered email. It returns an error wrapping ErrPermanent when
// retrying the same request cannot succeed.
type Mailer interface {
	Enqueue(ctx context.Context, email Email, idempotencyKey string) error
}

// Renderer produces the subject and bodies of each email.
type Renderer interface {
	Welcome(name string) (subject, text, html string, err error)
	PurchasePaid(e PurchasePaidEmail) (subject, text, html string, err error)
}

// WelcomeInput is the data needed to welcome a newly registered user.
type WelcomeInput struct {
	EventID string // outbox message UUID; stable across redeliveries
	Email   string
	Name    string
}

type Service struct {
	mailer        Mailer
	renderer      Renderer
	users         UserDirectory
	courseURLBase string
}

// NewService builds the notification use cases. courseURLBase is the frontend origin used for
// course links; links are omitted when it is empty.
func NewService(mailer Mailer, renderer Renderer, users UserDirectory, courseURLBase string) *Service {
	return &Service{mailer: mailer, renderer: renderer, users: users, courseURLBase: strings.TrimRight(courseURLBase, "/")}
}

// SendWelcome renders the welcome email and enqueues it exactly once per event.
func (s *Service) SendWelcome(ctx context.Context, in WelcomeInput) error {
	if in.EventID == "" || in.Email == "" {
		return fmt.Errorf("%w: welcome requires an event ID and an email", ErrPermanent)
	}
	subject, text, html, err := s.renderer.Welcome(in.Name)
	if err != nil {
		return fmt.Errorf("%w: render welcome: %w", ErrPermanent, err)
	}
	return s.mailer.Enqueue(ctx, Email{To: in.Email, Subject: subject, Text: text, HTML: html}, WelcomeKey(in.EventID))
}

// WelcomeKey is the idempotency key for the welcome email of one registration event.
// Changing the email's content requires a new version suffix.
func WelcomeKey(eventID string) string {
	return "ioe:identity.user_registered:" + eventID + ":welcome-v1"
}
