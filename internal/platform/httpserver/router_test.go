package httpserver_test

import (
	"context"
	"encoding/json"
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

func TestRouterMethodNotAllowedIsProblemWithAllowHeader(t *testing.T) {
	r, h := httpserver.NewRouter(options())
	r.HandleFunc("/v1/items", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}).Methods(http.MethodGet)

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/items", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("code = %d, want 405", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("content-type = %q", ct)
	}
	if allow := w.Header().Get("Allow"); allow != "GET" {
		t.Fatalf("allow = %q, want GET", allow)
	}
	if w.Header().Get("X-Content-Type-Options") != "nosniff" || w.Header().Get("X-Request-ID") == "" {
		t.Fatalf("outer middleware skipped on 405: %v", w.Header())
	}
	var p struct {
		Type   string `json:"type"`
		Title  string `json:"title"`
		Status int    `json:"status"`
	}
	if err := json.NewDecoder(w.Body).Decode(&p); err != nil {
		t.Fatalf("decode problem: %v", err)
	}
	if p.Type != "method_not_allowed" || p.Status != http.StatusMethodNotAllowed {
		t.Fatalf("problem payload = %+v", p)
	}

	sub := r.PathPrefix("/v1/sub").Subrouter()
	sub.MethodNotAllowedHandler = r.MethodNotAllowedHandler
	sub.HandleFunc("/action", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}).Methods(http.MethodPost)

	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, httptest.NewRequest(http.MethodGet, "/v1/sub/action", nil))
	if w2.Code != http.StatusMethodNotAllowed {
		t.Fatalf("code = %d, want 405", w2.Code)
	}
	if allow := w2.Header().Get("Allow"); allow != "POST" {
		t.Fatalf("subrouter allow = %q, want POST", allow)
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
