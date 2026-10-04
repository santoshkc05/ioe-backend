package app_test

import (
	"context"
	"errors"
	"testing"

	"github.com/santoshkc2200/ioe-backend/internal/notification/app"
)

type fakeMailer struct {
	calls int
	email app.Email
	key   string
	err   error
}

func (f *fakeMailer) Enqueue(_ context.Context, email app.Email, key string) error {
	f.calls++
	f.email, f.key = email, key
	return f.err
}

type fakeRenderer struct {
	name string
	err  error
}

func (f *fakeRenderer) Welcome(name string) (string, string, string, error) {
	f.name = name
	return "Subject", "Text", "<p>HTML</p>", f.err
}

func TestSendWelcomeRendersAndEnqueues(t *testing.T) {
	m, r := &fakeMailer{}, &fakeRenderer{}
	err := app.NewService(m, r).SendWelcome(context.Background(), app.WelcomeInput{EventID: "ev-1", Email: "a@example.com", Name: "Alice"})
	if err != nil {
		t.Fatal(err)
	}
	if r.name != "Alice" {
		t.Fatalf("rendered name %q", r.name)
	}
	want := app.Email{To: "a@example.com", Subject: "Subject", Text: "Text", HTML: "<p>HTML</p>"}
	if m.calls != 1 || m.email != want {
		t.Fatalf("calls=%d email=%+v", m.calls, m.email)
	}
	if m.key != "ioe:identity.user_registered:ev-1:welcome-v1" {
		t.Fatalf("key %q", m.key)
	}
}

func TestSendWelcomeRejectsMissingFieldsAsPermanent(t *testing.T) {
	for name, in := range map[string]app.WelcomeInput{
		"no event id": {Email: "a@example.com"},
		"no email":    {EventID: "ev-1"},
	} {
		t.Run(name, func(t *testing.T) {
			m := &fakeMailer{}
			err := app.NewService(m, &fakeRenderer{}).SendWelcome(context.Background(), in)
			if !errors.Is(err, app.ErrPermanent) {
				t.Fatalf("err = %v, want ErrPermanent", err)
			}
			if m.calls != 0 {
				t.Fatal("mailer called")
			}
		})
	}
}

func TestSendWelcomeRenderFailureIsPermanent(t *testing.T) {
	m := &fakeMailer{}
	err := app.NewService(m, &fakeRenderer{err: errors.New("bad template")}).
		SendWelcome(context.Background(), app.WelcomeInput{EventID: "ev-1", Email: "a@example.com"})
	if !errors.Is(err, app.ErrPermanent) || m.calls != 0 {
		t.Fatalf("err = %v, calls = %d", err, m.calls)
	}
}

func TestSendWelcomePropagatesMailerError(t *testing.T) {
	errDown := errors.New("down")
	err := app.NewService(&fakeMailer{err: errDown}, &fakeRenderer{}).
		SendWelcome(context.Background(), app.WelcomeInput{EventID: "ev-1", Email: "a@example.com"})
	if !errors.Is(err, errDown) || errors.Is(err, app.ErrPermanent) {
		t.Fatalf("err = %v", err)
	}
}
