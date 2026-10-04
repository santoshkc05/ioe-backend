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
	r.HandleFunc("GET /v1/things/{id}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	rq := httptest.NewRequest(http.MethodGet, "/v1/things/123", nil)
	rq.Header.Set("Authorization", "Bearer super-secret")
	rq.Header.Set("Cookie", "ioe_refresh=cookie-secret")
	h.ServeHTTP(httptest.NewRecorder(), rq)

	spans := exp.GetSpans()
	if len(spans) != 1 || spans[0].Name != "GET /v1/things/{id}" {
		t.Fatalf("spans = %+v", spans)
	}
	for _, a := range spans[0].Attributes {
		v := a.Value.Emit()
		if strings.Contains(v, "super-secret") || strings.Contains(v, "cookie-secret") {
			t.Fatalf("attribute %s leaked a secret", a.Key)
		}
	}
}

func TestRouterUnmatchedSpanName(t *testing.T) {
	exp := tracetest.NewInMemoryExporter()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp)))
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	_, h := httpserver.NewRouter(options())
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/nope/123", nil))
	if spans := exp.GetSpans(); len(spans) != 1 || spans[0].Name != "GET unmatched" {
		t.Fatalf("spans = %+v", spans)
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
	r.HandleFunc("GET /v1/items", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	r.HandleFunc("POST /v1/sub/action", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	for path, wantAllow := range map[string]string{"/v1/items": "GET, HEAD", "/v1/sub/action": "POST"} {
		method := http.MethodPost
		if path == "/v1/sub/action" {
			method = http.MethodGet
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(method, path, nil))
		if w.Code != http.StatusMethodNotAllowed || w.Header().Get("Content-Type") != "application/problem+json" {
			t.Fatalf("%s %s: %d %q", method, path, w.Code, w.Header().Get("Content-Type"))
		}
		if got := w.Header().Get("Allow"); got != wantAllow {
			t.Fatalf("%s: allow = %q, want %q", path, got, wantAllow)
		}
		if w.Header().Get("X-Content-Type-Options") != "nosniff" || w.Header().Get("X-Request-ID") == "" {
			t.Fatalf("outer middleware skipped on 405: %v", w.Header())
		}
		var p struct {
			Type   string `json:"type"`
			Status int    `json:"status"`
		}
		if err := json.NewDecoder(w.Body).Decode(&p); err != nil || p.Type != "method_not_allowed" || p.Status != 405 {
			t.Fatalf("problem = %+v, %v", p, err)
		}
	}
}

func TestRouterWrapUnmatchedAppliesOnlyUnderPrefix(t *testing.T) {
	r, h := httpserver.NewRouter(options())
	r.HandleFunc("POST /v1/auth/google", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	r.HandleFunc("GET /v1/other", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	r.WrapUnmatched("/v1/auth/", func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			w.Header().Set("X-Wrapped", "1")
			next.ServeHTTP(w, req)
		})
	})

	cases := []struct {
		method, path string
		code         int
		wrapped      bool
	}{
		{http.MethodGet, "/v1/auth/google", 405, true},
		{http.MethodGet, "/v1/auth/missing", 404, true},
		{http.MethodPost, "/v1/other", 405, false},
		{http.MethodPost, "/v1/auth/google", 200, false},
	}
	for _, c := range cases {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(c.method, c.path, nil))
		if w.Code != c.code || (w.Header().Get("X-Wrapped") == "1") != c.wrapped {
			t.Fatalf("%s %s: code %d wrapped %q", c.method, c.path, w.Code, w.Header().Get("X-Wrapped"))
		}
	}
}

func TestRouterNonCanonicalPathDoesNotBypassRouting(t *testing.T) {
	r, h := httpserver.NewRouter(options())
	r.HandleFunc("GET /v1/me", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	// The mux redirects unclean paths to the canonical one (307) and 404s a trailing
	// slash. Neither may reach the handler directly.
	for p, want := range map[string]int{"/v1//me": http.StatusTemporaryRedirect, "/v1/./me": http.StatusTemporaryRedirect, "/v1/me/": http.StatusNotFound} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, p, nil))
		if w.Code != want {
			t.Fatalf("%s: code %d, want %d", p, w.Code, want)
		}
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
