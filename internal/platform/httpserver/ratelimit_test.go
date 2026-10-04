package httpserver_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/santoshkc2200/ioe-backend/internal/platform/httpserver"
)

func TestRateLimiterMiddleware(t *testing.T) {
	rl := httpserver.NewRateLimiter(2)
	h := rl.Middleware(httpserver.NewIPResolver(nil))(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	codes := make([]int, 0, 3)
	for range 3 {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req("203.0.113.5:1", ""))
		codes = append(codes, w.Code)
		if w.Code == http.StatusTooManyRequests && w.Header().Get("Retry-After") == "" {
			t.Fatal("429 without Retry-After")
		}
	}
	if codes[0] != 200 || codes[1] != 200 || codes[2] != 429 {
		t.Fatalf("codes = %v", codes)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req("203.0.113.6:1", ""))
	if w.Code != http.StatusOK {
		t.Fatalf("other client limited: %d", w.Code)
	}
}
