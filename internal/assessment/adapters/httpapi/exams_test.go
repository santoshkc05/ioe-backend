package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
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

// examStub records the last call and returns its canned results.
type examStub struct {
	exams    []domain.Exam
	exam     domain.Exam
	detail   app.ExamDetail
	students []app.StudentExam
	attempts []domain.ExamAttempt
	attempt  app.AttemptDetail
	err      error

	called   string
	p        auth.Principal
	target   id.ID // the course, exam, or attempt ID from the path
	in       app.ExamInput
	settings app.ExamSettingsInput
	order    []id.ID
	answer   app.AnswerInput
}

func (s *examStub) record(name string, p auth.Principal, target id.ID) {
	s.called, s.p, s.target = name, p, target
}

func (s *examStub) ListAuthoring(_ context.Context, p auth.Principal, courseID id.ID) ([]domain.Exam, error) {
	s.record("listAuthoring", p, courseID)
	return s.exams, s.err
}

func (s *examStub) GetAuthoring(_ context.Context, p auth.Principal, examID id.ID) (app.ExamDetail, error) {
	s.record("getAuthoring", p, examID)
	return s.detail, s.err
}

func (s *examStub) Create(_ context.Context, p auth.Principal, courseID id.ID, in app.ExamInput) (app.ExamDetail, error) {
	s.record("create", p, courseID)
	s.in = in
	return s.detail, s.err
}

func (s *examStub) Save(_ context.Context, p auth.Principal, examID id.ID, in app.ExamInput) (app.ExamDetail, error) {
	s.record("save", p, examID)
	s.in = in
	return s.detail, s.err
}

func (s *examStub) SaveSettings(_ context.Context, p auth.Principal, examID id.ID, in app.ExamSettingsInput) (app.ExamDetail, error) {
	s.record("saveSettings", p, examID)
	s.settings = in
	return s.detail, s.err
}

func (s *examStub) Reorder(_ context.Context, p auth.Principal, courseID id.ID, examIDs []id.ID) error {
	s.record("reorder", p, courseID)
	s.order = examIDs
	return s.err
}

func (s *examStub) Publish(_ context.Context, p auth.Principal, examID id.ID) error {
	s.record("publish", p, examID)
	return s.err
}

func (s *examStub) Unpublish(_ context.Context, p auth.Principal, examID id.ID) error {
	s.record("unpublish", p, examID)
	return s.err
}

func (s *examStub) Duplicate(_ context.Context, p auth.Principal, examID id.ID) (app.ExamDetail, error) {
	s.record("duplicate", p, examID)
	return s.detail, s.err
}

func (s *examStub) Delete(_ context.Context, p auth.Principal, examID id.ID) error {
	s.record("delete", p, examID)
	return s.err
}

func (s *examStub) ListAttempts(_ context.Context, p auth.Principal, examID id.ID) ([]domain.ExamAttempt, error) {
	s.record("listAttempts", p, examID)
	return s.attempts, s.err
}

func (s *examStub) List(_ context.Context, p auth.Principal, courseID id.ID) ([]app.StudentExam, error) {
	s.record("list", p, courseID)
	return s.students, s.err
}

func (s *examStub) Get(_ context.Context, p auth.Principal, examID id.ID) (domain.Exam, error) {
	s.record("get", p, examID)
	return s.exam, s.err
}

func (s *examStub) Start(_ context.Context, p auth.Principal, examID id.ID) (app.AttemptDetail, error) {
	s.record("start", p, examID)
	return s.attempt, s.err
}

func (s *examStub) SaveAnswer(_ context.Context, p auth.Principal, attemptID id.ID, in app.AnswerInput) error {
	s.record("saveAnswer", p, attemptID)
	s.answer = in
	return s.err
}

func (s *examStub) Submit(_ context.Context, p auth.Principal, attemptID id.ID) (app.AttemptDetail, error) {
	s.record("submit", p, attemptID)
	return s.attempt, s.err
}

func (s *examStub) GetAttempt(_ context.Context, p auth.Principal, attemptID id.ID) (app.AttemptDetail, error) {
	s.record("getAttempt", p, attemptID)
	return s.attempt, s.err
}

func (s *examStub) Review(_ context.Context, p auth.Principal, attemptID id.ID) (app.AttemptDetail, error) {
	s.record("review", p, attemptID)
	return s.attempt, s.err
}

func newExamServer(s *examStub) http.Handler {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	r, h := httpserver.NewRouter(httpserver.Options{Logger: logger, AllowedOrigins: []string{"https://app.test"}, ServiceName: "test"})
	httpapi.New(&stub{}, s, httpapi.Config{RequireAuth: fakeAuth, Logger: logger}).Register(r)
	return h
}

// sampleExam has question 11 (single choice, option 111 correct, 112; 2 points, reference
// lecture 50) and question 12 (multiple choice, option 121 correct, 122; 1 point).
func sampleExam() domain.Exam {
	closes := t0.Add(2 * time.Hour)
	return domain.Exam{ID: 1, CourseID: 10, Title: "Final", Description: "d", Position: 2, Status: domain.ExamPublished,
		PassMark: 50, TimeLimit: 30 * time.Minute, ClosesAt: &closes, RevealPolicy: domain.RevealAfterClose,
		Questions: []domain.Question{
			{ID: 11, Prompt: "2+2?", Type: domain.QuestionSingleChoice, Explanation: "arith", Points: 2, ReferenceLectureID: 50,
				Options: []domain.Option{{ID: 111, Label: "4", IsCorrect: true}, {ID: 112, Label: "5"}}},
			{ID: 12, Prompt: "Primes?", Type: domain.QuestionMultipleChoice, Points: 1,
				Options: []domain.Option{{ID: 121, Label: "2", IsCorrect: true}, {ID: 122, Label: "4"}}},
		}}
}

// gradedAttempt answered question 11 correctly and question 12 not at all.
func gradedAttempt(e domain.Exam) domain.ExamAttempt {
	a := domain.NewExamAttempt(7, e, 200, t0)
	a.Answers = []domain.ExamAnswer{{QuestionID: 11, OptionIDs: []id.ID{111}}}
	a.Grade(e, t0.Add(time.Minute))
	return a
}

func decode(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("decode %s: %v", b, err)
	}
	return out
}

func TestExamRouteDispatch(t *testing.T) {
	e := sampleExam()
	full := &examStub{exams: []domain.Exam{e}, exam: e, detail: app.ExamDetail{Exam: e},
		attempt: app.AttemptDetail{Attempt: gradedAttempt(e), Exam: e}}
	cases := []struct {
		method, path, body string
		called             string
		status             int
		target             id.ID
	}{
		{"GET", "/v1/courses/10/exams/authoring", "", "listAuthoring", 200, 10},
		{"POST", "/v1/courses/10/exams", saveExamBody, "create", 201, 10},
		{"PUT", "/v1/courses/10/exams-order", `{"exam_ids":["2","1"]}`, "reorder", 204, 10},
		{"GET", "/v1/courses/10/exams", "", "list", 200, 10},
		{"GET", "/v1/exams/1/authoring", "", "getAuthoring", 200, 1},
		{"PUT", "/v1/exams/1", saveExamBody, "save", 200, 1},
		{"PATCH", "/v1/exams/1/settings", settingsBody, "saveSettings", 200, 1},
		{"POST", "/v1/exams/1/publish", "", "publish", 204, 1},
		{"POST", "/v1/exams/1/unpublish", "", "unpublish", 204, 1},
		{"POST", "/v1/exams/1/duplicate", "", "duplicate", 201, 1},
		{"DELETE", "/v1/exams/1", "", "delete", 204, 1},
		{"GET", "/v1/exams/1/attempts", "", "listAttempts", 200, 1},
		{"GET", "/v1/exams/1", "", "get", 200, 1},
		{"POST", "/v1/exams/1/attempts", "", "start", 201, 1},
		{"POST", "/v1/exam-attempts/7/answers", `{"question_id":"11","option_ids":["111"]}`, "saveAnswer", 204, 7},
		{"POST", "/v1/exam-attempts/7/submit", "", "submit", 200, 7},
		{"GET", "/v1/exam-attempts/7", "", "getAttempt", 200, 7},
		{"GET", "/v1/exam-attempts/7/review", "", "review", 200, 7},
	}
	for _, tc := range cases {
		s := *full
		code, body := call(newExamServer(&s), tc.method, tc.path, tc.body)
		if code != tc.status || s.called != tc.called || s.target != tc.target || s.p.UserID != 200 {
			t.Errorf("%s %s: code=%d called=%s target=%d body=%s", tc.method, tc.path, code, s.called, s.target, body)
		}
		if tc.status == http.StatusNoContent && len(body) != 0 {
			t.Errorf("%s %s: body %s", tc.method, tc.path, body)
		}
	}
	if code, _ := call(newExamServer(&examStub{}), "GET", "/v1/exams/abc", ""); code != http.StatusNotFound {
		t.Fatalf("malformed id: %d", code)
	}
}

func TestStudentExamHidesKey(t *testing.T) {
	code, body := call(newExamServer(&examStub{exam: sampleExam()}), "GET", "/v1/exams/1", "")
	if code != http.StatusOK {
		t.Fatalf("code=%d body=%s", code, body)
	}
	got := decode(t, body)
	q := got["questions"].([]any)[0].(map[string]any)
	if _, leaked := q["correct_option_ids"]; leaked {
		t.Fatalf("key leaked: %s", body)
	}
	if _, leaked := q["explanation"]; leaked {
		t.Fatalf("explanation leaked: %s", body)
	}
	if strings.Contains(string(body), "is_correct") {
		t.Fatalf("correctness leaked: %s", body)
	}
	if q["reference_lecture_id"] != "50" || q["points"] != 2.0 || got["course_id"] != "10" || got["time_limit_seconds"] != 1800.0 ||
		got["closes_at"] != "2026-10-06T11:00:00Z" || got["reveal_policy"] != "after_close" || got["position"] != 2.0 {
		t.Fatalf("exam = %s", body)
	}
	if _, ok := got["opens_at"]; ok {
		t.Fatalf("absent opens_at serialized: %s", body)
	}
	if _, ok := got["questions"].([]any)[1].(map[string]any)["reference_lecture_id"]; ok {
		t.Fatalf("absent reference serialized: %s", body)
	}
}

func TestUntimedExamWire(t *testing.T) {
	e := sampleExam()
	e.TimeLimit = 0
	_, body := call(newExamServer(&examStub{exam: e}), "GET", "/v1/exams/1", "")
	if !strings.Contains(string(body), `"time_limit_seconds":null`) {
		t.Fatalf("body = %s", body)
	}
}

func TestExamAuthoringWire(t *testing.T) {
	d := app.ExamDetail{Exam: sampleExam(), Locks: domain.Locks{OpenAttempts: 1, SubmittedAttempts: 2,
		AnsweredQuestionIDs: map[id.ID]struct{}{12: {}, 11: {}}}}
	_, body := call(newExamServer(&examStub{detail: d}), "GET", "/v1/exams/1/authoring", "")
	got := decode(t, body)
	locks, _ := json.Marshal(got["locks"])
	if string(locks) != `{"answered_question_ids":["11","12"],"has_open_attempts":true,"has_submitted_attempts":true,"open_attempt_count":1,"submitted_attempt_count":2}` {
		t.Fatalf("locks = %s", locks)
	}
	q := got["questions"].([]any)[0].(map[string]any)
	if got["status"] != "published" || q["explanation"] != "arith" || q["correct_option_ids"].([]any)[0] != "111" {
		t.Fatalf("authoring = %s", body)
	}
	_, body = call(newExamServer(&examStub{detail: app.ExamDetail{Exam: sampleExam()}}), "POST", "/v1/courses/10/exams", saveExamBody)
	if !strings.Contains(string(body), `"answered_question_ids":[]`) {
		t.Fatalf("empty locks = %s", body)
	}
}

func TestAuthoringSummaryWire(t *testing.T) {
	_, body := call(newExamServer(&examStub{exams: []domain.Exam{sampleExam()}}), "GET", "/v1/courses/10/exams/authoring", "")
	var got []map[string]any
	if err := json.Unmarshal(body, &got); err != nil || len(got) != 1 {
		t.Fatalf("body = %s", body)
	}
	if got[0]["question_count"] != 2.0 || got[0]["total_points"] != 3.0 || got[0]["status"] != "published" || got[0]["title"] != "Final" {
		t.Fatalf("summary = %v", got[0])
	}
	if _, body = call(newExamServer(&examStub{}), "GET", "/v1/courses/10/exams/authoring", ""); strings.TrimSpace(string(body)) != "[]" {
		t.Fatalf("empty = %s", body)
	}
}

const saveExamBody = `{"title":"Final","description":"d","position":2,"pass_mark":50,"time_limit_seconds":null,` +
	`"retakes_allowed":true,"opens_at":null,"closes_at":"2026-10-06T11:00:00Z","reveal_policy":"after_close",` +
	`"questions":[{"id":"","prompt":"2+2?","type":"single_choice","explanation":"arith","points":2,"reference_lecture_id":"",` +
	`"options":[{"label":"4","is_correct":true},{"id":"112","label":"5","is_correct":false}]}]}`

const settingsBody = `{"title":"Final","description":"","pass_mark":70,"points":{"11":4},"time_limit_seconds":600,` +
	`"retakes_allowed":false,"opens_at":"2026-10-06T08:00:00Z","closes_at":null,"reveal_policy":"never"}`

func TestSaveExamDecodes(t *testing.T) {
	s := &examStub{detail: app.ExamDetail{Exam: sampleExam()}}
	if code, body := call(newExamServer(s), "PUT", "/v1/exams/1", saveExamBody); code != http.StatusOK {
		t.Fatalf("code=%d body=%s", code, body)
	}
	in := s.in
	if in.Title != "Final" || in.Position != 2 || in.PassMark != 50 || in.TimeLimitSeconds != nil || !in.RetakesAllowed ||
		in.OpensAt != nil || !in.ClosesAt.Equal(t0.Add(2*time.Hour)) || in.RevealPolicy != "after_close" || len(in.Questions) != 1 ||
		in.Questions[0].Points != 2 || in.Questions[0].Options[1].ID != "112" {
		t.Fatalf("in = %+v", in)
	}
}

func TestSaveSettingsDecodes(t *testing.T) {
	s := &examStub{detail: app.ExamDetail{Exam: sampleExam()}}
	if code, body := call(newExamServer(s), "PATCH", "/v1/exams/1/settings", settingsBody); code != http.StatusOK {
		t.Fatalf("code=%d body=%s", code, body)
	}
	in := s.settings
	if in.PassMark != 70 || in.Points["11"] != 4 || *in.TimeLimitSeconds != 600 || in.RetakesAllowed || !in.OpensAt.Equal(t0.Add(-time.Hour)) ||
		in.ClosesAt != nil || in.RevealPolicy != "never" {
		t.Fatalf("in = %+v", in)
	}
}

func TestExamBadBodies(t *testing.T) {
	h := newExamServer(&examStub{})
	for _, tc := range []struct{ method, path, body string }{
		{"PUT", "/v1/courses/10/exams-order", `{"exam_ids":["x"]}`},
		{"PUT", "/v1/courses/10/exams-order", `{"exam_ids":[],"extra":1}`},
		{"PUT", "/v1/exams/1", `{"title":"x","opens_at":"tomorrow"}`},
		{"POST", "/v1/exam-attempts/7/answers", `{"question_id":"11","option_ids":["111"],"user_id":"200"}`},
	} {
		if code, body := call(h, tc.method, tc.path, tc.body); code != http.StatusBadRequest {
			t.Errorf("%s %s: %d %s", tc.method, tc.path, code, body)
		}
	}
}

func TestReorderAndAnswerDecode(t *testing.T) {
	s := &examStub{}
	call(newExamServer(s), "PUT", "/v1/courses/10/exams-order", `{"exam_ids":["2","1"]}`)
	if len(s.order) != 2 || s.order[0] != 2 || s.order[1] != 1 {
		t.Fatalf("order = %v", s.order)
	}
	call(newExamServer(s), "POST", "/v1/exam-attempts/7/answers", `{"question_id":"11","option_ids":["111"]}`)
	if s.answer.QuestionID != "11" || len(s.answer.OptionIDs) != 1 || s.answer.OptionIDs[0] != "111" {
		t.Fatalf("answer = %+v", s.answer)
	}
}

func TestStudentSummaryWire(t *testing.T) {
	score, passed := 80, true
	s := &examStub{students: []app.StudentExam{
		{Exam: sampleExam(), Availability: domain.AvailabilityOpen, OpenAttemptID: 9, BestScore: &score, BestPassed: &passed, AttemptCount: 3},
		{Exam: sampleExam(), Availability: domain.AvailabilityNotOpen},
	}}
	_, body := call(newExamServer(s), "GET", "/v1/courses/10/exams", "")
	var got []map[string]any
	if err := json.Unmarshal(body, &got); err != nil || len(got) != 2 {
		t.Fatalf("body = %s", body)
	}
	if got[0]["availability"] != "open" || got[0]["open_attempt_id"] != "9" || got[0]["best_score"] != 80.0 || got[0]["best_passed"] != true ||
		got[0]["attempt_count"] != 3.0 || got[0]["total_points"] != 3.0 || got[0]["question_count"] != 2.0 {
		t.Fatalf("first = %v", got[0])
	}
	for _, k := range []string{"open_attempt_id", "best_score", "best_passed"} {
		if _, ok := got[1][k]; ok {
			t.Errorf("%s serialized when absent", k)
		}
	}
	if got[1]["attempt_count"] != 0.0 || got[1]["availability"] != "not_open" {
		t.Fatalf("second = %v", got[1])
	}
}

func TestExamAttemptWire(t *testing.T) {
	e := sampleExam()
	open := domain.NewExamAttempt(7, e, 200, t0)
	open.Answers = []domain.ExamAnswer{{QuestionID: 12, OptionIDs: []id.ID{121}}, {QuestionID: 11, OptionIDs: []id.ID{112}}}
	_, body := call(newExamServer(&examStub{attempt: app.AttemptDetail{Attempt: open, Exam: e}}), "POST", "/v1/exams/1/attempts", "")
	want := `{"id":"7","course_id":"10","exam_id":"1","user_id":"200","started_at":"2026-10-06T09:00:00Z",` +
		`"deadline":"2026-10-06T09:30:00Z","auto_submitted":false,"answers":[{"question_id":"11","option_ids":["112"]},` +
		`{"question_id":"12","option_ids":["121"]}]}`
	if strings.TrimSpace(string(body)) != want {
		t.Fatalf("open attempt = %s", body)
	}

	graded := gradedAttempt(e)
	_, body = call(newExamServer(&examStub{attempt: app.AttemptDetail{Attempt: graded, Exam: e}}), "POST", "/v1/exam-attempts/7/submit", "")
	want = `{"id":"7","course_id":"10","exam_id":"1","user_id":"200","started_at":"2026-10-06T09:00:00Z",` +
		`"deadline":"2026-10-06T09:30:00Z","submitted_at":"2026-10-06T09:01:00Z","score":66,"passed":true,"auto_submitted":false,` +
		`"answers":[{"question_id":"11","option_ids":["111"],"is_correct":true,"points_possible":2,"points_awarded":2,"reference_lecture_id":"50"},` +
		`{"question_id":"12","option_ids":[],"is_correct":false,"points_possible":1,"points_awarded":0}]}`
	if strings.TrimSpace(string(body)) != want {
		t.Fatalf("graded attempt = %s", body)
	}
}

func TestAttemptSummaryWire(t *testing.T) {
	e := sampleExam()
	s := &examStub{attempts: []domain.ExamAttempt{gradedAttempt(e), domain.NewExamAttempt(8, e, 201, t0)}}
	_, body := call(newExamServer(s), "GET", "/v1/exams/1/attempts", "")
	want := `[{"id":"7","user_id":"200","started_at":"2026-10-06T09:00:00Z","submitted_at":"2026-10-06T09:01:00Z","score":66,` +
		`"passed":true,"auto_submitted":false,"still_open":false},` +
		`{"id":"8","user_id":"201","started_at":"2026-10-06T09:00:00Z","auto_submitted":false,"still_open":true}]`
	if strings.TrimSpace(string(body)) != want {
		t.Fatalf("summaries = %s", body)
	}
}

func TestReviewWire(t *testing.T) {
	e := sampleExam()
	_, body := call(newExamServer(&examStub{attempt: app.AttemptDetail{Attempt: gradedAttempt(e), Exam: e}}), "GET", "/v1/exam-attempts/7/review", "")
	want := `{"attempt_id":"7","exam_id":"1","title":"Final","score":66,"passed":true,"pass_mark":50,` +
		`"submitted_at":"2026-10-06T09:01:00Z","auto_submitted":false,"questions":[` +
		`{"id":"11","position":0,"prompt":"2+2?","type":"single_choice","options":[{"id":"111","label":"4"},{"id":"112","label":"5"}],` +
		`"selected_option_ids":["111"],"correct_option_ids":["111"],"explanation":"arith","points_possible":2,"points_awarded":2,"reference_lecture_id":"50"},` +
		`{"id":"12","position":1,"prompt":"Primes?","type":"multiple_choice","options":[{"id":"121","label":"2"},{"id":"122","label":"4"}],` +
		`"selected_option_ids":[],"correct_option_ids":["121"],"explanation":"","points_possible":1,"points_awarded":0}]}`
	if strings.TrimSpace(string(body)) != want {
		t.Fatalf("review = %s", body)
	}
}

func TestReviewSkipsQuestionsAddedLater(t *testing.T) {
	e := sampleExam()
	a := gradedAttempt(e)
	e.Questions = append(e.Questions, domain.Question{ID: 13, Prompt: "New?", Type: domain.QuestionTrueFalse, Points: 5,
		Options: []domain.Option{{ID: 131, Label: "yes", IsCorrect: true}, {ID: 132, Label: "no"}}})
	_, body := call(newExamServer(&examStub{attempt: app.AttemptDetail{Attempt: a, Exam: e}}), "GET", "/v1/exam-attempts/7/review", "")
	got := decode(t, body)
	if len(got["questions"].([]any)) != 2 || got["score"] != 66.0 || strings.Contains(string(body), `"13"`) {
		t.Fatalf("review = %s", body)
	}
	_, body = call(newExamServer(&examStub{attempt: app.AttemptDetail{Attempt: a, Exam: e}}), "GET", "/v1/exam-attempts/7", "")
	if strings.Contains(string(body), `"13"`) {
		t.Fatalf("attempt = %s", body)
	}
}

func TestExamErrorMapping(t *testing.T) {
	opens, closes := t0.Add(time.Hour), t0.Add(2*time.Hour)
	cases := []struct {
		err     error
		status  int
		typ     string
		details string
	}{
		{&app.WindowError{Err: app.ErrExamNotOpen, At: opens}, 409, "exam_not_open", `{"opens_at":"2026-10-06T10:00:00Z"}`},
		{&app.WindowError{Err: app.ErrExamClosed, At: closes}, 409, "exam_closed", `{"closes_at":"2026-10-06T11:00:00Z"}`},
		{app.ErrOpenAttemptExists, 409, "open_attempt_exists", ""},
		{app.ErrRetakesNotAllowed, 409, "retakes_not_allowed", ""},
		{app.ErrExamHasAttempts, 409, "exam_has_attempts", ""},
		{app.ErrEnrollmentRequired, 409, "enrollment_required", ""},
		{domain.ErrAttemptSubmitted, 409, "attempt_submitted", ""},
		{domain.ErrAttemptExpired, 409, "attempt_expired", ""},
		{domain.ErrRevealAttemptOpen, 409, "reveal_attempt_open", ""},
		{domain.ErrRevealDisabled, 403, "reveal_disabled", ""},
		{&domain.RevealNotYetError{At: closes}, 403, "reveal_not_yet", `{"reveal_at":"2026-10-06T11:00:00Z"}`},
		{&domain.EditViolation{Err: domain.ErrEditWouldTruncateAttempt}, 409, "edit_would_truncate_attempt", ""},
		{&domain.EditViolation{Err: domain.ErrEditAddDuringAttempt, QuestionID: 13}, 409, "edit_add_during_attempt", `{"question_id":"13"}`},
		{&domain.EditViolation{Err: domain.ErrEditQuestionAnswered, QuestionID: 11}, 409, "edit_question_answered", `{"question_id":"11"}`},
		{&domain.EditViolation{Err: domain.ErrEditKeyFrozen, QuestionID: 11, OptionID: 112}, 409, "edit_key_frozen", `{"option_id":"112","question_id":"11"}`},
		{errors.Join(app.ErrInvalidInput, domain.ErrInvalidAnswer), 400, "invalid_input", ""},
	}
	for _, tc := range cases {
		code, body := call(newExamServer(&examStub{err: tc.err}), "POST", "/v1/exams/1/attempts", "")
		got := decode(t, body)
		details := ""
		if d, ok := got["details"]; ok {
			b, _ := json.Marshal(d)
			details = string(b)
		}
		if code != tc.status || got["type"] != tc.typ || details != tc.details {
			t.Errorf("%v: code=%d body=%s", tc.err, code, body)
		}
	}
}
