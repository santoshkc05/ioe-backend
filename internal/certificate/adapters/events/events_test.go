package events_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/ThreeDotsLabs/watermill/message"

	"github.com/santoshkc2200/ioe-backend/internal/certificate/adapters/events"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

type revoker struct {
	calls  [][2]id.ID
	failed error
}

func (r *revoker) RevokeForRefund(_ context.Context, userID, courseID id.ID) error {
	r.calls = append(r.calls, [2]id.ID{userID, courseID})
	return r.failed
}

func handlers(r *revoker) *events.Handlers {
	return events.New(r, slog.New(slog.NewJSONHandler(io.Discard, nil)))
}

func TestTopicMatchesPaymentEventName(t *testing.T) {
	if events.PurchaseRefundedTopic != "payment.purchase.refunded" {
		t.Fatalf("topic = %q", events.PurchaseRefundedTopic)
	}
}

func TestRevokesWhenAccessWasRevoked(t *testing.T) {
	r := &revoker{}
	msg := message.NewMessage("1", []byte(`{"purchase_id":"5","user_id":"200","course_id":"10","access_revoked":true,"occurred_at":"2026-10-08T09:00:00Z"}`))
	if err := handlers(r).PurchaseRefunded(msg); err != nil {
		t.Fatal(err)
	}
	if len(r.calls) != 1 || r.calls[0] != [2]id.ID{200, 10} {
		t.Fatalf("calls = %v", r.calls)
	}
}

func TestIgnoresRefundsThatKeepAccess(t *testing.T) {
	r := &revoker{}
	msg := message.NewMessage("1", []byte(`{"user_id":"200","course_id":"10","access_revoked":false}`))
	if err := handlers(r).PurchaseRefunded(msg); err != nil || len(r.calls) != 0 {
		t.Fatalf("err=%v calls=%v", err, r.calls)
	}
}

func TestMalformedPayloadsAreAcknowledged(t *testing.T) {
	for name, payload := range map[string]string{
		"not json":       `nope`,
		"empty object":   `{}`,
		"missing course": `{"user_id":"200","access_revoked":true}`,
		"missing user":   `{"course_id":"10","access_revoked":true}`,
		"zero id":        `{"user_id":"0","course_id":"10","access_revoked":true}`,
		"numeric ids":    `{"user_id":200,"course_id":10,"access_revoked":true}`,
		"null":           `null`,
	} {
		r := &revoker{}
		if err := handlers(r).PurchaseRefunded(message.NewMessage("1", []byte(payload))); err != nil || len(r.calls) != 0 {
			t.Errorf("%s: err=%v calls=%v", name, err, r.calls)
		}
	}
}

func TestStorageFailureIsRetried(t *testing.T) {
	r := &revoker{failed: errors.New("db down")}
	msg := message.NewMessage("1", []byte(`{"user_id":"200","course_id":"10","access_revoked":true}`))
	if err := handlers(r).PurchaseRefunded(msg); !errors.Is(err, r.failed) {
		t.Fatalf("err = %v, want the storage error so the outbox retries", err)
	}
}
