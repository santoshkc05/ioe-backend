package httpapi

import (
	"net/http"

	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/app"
	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/httpserver"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

type categoryWire struct {
	ID   id.ID  `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
}

type categoryListItemWire struct {
	categoryWire
	CourseCount int `json:"course_count"`
}

type categoryListWire struct {
	Categories []categoryListItemWire `json:"categories"`
}

type categoryNameRequest struct {
	Name string `json:"name"`
}

func toCategoryWire(c domain.Category) categoryWire {
	return categoryWire{ID: c.ID, Name: c.Name, Slug: c.Slug}
}

// toCategoryRefWires never returns nil, so the field encodes as [] rather than null.
func toCategoryRefWires(refs []domain.CategoryRef) []categoryWire {
	out := make([]categoryWire, 0, len(refs))
	for _, r := range refs {
		out = append(out, categoryWire{ID: r.ID, Name: r.Name, Slug: r.Slug})
	}
	return out
}

func toCategoryListWire(cs []app.CategoryWithCount) categoryListWire {
	w := categoryListWire{Categories: make([]categoryListItemWire, 0, len(cs))}
	for _, c := range cs {
		w.Categories = append(w.Categories, categoryListItemWire{categoryWire: toCategoryWire(c.Category), CourseCount: c.CourseCount})
	}
	return w
}

func (h *Handler) listCategories(w http.ResponseWriter, r *http.Request) {
	cs, err := h.categories.List(r.Context())
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toCategoryListWire(cs))
}

func (h *Handler) createCategory(w http.ResponseWriter, r *http.Request) {
	var req categoryNameRequest
	if !httpserver.DecodeJSON(w, r, &req) {
		return
	}
	c, err := h.categories.Create(r.Context(), principal(r), req.Name)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, toCategoryWire(c))
}

func (h *Handler) renameCategory(w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "categoryID")
	if !ok {
		return
	}
	var req categoryNameRequest
	if !httpserver.DecodeJSON(w, r, &req) {
		return
	}
	c, err := h.categories.Rename(r.Context(), principal(r), ids[0], req.Name)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toCategoryWire(c))
}

func (h *Handler) deleteCategory(w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "categoryID")
	if !ok {
		return
	}
	h.noContent(w, r, h.categories.Delete(r.Context(), principal(r), ids[0]))
}
