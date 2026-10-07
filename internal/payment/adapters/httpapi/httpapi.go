// Package httpapi exposes payment over HTTP.
package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/santoshkc2200/ioe-backend/internal/payment/app"
	"github.com/santoshkc2200/ioe-backend/internal/payment/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/httpserver"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
	"github.com/santoshkc2200/ioe-backend/internal/platform/problem"
)

// Service is the payment use-case surface the handlers call.
type Service interface {
	Checkout(ctx context.Context, p auth.Principal, courseID id.ID, gateway string) (domain.Purchase, app.Checkout, error)
	Confirm(ctx context.Context, p auth.Principal, purchaseID id.ID) (domain.Purchase, error)
	Get(ctx context.Context, p auth.Principal, purchaseID id.ID) (domain.Purchase, error)
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

// Register mounts the payment routes. Every route requires authentication.
func (h *Handler) Register(r *httpserver.Router) {
	a := func(f http.HandlerFunc) http.Handler { return h.cfg.RequireAuth(f) }
	r.Handle("POST /v1/courses/{courseID}/purchases", a(h.checkout))
	r.Handle("POST /v1/purchases/{purchaseID}/confirm", a(h.confirm))
	r.Handle("GET /v1/purchases/{purchaseID}", a(h.get))
}

func (h *Handler) checkout(w http.ResponseWriter, r *http.Request) {
	courseID, ok := pathID(w, r, "courseID")
	if !ok {
		return
	}
	var req checkoutRequest
	if !httpserver.DecodeJSON(w, r, &req) {
		return
	}
	p, co, err := h.svc.Checkout(r.Context(), principal(r), courseID, req.Gateway)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, checkoutResponse{Purchase: toWire(p), Checkout: toCheckoutWire(co)})
}

func (h *Handler) confirm(w http.ResponseWriter, r *http.Request) {
	purchaseID, ok := pathID(w, r, "purchaseID")
	if !ok {
		return
	}
	p, err := h.svc.Confirm(r.Context(), principal(r), purchaseID)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, purchaseResponse{Purchase: toWire(p)})
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	purchaseID, ok := pathID(w, r, "purchaseID")
	if !ok {
		return
	}
	p, err := h.svc.Get(r.Context(), principal(r), purchaseID)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, purchaseResponse{Purchase: toWire(p)})
}

// pathID parses a path value. A malformed ID names no resource, so it is a 404.
func pathID(w http.ResponseWriter, r *http.Request, name string) (id.ID, bool) {
	v, err := id.Parse(r.PathValue(name))
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
	{app.ErrInvalidInput, http.StatusBadRequest, "invalid_input", "Invalid Input"},
	{app.ErrNotFound, http.StatusNotFound, "not_found", "Not Found"},
	{app.ErrCourseFree, http.StatusConflict, "course_free", "Course Is Free"},
	{app.ErrAlreadyEnrolled, http.StatusConflict, "already_enrolled", "Already Enrolled"},
	{app.ErrAlreadyPurchased, http.StatusConflict, "already_purchased", "Already Purchased"},
	{app.ErrConcurrentModification, http.StatusConflict, "concurrent_modification", "Concurrent Modification"},
	{app.ErrGatewayUnavailable, http.StatusServiceUnavailable, "payment_unavailable", "Payment Unavailable"},
}

func (h *Handler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	for _, m := range errorMappings {
		if errors.Is(err, m.err) {
			detail := ""
			if m.status == http.StatusBadRequest {
				detail = err.Error()
			}
			if m.status >= http.StatusInternalServerError {
				h.cfg.Logger.WarnContext(r.Context(), "payment gateway unavailable", "error", err)
			}
			problem.Write(w, r, m.status, m.typ, m.title, detail)
			return
		}
	}
	h.cfg.Logger.ErrorContext(r.Context(), "payment request failed", "error", err)
	problem.Write(w, r, http.StatusInternalServerError, problem.TypeInternal, "Internal Server Error", "")
}
