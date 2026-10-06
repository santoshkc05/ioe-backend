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

	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/httpserver"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
	"github.com/santoshkc2200/ioe-backend/internal/progress/adapters/httpapi"
	"github.com/santoshkc2200/ioe-backend/internal/progress/app"
	"github.com/santoshkc2200/ioe-backend/internal/progress/domain"
)

var t0 = time.Date(2026, 10, 6, 9, 0, 0, 0, time.UTC)

type stub struct {
	course  domain.CourseProgress
	courses []domain.CourseProgress
	days    []domain.ActivityDay
	err     error

	recorded                    bool
	courseID, lectureID, userID id.ID
	state                       domain.LectureState
	positionMs                  int64
	principal                   auth.Principal
}

func (s *stub) RecordLecture(_ context.Context, p auth.Principal, courseID, lectureID, userID id.ID, state domain.LectureState, positionMs int64) error {
	s.recorded, s.principal, s.courseID, s.lectureID, s.userID, s.state, s.positionMs = true, p, courseID, lectureID, userID, state, positionMs
	return s.err
}

func (s *stub) CourseProgress(_ context.Context, p auth.Principal, courseID, userID id.ID) (domain.CourseProgress, error) {
	s.principal, s.courseID, s.userID = p, courseID, userID
	return s.course, s.err
}

func (s *stub) UserProgress(_ context.Context, p auth.Principal, userID id.ID) ([]domain.CourseProgress, []domain.ActivityDay, error) {
	s.principal, s.userID = p, userID
	return s.courses, s.days, s.err
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

func problemType(t *testing.T, b []byte) string {
	t.Helper()
	var p struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatalf("decode %s: %v", b, err)
	}
	return p.Type
}

func TestRecordLecture(t *testing.T) {
	s := &stub{}
	code, body := call(newServer(s), "PUT", "/v1/courses/10/lectures/50/progress/200", `{"state":"completed","position_ms":1200}`)
	if code != http.StatusNoContent || len(body) != 0 {
		t.Fatalf("code=%d body=%s", code, body)
	}
	if !s.recorded || s.courseID != 10 || s.lectureID != 50 || s.userID != 200 || s.state != domain.LectureStateCompleted || s.positionMs != 1200 || s.principal.UserID != 200 {
		t.Fatalf("stub=%+v", s)
	}
}

func TestRecordLectureBadBodies(t *testing.T) {
	h := newServer(&stub{})
	if code, body := call(h, "PUT", "/v1/courses/10/lectures/50/progress/200", `{"state":"completed","extra":1}`); code != http.StatusBadRequest || problemType(t, body) != "invalid_request" {
		t.Fatalf("unknown field: %d %s", code, body)
	}
	if code, _ := call(h, "PUT", "/v1/courses/10/lectures/50/progress/200", ""); code != http.StatusUnsupportedMediaType {
		t.Fatalf("no body: %d", code)
	}
}

func TestCourseProgressWire(t *testing.T) {
	s := &stub{course: domain.CourseProgress{
		CourseID: 10, UserID: 200, LastLectureID: 51, UpdatedAt: t0,
		Lectures: []domain.LectureProgress{
			{LectureID: 50, State: domain.LectureStateCompleted, PositionMs: 0, UpdatedAt: t0},
			{LectureID: 51, State: domain.LectureStateInProgress, PositionMs: 900, UpdatedAt: t0},
		},
	}}
	code, body := call(newServer(s), "GET", "/v1/courses/10/progress/200", "")
	want := `{"course_id":"10","user_id":"200","last_lecture_id":"51","completed_lecture_ids":["50"],` +
		`"lectures":[{"lecture_id":"50","state":"completed","position_ms":0,"updated_at":"2026-10-06T09:00:00Z"},` +
		`{"lecture_id":"51","state":"in_progress","position_ms":900,"updated_at":"2026-10-06T09:00:00Z"}],` +
		`"updated_at":"2026-10-06T09:00:00Z"}`
	if code != http.StatusOK || strings.TrimSpace(string(body)) != want {
		t.Fatalf("code=%d body=%s", code, body)
	}
}

func TestEmptyCourseProgressWire(t *testing.T) {
	s := &stub{course: domain.CourseProgress{CourseID: 10, UserID: 200}}
	code, body := call(newServer(s), "GET", "/v1/courses/10/progress/200", "")
	want := `{"course_id":"10","user_id":"200","completed_lecture_ids":[],"lectures":[]}`
	if code != http.StatusOK || strings.TrimSpace(string(body)) != want {
		t.Fatalf("code=%d body=%s", code, body)
	}
}

func TestUserProgressWire(t *testing.T) {
	s := &stub{
		courses: []domain.CourseProgress{{CourseID: 10, UserID: 200, LastLectureID: 50, UpdatedAt: t0,
			Lectures: []domain.LectureProgress{{LectureID: 50, State: domain.LectureStateCompleted, UpdatedAt: t0}}}},
		days: []domain.ActivityDay{{Date: time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC), LectureCount: 1}},
	}
	code, body := call(newServer(s), "GET", "/v1/users/200/progress", "")
	want := `{"user_id":"200","courses":[{"course_id":"10","user_id":"200","last_lecture_id":"50","completed_lecture_ids":["50"],` +
		`"lectures":[{"lecture_id":"50","state":"completed","position_ms":0,"updated_at":"2026-10-06T09:00:00Z"}],` +
		`"updated_at":"2026-10-06T09:00:00Z"}],"activity_days":[{"date":"2026-10-06","lecture_count":1}]}`
	if code != http.StatusOK || strings.TrimSpace(string(body)) != want {
		t.Fatalf("code=%d body=%s", code, body)
	}

	code, body = call(newServer(&stub{}), "GET", "/v1/users/200/progress", "")
	if code != http.StatusOK || strings.TrimSpace(string(body)) != `{"user_id":"200","courses":[],"activity_days":[]}` {
		t.Fatalf("empty: code=%d body=%s", code, body)
	}
}

func TestErrorMapping(t *testing.T) {
	cases := []struct {
		err    error
		status int
		typ    string
	}{
		{app.ErrNotFound, http.StatusNotFound, "not_found"},
		{domain.ErrCourseHidden, http.StatusNotFound, "not_found"},
		{domain.ErrLectureNotFound, http.StatusNotFound, "not_found"},
		{domain.ErrForbidden, http.StatusForbidden, "forbidden"},
		{app.ErrEnrollmentRequired, http.StatusConflict, "enrollment_required"},
		{app.ErrInvalidInput, http.StatusBadRequest, "invalid_input"},
		{context.DeadlineExceeded, http.StatusInternalServerError, "internal"},
	}
	for _, tc := range cases {
		code, body := call(newServer(&stub{err: tc.err}), "PUT", "/v1/courses/10/lectures/50/progress/200", `{"state":"completed","position_ms":0}`)
		if code != tc.status || problemType(t, body) != tc.typ {
			t.Fatalf("%v: code=%d body=%s", tc.err, code, body)
		}
	}
}

func TestUnparsablePathIDs(t *testing.T) {
	h := newServer(&stub{})
	for _, req := range [][2]string{
		{"PUT", "/v1/courses/abc/lectures/50/progress/200"},
		{"PUT", "/v1/courses/10/lectures/0/progress/200"},
		{"PUT", "/v1/courses/10/lectures/50/progress/-1"},
		{"GET", "/v1/courses/x/progress/200"},
		{"GET", "/v1/users/01/progress"},
	} {
		body := ""
		if req[0] == "PUT" {
			body = `{"state":"completed","position_ms":0}`
		}
		if code, b := call(h, req[0], req[1], body); code != http.StatusNotFound || problemType(t, b) != "not_found" {
			t.Fatalf("%s %s: %d %s", req[0], req[1], code, b)
		}
	}
}
