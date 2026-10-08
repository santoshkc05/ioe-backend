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

	"github.com/santoshkc2200/ioe-backend/internal/certificate/adapters/httpapi"
	"github.com/santoshkc2200/ioe-backend/internal/certificate/app"
	"github.com/santoshkc2200/ioe-backend/internal/certificate/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/httpserver"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

var t0 = time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC)

type stub struct {
	policy  domain.Policy
	cert    domain.Certificate
	created bool
	list    []domain.Certificate
	err     error

	principal auth.Principal
	courseID  id.ID
	mode      domain.Mode
	examID    id.ID
	code      string
}

func (s *stub) GetPolicy(_ context.Context, p auth.Principal, courseID id.ID) (domain.Policy, error) {
	s.principal, s.courseID = p, courseID
	return s.policy, s.err
}

func (s *stub) SetPolicy(_ context.Context, p auth.Principal, courseID id.ID, mode domain.Mode, examID id.ID) (domain.Policy, error) {
	s.principal, s.courseID, s.mode, s.examID = p, courseID, mode, examID
	return s.policy, s.err
}

func (s *stub) Claim(_ context.Context, p auth.Principal, courseID id.ID) (domain.Certificate, bool, error) {
	s.principal, s.courseID = p, courseID
	return s.cert, s.created, s.err
}

func (s *stub) GetMine(_ context.Context, p auth.Principal, courseID id.ID) (domain.Certificate, error) {
	s.principal, s.courseID = p, courseID
	return s.cert, s.err
}

func (s *stub) ListMine(_ context.Context, p auth.Principal) ([]domain.Certificate, error) {
	s.principal = p
	return s.list, s.err
}

func (s *stub) Verify(_ context.Context, code string) (domain.Certificate, error) {
	s.code = code
	return s.cert, s.err
}

func fakeAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{UserID: 200, Role: auth.RoleStudent})))
	})
}

// denyAuth rejects every request, proving a route is mounted behind authentication.
func denyAuth(http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) })
}

func newServer(s *stub, requireAuth httpserver.Middleware) http.Handler {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	r, h := httpserver.NewRouter(httpserver.Options{Logger: logger, AllowedOrigins: []string{"https://app.test"}, ServiceName: "test"})
	httpapi.New(s, httpapi.Config{
		RequireAuth: requireAuth, VerifyLimiter: httpserver.NewRateLimiter(1000), IPs: httpserver.NewIPResolver(nil), Logger: logger,
	}).Register(r)
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

func validCert() domain.Certificate {
	return domain.Certificate{
		ID: 1, Code: strings.Repeat("A", domain.CodeLen), UserID: 200, CourseID: 10,
		StudentName: "Asha Rai", CourseTitle: "Go", IssuedAt: t0,
	}
}

func TestClaim(t *testing.T) {
	s := &stub{cert: validCert(), created: true}
	code, body := call(newServer(s, fakeAuth), "POST", "/v1/courses/10/certificate", "")
	if code != http.StatusCreated {
		t.Fatalf("created: %d %s", code, body)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"id": "1", "code": strings.Repeat("A", 26), "course_id": "10", "course_title": "Go",
		"student_name": "Asha Rai", "issued_at": "2026-10-08T09:00:00Z", "status": "valid",
	}
	if len(got) != len(want) {
		t.Fatalf("fields = %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %v, want %v", k, got[k], v)
		}
	}
	if s.principal.UserID != 200 || s.courseID != 10 {
		t.Fatalf("stub = %+v", s)
	}

	s.created = false
	if code, _ := call(newServer(s, fakeAuth), "POST", "/v1/courses/10/certificate", ""); code != http.StatusOK {
		t.Fatalf("existing: %d", code)
	}
}

func TestClaimRefusals(t *testing.T) {
	for err, want := range map[error]struct {
		status int
		typ    string
	}{
		app.ErrCertificatesDisabled: {http.StatusConflict, "certificates_disabled"},
		app.ErrNotEnrolled:          {http.StatusConflict, "not_enrolled"},
		app.ErrProgressIncomplete:   {http.StatusConflict, "progress_incomplete"},
		app.ErrExamNotPassed:        {http.StatusConflict, "exam_not_passed"},
		app.ErrNotFound:             {http.StatusNotFound, "not_found"},
	} {
		code, body := call(newServer(&stub{err: err}, fakeAuth), "POST", "/v1/courses/10/certificate", "")
		if code != want.status || problemType(t, body) != want.typ {
			t.Errorf("%v: %d %s", err, code, body)
		}
	}
	code, body := call(newServer(&stub{err: io.ErrUnexpectedEOF}, fakeAuth), "POST", "/v1/courses/10/certificate", "")
	if code != http.StatusInternalServerError || strings.Contains(string(body), "unexpected EOF") {
		t.Fatalf("internal error leaked: %d %s", code, body)
	}
}

func TestRevokedCertificateStatus(t *testing.T) {
	c := validCert()
	c.RevokedAt = t0.Add(time.Hour)
	code, body := call(newServer(&stub{list: []domain.Certificate{c}}, fakeAuth), "GET", "/v1/me/certificates", "")
	var got struct {
		Items []struct {
			Status string `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &got); err != nil || code != http.StatusOK || len(got.Items) != 1 || got.Items[0].Status != "revoked" {
		t.Fatalf("list: %d %s", code, body)
	}
	code, body = call(newServer(&stub{list: nil}, fakeAuth), "GET", "/v1/me/certificates", "")
	if code != http.StatusOK || !strings.Contains(string(body), `"items":[]`) {
		t.Fatalf("empty list: %d %s", code, body)
	}
}

func TestVerifyIsPublicAndExposesOnlyThePublicFields(t *testing.T) {
	s := &stub{cert: validCert()}
	h := newServer(s, denyAuth) // authentication would reject everything else
	code, body := call(h, "GET", "/v1/certificates/"+strings.Repeat("A", 26), "")
	if code != http.StatusOK {
		t.Fatalf("verify: %d %s", code, body)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"code": strings.Repeat("A", 26), "student_name": "Asha Rai", "course_title": "Go",
		"issued_at": "2026-10-08T09:00:00Z", "status": "valid",
	}
	if len(got) != len(want) {
		t.Fatalf("verify exposes %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %v, want %v", k, got[k], v)
		}
	}
	if s.code != strings.Repeat("A", 26) {
		t.Fatalf("code passed = %q", s.code)
	}

	if code, _ := call(newServer(&stub{err: app.ErrNotFound}, denyAuth), "GET", "/v1/certificates/short", ""); code != http.StatusNotFound {
		t.Fatalf("unknown code: %d", code)
	}
	if code, _ := call(h, "GET", "/v1/courses/10/certificate", ""); code != http.StatusUnauthorized {
		t.Fatalf("own certificate route is not authenticated: %d", code)
	}
}

func TestPolicyRoutes(t *testing.T) {
	s := &stub{policy: domain.Policy{CourseID: 10, Mode: domain.ModeCompletionAndExam, ExamID: 700}}
	h := newServer(s, fakeAuth)
	code, body := call(h, "PUT", "/v1/courses/10/certificate-policy", `{"mode":"completion_and_exam","exam_id":"700"}`)
	if code != http.StatusOK || s.mode != domain.ModeCompletionAndExam || s.examID != 700 || s.courseID != 10 {
		t.Fatalf("put: %d %s stub=%+v", code, body, s)
	}
	var got map[string]any
	_ = json.Unmarshal(body, &got)
	if got["mode"] != "completion_and_exam" || got["exam_id"] != "700" {
		t.Fatalf("put body = %v", got)
	}

	s.policy = domain.Policy{CourseID: 10, Mode: domain.ModeCompletion}
	code, body = call(h, "PUT", "/v1/courses/10/certificate-policy", `{"mode":"completion"}`)
	if code != http.StatusOK || s.examID != 0 || strings.Contains(string(body), "exam_id") {
		t.Fatalf("put without exam: %d %s examID=%d", code, body, s.examID)
	}

	if code, body := call(h, "GET", "/v1/courses/10/certificate-policy", ""); code != http.StatusOK || !strings.Contains(string(body), `"mode":"completion"`) {
		t.Fatalf("get: %d %s", code, body)
	}
}

func TestPolicyBadInput(t *testing.T) {
	h := newServer(&stub{}, fakeAuth)
	for name, body := range map[string]string{
		"unknown field": `{"mode":"completion","extra":1}`,
		"not json":      `nope`,
		"numeric id":    `{"mode":"completion_and_exam","exam_id":700}`,
	} {
		if code, b := call(h, "PUT", "/v1/courses/10/certificate-policy", body); code != http.StatusBadRequest || problemType(t, b) != "invalid_request" {
			t.Errorf("%s: %d %s", name, code, b)
		}
	}
	code, b := call(newServer(&stub{err: app.ErrInvalidInput}, fakeAuth), "PUT", "/v1/courses/10/certificate-policy", `{"mode":"always"}`)
	if code != http.StatusBadRequest || problemType(t, b) != "invalid_input" {
		t.Errorf("domain invalid: %d %s", code, b)
	}
	if code, _ := call(newServer(&stub{err: app.ErrForbidden}, fakeAuth), "PUT", "/v1/courses/10/certificate-policy", `{"mode":"off"}`); code != http.StatusForbidden {
		t.Errorf("forbidden: %d", code)
	}
	for _, path := range []string{"/v1/courses/abc/certificate", "/v1/courses/0/certificate-policy"} {
		if code, _ := call(h, "GET", path, ""); code != http.StatusNotFound {
			t.Errorf("%s: %d", path, code)
		}
	}
}
