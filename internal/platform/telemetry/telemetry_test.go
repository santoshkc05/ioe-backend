package telemetry_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"go.opentelemetry.io/otel"

	"github.com/santoshkc2200/ioe-backend/internal/platform/telemetry"
)

func TestSetupDisabledWithoutEndpoint(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	tel, err := telemetry.Setup(context.Background(), "test")
	if err != nil {
		t.Fatal(err)
	}
	if tel.LogHandler != nil {
		t.Fatal("log handler set while disabled")
	}
	if err := tel.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSetupDisabledFlagWins(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:1")
	t.Setenv("OTEL_SDK_DISABLED", "true")
	tel, err := telemetry.Setup(context.Background(), "test")
	if err != nil || tel.LogHandler != nil {
		t.Fatalf("tel=%+v err=%v", tel, err)
	}
}

func TestSetupExportsTracesOverOTLPHTTP(t *testing.T) {
	var mu sync.Mutex
	paths := map[string]bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths[r.URL.Path] = true
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", srv.URL+"/api/default")
	t.Setenv("OTEL_SDK_DISABLED", "")

	ctx := context.Background()
	tel, err := telemetry.Setup(ctx, "test")
	if err != nil {
		t.Fatal(err)
	}
	if tel.LogHandler == nil {
		t.Fatal("log handler missing while enabled")
	}
	_, span := otel.Tracer("test").Start(ctx, "unit")
	span.End()
	if err := tel.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !paths["/api/default/v1/traces"] {
		t.Fatalf("no trace export; saw %v", paths)
	}
}
