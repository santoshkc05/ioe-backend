package httpserver_test

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/santoshkc2200/ioe-backend/internal/platform/httpserver"
	"github.com/santoshkc2200/ioe-backend/internal/platform/logging"
)

func TestRequestIDGeneratedOrPropagated(t *testing.T) {
	var seen string
	h := httpserver.RequestID(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = logging.RequestID(r.Context())
	}))

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Request-ID", "abc-123")
	h.ServeHTTP(w, r)
	if seen != "abc-123" || w.Header().Get("X-Request-ID") != "abc-123" {
		t.Fatalf("valid id not propagated: %q", seen)
	}

	w = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Request-ID", "bad\nid")
	h.ServeHTTP(w, r)
	if seen == "bad\nid" || len(seen) != 36 {
		t.Fatalf("invalid id not replaced: %q", seen)
	}
}

func TestRecoverWritesProblem(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	h := httpserver.Recover(logger)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") }))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Code != http.StatusInternalServerError || !strings.Contains(w.Body.String(), `"type":"internal"`) {
		t.Fatalf("got %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(logs.String(), "boom") {
		t.Fatalf("panic not logged: %s", logs.String())
	}
}

func TestSecurityHeaders(t *testing.T) {
	w := httptest.NewRecorder()
	httpserver.SecurityHeaders(http.NotFoundHandler()).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	want := map[string]string{
		"Strict-Transport-Security": "max-age=63072000; includeSubDomains",
		"X-Content-Type-Options":    "nosniff",
		"Content-Security-Policy":   "default-src 'none'; frame-ancestors 'none'",
		"Referrer-Policy":           "no-referrer",
	}
	for k, v := range want {
		if got := w.Header().Get(k); got != v {
			t.Errorf("%s = %q want %q", k, got, v)
		}
	}
}

func TestCORS(t *testing.T) {
	h := httpserver.CORS([]string{"https://app.example.com"})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))

	pre := httptest.NewRequest(http.MethodOptions, "/v1/auth/google", nil)
	pre.Header.Set("Origin", "https://app.example.com")
	pre.Header.Set("Access-Control-Request-Method", "POST")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, pre)
	if w.Code != http.StatusNoContent ||
		w.Header().Get("Access-Control-Allow-Origin") != "https://app.example.com" ||
		w.Header().Get("Access-Control-Allow-Credentials") != "true" ||
		!strings.Contains(w.Header().Get("Access-Control-Allow-Headers"), "Content-Type") {
		t.Fatalf("allowed preflight: %d %v", w.Code, w.Header())
	}

	evil := httptest.NewRequest(http.MethodOptions, "/v1/auth/google", nil)
	evil.Header.Set("Origin", "https://evil.example")
	evil.Header.Set("Access-Control-Request-Method", "POST")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, evil)
	if w.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("disallowed origin got CORS headers: %v", w.Header())
	}
	if w.Code != http.StatusTeapot {
		t.Fatalf("disallowed preflight should fall through, got %d", w.Code)
	}
}
