//go:build integration

package outbox_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/santoshkc2200/ioe-backend/internal/platform/outbox"
	"github.com/santoshkc2200/ioe-backend/internal/platform/postgres/pgtest"
)

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

	fw, err := outbox.NewForwarder(pool, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer fw.Close()
	msgs, err := fw.Subscriber().Subscribe(ctx, "test.topic")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = fw.Run(ctx) }()

	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    trace.TraceID{0xaa, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15},
		SpanID:     trace.SpanID{1, 2, 3, 4, 5, 6, 7, 8},
		TraceFlags: trace.FlagsSampled,
	})
	pubCtx := trace.ContextWithSpanContext(ctx, sc)
	if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		return outbox.Publish(pubCtx, tx, "test.topic", message.NewMessage("m2", []byte(`{"ok":true}`)))
	}); err != nil {
		t.Fatal(err)
	}

	select {
	case m := <-msgs:
		m.Ack()
		if string(m.Payload) != `{"ok":true}` {
			t.Fatalf("payload = %s", m.Payload)
		}
		if tp := m.Metadata.Get("traceparent"); !strings.Contains(tp, sc.TraceID().String()) {
			t.Fatalf("traceparent = %q", tp)
		}
	case <-ctx.Done():
		t.Fatal("message not forwarded")
	}
}
