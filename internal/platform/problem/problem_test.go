package problem_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/santoshkc2200/ioe-backend/internal/platform/logging"
	"github.com/santoshkc2200/ioe-backend/internal/platform/problem"
)

func TestWrite(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/x", nil)
	r = r.WithContext(logging.WithRequestID(r.Context(), "req-9"))
	w := httptest.NewRecorder()

	problem.Write(w, r, http.StatusNotFound, problem.TypeNotFound, "Not Found", "no such thing")

	if w.Code != http.StatusNotFound || w.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("status %d content-type %q", w.Code, w.Header().Get("Content-Type"))
	}
	var p problem.Problem
	if err := json.NewDecoder(w.Body).Decode(&p); err != nil {
		t.Fatal(err)
	}
	want := problem.Problem{Type: "not_found", Title: "Not Found", Status: 404, Detail: "no such thing", Instance: "req-9"}
	if p != want {
		t.Fatalf("got %+v want %+v", p, want)
	}
}
