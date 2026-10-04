package outbox

import (
	"errors"
	"testing"

	"github.com/ThreeDotsLabs/watermill/message"
)

func TestDispatcherCallsHandlersInOrderAndStopsAtFirstError(t *testing.T) {
	var calls []string
	errFail := errors.New("fail")
	d := &dispatcher{handlers: map[string][]Handler{}}
	d.handlers["t"] = []Handler{
		func(m *message.Message) error { calls = append(calls, "a:"+m.UUID); return nil },
		func(m *message.Message) error { calls = append(calls, "b:"+m.UUID); return errFail },
		func(m *message.Message) error { calls = append(calls, "c:"+m.UUID); return nil },
	}
	err := d.Publish("t", message.NewMessage("m1", nil))
	if !errors.Is(err, errFail) {
		t.Fatalf("err = %v, want %v", err, errFail)
	}
	if len(calls) != 2 || calls[0] != "a:m1" || calls[1] != "b:m1" {
		t.Fatalf("calls = %v", calls)
	}
}

func TestDispatcherWithoutHandlersAcknowledges(t *testing.T) {
	d := &dispatcher{handlers: map[string][]Handler{}}
	if err := d.Publish("unknown", message.NewMessage("m1", nil)); err != nil {
		t.Fatalf("err = %v", err)
	}
}
