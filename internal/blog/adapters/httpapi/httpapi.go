// Package httpapi exposes the blog over HTTP.
package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/santoshkc2200/ioe-backend/internal/blog/app"
	"github.com/santoshkc2200/ioe-backend/internal/blog/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/contentblocks"
	"github.com/santoshkc2200/ioe-backend/internal/platform/httpserver"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
	"github.com/santoshkc2200/ioe-backend/internal/platform/problem"
)

type PostService interface {
	Create(ctx context.Context, p auth.Principal, in app.DetailsInput) (domain.Post, error)
	Get(ctx context.Context, p auth.Principal, postID id.ID) (domain.Post, error)
	ListByAuthor(ctx context.Context, p auth.Principal, authorID id.ID) ([]domain.Post, error)
	UpdateDetails(ctx context.Context, p auth.Principal, postID id.ID, version int64, in app.DetailsInput) (domain.Post, error)
	SetSlug(ctx context.Context, p auth.Principal, postID id.ID, slug string) (domain.Post, error)
	Publish(ctx context.Context, p auth.Principal, postID id.ID) (domain.Post, error)
	Unpublish(ctx context.Context, p auth.Principal, postID id.ID) (domain.Post, error)
	Archive(ctx context.Context, p auth.Principal, postID id.ID) (domain.Post, error)
	ListVersions(ctx context.Context, p auth.Principal, postID id.ID) ([]app.VersionSummary, error)
	GetVersion(ctx context.Context, p auth.Principal, postID id.ID, number int) (app.VersionView, error)
	DiscardDraft(ctx context.Context, p auth.Principal, postID id.ID) (domain.Post, error)
}

type ContentService interface {
	Get(ctx context.Context, p auth.Principal, postID id.ID) (app.ContentView, error)
	Replace(ctx context.Context, p auth.Principal, postID id.ID, blocks []app.BlockInput) (int64, error)
	Patch(ctx context.Context, p auth.Principal, postID id.ID, in app.PatchInput) (int64, error)
}

type PublicService interface {
	List(ctx context.Context, tag string, after *app.Cursor, limit int) (app.CardPage, error)
	GetBySlug(ctx context.Context, slug string) (app.PublicPost, error)
	Tags(ctx context.Context) ([]app.TagCount, error)
	Index(ctx context.Context, after *app.Cursor, limit int) (app.IndexPage, error)
}

type Config struct {
	RequireAuth    httpserver.Middleware
	IPs            httpserver.IPResolver
	ContentLimiter *httpserver.RateLimiter
	PublicLimiter  *httpserver.RateLimiter
	Logger         *slog.Logger
}

type Handler struct {
	posts    PostService
	contents ContentService
	public   PublicService
	cfg      Config
}

func New(posts PostService, contents ContentService, public PublicService, cfg Config) *Handler {
	return &Handler{posts: posts, contents: contents, public: public, cfg: cfg}
}

// Register mounts the blog routes. /v1/blog/public/* is anonymous, cacheable and
// rate-limited per client IP; every other route requires authentication.
func (h *Handler) Register(r *httpserver.Router) {
	a := func(f http.HandlerFunc) http.Handler { return h.cfg.RequireAuth(httpserver.NoStore(f)) }
	pub := func(f http.HandlerFunc) http.Handler {
		return h.cfg.PublicLimiter.Middleware(h.cfg.IPs)(publicCache(f))
	}
	r.Handle("POST /v1/blog/posts", a(h.create))
	r.Handle("GET /v1/blog/posts/{postID}", a(h.get))
	r.Handle("PATCH /v1/blog/posts/{postID}", a(h.updateDetails))
	r.Handle("PUT /v1/blog/posts/{postID}/slug", a(h.setSlug))
	r.Handle("POST /v1/blog/posts/{postID}/publish", a(h.lifecycle(h.posts.Publish)))
	r.Handle("POST /v1/blog/posts/{postID}/unpublish", a(h.lifecycle(h.posts.Unpublish)))
	r.Handle("POST /v1/blog/posts/{postID}/archive", a(h.lifecycle(h.posts.Archive)))
	r.Handle("POST /v1/blog/posts/{postID}/discard-draft", a(h.lifecycle(h.posts.DiscardDraft)))
	r.Handle("GET /v1/blog/posts/{postID}/versions", a(h.listVersions))
	r.Handle("GET /v1/blog/posts/{postID}/versions/{versionNumber}", a(h.getVersion))
	r.Handle("GET /v1/blog/posts/{postID}/content", a(h.getContent))
	r.Handle("PUT /v1/blog/posts/{postID}/content", a(h.limitContent(h.replaceContent)))
	r.Handle("PATCH /v1/blog/posts/{postID}/content", a(h.limitContent(h.patchContent)))
	r.Handle("GET /v1/users/{authorID}/blog/posts", a(h.listByAuthor))
	r.Handle("GET /v1/blog/public/posts", pub(h.publicList))
	r.Handle("GET /v1/blog/public/posts/{slug}", pub(h.publicGet))
	r.Handle("GET /v1/blog/public/tags", pub(h.publicTags))
	r.Handle("GET /v1/blog/public/index", pub(h.publicIndex))
}

func publicCache(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=60")
		next.ServeHTTP(w, r)
	})
}

func principal(r *http.Request) auth.Principal {
	p, _ := auth.PrincipalFrom(r.Context())
	return p
}

// pathID parses a path value. A malformed ID names no resource: 404.
func pathID(w http.ResponseWriter, r *http.Request, name string) (id.ID, bool) {
	v, err := id.Parse(r.PathValue(name))
	if err != nil {
		problem.Write(w, r, http.StatusNotFound, problem.TypeNotFound, "Not Found", "")
		return 0, false
	}
	return v, true
}

func pathNumber(w http.ResponseWriter, r *http.Request, name string) (int, bool) {
	n, err := strconv.Atoi(r.PathValue(name))
	if err != nil || n < 1 {
		problem.Write(w, r, http.StatusNotFound, problem.TypeNotFound, "Not Found", "")
		return 0, false
	}
	return n, true
}

// limitContent rate-limits content writes per (user, post).
func (h *Handler) limitContent(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !h.cfg.ContentLimiter.Allow(principal(r).UserID.String() + ":" + r.PathValue("postID")) {
			w.Header().Set("Retry-After", "60")
			problem.Write(w, r, http.StatusTooManyRequests, problem.TypeRateLimited, "Too Many Requests", "")
			return
		}
		next(w, r)
	}
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
	{app.ErrForbidden, http.StatusForbidden, "forbidden", "Forbidden"},
	{app.ErrSlugTaken, http.StatusConflict, "slug_taken", "Slug Taken"},
	{app.ErrConcurrentModification, http.StatusConflict, "concurrent_modification", "Concurrent Modification"},
	{domain.ErrInvalidStatusTransition, http.StatusConflict, "invalid_transition", "Invalid Transition"},
	{domain.ErrPostArchived, http.StatusConflict, "post_archived", "Post Archived"},
	{domain.ErrEmptyPost, http.StatusBadRequest, "empty_post", "Empty Post"},
	{domain.ErrBlockKindNotAllowed, http.StatusBadRequest, "block_kind_not_allowed", "Block Kind Not Allowed"},
	{domain.ErrInvalidImage, http.StatusBadRequest, "invalid_image", "Invalid Image"},
	{app.ErrRevisionRequired, http.StatusBadRequest, "post_content_revision_required", "Revision Required"},
	{app.ErrPatchTooLarge, http.StatusBadRequest, "patch_too_large", "Patch Too Large"},
	{contentblocks.ErrBlockSetMismatch, http.StatusBadRequest, "block_set_mismatch", "Block Set Mismatch"},
	{contentblocks.ErrOrderDeleteOverlap, http.StatusBadRequest, "order_delete_overlap", "Order Delete Overlap"},
	{contentblocks.ErrDuplicateClientBlockID, http.StatusBadRequest, "duplicate_client_block_id", "Duplicate Client Block ID"},
	{contentblocks.ErrInvalidClientBlockID, http.StatusBadRequest, "invalid_client_block_id", "Invalid Client Block ID"},
	{contentblocks.ErrUnsafeContent, http.StatusBadRequest, "unsafe_content", "Unsafe Content"},
	{app.ErrInvalidInput, http.StatusBadRequest, "invalid_input", "Invalid Input"},
	{domain.ErrInvalidSlug, http.StatusBadRequest, "invalid_input", "Invalid Input"},
	{domain.ErrInvalidSummary, http.StatusBadRequest, "invalid_input", "Invalid Input"},
	{domain.ErrInvalidCoverURL, http.StatusBadRequest, "invalid_input", "Invalid Input"},
	{domain.ErrInvalidTag, http.StatusBadRequest, "invalid_input", "Invalid Input"},
	{domain.ErrTooManyTags, http.StatusBadRequest, "invalid_input", "Invalid Input"},
}

func (h *Handler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	var conflict *app.RevisionConflictError
	if errors.As(err, &conflict) {
		problem.WriteWithExtensions(w, r, http.StatusConflict, "revision_conflict", "Revision Conflict", "",
			contentConflictExt(conflict.Current))
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
	h.cfg.Logger.ErrorContext(r.Context(), "blog request failed", "error", err)
	problem.Write(w, r, http.StatusInternalServerError, problem.TypeInternal, "Internal Server Error", "")
}

func (h *Handler) writePost(w http.ResponseWriter, r *http.Request, status int, p domain.Post, err error) {
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, status, toPostWire(p))
}
