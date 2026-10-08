package httpapi

import (
	"net/http"

	"github.com/santoshkc2200/ioe-backend/internal/blog/app"
	"github.com/santoshkc2200/ioe-backend/internal/platform/httpserver"
)

func (h *Handler) getContent(w http.ResponseWriter, r *http.Request) {
	postID, ok := pathID(w, r, "postID")
	if !ok {
		return
	}
	v, err := h.contents.Get(r.Context(), principal(r), postID)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toContentWire(v))
}

func (h *Handler) replaceContent(w http.ResponseWriter, r *http.Request) {
	postID, ok := pathID(w, r, "postID")
	if !ok {
		return
	}
	var req replaceContentRequest
	if !httpserver.DecodeJSON(w, r, &req) {
		return
	}
	rev, err := h.contents.Replace(r.Context(), principal(r), postID, toBlockInputs(req.Blocks))
	h.writeRevision(w, r, rev, err)
}

func (h *Handler) patchContent(w http.ResponseWriter, r *http.Request) {
	postID, ok := pathID(w, r, "postID")
	if !ok {
		return
	}
	var req patchContentRequest
	if !httpserver.DecodeJSON(w, r, &req) {
		return
	}
	rev, err := h.contents.Patch(r.Context(), principal(r), postID, app.PatchInput{
		BaseRevision: req.BaseRevision, Order: req.Order, Upserts: toBlockInputs(req.Upserts), Deletes: req.Deletes,
	})
	h.writeRevision(w, r, rev, err)
}

func (h *Handler) writeRevision(w http.ResponseWriter, r *http.Request, rev int64, err error) {
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]int64{"content_revision": rev})
}
