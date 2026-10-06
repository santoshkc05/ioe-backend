// Package httpapi exposes quizzes and exams over HTTP.
package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/santoshkc2200/ioe-backend/internal/assessment/app"
	"github.com/santoshkc2200/ioe-backend/internal/assessment/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/httpserver"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
	"github.com/santoshkc2200/ioe-backend/internal/platform/problem"
)

// QuizService is the quiz use-case surface the handlers call.
type QuizService interface {
	List(ctx context.Context, p auth.Principal, courseID, lectureID id.ID) ([]domain.Quiz, error)
	Create(ctx context.Context, p auth.Principal, courseID, lectureID id.ID, in app.QuizInput) (domain.Quiz, error)
	Update(ctx context.Context, p auth.Principal, courseID, lectureID, quizID id.ID, in app.QuizInput) (domain.Quiz, error)
	Delete(ctx context.Context, p auth.Principal, quizID id.ID) error
	RecordAttempt(ctx context.Context, p auth.Principal, quizID, userID id.ID, answers []app.AnswerInput, key string) (id.ID, error)
}

type Config struct {
	RequireAuth httpserver.Middleware
	Logger      *slog.Logger
}

type Handler struct {
	quizzes QuizService
	exams   ExamService
	cfg     Config
}

func New(quizzes QuizService, exams ExamService, cfg Config) *Handler {
	return &Handler{quizzes: quizzes, exams: exams, cfg: cfg}
}

// Register mounts the quiz and exam routes. Every route requires authentication.
func (h *Handler) Register(r *httpserver.Router) {
	a := func(f http.HandlerFunc) http.Handler { return h.cfg.RequireAuth(f) }
	r.Handle("GET /v1/courses/{courseID}/lectures/{lectureID}/quizzes", a(h.list))
	r.Handle("POST /v1/courses/{courseID}/lectures/{lectureID}/quizzes", a(h.create))
	r.Handle("PUT /v1/courses/{courseID}/lectures/{lectureID}/quizzes/{quizID}", a(h.update))
	r.Handle("DELETE /v1/quizzes/{quizID}", a(h.delete))
	r.Handle("POST /v1/quizzes/{quizID}/attempts", a(h.recordAttempt))
	h.registerExams(r, a)
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "courseID", "lectureID")
	if !ok {
		return
	}
	qs, err := h.quizzes.List(r.Context(), principal(r), ids[0], ids[1])
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	out := make([]quizWire, len(qs))
	for i, q := range qs {
		out[i] = toQuizWire(q)
	}
	httpserver.WriteJSON(w, http.StatusOK, out)
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "courseID", "lectureID")
	if !ok {
		return
	}
	var req saveQuizRequest
	if !httpserver.DecodeJSON(w, r, &req) {
		return
	}
	q, err := h.quizzes.Create(r.Context(), principal(r), ids[0], ids[1], req.toInput())
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, toQuizWire(q))
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "courseID", "lectureID", "quizID")
	if !ok {
		return
	}
	var req saveQuizRequest
	if !httpserver.DecodeJSON(w, r, &req) {
		return
	}
	q, err := h.quizzes.Update(r.Context(), principal(r), ids[0], ids[1], ids[2], req.toInput())
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toQuizWire(q))
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "quizID")
	if !ok {
		return
	}
	if err := h.quizzes.Delete(r.Context(), principal(r), ids[0]); err != nil {
		h.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) recordAttempt(w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "quizID")
	if !ok {
		return
	}
	var req recordAttemptRequest
	if !httpserver.DecodeJSON(w, r, &req) {
		return
	}
	if req.UserID.IsZero() {
		problem.Write(w, r, http.StatusBadRequest, "invalid_input", "Invalid Input", "user_id is required")
		return
	}
	attemptID, err := h.quizzes.RecordAttempt(r.Context(), principal(r), ids[0], req.UserID, req.toAnswers(), r.Header.Get("Idempotency-Key"))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, attemptWire{ID: attemptID, Recorded: true})
}

// pathIDs parses the named path values. Any parse failure is a 404: a malformed ID
// names no resource.
func pathIDs(w http.ResponseWriter, r *http.Request, names ...string) ([]id.ID, bool) {
	out := make([]id.ID, len(names))
	for i, n := range names {
		v, err := id.Parse(r.PathValue(n))
		if err != nil {
			problem.Write(w, r, http.StatusNotFound, problem.TypeNotFound, "Not Found", "")
			return nil, false
		}
		out[i] = v
	}
	return out, true
}

func principal(r *http.Request) auth.Principal {
	p, _ := auth.PrincipalFrom(r.Context()) // RequireAuth guarantees presence
	return p
}

type errorMapping struct {
	err    error
	status int
	typ    string
	title  string
}

// enrollment_required is 409, as in progress: the frontend treats 409 as permanent and stops
// retrying a queued attempt.
var errorMappings = []errorMapping{
	{app.ErrNotFound, http.StatusNotFound, "not_found", "Not Found"},
	{app.ErrForbidden, http.StatusForbidden, "forbidden", "Forbidden"},
	{app.ErrCourseNotEditable, http.StatusConflict, "course_not_editable", "Course Not Editable"},
	{app.ErrEnrollmentRequired, http.StatusConflict, "enrollment_required", "Enrollment Required"},
	{app.ErrInvalidInput, http.StatusBadRequest, "invalid_input", "Invalid Input"},
	{app.ErrExamNotOpen, http.StatusConflict, "exam_not_open", "Exam Not Open"},
	{app.ErrExamClosed, http.StatusConflict, "exam_closed", "Exam Closed"},
	{app.ErrOpenAttemptExists, http.StatusConflict, "open_attempt_exists", "Open Attempt Exists"},
	{app.ErrRetakesNotAllowed, http.StatusConflict, "retakes_not_allowed", "Retakes Not Allowed"},
	{app.ErrExamHasAttempts, http.StatusConflict, "exam_has_attempts", "Exam Has Attempts"},
	{domain.ErrAttemptSubmitted, http.StatusConflict, "attempt_submitted", "Attempt Submitted"},
	{domain.ErrAttemptExpired, http.StatusConflict, "attempt_expired", "Attempt Expired"},
	{domain.ErrRevealAttemptOpen, http.StatusConflict, "reveal_attempt_open", "Attempt Still Open"},
	{domain.ErrRevealDisabled, http.StatusForbidden, "reveal_disabled", "Reveal Disabled"},
	{domain.ErrRevealNotYet, http.StatusForbidden, "reveal_not_yet", "Reveal Not Yet"},
	{domain.ErrEditWouldTruncateAttempt, http.StatusConflict, "edit_would_truncate_attempt", "Edit Would Truncate Attempt"},
	{domain.ErrEditAddDuringAttempt, http.StatusConflict, "edit_add_during_attempt", "Edit During Attempt"},
	{domain.ErrEditQuestionAnswered, http.StatusConflict, "edit_question_answered", "Question Answered"},
	{domain.ErrEditKeyFrozen, http.StatusConflict, "edit_key_frozen", "Answer Key Frozen"},
}

func (h *Handler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	for _, m := range errorMappings {
		if errors.Is(err, m.err) {
			detail := ""
			if m.status == http.StatusBadRequest {
				detail = err.Error()
			}
			if d := errorDetails(err); d != nil {
				problem.WriteWithExtensions(w, r, m.status, m.typ, m.title, detail, map[string]any{"details": d})
				return
			}
			problem.Write(w, r, m.status, m.typ, m.title, detail)
			return
		}
	}
	h.cfg.Logger.ErrorContext(r.Context(), "assessment request failed", "error", err)
	problem.Write(w, r, http.StatusInternalServerError, problem.TypeInternal, "Internal Server Error", "")
}

// errorDetails returns the data a client needs to explain err: when an exam window opens or
// closed, when answers are revealed, or which question and option an edit would break.
func errorDetails(err error) map[string]any {
	if w := (*app.WindowError)(nil); errors.As(err, &w) {
		if errors.Is(w.Err, app.ErrExamClosed) {
			return map[string]any{"closes_at": w.At}
		}
		return map[string]any{"opens_at": w.At}
	}
	if r := (*domain.RevealNotYetError)(nil); errors.As(err, &r) {
		return map[string]any{"reveal_at": r.At}
	}
	if v := (*domain.EditViolation)(nil); errors.As(err, &v) {
		d := map[string]any{}
		if !v.QuestionID.IsZero() {
			d["question_id"] = v.QuestionID
		}
		if !v.OptionID.IsZero() {
			d["option_id"] = v.OptionID
		}
		if len(d) > 0 {
			return d
		}
	}
	return nil
}
