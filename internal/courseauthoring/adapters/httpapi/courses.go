package httpapi

import (
	"net/http"

	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/app"
	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/httpserver"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

func (h *Handler) createCourse(w http.ResponseWriter, r *http.Request) {
	var req createCourseRequest
	if !httpserver.DecodeJSON(w, r, &req) {
		return
	}
	in := app.CreateCourseInput{Title: req.Title, Description: req.Description}
	if req.OwnerID != nil {
		in.OwnerID = *req.OwnerID
	}
	c, err := h.courses.Create(r.Context(), principal(r), in)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, toCourseWire(c))
}

func (h *Handler) listByOwner(w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "ownerID")
	if !ok {
		return
	}
	cs, err := h.courses.ListByOwner(r.Context(), principal(r), ids[0])
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	page := coursePageWire{Courses: make([]courseWire, 0, len(cs)), Total: len(cs)}
	for _, c := range cs {
		page.Courses = append(page.Courses, toCourseWire(c))
	}
	httpserver.WriteJSON(w, http.StatusOK, page)
}

func (h *Handler) getCourse(w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "courseID")
	if !ok {
		return
	}
	c, err := h.courses.Get(r.Context(), principal(r), ids[0])
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toCourseWire(c))
}

func (h *Handler) updateDetails(w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "courseID")
	if !ok {
		return
	}
	var req updateDetailsRequest
	if !httpserver.DecodeJSON(w, r, &req) {
		return
	}
	err := h.courses.UpdateDetails(r.Context(), principal(r), ids[0],
		app.DetailsInput{Title: req.Title, Description: req.Description, Level: req.Level, ThumbnailURL: req.ThumbnailURL})
	h.noContent(w, r, err)
}

func (h *Handler) setPrice(w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "courseID")
	if !ok {
		return
	}
	var req setPriceRequest
	if !httpserver.DecodeJSON(w, r, &req) {
		return
	}
	c, err := h.courses.SetPrice(r.Context(), principal(r), ids[0], req.AmountMinor, req.Currency)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toCourseWire(c))
}

func (h *Handler) publish(w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "courseID")
	if !ok {
		return
	}
	h.noContent(w, r, h.courses.Publish(r.Context(), principal(r), ids[0]))
}

func (h *Handler) archive(w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "courseID")
	if !ok {
		return
	}
	h.noContent(w, r, h.courses.Archive(r.Context(), principal(r), ids[0]))
}

func (h *Handler) addSection(w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "courseID")
	if !ok {
		return
	}
	var req titleRequest
	if !httpserver.DecodeJSON(w, r, &req) {
		return
	}
	c, err := h.courses.AddSection(r.Context(), principal(r), ids[0], req.Title)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, toCourseWire(c))
}

func (h *Handler) renameSection(w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "courseID", "sectionID")
	if !ok {
		return
	}
	var req titleRequest
	if !httpserver.DecodeJSON(w, r, &req) {
		return
	}
	h.noContent(w, r, h.courses.RenameSection(r.Context(), principal(r), ids[0], ids[1], req.Title))
}

func (h *Handler) removeSection(w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "courseID", "sectionID")
	if !ok {
		return
	}
	h.noContent(w, r, h.courses.RemoveSection(r.Context(), principal(r), ids[0], ids[1]))
}

func (h *Handler) addLecture(w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "courseID")
	if !ok {
		return
	}
	var req addLectureRequest
	if !httpserver.DecodeJSON(w, r, &req) {
		return
	}
	c, err := h.courses.AddLecture(r.Context(), principal(r), ids[0], app.AddLectureInput{
		Title:  req.Title,
		Legacy: app.LegacyContent{TextBody: req.TextBody, VideoURL: req.VideoURL, VideoDurationMs: req.VideoDurationMs},
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, toCourseWire(c))
}

func (h *Handler) renameLecture(w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "courseID", "lectureID")
	if !ok {
		return
	}
	var req titleRequest
	if !httpserver.DecodeJSON(w, r, &req) {
		return
	}
	h.noContent(w, r, h.courses.RenameLecture(r.Context(), principal(r), ids[0], ids[1], req.Title))
}

func (h *Handler) removeLecture(w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "courseID", "lectureID")
	if !ok {
		return
	}
	h.noContent(w, r, h.courses.RemoveLecture(r.Context(), principal(r), ids[0], ids[1]))
}

func (h *Handler) reorderLectures(w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "courseID")
	if !ok {
		return
	}
	var req reorderLecturesRequest
	if !httpserver.DecodeJSON(w, r, &req) {
		return
	}
	h.noContent(w, r, h.courses.ReorderLectures(r.Context(), principal(r), ids[0], req.LectureIDs))
}

func (h *Handler) moveLecture(w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "courseID", "lectureID")
	if !ok {
		return
	}
	var req moveLectureRequest
	if !httpserver.DecodeJSON(w, r, &req) {
		return
	}
	var sectionID id.ID
	if req.SectionID != "" {
		v, err := id.Parse(req.SectionID)
		if err != nil {
			h.writeError(w, r, domain.ErrSectionNotFound)
			return
		}
		sectionID = v
	}
	h.noContent(w, r, h.courses.MoveLectureToSection(r.Context(), principal(r), ids[0], ids[1], sectionID))
}

func (h *Handler) setFreePreview(w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "courseID", "lectureID")
	if !ok {
		return
	}
	var req freePreviewRequest
	if !httpserver.DecodeJSON(w, r, &req) {
		return
	}
	h.noContent(w, r, h.courses.SetLectureFreePreview(r.Context(), principal(r), ids[0], ids[1], req.FreePreview))
}

// noContent answers 204 on success and maps err otherwise.
func (h *Handler) noContent(w http.ResponseWriter, r *http.Request, err error) {
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
