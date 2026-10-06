// Package httpapi exposes course authoring over HTTP.
package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/app"
	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/contentblocks"
	"github.com/santoshkc2200/ioe-backend/internal/platform/httpserver"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
	"github.com/santoshkc2200/ioe-backend/internal/platform/problem"
)

// CourseService is the course use-case surface the handlers call.
type CourseService interface {
	Create(ctx context.Context, p auth.Principal, in app.CreateCourseInput) (domain.Course, error)
	Get(ctx context.Context, p auth.Principal, courseID id.ID) (domain.Course, error)
	ListByOwner(ctx context.Context, p auth.Principal, ownerID id.ID) ([]domain.Course, error)
	ListPublished(ctx context.Context, q app.CatalogQuery) (app.CatalogPage, error)
	UpdateDetails(ctx context.Context, p auth.Principal, courseID id.ID, in app.DetailsInput) error
	SetPrice(ctx context.Context, p auth.Principal, courseID id.ID, amountMinor int64, currency string) (domain.Course, error)
	Publish(ctx context.Context, p auth.Principal, courseID id.ID) error
	Archive(ctx context.Context, p auth.Principal, courseID id.ID) error
	AddSection(ctx context.Context, p auth.Principal, courseID id.ID, title string) (domain.Course, error)
	RenameSection(ctx context.Context, p auth.Principal, courseID, sectionID id.ID, title string) error
	RemoveSection(ctx context.Context, p auth.Principal, courseID, sectionID id.ID) error
	AddLecture(ctx context.Context, p auth.Principal, courseID id.ID, in app.AddLectureInput) (domain.Course, error)
	RenameLecture(ctx context.Context, p auth.Principal, courseID, lectureID id.ID, title string) error
	RemoveLecture(ctx context.Context, p auth.Principal, courseID, lectureID id.ID) error
	ReorderLectures(ctx context.Context, p auth.Principal, courseID id.ID, lectureIDs []id.ID) error
	MoveLectureToSection(ctx context.Context, p auth.Principal, courseID, lectureID, sectionID id.ID) error
	SetLectureFreePreview(ctx context.Context, p auth.Principal, courseID, lectureID id.ID, freePreview bool) error
}

// ContentService is the lecture content use-case surface the handlers call.
type ContentService interface {
	Get(ctx context.Context, p auth.Principal, courseID, lectureID id.ID) (app.LectureContentView, error)
	Replace(ctx context.Context, p auth.Principal, courseID, lectureID id.ID, blocks []app.BlockInput, legacy app.LegacyContent) error
	Patch(ctx context.Context, p auth.Principal, courseID, lectureID id.ID, in app.PatchInput) (int64, error)
}

type Config struct {
	RequireAuth    httpserver.Middleware
	OptionalAuth   httpserver.Middleware
	IPs            httpserver.IPResolver
	ContentLimiter *httpserver.RateLimiter
	CatalogLimiter *httpserver.RateLimiter
	Logger         *slog.Logger
}

type Handler struct {
	courses  CourseService
	contents ContentService
	cfg      Config
}

func New(courses CourseService, contents ContentService, cfg Config) *Handler {
	return &Handler{courses: courses, contents: contents, cfg: cfg}
}

// Register mounts the course authoring routes. GET /v1/courses and GET /v1/courses/{courseID}
// are public and rate-limited per client IP; every other route requires authentication.
func (h *Handler) Register(r *httpserver.Router) {
	a := func(f http.HandlerFunc) http.Handler { return h.cfg.RequireAuth(f) }
	public := func(f http.HandlerFunc) http.Handler {
		return h.cfg.CatalogLimiter.Middleware(h.cfg.IPs)(h.cfg.OptionalAuth(f))
	}
	r.Handle("GET /v1/courses", public(h.listCatalog))
	r.Handle("POST /v1/courses", a(h.createCourse))
	r.Handle("GET /v1/users/{ownerID}/courses", a(h.listByOwner))
	r.Handle("GET /v1/courses/{courseID}", public(h.getCourse))
	r.Handle("PATCH /v1/courses/{courseID}", a(h.updateDetails))
	r.Handle("POST /v1/courses/{courseID}/price", a(h.setPrice))
	r.Handle("POST /v1/courses/{courseID}/publish", a(h.publish))
	r.Handle("POST /v1/courses/{courseID}/archive", a(h.archive))
	r.Handle("POST /v1/courses/{courseID}/sections", a(h.addSection))
	r.Handle("PATCH /v1/courses/{courseID}/sections/{sectionID}", a(h.renameSection))
	r.Handle("DELETE /v1/courses/{courseID}/sections/{sectionID}", a(h.removeSection))
	r.Handle("POST /v1/courses/{courseID}/lectures", a(h.addLecture))
	r.Handle("PATCH /v1/courses/{courseID}/lectures/{lectureID}", a(h.renameLecture))
	r.Handle("DELETE /v1/courses/{courseID}/lectures/{lectureID}", a(h.removeLecture))
	r.Handle("PUT /v1/courses/{courseID}/lectures-order", a(h.reorderLectures))
	r.Handle("POST /v1/courses/{courseID}/lectures/{lectureID}/section", a(h.moveLecture))
	r.Handle("POST /v1/courses/{courseID}/lectures/{lectureID}/free-preview", a(h.setFreePreview))
	r.Handle("GET /v1/courses/{courseID}/lectures/{lectureID}/content", a(h.getContent))
	r.Handle("PUT /v1/courses/{courseID}/lectures/{lectureID}/content", a(h.limitContent(h.replaceContent)))
	r.Handle("PATCH /v1/courses/{courseID}/lectures/{lectureID}/content", a(h.limitContent(h.patchContent)))
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

// principal returns the caller, or the zero Principal on a public route without a token.
// The zero Principal manages no course, so it sees published courses only.
func principal(r *http.Request) auth.Principal {
	p, _ := auth.PrincipalFrom(r.Context())
	return p
}

type errorMapping struct {
	err    error
	status int
	typ    string
	title  string
}

// errorMappings is checked in order; the first errors.Is match wins. Specific sentinels
// precede app.ErrInvalidInput because block building wraps both.
var errorMappings = []errorMapping{
	{app.ErrNotFound, http.StatusNotFound, "not_found", "Not Found"},
	{domain.ErrSectionNotFound, http.StatusNotFound, "not_found", "Not Found"},
	{domain.ErrLectureNotFound, http.StatusNotFound, "not_found", "Not Found"},
	{app.ErrForbidden, http.StatusForbidden, "forbidden", "Forbidden"},
	{app.ErrEnrollmentRequired, http.StatusForbidden, "enrollment_required", "Enrollment Required"},
	{app.ErrConcurrentModification, http.StatusConflict, "concurrent_modification", "Concurrent Modification"},
	{domain.ErrCourseNotEditable, http.StatusConflict, "course_not_editable", "Course Not Editable"},
	{domain.ErrInvalidStatusTransition, http.StatusConflict, "invalid_transition", "Invalid Transition"},
	{app.ErrRevisionRequired, http.StatusBadRequest, "lecture_content_revision_required", "Revision Required"},
	{app.ErrPatchTooLarge, http.StatusBadRequest, "patch_too_large", "Patch Too Large"},
	{app.ErrBlockSetMismatch, http.StatusBadRequest, "block_set_mismatch", "Block Set Mismatch"},
	{app.ErrOrderDeleteOverlap, http.StatusBadRequest, "order_delete_overlap", "Order Delete Overlap"},
	{app.ErrDuplicateClientBlockID, http.StatusBadRequest, "duplicate_client_block_id", "Duplicate Client Block ID"},
	{contentblocks.ErrDuplicateClientBlockID, http.StatusBadRequest, "duplicate_client_block_id", "Duplicate Client Block ID"},
	{contentblocks.ErrInvalidClientBlockID, http.StatusBadRequest, "invalid_client_block_id", "Invalid Client Block ID"},
	{contentblocks.ErrUnsafeContent, http.StatusBadRequest, "unsafe_content", "Unsafe Content"},
	{domain.ErrDuplicateSectionTitle, http.StatusBadRequest, "duplicate_title", "Duplicate Title"},
	{domain.ErrCourseHasNoLectures, http.StatusBadRequest, "empty_course", "Empty Course"},
	{domain.ErrUnsupportedCurrency, http.StatusBadRequest, "unsupported_currency", "Unsupported Currency"},
	{app.ErrInvalidMediaReference, http.StatusBadRequest, "invalid_media_reference", "Invalid Media Reference"},
	{app.ErrInvalidInput, http.StatusBadRequest, "invalid_input", "Invalid Input"},
	{domain.ErrInvalidPrice, http.StatusBadRequest, "invalid_input", "Invalid Input"},
	{domain.ErrInvalidLevel, http.StatusBadRequest, "invalid_input", "Invalid Input"},
	{domain.ErrInvalidThumbnailURL, http.StatusBadRequest, "invalid_input", "Invalid Input"},
	{domain.ErrInvalidLectureOrder, http.StatusBadRequest, "invalid_input", "Invalid Input"},
}

func (h *Handler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	var conflict *app.RevisionConflictError
	if errors.As(err, &conflict) {
		problem.WriteWithExtensions(w, r, http.StatusConflict, "revision_conflict", "Revision Conflict", "",
			lectureContentConflictExt(conflict.Current))
		return
	}
	for _, m := range errorMappings {
		if errors.Is(err, m.err) {
			detail := ""
			if m.status == http.StatusBadRequest {
				detail = err.Error()
			}
			problem.Write(w, r, m.status, m.typ, m.title, detail)
			return
		}
	}
	h.cfg.Logger.ErrorContext(r.Context(), "courseauthoring request failed", "error", err)
	problem.Write(w, r, http.StatusInternalServerError, problem.TypeInternal, "Internal Server Error", "")
}
