package events_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/ThreeDotsLabs/watermill/message"
	"go.opentelemetry.io/otel/metric/noop"

	"github.com/santoshkc2200/ioe-backend/internal/notification/adapters/events"
	"github.com/santoshkc2200/ioe-backend/internal/notification/app"
)

type fakeSender struct {
	calls int
	in    app.WelcomeInput
	err   error
}

func (f *fakeSender) SendWelcome(_ context.Context, in app.WelcomeInput) error {
	f.calls++
	f.in = in
	return f.err
}

func newHandlers(t *testing.T, sender *fakeSender) (*events.Handlers, *bytes.Buffer) {
	t.Helper()
	var logs bytes.Buffer
	h, err := events.New(sender, slog.New(slog.NewJSONHandler(&logs, nil)), noop.NewMeterProvider().Meter("test"))
	if err != nil {
		t.Fatal(err)
	}
	return h, &logs
}

func TestWelcomeSendsForRegisteredUser(t *testing.T) {
	s := &fakeSender{}
	h, _ := newHandlers(t, s)
	msg := message.NewMessage("ev-1", []byte(`{"user_id":"u1","email":"a@example.com","name":"Alice","occurred_at":"2026-10-04T00:00:00Z"}`))
	if err := h.Welcome(msg); err != nil {
		t.Fatal(err)
	}
	want := app.WelcomeInput{EventID: "ev-1", Email: "a@example.com", Name: "Alice"}
	if s.calls != 1 || s.in != want {
		t.Fatalf("calls=%d in=%+v", s.calls, s.in)
	}
}

func TestWelcomeAcceptsEventWithoutName(t *testing.T) {
	s := &fakeSender{}
	h, _ := newHandlers(t, s)
	if err := h.Welcome(message.NewMessage("ev-1", []byte(`{"user_id":"u1","email":"a@example.com"}`))); err != nil {
		t.Fatal(err)
	}
	if s.in.Name != "" || s.in.Email != "a@example.com" {
		t.Fatalf("in=%+v", s.in)
	}
}

func TestWelcomeDropsInvalidPayload(t *testing.T) {
	s := &fakeSender{}
	h, logs := newHandlers(t, s)
	if err := h.Welcome(message.NewMessage("ev-1", []byte(`{"email":"a@example.com"`))); err != nil {
		t.Fatalf("err = %v, want nil (acknowledged)", err)
	}
	if s.calls != 0 {
		t.Fatal("sender called")
	}
	if out := logs.String(); !strings.Contains(out, "invalid_payload") || !strings.Contains(out, "ev-1") || strings.Contains(out, "a@example.com") {
		t.Fatalf("log %q", out)
	}
}

func TestWelcomeDropsPermanentFailure(t *testing.T) {
	s := &fakeSender{err: fmt.Errorf("%w: notification service responded 409", app.ErrPermanent)}
	h, logs := newHandlers(t, s)
	if err := h.Welcome(message.NewMessage("ev-1", []byte(`{"email":"a@example.com","name":"Alice"}`))); err != nil {
		t.Fatalf("err = %v, want nil (acknowledged)", err)
	}
	if out := logs.String(); !strings.Contains(out, `"reason":"permanent"`) || strings.Contains(out, "a@example.com") || strings.Contains(out, "Alice") {
		t.Fatalf("log %q", out)
	}
}

func TestWelcomeReturnsRetryableFailure(t *testing.T) {
	errDown := errors.New("notification service request: connection refused")
	h, _ := newHandlers(t, &fakeSender{err: errDown})
	if err := h.Welcome(message.NewMessage("ev-1", []byte(`{"email":"a@example.com"}`))); !errors.Is(err, errDown) {
		t.Fatalf("err = %v, want %v", err, errDown)
	}
}
