package events_test

import (
	"maps"
	"testing"

	"github.com/ThreeDotsLabs/watermill/message"

	"github.com/santoshkc2200/ioe-backend/internal/assessment/adapters/events"
	"github.com/santoshkc2200/ioe-backend/internal/assessment/app"
)

func TestDecodeDraftDiscarded(t *testing.T) {
	msg := message.NewMessage("1", []byte(`{"course_id":"10","actor_id":"100","pins":[{"kind":"quiz","id":"700","revision":2},{"kind":"exam","id":"800","revision":1}],"occurred_at":"2026-10-07T09:00:00Z"}`))
	courseID, actorID, pins, err := events.DecodeDraftDiscarded(msg)
	if err != nil || courseID != 10 || actorID != 100 {
		t.Fatalf("decode: %v %v %v", courseID, actorID, err)
	}
	want := app.Pins{{Kind: app.KindQuiz, ID: 700}: 2, {Kind: app.KindExam, ID: 800}: 1}
	if !maps.Equal(pins, want) {
		t.Fatalf("pins = %v", pins)
	}
	bad := message.NewMessage("2", []byte(`{"course_id":"10","actor_id":"1","pins":[{"kind":"poll","id":"1","revision":1}]}`))
	if _, _, _, err := events.DecodeDraftDiscarded(bad); err == nil {
		t.Fatal("unknown kind accepted")
	}
}
