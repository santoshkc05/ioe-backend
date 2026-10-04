package logging_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/trace"

	"github.com/santoshkc2200/ioe-backend/internal/platform/logging"
)

func TestRedactsSensitiveKeysEverywhere(t *testing.T) {
	var out, extraOut bytes.Buffer
	extra := slog.NewJSONHandler(&extraOut, &slog.HandlerOptions{Level: slog.LevelDebug})
	log := logging.New(slog.LevelInfo, &out, extra)

	log.With("id_token", "secret-1").Info("msg",
		"Authorization", "Bearer secret-2",
		slog.Group("req", "cookie", "secret-3", "path", "/v1/me"),
		"refresh_token", "secret-4",
	)

	for name, buf := range map[string]*bytes.Buffer{"stdout": &out, "extra": &extraOut} {
		s := buf.String()
		for _, secret := range []string{"secret-1", "secret-2", "secret-3", "secret-4"} {
			if strings.Contains(s, secret) {
				t.Errorf("%s output leaked %s: %s", name, secret, s)
			}
		}
		if !strings.Contains(s, "[REDACTED]") || !strings.Contains(s, "/v1/me") {
			t.Errorf("%s output missing redaction marker or safe value: %s", name, s)
		}
	}
}

func TestLevelAppliesToAllOutputs(t *testing.T) {
	var out, extraOut bytes.Buffer
	extra := slog.NewJSONHandler(&extraOut, &slog.HandlerOptions{Level: slog.LevelDebug})
	logging.New(slog.LevelInfo, &out, extra).Debug("hidden")
	if out.Len() != 0 || extraOut.Len() != 0 {
		t.Fatalf("debug record emitted: %q %q", out.String(), extraOut.String())
	}
}

func TestAddsRequestAndTraceIDs(t *testing.T) {
	var out bytes.Buffer
	log := logging.New(slog.LevelInfo, &out, nil)
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    trace.TraceID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16},
		SpanID:     trace.SpanID{1, 2, 3, 4, 5, 6, 7, 8},
		TraceFlags: trace.FlagsSampled,
	})
	ctx := trace.ContextWithSpanContext(logging.WithRequestID(context.Background(), "req-1"), sc)
	log.InfoContext(ctx, "msg")
	s := out.String()
	for _, want := range []string{`"request_id":"req-1"`, `"trace_id":"` + sc.TraceID().String(), `"span_id":"` + sc.SpanID().String()} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %s in %s", want, s)
		}
	}
}
