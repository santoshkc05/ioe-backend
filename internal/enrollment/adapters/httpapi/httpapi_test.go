package httpapi_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/enrollment/adapters/httpapi"
	"github.com/santoshkc2200/ioe-backend/internal/enrollment/app"
	"github.com/santoshkc2200/ioe-backend/internal/enrollment/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/httpserver"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
	"github.com/santoshkc2200/ioe-backend/internal/platform/problem"
)

var t0 = time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)

// stub records the last call and returns canned results.
type stub struct {
	enrollment domain.Enrollment
	activated  bool
	list       []domain.Enrollment
	total      int
	err        error

	reason        string
	limit, offset int
	principal     auth.Principal
}

func (s *stub) Enroll(_ context.Context, p auth.Principal, _, _ id.ID) (domain.Enrollment, bool, error) {
	s.principal = p
	return s.enrollment, s.activated, s.err
}

func (s *stub) Cancel(_ context.Context, p auth.Principal, _, _ id.ID, reason string) (domain.Enrollment, error) {
	s.principal, s.reason = p, reason
	return s.enrollment, s.err
}

func (s *stub) ListByCourse(_ context.Context, _ auth.Principal, _ id.ID, limit, offset int) ([]domain.Enrollment, int, error) {
	s.limit, s.offset = limit, offset
	return s.list, s.total, s.err
}

func (s *stub) ListByUser(context.Context, auth.Principal, id.ID) ([]domain.Enrollment, error) {
	return s.list, s.err
}

func fakeAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{UserID: 200, Role: auth.RoleStudent})))
	})
}

func newServer(s *stub) http.Handler {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	r, h := httpserver.NewRouter(httpserver.Options{Logger: logger, AllowedOrigins: []string{"https://app.test"}, ServiceName: "test"})
	httpapi.New(s, httpapi.Config{RequireAuth: fakeAuth, Logger: logger}).Register(r)
	return h
}

func call(h http.Handler, method, path, body string) (int, []byte) {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w.Code, w.Body.Bytes()
}

func decode[T any](t *testing.T, b []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("decode %s: %v", b, err)
	}
	return v
}

var active = domain.Enrollment{ID: 1, CourseID: 10, UserID: 200, Status: domain.StatusActive, EnrolledAt: t0, Version: 1}

func TestEnrollStatusAndShape(t *testing.T) {
	s := &stub{enrollment: active, activated: true}
	code, body := call(newServer(s), "POST", "/v1/courses/10/enrollments/200", "")
	if code != http.StatusCreated {
		t.Fatalf("code = %d %s", code, body)
	}
	got := decode[map[string]any](t, body)
	want := map[string]any{"id": "1", "course_id": "10", "user_id": "200", "status": "active", "enrolled_at": "2026-10-05T00:00:00Z"}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("%s = %v, want %v (body %s)", k, got[k], v, body)
		}
	}
	if _, ok := got["canceled_at"]; ok {
		t.Fatalf("canceled_at present on active enrollment: %s", body)
	}

	s.activated = false
	if code, _ := call(newServer(s), "POST", "/v1/courses/10/enrollments/200", ""); code != http.StatusOK {
		t.Fatalf("already active code = %d", code)
	}
}

func TestCancelWithoutBody(t *testing.T) {
	canceled := active
	canceled.Status, canceled.CanceledAt = domain.StatusCanceled, t0.Add(time.Hour)
	s := &stub{enrollment: canceled}
	code, body := call(newServer(s), "DELETE", "/v1/courses/10/enrollments/200", "")
	if code != http.StatusOK || s.reason != "" {
		t.Fatalf("code = %d reason = %q body = %s", code, s.reason, body)
	}
	got := decode[map[string]any](t, body)
	if got["status"] != "canceled" || got["canceled_at"] != "2026-10-05T01:00:00Z" {
		t.Fatalf("body = %s", body)
	}
}

func TestCancelWithReason(t *testing.T) {
	s := &stub{enrollment: active}
	if code, _ := call(newServer(s), "DELETE", "/v1/courses/10/enrollments/200", `{"reason":"busy"}`); code != http.StatusOK || s.reason != "busy" {
		t.Fatalf("code = %d reason = %q", code, s.reason)
	}
	if code, _ := call(newServer(s), "DELETE", "/v1/courses/10/enrollments/200", `{"why":"x"}`); code != http.StatusBadRequest {
		t.Fatalf("unknown field code = %d", code)
	}
}

func TestListByCourse(t *testing.T) {
	s := &stub{list: []domain.Enrollment{active}, total: 7}
	code, body := call(newServer(s), "GET", "/v1/courses/10/enrollments", "")
	if code != http.StatusOK || s.limit != app.DefaultPageLimit || s.offset != 0 {
		t.Fatalf("code=%d limit=%d offset=%d", code, s.limit, s.offset)
	}
	page := decode[struct {
		Enrollments []map[string]any `json:"enrollments"`
		Total       int              `json:"total"`
	}](t, body)
	if page.Total != 7 || len(page.Enrollments) != 1 || page.Enrollments[0]["id"] != "1" {
		t.Fatalf("body = %s", body)
	}
	if code, _ := call(newServer(s), "GET", "/v1/courses/10/enrollments?limit=5&offset=10", ""); code != http.StatusOK || s.limit != 5 || s.offset != 10 {
		t.Fatalf("code=%d limit=%d offset=%d", code, s.limit, s.offset)
	}
	for _, q := range []string{"?limit=x", "?offset=1.5"} {
		code, body := call(newServer(s), "GET", "/v1/courses/10/enrollments"+q, "")
		if code != http.StatusBadRequest || decode[map[string]any](t, body)["type"] != "invalid_input" {
			t.Fatalf("%s: %d %s", q, code, body)
		}
	}
}

func TestListByUserEmptyIsArray(t *testing.T) {
	code, body := call(newServer(&stub{}), "GET", "/v1/users/200/enrollments", "")
	if code != http.StatusOK || strings.TrimSpace(string(body)) != "[]" {
		t.Fatalf("code = %d body = %s", code, body)
	}
	code, body = call(newServer(&stub{list: []domain.Enrollment{active}}), "GET", "/v1/users/200/enrollments", "")
	if code != http.StatusOK || len(decode[[]map[string]any](t, body)) != 1 {
		t.Fatalf("code = %d body = %s", code, body)
	}
}

func TestErrorMapping(t *testing.T) {
	cases := []struct {
		err    error
		status int
		typ    string
	}{
		{app.ErrNotFound, 404, "not_found"},
		{domain.ErrCourseHidden, 404, "not_found"},
		{domain.ErrForbidden, 403, "forbidden"},
		{domain.ErrPaymentRequired, 402, "payment_required"},
		{domain.ErrCourseNotPublished, 409, "course_not_published"},
		{app.ErrConcurrentModification, 409, "concurrent_modification"},
		{app.ErrInvalidInput, 400, "invalid_input"},
	}
	for _, tc := range cases {
		code, body := call(newServer(&stub{err: tc.err}), "POST", "/v1/courses/10/enrollments/200", "")
		if code != tc.status || decode[map[string]any](t, body)["type"] != tc.typ {
			t.Fatalf("%v: %d %s", tc.err, code, body)
		}
	}
}

func TestUnparsablePathIDs(t *testing.T) {
	for _, p := range []string{"/v1/courses/abc/enrollments/200", "/v1/courses/10/enrollments/0", "/v1/courses/x/enrollments", "/v1/users/-1/enrollments"} {
		method := "POST"
		if strings.HasSuffix(p, "enrollments") {
			method = "GET"
		}
		code, body := call(newServer(&stub{}), method, p, "")
		if code != http.StatusNotFound || decode[map[string]any](t, body)["type"] != problem.TypeNotFound {
			t.Fatalf("%s %s: %d %s", method, p, code, body)
		}
	}
}
