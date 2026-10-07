// Package httpapi exposes media uploads and playback over HTTP.
package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/santoshkc2200/ioe-backend/internal/media/app"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/httpserver"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
	"github.com/santoshkc2200/ioe-backend/internal/platform/problem"
)

// Service is the media use-case surface the handlers call.
type Service interface {
	CreateUpload(ctx context.Context, p auth.Principal, courseID id.ID, in app.UploadInput) (app.Upload, error)
	PresignParts(ctx context.Context, p auth.Principal, assetID id.ID, partNumbers []int) ([]app.UploadPart, error)
	Complete(ctx context.Context, p auth.Principal, assetID id.ID, parts []app.CompletedPart) (app.AssetView, error)
	Status(ctx context.Context, p auth.Principal, assetID id.ID) (app.AssetView, error)
	Delete(ctx context.Context, p auth.Principal, assetID id.ID) error
	Resolve(ctx context.Context, p auth.Principal, courseID, lectureID, assetID id.ID) (app.Playback, error)
}

type Config struct {
	RequireAuth   httpserver.Middleware
	CreateLimiter *httpserver.RateLimiter
	Logger        *slog.Logger
}

type Handler struct {
	svc Service
	cfg Config
}

func New(svc Service, cfg Config) *Handler { return &Handler{svc: svc, cfg: cfg} }

// Register mounts the media routes. Every route requires authentication and is not cacheable.
func (h *Handler) Register(r *httpserver.Router) {
	a := func(f http.HandlerFunc) http.Handler { return httpserver.NoStore(h.cfg.RequireAuth(f)) }
	r.Handle("POST /v1/courses/{courseID}/media/uploads", a(h.limitCreate(h.createUpload)))
	r.Handle("POST /v1/media/uploads/{assetID}/parts", a(h.presignParts))
	r.Handle("POST /v1/media/uploads/{assetID}/complete", a(h.complete))
	r.Handle("GET /v1/media/assets/{assetID}", a(h.status))
	r.Handle("DELETE /v1/media/assets/{assetID}", a(h.delete))
	r.Handle("GET /v1/courses/{courseID}/lectures/{lectureID}/media/{assetID}", a(h.playback))
}

// limitCreate rate-limits upload creation per user.
func (h *Handler) limitCreate(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !h.cfg.CreateLimiter.Allow(principal(r).UserID.String()) {
			w.Header().Set("Retry-After", "60")
			problem.Write(w, r, http.StatusTooManyRequests, problem.TypeRateLimited, "Too Many Requests", "")
			return
		}
		next(w, r)
	}
}

func (h *Handler) createUpload(w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "courseID")
	if !ok {
		return
	}
	var req createUploadRequest
	if !httpserver.DecodeJSON(w, r, &req) {
		return
	}
	up, err := h.svc.CreateUpload(r.Context(), principal(r), ids[0], app.UploadInput{
		Kind: req.Kind, ContentType: req.ContentType, Filename: req.Filename, SizeBytes: req.SizeBytes})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, toUploadWire(up))
}

func (h *Handler) presignParts(w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "assetID")
	if !ok {
		return
	}
	var req partsRequest
	if !httpserver.DecodeJSON(w, r, &req) {
		return
	}
	parts, err := h.svc.PresignParts(r.Context(), principal(r), ids[0], req.PartNumbers)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, partsWire{PartURLs: toParts(parts)})
}

func (h *Handler) complete(w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "assetID")
	if !ok {
		return
	}
	var req completeRequest
	if !httpserver.DecodeJSON(w, r, &req) {
		return
	}
	parts := make([]app.CompletedPart, len(req.Parts))
	for i, p := range req.Parts {
		parts[i] = app.CompletedPart{PartNumber: p.PartNumber, ETag: p.ETag}
	}
	v, err := h.svc.Complete(r.Context(), principal(r), ids[0], parts)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toAssetWire(v))
}

func (h *Handler) status(w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "assetID")
	if !ok {
		return
	}
	v, err := h.svc.Status(r.Context(), principal(r), ids[0])
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toAssetWire(v))
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "assetID")
	if !ok {
		return
	}
	if err := h.svc.Delete(r.Context(), principal(r), ids[0]); err != nil {
		h.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) playback(w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "courseID", "lectureID", "assetID")
	if !ok {
		return
	}
	pb, err := h.svc.Resolve(r.Context(), principal(r), ids[0], ids[1], ids[2])
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toPlaybackWire(pb))
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

var errorMappings = []errorMapping{
	{app.ErrNotFound, http.StatusNotFound, "not_found", "Not Found"},
	{app.ErrRemoteNotFound, http.StatusNotFound, "not_found", "Not Found"},
	{app.ErrForbidden, http.StatusForbidden, "forbidden", "Forbidden"},
	{app.ErrEnrollmentRequired, http.StatusForbidden, "enrollment_required", "Enrollment Required"},
	{app.ErrCourseNotEditable, http.StatusConflict, "course_not_editable", "Course Not Editable"},
	{app.ErrRemoteConflict, http.StatusConflict, "asset_state_conflict", "Asset State Conflict"},
	{app.ErrInvalidInput, http.StatusBadRequest, "invalid_input", "Invalid Input"},
	{app.ErrRemoteInvalid, http.StatusBadRequest, "invalid_input", "Invalid Input"},
	{app.ErrRemoteTooLarge, http.StatusRequestEntityTooLarge, problem.TypePayloadTooLarge, "Payload Too Large"},
	{app.ErrRemoteUnavailable, http.StatusBadGateway, "media_unavailable", "Media Unavailable"},
}

func (h *Handler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	var inUse *app.InUseError
	if errors.As(err, &inUse) {
		ids := make([]string, len(inUse.LectureIDs))
		for i, v := range inUse.LectureIDs {
			ids[i] = v.String()
		}
		problem.WriteWithExtensions(w, r, http.StatusConflict, "asset_in_use", "Asset In Use", "",
			map[string]any{"lecture_ids": ids})
		return
	}
	for _, m := range errorMappings {
		if errors.Is(err, m.err) {
			detail := ""
			if m.status == http.StatusBadRequest {
				detail = err.Error()
			}
			if m.status == http.StatusBadGateway {
				h.cfg.Logger.WarnContext(r.Context(), "media service unavailable", "error", err)
			}
			problem.Write(w, r, m.status, m.typ, m.title, detail)
			return
		}
	}
	h.cfg.Logger.ErrorContext(r.Context(), "media request failed", "error", err)
	problem.Write(w, r, http.StatusInternalServerError, problem.TypeInternal, "Internal Server Error", "")
}
