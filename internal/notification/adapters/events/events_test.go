package events_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/ThreeDotsLabs/watermill/message"
	"go.opentelemetry.io/otel/metric/noop"

	"github.com/santoshkc2200/ioe-backend/internal/notification/adapters/events"
	"github.com/santoshkc2200/ioe-backend/internal/notification/app"
)

type fakeSender struct {
	calls int
	in    app.WelcomeInput
	err   error

	paidCalls int
	paid      app.PurchasePaidInput

	refundedCalls int
	refunded      app.PurchaseRefundedInput
}

func (f *fakeSender) SendWelcome(_ context.Context, in app.WelcomeInput) error {
	f.calls++
	f.in = in
	return f.err
}

func (f *fakeSender) SendPurchasePaid(_ context.Context, in app.PurchasePaidInput) error {
	f.paidCalls++
	f.paid = in
	return f.err
}

func (f *fakeSender) SendPurchaseRefunded(_ context.Context, in app.PurchaseRefundedInput) error {
	f.refundedCalls++
	f.refunded = in
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

const paidPayload = `{"purchase_id":"9","user_id":"200","course_id":"11","course_title":"Go","amount_minor":150000,
"currency":"NPR","gateway":"manual","gateway_txn":"R-1","manual_method":"cash","occurred_at":"2026-10-07T04:15:00Z"}`

func TestPurchasePaidSends(t *testing.T) {
	s := &fakeSender{}
	h, _ := newHandlers(t, s)
	if err := h.PurchasePaid(message.NewMessage("ev-9", []byte(paidPayload))); err != nil {
		t.Fatal(err)
	}
	want := app.PurchasePaidInput{EventID: "ev-9", UserID: "200", CourseID: "11", CourseTitle: "Go", AmountMinor: 150000,
		Currency: "NPR", Gateway: "manual", ManualMethod: "cash", PaidAt: time.Date(2026, 10, 7, 4, 15, 0, 0, time.UTC)}
	if s.paidCalls != 1 || s.paid != want {
		t.Fatalf("calls=%d in=%+v", s.paidCalls, s.paid)
	}
}

func TestPurchasePaidDropsInvalidPayloadAndPermanentFailure(t *testing.T) {
	s := &fakeSender{}
	h, logs := newHandlers(t, s)
	if err := h.PurchasePaid(message.NewMessage("ev-1", []byte("not json"))); err != nil || s.paidCalls != 0 {
		t.Fatalf("err=%v calls=%d", err, s.paidCalls)
	}
	s.err = fmt.Errorf("%w: unknown user", app.ErrPermanent)
	if err := h.PurchasePaid(message.NewMessage("ev-2", []byte(paidPayload))); err != nil {
		t.Fatalf("permanent err = %v", err)
	}
	if !strings.Contains(logs.String(), "invalid_payload") || !strings.Contains(logs.String(), "permanent") {
		t.Fatalf("logs = %s", logs)
	}
}

func TestPurchasePaidReturnsRetryableFailure(t *testing.T) {
	errDown := errors.New("down")
	h, _ := newHandlers(t, &fakeSender{err: errDown})
	if err := h.PurchasePaid(message.NewMessage("ev-1", []byte(paidPayload))); !errors.Is(err, errDown) {
		t.Fatalf("err = %v", err)
	}
}

const refundedPayload = `{"purchase_id":"9","user_id":"200","course_id":"11","course_title":"Go","amount_minor":150000,
"currency":"NPR","gateway":"manual","manual_method":"cash","refund_reference":"RF-1","access_revoked":true,
"occurred_at":"2026-10-08T04:15:00Z"}`

func TestPurchaseRefundedSends(t *testing.T) {
	s := &fakeSender{}
	h, _ := newHandlers(t, s)
	if err := h.PurchaseRefunded(message.NewMessage("ev-7", []byte(refundedPayload))); err != nil {
		t.Fatal(err)
	}
	want := app.PurchaseRefundedInput{EventID: "ev-7", UserID: "200", CourseID: "11", CourseTitle: "Go", AmountMinor: 150000,
		Currency: "NPR", Gateway: "manual", ManualMethod: "cash", Reference: "RF-1", AccessRevoked: true,
		RefundedAt: time.Date(2026, 10, 8, 4, 15, 0, 0, time.UTC)}
	if s.refundedCalls != 1 || s.refunded != want {
		t.Fatalf("calls=%d in=%+v", s.refundedCalls, s.refunded)
	}
}

func TestPurchaseRefundedDropsAndRetries(t *testing.T) {
	s := &fakeSender{}
	h, logs := newHandlers(t, s)
	if err := h.PurchaseRefunded(message.NewMessage("ev-1", []byte("not json"))); err != nil || s.refundedCalls != 0 {
		t.Fatalf("err=%v calls=%d", err, s.refundedCalls)
	}
	s.err = fmt.Errorf("%w: unknown user", app.ErrPermanent)
	if err := h.PurchaseRefunded(message.NewMessage("ev-2", []byte(refundedPayload))); err != nil {
		t.Fatalf("permanent err = %v", err)
	}
	if !strings.Contains(logs.String(), "invalid_payload") || !strings.Contains(logs.String(), "permanent") {
		t.Fatalf("logs = %s", logs)
	}
	errDown := errors.New("down")
	s.err = errDown
	if err := h.PurchaseRefunded(message.NewMessage("ev-3", []byte(refundedPayload))); !errors.Is(err, errDown) {
		t.Fatalf("retryable err = %v", err)
	}
}
