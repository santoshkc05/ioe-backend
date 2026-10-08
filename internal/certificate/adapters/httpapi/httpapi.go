// Package httpapi exposes certificates over HTTP.
package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/santoshkc2200/ioe-backend/internal/certificate/app"
	"github.com/santoshkc2200/ioe-backend/internal/certificate/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/httpserver"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
	"github.com/santoshkc2200/ioe-backend/internal/platform/problem"
)

// Service is the certificate use-case surface the handlers call.
type Service interface {
	GetPolicy(ctx context.Context, p auth.Principal, courseID id.ID) (domain.Policy, error)
	SetPolicy(ctx context.Context, p auth.Principal, courseID id.ID, mode domain.Mode, examID id.ID) (domain.Policy, error)
	Claim(ctx context.Context, p auth.Principal, courseID id.ID) (domain.Certificate, bool, error)
	GetMine(ctx context.Context, p auth.Principal, courseID id.ID) (domain.Certificate, error)
	ListMine(ctx context.Context, p auth.Principal) ([]domain.Certificate, error)
	Verify(ctx context.Context, code string) (domain.Certificate, error)
}

type Config struct {
	RequireAuth   httpserver.Middleware
	VerifyLimiter *httpserver.RateLimiter
	IPs           httpserver.IPResolver
	Logger        *slog.Logger
}

type Handler struct {
	svc Service
	cfg Config
}

func New(svc Service, cfg Config) *Handler { return &Handler{svc: svc, cfg: cfg} }

// Register mounts the certificate routes. Only verification is public.
func (h *Handler) Register(r *httpserver.Router) {
	a := func(f http.HandlerFunc) http.Handler { return h.cfg.RequireAuth(f) }
	r.Handle("GET /v1/courses/{courseID}/certificate-policy", a(h.getPolicy))
	r.Handle("PUT /v1/courses/{courseID}/certificate-policy", a(h.setPolicy))
	r.Handle("POST /v1/courses/{courseID}/certificate", a(h.claim))
	r.Handle("GET /v1/courses/{courseID}/certificate", a(h.getMine))
	r.Handle("GET /v1/me/certificates", a(h.listMine))
	r.Handle("GET /v1/certificates/{code}", h.cfg.VerifyLimiter.Middleware(h.cfg.IPs)(http.HandlerFunc(h.verify)))
}

func (h *Handler) getPolicy(w http.ResponseWriter, r *http.Request) {
	courseID, ok := courseID(w, r)
	if !ok {
		return
	}
	p, err := h.svc.GetPolicy(r.Context(), principal(r), courseID)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toPolicyWire(p))
}

func (h *Handler) setPolicy(w http.ResponseWriter, r *http.Request) {
	courseID, ok := courseID(w, r)
	if !ok {
		return
	}
	var req policyRequest
	if !httpserver.DecodeJSON(w, r, &req) {
		return
	}
	var examID id.ID
	if req.ExamID != nil {
		examID = *req.ExamID
	}
	p, err := h.svc.SetPolicy(r.Context(), principal(r), courseID, domain.Mode(req.Mode), examID)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toPolicyWire(p))
}

func (h *Handler) claim(w http.ResponseWriter, r *http.Request) {
	courseID, ok := courseID(w, r)
	if !ok {
		return
	}
	c, created, err := h.svc.Claim(r.Context(), principal(r), courseID)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	httpserver.WriteJSON(w, status, toCertificateWire(c))
}

func (h *Handler) getMine(w http.ResponseWriter, r *http.Request) {
	courseID, ok := courseID(w, r)
	if !ok {
		return
	}
	c, err := h.svc.GetMine(r.Context(), principal(r), courseID)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toCertificateWire(c))
}

func (h *Handler) listMine(w http.ResponseWriter, r *http.Request) {
	cs, err := h.svc.ListMine(r.Context(), principal(r))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toListWire(cs))
}

func (h *Handler) verify(w http.ResponseWriter, r *http.Request) {
	c, err := h.svc.Verify(r.Context(), r.PathValue("code"))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toVerifyWire(c))
}

// courseID parses the courseID path value. A malformed ID names no resource: 404.
func courseID(w http.ResponseWriter, r *http.Request) (id.ID, bool) {
	v, err := id.Parse(r.PathValue("courseID"))
	if err != nil {
		problem.Write(w, r, http.StatusNotFound, problem.TypeNotFound, "Not Found", "")
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
	{app.ErrForbidden, http.StatusForbidden, "forbidden", "Forbidden"},
	{app.ErrInvalidInput, http.StatusBadRequest, "invalid_input", "Invalid Input"},
	{app.ErrCertificatesDisabled, http.StatusConflict, "certificates_disabled", "Certificates Disabled"},
	{app.ErrNotEnrolled, http.StatusConflict, "not_enrolled", "Not Enrolled"},
	{app.ErrProgressIncomplete, http.StatusConflict, "progress_incomplete", "Course Not Complete"},
	{app.ErrExamNotPassed, http.StatusConflict, "exam_not_passed", "Exam Not Passed"},
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
	h.cfg.Logger.ErrorContext(r.Context(), "certificate request failed", "error", err)
	problem.Write(w, r, http.StatusInternalServerError, problem.TypeInternal, "Internal Server Error", "")
}
