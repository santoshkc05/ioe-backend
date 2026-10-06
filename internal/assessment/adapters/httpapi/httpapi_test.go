package httpapi_test

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
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/assessment/adapters/httpapi"
	"github.com/santoshkc2200/ioe-backend/internal/assessment/app"
	"github.com/santoshkc2200/ioe-backend/internal/assessment/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/httpserver"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

var t0 = time.Date(2026, 10, 6, 9, 0, 0, 0, time.UTC)

type stub struct {
	quizzes []domain.Quiz
	quiz    domain.Quiz
	attempt id.ID
	err     error

	principal                   auth.Principal
	courseID, lectureID, quizID id.ID
	userID                      id.ID
	in                          app.QuizInput
	answers                     []app.AnswerInput
	key                         string
	called                      string
}

func (s *stub) List(_ context.Context, p auth.Principal, courseID, lectureID id.ID) ([]domain.Quiz, error) {
	s.called, s.principal, s.courseID, s.lectureID = "list", p, courseID, lectureID
	return s.quizzes, s.err
}

func (s *stub) Create(_ context.Context, p auth.Principal, courseID, lectureID id.ID, in app.QuizInput) (domain.Quiz, error) {
	s.called, s.principal, s.courseID, s.lectureID, s.in = "create", p, courseID, lectureID, in
	return s.quiz, s.err
}

func (s *stub) Update(_ context.Context, p auth.Principal, courseID, lectureID, quizID id.ID, in app.QuizInput) (domain.Quiz, error) {
	s.called, s.principal, s.courseID, s.lectureID, s.quizID, s.in = "update", p, courseID, lectureID, quizID, in
	return s.quiz, s.err
}

func (s *stub) Delete(_ context.Context, p auth.Principal, quizID id.ID) error {
	s.called, s.principal, s.quizID = "delete", p, quizID
	return s.err
}

func (s *stub) RecordAttempt(_ context.Context, p auth.Principal, quizID, userID id.ID, answers []app.AnswerInput, key string) (id.ID, error) {
	s.called, s.principal, s.quizID, s.userID, s.answers, s.key = "record", p, quizID, userID, answers, key
	return s.attempt, s.err
}

func fakeAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{UserID: 200, Role: auth.RoleStudent})))
	})
}

func newServer(s *stub) http.Handler {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	r, h := httpserver.NewRouter(httpserver.Options{Logger: logger, AllowedOrigins: []string{"https://app.test"}, ServiceName: "test"})
	httpapi.New(s, &examStub{}, httpapi.Config{RequireAuth: fakeAuth, Logger: logger}).Register(r)
	return h
}

func call(h http.Handler, method, path, body string, headers ...string) (int, []byte) {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		r.Header.Set(headers[i], headers[i+1])
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

func sampleQuiz() domain.Quiz {
	return domain.Quiz{ID: 7, CourseID: 10, LectureID: 50, Position: 1, CreatedAt: t0, UpdatedAt: t0, Questions: []domain.Question{{
		ID: 71, Prompt: "2+2?", Type: domain.QuestionSingleChoice, Explanation: "arith", Points: 3, ReferenceLectureID: 51,
		Options: []domain.Option{{ID: 72, Label: "4", IsCorrect: true}, {ID: 73, Label: "5"}},
	}}}
}

const quizJSON = `{"id":"7","lecture_id":"50","position":1,"questions":[{"id":"71","prompt":"2+2?","type":"single_choice",` +
	`"options":[{"id":"72","label":"4"},{"id":"73","label":"5"}],"correct_option_ids":["72"],"explanation":"arith"}]}`

func TestListWire(t *testing.T) {
	s := &stub{quizzes: []domain.Quiz{sampleQuiz()}}
	code, body := call(newServer(s), "GET", "/v1/courses/10/lectures/50/quizzes", "")
	if code != http.StatusOK || strings.TrimSpace(string(body)) != "["+quizJSON+"]" {
		t.Fatalf("code=%d body=%s", code, body)
	}
	if s.courseID != 10 || s.lectureID != 50 || s.principal.UserID != 200 {
		t.Fatalf("stub=%+v", s)
	}
	code, body = call(newServer(&stub{}), "GET", "/v1/courses/10/lectures/50/quizzes", "")
	if code != http.StatusOK || strings.TrimSpace(string(body)) != "[]" {
		t.Fatalf("empty: code=%d body=%s", code, body)
	}
}

const saveBody = `{"position":1,"questions":[{"id":"","prompt":"2+2?","type":"single_choice","explanation":"arith",` +
	`"points":3,"reference_lecture_id":"","options":[{"label":"4","is_correct":true},{"id":"73","label":"5","is_correct":false}]}]}`

func TestCreate(t *testing.T) {
	s := &stub{quiz: sampleQuiz()}
	code, body := call(newServer(s), "POST", "/v1/courses/10/lectures/50/quizzes", saveBody)
	if code != http.StatusCreated || strings.TrimSpace(string(body)) != quizJSON {
		t.Fatalf("code=%d body=%s", code, body)
	}
	q := s.in.Questions[0]
	if s.called != "create" || s.in.Position != 1 || q.Prompt != "2+2?" || q.Points != 3 || q.ID != "" || q.ReferenceLectureID != "" ||
		len(q.Options) != 2 || !q.Options[0].IsCorrect || q.Options[1].ID != "73" {
		t.Fatalf("in=%+v", s.in)
	}
}

func TestUpdate(t *testing.T) {
	s := &stub{quiz: sampleQuiz()}
	code, body := call(newServer(s), "PUT", "/v1/courses/10/lectures/50/quizzes/7", saveBody)
	if code != http.StatusOK || strings.TrimSpace(string(body)) != quizJSON || s.called != "update" || s.quizID != 7 {
		t.Fatalf("code=%d body=%s stub=%+v", code, body, s)
	}
}

func TestDelete(t *testing.T) {
	s := &stub{}
	code, body := call(newServer(s), "DELETE", "/v1/quizzes/7", "")
	if code != http.StatusNoContent || len(body) != 0 || s.called != "delete" || s.quizID != 7 {
		t.Fatalf("code=%d body=%s stub=%+v", code, body, s)
	}
}

func TestRecordAttempt(t *testing.T) {
	s := &stub{attempt: 900}
	code, body := call(newServer(s), "POST", "/v1/quizzes/7/attempts",
		`{"user_id":"200","answers":[{"question_id":"71","option_ids":["72"]}]}`, "Idempotency-Key", "k-1")
	if code != http.StatusCreated || strings.TrimSpace(string(body)) != `{"id":"900","recorded":true}` {
		t.Fatalf("code=%d body=%s", code, body)
	}
	if s.quizID != 7 || s.userID != 200 || s.key != "k-1" || len(s.answers) != 1 || s.answers[0].QuestionID != "71" || s.answers[0].OptionIDs[0] != "72" {
		t.Fatalf("stub=%+v", s)
	}
}

func TestRecordAttemptBadBodies(t *testing.T) {
	h := newServer(&stub{})
	for _, body := range []string{`{"answers":[]}`, `{"user_id":"abc","answers":[]}`, `{"user_id":"200","answers":[],"extra":1}`} {
		if code, b := call(h, "POST", "/v1/quizzes/7/attempts", body); code != http.StatusBadRequest {
			t.Fatalf("%s: %d %s", body, code, b)
		}
	}
}

func TestMalformedPathIDIsNotFound(t *testing.T) {
	if code, _ := call(newServer(&stub{}), "DELETE", "/v1/quizzes/abc", ""); code != http.StatusNotFound {
		t.Fatalf("code=%d", code)
	}
}

func TestErrorMapping(t *testing.T) {
	cases := []struct {
		err    error
		status int
		typ    string
	}{
		{app.ErrNotFound, http.StatusNotFound, "not_found"},
		{app.ErrForbidden, http.StatusForbidden, "forbidden"},
		{app.ErrCourseNotEditable, http.StatusConflict, "course_not_editable"},
		{app.ErrEnrollmentRequired, http.StatusConflict, "enrollment_required"},
		{app.ErrInvalidInput, http.StatusBadRequest, "invalid_input"},
		{errors.New("boom"), http.StatusInternalServerError, "internal"},
	}
	for _, tc := range cases {
		code, body := call(newServer(&stub{err: tc.err}), "DELETE", "/v1/quizzes/7", "")
		if code != tc.status || problemType(t, body) != tc.typ {
			t.Fatalf("%v: code=%d body=%s", tc.err, code, body)
		}
	}
}
