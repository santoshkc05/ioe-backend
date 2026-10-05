// Package httpapi exposes enrollment over HTTP.
package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/santoshkc2200/ioe-backend/internal/enrollment/app"
	"github.com/santoshkc2200/ioe-backend/internal/enrollment/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/httpserver"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
	"github.com/santoshkc2200/ioe-backend/internal/platform/problem"
)

// Service is the enrollment use-case surface the handlers call.
type Service interface {
	Enroll(ctx context.Context, p auth.Principal, courseID, userID id.ID) (domain.Enrollment, bool, error)
	Cancel(ctx context.Context, p auth.Principal, courseID, userID id.ID, reason string) (domain.Enrollment, error)
	ListByCourse(ctx context.Context, p auth.Principal, courseID id.ID, limit, offset int) ([]domain.Enrollment, int, error)
	ListByUser(ctx context.Context, p auth.Principal, userID id.ID) ([]domain.Enrollment, error)
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

// Register mounts the enrollment routes. Every route requires authentication.
func (h *Handler) Register(r *httpserver.Router) {
	a := func(f http.HandlerFunc) http.Handler { return h.cfg.RequireAuth(f) }
	r.Handle("POST /v1/courses/{courseID}/enrollments/{userID}", a(h.enroll))
	r.Handle("DELETE /v1/courses/{courseID}/enrollments/{userID}", a(h.cancel))
	r.Handle("GET /v1/courses/{courseID}/enrollments", a(h.listByCourse))
	r.Handle("GET /v1/users/{userID}/enrollments", a(h.listByUser))
}

func (h *Handler) enroll(w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "courseID", "userID")
	if !ok {
		return
	}
	e, activated, err := h.svc.Enroll(r.Context(), principal(r), ids[0], ids[1])
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	status := http.StatusOK
	if activated {
		status = http.StatusCreated
	}
	httpserver.WriteJSON(w, status, toWire(e))
}

func (h *Handler) cancel(w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "courseID", "userID")
	if !ok {
		return
	}
	// The body is optional: clients send it only when they have a reason.
	var req cancelRequest
	if r.ContentLength != 0 && !httpserver.DecodeJSON(w, r, &req) {
		return
	}
	e, err := h.svc.Cancel(r.Context(), principal(r), ids[0], ids[1], req.Reason)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toWire(e))
}

func (h *Handler) listByCourse(w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "courseID")
	if !ok {
		return
	}
	limit, ok := queryInt(w, r, "limit", app.DefaultPageLimit)
	if !ok {
		return
	}
	offset, ok := queryInt(w, r, "offset", 0)
	if !ok {
		return
	}
	es, total, err := h.svc.ListByCourse(r.Context(), principal(r), ids[0], limit, offset)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, enrollmentPageWire{Enrollments: toWires(es), Total: total})
}

func (h *Handler) listByUser(w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "userID")
	if !ok {
		return
	}
	es, err := h.svc.ListByUser(r.Context(), principal(r), ids[0])
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toWires(es))
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

// queryInt parses an optional integer query parameter; range checks belong to the service.
func queryInt(w http.ResponseWriter, r *http.Request, name string, def int) (int, bool) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return def, true
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		problem.Write(w, r, http.StatusBadRequest, "invalid_input", "Invalid Input", name+" must be an integer")
		return 0, false
	}
	return v, true
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

var errorMappings = []errorMapping{
	{app.ErrNotFound, http.StatusNotFound, "not_found", "Not Found"},
	{domain.ErrCourseHidden, http.StatusNotFound, "not_found", "Not Found"},
	{domain.ErrForbidden, http.StatusForbidden, "forbidden", "Forbidden"},
	{domain.ErrPaymentRequired, http.StatusPaymentRequired, "payment_required", "Payment Required"},
	{domain.ErrCourseNotPublished, http.StatusConflict, "course_not_published", "Course Not Published"},
	{app.ErrConcurrentModification, http.StatusConflict, "concurrent_modification", "Concurrent Modification"},
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
	h.cfg.Logger.ErrorContext(r.Context(), "enrollment request failed", "error", err)
	problem.Write(w, r, http.StatusInternalServerError, problem.TypeInternal, "Internal Server Error", "")
}
