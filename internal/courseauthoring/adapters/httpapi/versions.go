package httpapi

import (
	"net/http"
	"strconv"

	"github.com/santoshkc2200/ioe-backend/internal/platform/httpserver"
	"github.com/santoshkc2200/ioe-backend/internal/platform/problem"
)

func (h *Handler) listVersions(w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "courseID")
	if !ok {
		return
	}
	vs, err := h.courses.ListVersions(r.Context(), principal(r), ids[0])
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	out := make([]versionWire, 0, len(vs))
	for _, v := range vs {
		out = append(out, versionWire{VersionNumber: v.Number, PublishedBy: v.PublishedBy, PublishedAt: v.PublishedAt.UnixMilli()})
	}
	httpserver.WriteJSON(w, http.StatusOK, out)
}

func (h *Handler) getVersion(w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "courseID")
	if !ok {
		return
	}
	// A malformed number names no version.
	number, err := strconv.Atoi(r.PathValue("versionNumber"))
	if err != nil || number < 1 {
		problem.Write(w, r, http.StatusNotFound, problem.TypeNotFound, "Not Found", "")
		return
	}
	c, err := h.courses.GetVersion(r.Context(), principal(r), ids[0], number)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toCourseWire(c))
}

func (h *Handler) discardDraft(w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "courseID")
	if !ok {
		return
	}
	c, err := h.courses.DiscardDraft(r.Context(), principal(r), ids[0])
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toCourseWire(c))
}
