package httpserver_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/santoshkc2200/ioe-backend/internal/platform/httpserver"
)

func options() httpserver.Options {
	return httpserver.Options{
		Logger:         slog.New(slog.NewJSONHandler(io.Discard, nil)),
		AllowedOrigins: []string{"https://app.example.com"},
		ServiceName:    "test",
	}
}

func TestRouterSpanUsesRouteTemplateAndOmitsSecrets(t *testing.T) {
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	r, h := httpserver.NewRouter(options())
	r.HandleFunc("/v1/things/{id}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}).Methods(http.MethodGet)

	rq := httptest.NewRequest(http.MethodGet, "/v1/things/123", nil)
	rq.Header.Set("Authorization", "Bearer super-secret")
	rq.Header.Set("Cookie", "ioe_refresh=cookie-secret")
	h.ServeHTTP(httptest.NewRecorder(), rq)

	spans := exp.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("spans = %d", len(spans))
	}
	if !strings.Contains(spans[0].Name, "/v1/things/{id}") {
		t.Fatalf("span name %q is not the route template", spans[0].Name)
	}
	for _, a := range spans[0].Attributes {
		v := a.Value.Emit()
		if strings.Contains(v, "super-secret") || strings.Contains(v, "cookie-secret") {
			t.Fatalf("attribute %s leaked a secret", a.Key)
		}
	}
}

func TestRouterNotFoundIsProblemWithHeaders(t *testing.T) {
	_, h := httpserver.NewRouter(options())
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/nope", nil))
	if w.Code != http.StatusNotFound || w.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("%d %q", w.Code, w.Header().Get("Content-Type"))
	}
	if w.Header().Get("X-Content-Type-Options") != "nosniff" || w.Header().Get("X-Request-ID") == "" {
		t.Fatalf("outer middleware skipped on 404: %v", w.Header())
	}
}

func TestHealthEndpoints(t *testing.T) {
	r, h := httpserver.NewRouter(options())
	var readyErr error
	httpserver.MountHealth(r, func(context.Context) error { return readyErr })

	for path, want := range map[string]int{"/healthz": 200, "/readyz": 200} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != want {
			t.Fatalf("%s = %d", path, w.Code)
		}
	}
	readyErr = errors.New("db down")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if w.Code != http.StatusServiceUnavailable || strings.Contains(w.Body.String(), "db down") {
		t.Fatalf("readyz failing: %d %s", w.Code, w.Body.String())
	}
}
