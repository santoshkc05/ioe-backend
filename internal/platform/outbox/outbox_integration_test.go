//go:build integration

package outbox_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/santoshkc2200/ioe-backend/internal/platform/outbox"
	"github.com/santoshkc2200/ioe-backend/internal/platform/postgres/pgtest"
)

func publish(ctx context.Context, t *testing.T, pool *pgxpool.Pool, topic, id, payload string) {
	t.Helper()
	if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		return outbox.Publish(ctx, tx, topic, message.NewMessage(id, []byte(payload)))
	}); err != nil {
		t.Fatal(err)
	}
}

// startForwarder runs a forwarder with handlers until the returned stop function is called
// (or the test ends). stop returns only after Run has returned and the forwarder is closed.
func startForwarder(ctx context.Context, t *testing.T, pool *pgxpool.Pool, handlers map[string]outbox.Handler) (stop func()) {
	t.Helper()
	fw, err := outbox.NewForwarder(pool, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	for topic, h := range handlers {
		fw.Handle(topic, h)
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = fw.Run(runCtx)
	}()
	var once sync.Once
	stop = func() {
		once.Do(func() {
			cancel()
			<-done
			_ = fw.Close()
		})
	}
	t.Cleanup(stop)
	return stop
}

func receive(ctx context.Context, t *testing.T, ch <-chan string) string {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-ctx.Done():
		t.Fatal("timed out waiting for a forwarded message")
		return ""
	}
}

func TestRolledBackPublishWritesNothing(t *testing.T) {
	ctx := context.Background()
	pool := pgtest.New(t)
	errAbort := errors.New("abort")
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if err := outbox.Publish(ctx, tx, "test.topic", message.NewMessage("m1", []byte(`{}`))); err != nil {
			return err
		}
		return errAbort
	})
	if !errors.Is(err, errAbort) {
		t.Fatalf("err = %v", err)
	}
	var n int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM platform.outbox_messages").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("outbox rows = %d after rollback", n)
	}
}

func TestCommittedPublishIsForwardedWithTraceContext(t *testing.T) {
	otel.SetTextMapPropagator(propagation.TraceContext{})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := pgtest.New(t)

	type forwarded struct{ payload, traceparent string }
	got := make(chan forwarded, 1)
	startForwarder(ctx, t, pool, map[string]outbox.Handler{"test.topic": func(m *message.Message) error {
		got <- forwarded{payload: string(m.Payload), traceparent: m.Metadata.Get("traceparent")}
		return nil
	}})

	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    trace.TraceID{0xaa, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15},
		SpanID:     trace.SpanID{1, 2, 3, 4, 5, 6, 7, 8},
		TraceFlags: trace.FlagsSampled,
	})
	publish(trace.ContextWithSpanContext(ctx, sc), t, pool, "test.topic", "m2", `{"ok":true}`)

	select {
	case m := <-got:
		if m.payload != `{"ok":true}` {
			t.Fatalf("payload = %s", m.payload)
		}
		if !strings.Contains(m.traceparent, sc.TraceID().String()) {
			t.Fatalf("traceparent = %q", m.traceparent)
		}
	case <-ctx.Done():
		t.Fatal("message not forwarded")
	}
}

func TestFailingAndPanickingHandlerIsRetriedUntilItSucceeds(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool := pgtest.New(t)

	var calls atomic.Int32
	done := make(chan string, 1)
	startForwarder(ctx, t, pool, map[string]outbox.Handler{"test.topic": func(m *message.Message) error {
		switch calls.Add(1) {
		case 1:
			panic("handler bug")
		case 2:
			return errors.New("transient")
		default:
			done <- m.UUID
			return nil
		}
	}})
	publish(ctx, t, pool, "test.topic", "m1", `{}`)

	if got := receive(ctx, t, done); got != "m1" {
		t.Fatalf("delivered %q, want m1", got)
	}
	if n := calls.Load(); n != 3 {
		t.Fatalf("handler calls = %d, want 3", n)
	}
}

func TestClosedForwarderRedeliversUnacknowledgedMessage(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool := pgtest.New(t)

	attempted := make(chan string, 1)
	stop := startForwarder(ctx, t, pool, map[string]outbox.Handler{"test.topic": func(m *message.Message) error {
		select {
		case attempted <- m.UUID:
		default:
		}
		return errors.New("notification service down")
	}})
	publish(ctx, t, pool, "test.topic", "m1", `{}`)
	if got := receive(ctx, t, attempted); got != "m1" {
		t.Fatalf("attempted %q, want m1", got)
	}
	stop()

	delivered := make(chan string, 1)
	startForwarder(ctx, t, pool, map[string]outbox.Handler{"test.topic": func(m *message.Message) error {
		delivered <- m.UUID
		return nil
	}})
	if got := receive(ctx, t, delivered); got != "m1" {
		t.Fatalf("redelivered %q, want m1", got)
	}
}

func TestMessageWithoutHandlerIsAcknowledged(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool := pgtest.New(t)

	got := make(chan string, 2)
	stop := startForwarder(ctx, t, pool, map[string]outbox.Handler{"test.handled": func(m *message.Message) error {
		got <- m.UUID
		return nil
	}})
	publish(ctx, t, pool, "test.unhandled", "m1", `{}`)
	publish(ctx, t, pool, "test.handled", "m2", `{}`)
	if v := receive(ctx, t, got); v != "m2" {
		t.Fatalf("got %q, want m2", v)
	}
	// Wait for the subscriber to commit the acked offsets before stopping it,
	// so the cancel does not abort the commit transaction.
	var maxOffset int64
	if err := pool.QueryRow(ctx, `SELECT max("offset") FROM platform.outbox_messages`).Scan(&maxOffset); err != nil {
		t.Fatal(err)
	}
	for {
		var acked int64
		_ = pool.QueryRow(ctx, "SELECT COALESCE(offset_acked, 0) FROM platform.outbox_offsets WHERE consumer_group = 'forwarder'").Scan(&acked)
		if acked >= maxOffset {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("timed out waiting for offset commit")
		case <-time.After(10 * time.Millisecond):
		}
	}
	stop()

	// Messages are forwarded in offset order, so a redelivered m1 would arrive before m3.
	startForwarder(ctx, t, pool, map[string]outbox.Handler{
		"test.unhandled": func(m *message.Message) error { got <- "unhandled:" + m.UUID; return nil },
		"test.handled":   func(m *message.Message) error { got <- m.UUID; return nil },
	})
	publish(ctx, t, pool, "test.handled", "m3", `{}`)
	if v := receive(ctx, t, got); v != "m3" {
		t.Fatalf("got %q, want m3 (m1 must not be redelivered)", v)
	}
}
