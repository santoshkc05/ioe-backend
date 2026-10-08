package events_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/ThreeDotsLabs/watermill/message"

	"github.com/santoshkc2200/ioe-backend/internal/certificate/adapters/events"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

type call struct {
	userID, courseID id.ID
	refundedAt       time.Time
}

type revoker struct {
	calls  []call
	failed error
}

func (r *revoker) RevokeForRefund(_ context.Context, userID, courseID id.ID, refundedAt time.Time) error {
	r.calls = append(r.calls, call{userID, courseID, refundedAt})
	return r.failed
}

func handlers(r *revoker) *events.Handlers {
	return events.New(r, slog.New(slog.NewJSONHandler(io.Discard, nil)))
}

func TestTopicMatchesEnrollmentEventName(t *testing.T) {
	if events.EnrollmentCanceledTopic != "enrollment.enrollment.canceled" {
		t.Fatalf("topic = %q", events.EnrollmentCanceledTopic)
	}
}

func TestRevokesWhenARefundCanceledTheEnrollment(t *testing.T) {
	r := &revoker{}
	msg := message.NewMessage("1", []byte(`{"enrollment_id":"5","user_id":"200","course_id":"10","reason":"refunded","occurred_at":"2026-10-08T09:00:00Z"}`))
	if err := handlers(r).EnrollmentCanceled(msg); err != nil {
		t.Fatal(err)
	}
	want := call{200, 10, time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC)}
	if len(r.calls) != 1 || r.calls[0].userID != want.userID || r.calls[0].courseID != want.courseID || !r.calls[0].refundedAt.Equal(want.refundedAt) {
		t.Fatalf("calls = %v", r.calls)
	}
}

func TestIgnoresCancelsForOtherReasons(t *testing.T) {
	r := &revoker{}
	msg := message.NewMessage("1", []byte(`{"user_id":"200","course_id":"10","reason":"admin","occurred_at":"2026-10-08T09:00:00Z"}`))
	if err := handlers(r).EnrollmentCanceled(msg); err != nil || len(r.calls) != 0 {
		t.Fatalf("err=%v calls=%v", err, r.calls)
	}
}

func TestMalformedPayloadsAreAcknowledged(t *testing.T) {
	for name, payload := range map[string]string{
		"not json":       `nope`,
		"empty object":   `{}`,
		"missing course": `{"user_id":"200","reason":"refunded","occurred_at":"2026-10-08T09:00:00Z"}`,
		"missing user":   `{"course_id":"10","reason":"refunded","occurred_at":"2026-10-08T09:00:00Z"}`,
		"missing time":   `{"user_id":"200","course_id":"10","reason":"refunded"}`,
		"zero id":        `{"user_id":"0","course_id":"10","reason":"refunded","occurred_at":"2026-10-08T09:00:00Z"}`,
		"numeric ids":    `{"user_id":200,"course_id":10,"reason":"refunded","occurred_at":"2026-10-08T09:00:00Z"}`,
		"null":           `null`,
	} {
		r := &revoker{}
		if err := handlers(r).EnrollmentCanceled(message.NewMessage("1", []byte(payload))); err != nil || len(r.calls) != 0 {
			t.Errorf("%s: err=%v calls=%v", name, err, r.calls)
		}
	}
}

func TestStorageFailureIsRetried(t *testing.T) {
	r := &revoker{failed: errors.New("db down")}
	msg := message.NewMessage("1", []byte(`{"user_id":"200","course_id":"10","reason":"refunded","occurred_at":"2026-10-08T09:00:00Z"}`))
	if err := handlers(r).EnrollmentCanceled(msg); !errors.Is(err, r.failed) {
		t.Fatalf("err = %v, want the storage error so the outbox retries", err)
	}
}
