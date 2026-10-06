// Package httpapi exposes learner progress over HTTP.
package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/httpserver"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
	"github.com/santoshkc2200/ioe-backend/internal/platform/problem"
	"github.com/santoshkc2200/ioe-backend/internal/progress/app"
	"github.com/santoshkc2200/ioe-backend/internal/progress/domain"
)

// Service is the progress use-case surface the handlers call.
type Service interface {
	RecordLecture(ctx context.Context, p auth.Principal, courseID, lectureID, userID id.ID, state domain.LectureState, positionMs int64) error
	CourseProgress(ctx context.Context, p auth.Principal, courseID, userID id.ID) (domain.CourseProgress, error)
	UserProgress(ctx context.Context, p auth.Principal, userID id.ID) ([]domain.CourseProgress, []domain.ActivityDay, error)
}

type Config struct {
	RequireAuth httpserver.Middleware
	Logger      *slog.Logger
}

type Handler struct {
	svc Service
	cfg Config
}

func New(svc Service, cfg Config) *Handler { return &Handler{svc: svc, cfg: cfg} }

// Register mounts the progress routes. Every route requires authentication.
func (h *Handler) Register(r *httpserver.Router) {
	a := func(f http.HandlerFunc) http.Handler { return h.cfg.RequireAuth(f) }
	r.Handle("PUT /v1/courses/{courseID}/lectures/{lectureID}/progress/{userID}", a(h.record))
	r.Handle("GET /v1/courses/{courseID}/progress/{userID}", a(h.courseProgress))
	r.Handle("GET /v1/users/{userID}/progress", a(h.userProgress))
}

func (h *Handler) record(w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "courseID", "lectureID", "userID")
	if !ok {
		return
	}
	var req recordRequest
	if !httpserver.DecodeJSON(w, r, &req) {
		return
	}
	err := h.svc.RecordLecture(r.Context(), principal(r), ids[0], ids[1], ids[2], domain.LectureState(req.State), req.PositionMs)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) courseProgress(w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "courseID", "userID")
	if !ok {
		return
	}
	cp, err := h.svc.CourseProgress(r.Context(), principal(r), ids[0], ids[1])
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toCourseWire(cp))
}

func (h *Handler) userProgress(w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "userID")
	if !ok {
		return
	}
	courses, days, err := h.svc.UserProgress(r.Context(), principal(r), ids[0])
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toUserWire(ids[0], courses, days))
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

// enrollment_required is 409 here, unlike courseauthoring's 403 on content reads: the
// frontend's write path treats only 409 as permanent and stops retrying.
var errorMappings = []errorMapping{
	{app.ErrNotFound, http.StatusNotFound, "not_found", "Not Found"},
	{domain.ErrCourseHidden, http.StatusNotFound, "not_found", "Not Found"},
	{domain.ErrLectureNotFound, http.StatusNotFound, "not_found", "Not Found"},
	{domain.ErrForbidden, http.StatusForbidden, "forbidden", "Forbidden"},
	{app.ErrEnrollmentRequired, http.StatusConflict, "enrollment_required", "Enrollment Required"},
	{app.ErrInvalidInput, http.StatusBadRequest, "invalid_input", "Invalid Input"},
}

func (h *Handler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	for _, m := range errorMappings {
		if errors.Is(err, m.err) {
			detail := ""
			if m.status == http.StatusBadRequest {
				detail = err.Error()
			}
			problem.Write(w, r, m.status, m.typ, m.title, detail)
			return
		}
	}
	h.cfg.Logger.ErrorContext(r.Context(), "progress request failed", "error", err)
	problem.Write(w, r, http.StatusInternalServerError, problem.TypeInternal, "Internal Server Error", "")
}
