package httpapi

import (
	"net/http"

	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/httpserver"
)

func (h *Handler) submit(w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "courseID")
	if !ok {
		return
	}
	h.noContent(w, r, h.courses.Submit(r.Context(), principal(r), ids[0]))
}

// approve accepts an optional {"note": ...} body; an empty body approves without a note.
func (h *Handler) approve(w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "courseID")
	if !ok {
		return
	}
	var req reviewNoteRequest
	if r.ContentLength != 0 && !httpserver.DecodeJSON(w, r, &req) {
		return
	}
	h.noContent(w, r, h.courses.Approve(r.Context(), principal(r), ids[0], req.Note))
}

func (h *Handler) requestChanges(w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "courseID")
	if !ok {
		return
	}
	var req reviewNoteRequest
	if !httpserver.DecodeJSON(w, r, &req) {
		return
	}
	h.noContent(w, r, h.courses.RequestChanges(r.Context(), principal(r), ids[0], req.Note))
}

func (h *Handler) unpublish(w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "courseID")
	if !ok {
		return
	}
	var req reviewNoteRequest
	if !httpserver.DecodeJSON(w, r, &req) {
		return
	}
	h.noContent(w, r, h.courses.Unpublish(r.Context(), principal(r), ids[0], req.Note))
}

func (h *Handler) listReviews(w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "courseID")
	if !ok {
		return
	}
	reviews, err := h.courses.ListReviews(r.Context(), principal(r), ids[0])
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	out := make([]reviewWire, 0, len(reviews))
	for _, rev := range reviews {
		out = append(out, reviewWire{ID: rev.ID, ReviewerID: rev.ActorID, Decision: string(rev.Decision), Note: rev.Note, CreatedAt: rev.CreatedAt})
	}
	httpserver.WriteJSON(w, http.StatusOK, out)
}

func (h *Handler) listInReview(w http.ResponseWriter, r *http.Request) {
	cs, err := h.courses.ListInReview(r.Context(), principal(r))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeCoursePage(w, cs)
}

// publishingSettings reports the caller's publishing policy: root admins publish directly
// ("independent"); everyone else submits for review first ("review_required").
func (h *Handler) publishingSettings(w http.ResponseWriter, r *http.Request) {
	policy := "review_required"
	if principal(r).Role == auth.RoleRootAdmin {
		policy = "independent"
	}
	httpserver.WriteJSON(w, http.StatusOK, publishingSettingsWire{PublishingPolicy: policy})
}
