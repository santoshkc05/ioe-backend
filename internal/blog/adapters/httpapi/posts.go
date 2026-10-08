package httpapi

import (
	"context"
	"net/http"

	"github.com/santoshkc2200/ioe-backend/internal/blog/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/httpserver"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	var req detailsRequest
	if !httpserver.DecodeJSON(w, r, &req) {
		return
	}
	p, err := h.posts.Create(r.Context(), principal(r), req.input())
	h.writePost(w, r, http.StatusCreated, p, err)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	postID, ok := pathID(w, r, "postID")
	if !ok {
		return
	}
	p, err := h.posts.Get(r.Context(), principal(r), postID)
	h.writePost(w, r, http.StatusOK, p, err)
}

func (h *Handler) updateDetails(w http.ResponseWriter, r *http.Request) {
	postID, ok := pathID(w, r, "postID")
	if !ok {
		return
	}
	var req updateDetailsRequest
	if !httpserver.DecodeJSON(w, r, &req) {
		return
	}
	p, err := h.posts.UpdateDetails(r.Context(), principal(r), postID, req.Version, req.input())
	h.writePost(w, r, http.StatusOK, p, err)
}

func (h *Handler) setSlug(w http.ResponseWriter, r *http.Request) {
	postID, ok := pathID(w, r, "postID")
	if !ok {
		return
	}
	var req setSlugRequest
	if !httpserver.DecodeJSON(w, r, &req) {
		return
	}
	p, err := h.posts.SetSlug(r.Context(), principal(r), postID, req.Slug)
	h.writePost(w, r, http.StatusOK, p, err)
}

// lifecycle adapts a body-less post transition.
func (h *Handler) lifecycle(op func(context.Context, auth.Principal, id.ID) (domain.Post, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		postID, ok := pathID(w, r, "postID")
		if !ok {
			return
		}
		p, err := op(r.Context(), principal(r), postID)
		h.writePost(w, r, http.StatusOK, p, err)
	}
}

func (h *Handler) listByAuthor(w http.ResponseWriter, r *http.Request) {
	authorID, ok := pathID(w, r, "authorID")
	if !ok {
		return
	}
	ps, err := h.posts.ListByAuthor(r.Context(), principal(r), authorID)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	out := postPageWire{Posts: make([]postWire, 0, len(ps))}
	for _, p := range ps {
		out.Posts = append(out.Posts, toPostWire(p))
	}
	httpserver.WriteJSON(w, http.StatusOK, out)
}

func (h *Handler) listVersions(w http.ResponseWriter, r *http.Request) {
	postID, ok := pathID(w, r, "postID")
	if !ok {
		return
	}
	vs, err := h.posts.ListVersions(r.Context(), principal(r), postID)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	out := make([]versionSummaryWire, 0, len(vs))
	for _, v := range vs {
		out = append(out, versionSummaryWire{Number: v.Number, PublishedBy: v.PublishedBy, PublishedAt: v.PublishedAt})
	}
	httpserver.WriteJSON(w, http.StatusOK, out)
}

func (h *Handler) getVersion(w http.ResponseWriter, r *http.Request) {
	postID, ok := pathID(w, r, "postID")
	if !ok {
		return
	}
	number, ok := pathNumber(w, r, "versionNumber")
	if !ok {
		return
	}
	v, err := h.posts.GetVersion(r.Context(), principal(r), postID, number)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toVersionWire(v))
}
