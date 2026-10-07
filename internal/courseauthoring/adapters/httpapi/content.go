package httpapi

import (
	"net/http"

	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/app"
	"github.com/santoshkc2200/ioe-backend/internal/platform/httpserver"
	"github.com/santoshkc2200/ioe-backend/internal/platform/problem"
)

// limitContent rate-limits content writes per (user, lecture).
func (h *Handler) limitContent(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := principal(r).UserID.String() + ":" + r.PathValue("lectureID")
		if !h.cfg.ContentLimiter.Allow(key) {
			w.Header().Set("Retry-After", "60")
			problem.Write(w, r, http.StatusTooManyRequests, problem.TypeRateLimited, "Too Many Requests", "")
			return
		}
		next(w, r)
	}
}

func (h *Handler) getContent(w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "courseID", "lectureID")
	if !ok {
		return
	}
	live, ok := wantsLive(w, r)
	if !ok {
		return
	}
	get := h.contents.Get
	if live {
		get = h.contents.GetLive
	}
	v, err := get(r.Context(), principal(r), ids[0], ids[1])
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toLectureContentWire(v))
}

func (h *Handler) replaceContent(w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "courseID", "lectureID")
	if !ok {
		return
	}
	var req replaceContentRequest
	if !httpserver.DecodeJSON(w, r, &req) {
		return
	}
	err := h.contents.Replace(r.Context(), principal(r), ids[0], ids[1], toContentBlockInputs(req.Blocks),
		app.LegacyContent{TextBody: req.TextBody, VideoURL: req.VideoURL, VideoDurationMs: req.VideoDurationMs})
	h.noContent(w, r, err)
}

func (h *Handler) patchContent(w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "courseID", "lectureID")
	if !ok {
		return
	}
	var req patchContentRequest
	if !httpserver.DecodeJSON(w, r, &req) {
		return
	}
	rev, err := h.contents.Patch(r.Context(), principal(r), ids[0], ids[1], app.PatchInput{
		BaseRevision: req.BaseRevision, Order: req.Order, Upserts: toContentBlockInputs(req.Upserts), Deletes: req.Deletes,
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]int64{"content_revision": rev})
}
