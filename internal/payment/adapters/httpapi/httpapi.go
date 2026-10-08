// Package httpapi exposes payment over HTTP.
package httpapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"

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
	ListByUser(ctx context.Context, p auth.Principal, userID, before id.ID, limit int) ([]domain.Purchase, id.ID, error)
	RecordManual(ctx context.Context, p auth.Principal, in app.ManualInput) (domain.Purchase, error)
	Refund(ctx context.Context, p auth.Principal, purchaseID id.ID, in app.RefundInput) (domain.Purchase, error)
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
	r.Handle("GET /v1/me/purchases", a(h.listMine))
	r.Handle("GET /v1/users/{userID}/purchases", a(h.listUser))
	r.Handle("POST /v1/users/{userID}/purchases", a(h.recordManual))
	r.Handle("POST /v1/purchases/{purchaseID}/refund", a(h.refund))
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

const (
	defaultPageLimit = 20
	maxPageLimit     = 50
)

func (h *Handler) listMine(w http.ResponseWriter, r *http.Request) {
	h.list(w, r, principal(r).UserID)
}

func (h *Handler) listUser(w http.ResponseWriter, r *http.Request) {
	userID, ok := pathID(w, r, "userID")
	if !ok {
		return
	}
	h.list(w, r, userID)
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request, userID id.ID) {
	before, limit, err := pageQuery(r)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	items, next, err := h.svc.ListByUser(r.Context(), principal(r), userID, before, limit)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toPageWire(items, next))
}

// pageQuery reads limit (default 20, 1..50) and cursor. The service enforces the range too.
func pageQuery(r *http.Request) (id.ID, int, error) {
	v := r.URL.Query()
	limit := defaultPageLimit
	if s := v.Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil {
			return 0, 0, fmt.Errorf("%w: limit must be a number", app.ErrInvalidInput)
		}
		if n < 1 || n > maxPageLimit {
			return 0, 0, fmt.Errorf("%w: limit must be between 1 and %d", app.ErrInvalidInput, maxPageLimit)
		}
		limit = n
	}
	var before id.ID
	if s := v.Get("cursor"); s != "" {
		c, err := id.Parse(s)
		if err != nil || c <= 0 {
			return 0, 0, fmt.Errorf("%w: invalid cursor", app.ErrInvalidInput)
		}
		before = c
	}
	return before, limit, nil
}

func (h *Handler) recordManual(w http.ResponseWriter, r *http.Request) {
	userID, ok := pathID(w, r, "userID")
	if !ok {
		return
	}
	var req manualRequest
	if !httpserver.DecodeJSON(w, r, &req) {
		return
	}
	p, err := h.svc.RecordManual(r.Context(), principal(r), app.ManualInput{
		UserID: userID, CourseID: req.CourseID, AmountMinor: req.AmountMinor, Currency: req.Currency,
		Method: req.Method, Reference: req.Reference, Note: req.Note,
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, purchaseResponse{Purchase: toWire(p)})
}

func (h *Handler) refund(w http.ResponseWriter, r *http.Request) {
	purchaseID, ok := pathID(w, r, "purchaseID")
	if !ok {
		return
	}
	var req refundRequest
	if !httpserver.DecodeJSON(w, r, &req) {
		return
	}
	p, err := h.svc.Refund(r.Context(), principal(r), purchaseID, app.RefundInput{Reference: req.Reference, Note: req.Note})
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
	{app.ErrForbidden, http.StatusForbidden, "forbidden", "Forbidden"},
	{app.ErrCourseFree, http.StatusConflict, "course_free", "Course Is Free"},
	{app.ErrAlreadyEnrolled, http.StatusConflict, "already_enrolled", "Already Enrolled"},
	{app.ErrAlreadyPurchased, http.StatusConflict, "already_purchased", "Already Purchased"},
	{app.ErrConcurrentModification, http.StatusConflict, "concurrent_modification", "Concurrent Modification"},
	{app.ErrGatewayUnavailable, http.StatusServiceUnavailable, "payment_unavailable", "Payment Unavailable"},
	{app.ErrNotRefundable, http.StatusConflict, "not_refundable", "Not Refundable"},
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
