package httpserver_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/santoshkc2200/ioe-backend/internal/platform/httpserver"
)

type payload struct {
	Name string `json:"name"`
}

func decode(t *testing.T, contentType, body string, limit int64) (*httptest.ResponseRecorder, bool, payload) {
	t.Helper()
	var p payload
	w := httptest.NewRecorder()
	ok := false
	h := httpserver.BodyLimit(limit)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ok = httpserver.DecodeJSON(w, r, &p)
	}))
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	h.ServeHTTP(w, r)
	return w, ok, p
}

func TestDecodeJSONAcceptsValidObject(t *testing.T) {
	_, ok, p := decode(t, "application/json; charset=utf-8", `{"name":"a"}`, 1<<20)
	if !ok || p.Name != "a" {
		t.Fatalf("ok=%v p=%+v", ok, p)
	}
}

func TestDecodeJSONRequiresJSONContentType(t *testing.T) {
	for _, ct := range []string{"", "text/plain", "application/x-www-form-urlencoded"} {
		w, ok, _ := decode(t, ct, `{"name":"a"}`, 1<<20)
		if ok || w.Code != http.StatusUnsupportedMediaType || !strings.Contains(w.Body.String(), "unsupported_media_type") {
			t.Fatalf("content-type %q: ok=%v code=%d", ct, ok, w.Code)
		}
	}
}

func TestDecodeJSONRejectsBadBodies(t *testing.T) {
	for _, body := range []string{`{"name":"a","extra":1}`, `{"name":`, `{"name":"a"}{"name":"b"}`, `[]`} {
		w, ok, _ := decode(t, "application/json", body, 1<<20)
		if ok || w.Code != http.StatusBadRequest {
			t.Fatalf("body %q: ok=%v code=%d", body, ok, w.Code)
		}
	}
}

func TestDecodeJSONTooLarge(t *testing.T) {
	w, ok, _ := decode(t, "application/json", `{"name":"`+strings.Repeat("a", 100)+`"}`, 16)
	if ok || w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("ok=%v code=%d", ok, w.Code)
	}
}
