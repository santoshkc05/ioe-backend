package httpapi_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/blog/adapters/httpapi"
	"github.com/santoshkc2200/ioe-backend/internal/blog/app"
	"github.com/santoshkc2200/ioe-backend/internal/blog/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/contentblocks"
	"github.com/santoshkc2200/ioe-backend/internal/platform/httpserver"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
	"github.com/santoshkc2200/ioe-backend/internal/platform/problem"
)

func fakeAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uid, err := id.Parse(r.Header.Get("X-Test-User"))
		if err != nil {
			problem.Write(w, r, http.StatusUnauthorized, "invalid_token", "Invalid Token", "")
			return
		}
		next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(),
			auth.Principal{UserID: uid, Role: auth.Role(r.Header.Get("X-Test-Role"))})))
	})
}

// stubPosts returns canned results so the tests pin the HTTP contract, not the use cases.
type stubPosts struct{ err error }

var stubPost = func() domain.Post {
	d, _ := domain.NewDetails("Hello", "s", "", []string{"go"})
	p := domain.NewPost(7, 10, d, domain.SlugFromTitle("Hello", 7), time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC))
	p.Version = 1
	return p
}()

func (s stubPosts) Create(context.Context, auth.Principal, app.DetailsInput) (domain.Post, error) {
	return stubPost, s.err
}
func (s stubPosts) Get(context.Context, auth.Principal, id.ID) (domain.Post, error) {
	return stubPost, s.err
}
func (s stubPosts) ListByAuthor(context.Context, auth.Principal, id.ID) ([]domain.Post, error) {
	return []domain.Post{stubPost}, s.err
}
func (s stubPosts) UpdateDetails(context.Context, auth.Principal, id.ID, int64, app.DetailsInput) (domain.Post, error) {
	return stubPost, s.err
}
func (s stubPosts) SetSlug(context.Context, auth.Principal, id.ID, string) (domain.Post, error) {
	return stubPost, s.err
}
func (s stubPosts) Publish(context.Context, auth.Principal, id.ID) (domain.Post, error) {
	return stubPost, s.err
}
func (s stubPosts) Unpublish(context.Context, auth.Principal, id.ID) (domain.Post, error) {
	return stubPost, s.err
}
func (s stubPosts) Archive(context.Context, auth.Principal, id.ID) (domain.Post, error) {
	return stubPost, s.err
}
func (s stubPosts) ListVersions(context.Context, auth.Principal, id.ID) ([]app.VersionSummary, error) {
	return nil, s.err
}
func (s stubPosts) GetVersion(context.Context, auth.Principal, id.ID, int) (app.VersionView, error) {
	return app.VersionView{Number: 1, Details: stubPost.Details}, s.err
}
func (s stubPosts) DiscardDraft(context.Context, auth.Principal, id.ID) (domain.Post, error) {
	return stubPost, s.err
}

type stubContents struct{ err error }

func (s stubContents) Get(context.Context, auth.Principal, id.ID) (app.ContentView, error) {
	return app.ContentView{PostID: 7, ContentRevision: 3}, s.err
}
func (s stubContents) Replace(context.Context, auth.Principal, id.ID, []app.BlockInput) (int64, error) {
	return 4, s.err
}
func (s stubContents) Patch(context.Context, auth.Principal, id.ID, app.PatchInput) (int64, error) {
	return 4, s.err
}

type stubPublic struct {
	err       error
	lastAfter *app.Cursor
	lastLimit int
}

func (s *stubPublic) List(_ context.Context, _ string, after *app.Cursor, limit int) (app.CardPage, error) {
	s.lastAfter, s.lastLimit = after, limit
	next := &app.Cursor{FirstPublishedAt: time.Date(2026, 10, 7, 0, 0, 0, 123456000, time.UTC), PostID: 5}
	return app.CardPage{Next: next}, s.err
}
func (s *stubPublic) GetBySlug(context.Context, string) (app.PublicPost, error) {
	return app.PublicPost{Card: app.Card{LivePost: app.LivePost{PostID: 7, Slug: "new", Details: stubPost.Details}},
		RequestedSlug: "old", Blocks: []contentblocks.Block{}}, s.err
}
func (s *stubPublic) Tags(context.Context) ([]app.TagCount, error) {
	return []app.TagCount{{Tag: "go", Count: 2}}, s.err
}
func (s *stubPublic) Index(_ context.Context, after *app.Cursor, limit int) (app.IndexPage, error) {
	s.lastAfter, s.lastLimit = after, limit
	return app.IndexPage{}, s.err
}

func newServer(t *testing.T, posts stubPosts, contents stubContents, public *stubPublic, perMinute int) http.Handler {
	t.Helper()
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	r, h := httpserver.NewRouter(httpserver.Options{Logger: logger, AllowedOrigins: []string{"https://app.test"}, ServiceName: "test"})
	httpapi.New(posts, contents, public, httpapi.Config{RequireAuth: fakeAuth, IPs: httpserver.NewIPResolver(nil),
		ContentLimiter: httpserver.NewRateLimiter(perMinute), PublicLimiter: httpserver.NewRateLimiter(perMinute), Logger: logger}).Register(r)
	return h
}

func do(h http.Handler, method, path, body string, user bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if user {
		req.Header.Set("X-Test-User", "10")
		req.Header.Set("X-Test-Role", "instructor")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestAuthoringRoutesRequireAuthAndNoStore(t *testing.T) {
	h := newServer(t, stubPosts{}, stubContents{}, &stubPublic{}, 100)
	if rec := do(h, http.MethodPost, "/v1/blog/posts", `{"title":"Hello"}`, false); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous create %d", rec.Code)
	}
	rec := do(h, http.MethodPost, "/v1/blog/posts", `{"title":"Hello","tags":["go"]}`, true)
	if rec.Code != http.StatusCreated || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("create %d %q", rec.Code, rec.Header().Get("Cache-Control"))
	}
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body["slug"] != "hello" || body["status"] != "draft" || body["id"] != "7" || body["live_version"] != nil {
		t.Fatalf("body %v", body)
	}
}

func TestErrorMapping(t *testing.T) {
	cases := map[error]struct {
		status int
		typ    string
	}{
		app.ErrNotFound:                   {404, "not_found"},
		app.ErrForbidden:                  {403, "forbidden"},
		app.ErrSlugTaken:                  {409, "slug_taken"},
		app.ErrConcurrentModification:     {409, "concurrent_modification"},
		domain.ErrInvalidStatusTransition: {409, "invalid_transition"},
		domain.ErrPostArchived:            {409, "post_archived"},
		domain.ErrEmptyPost:               {400, "empty_post"},
		domain.ErrInvalidSlug:             {400, "invalid_input"},
		app.ErrInvalidInput:               {400, "invalid_input"},
	}
	for err, want := range cases {
		h := newServer(t, stubPosts{err: err}, stubContents{}, &stubPublic{}, 100)
		rec := do(h, http.MethodPost, "/v1/blog/posts/7/publish", "", true)
		var p map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &p)
		if rec.Code != want.status || p["type"] != want.typ {
			t.Errorf("%v: %d %v", err, rec.Code, p["type"])
		}
	}
}

func TestRevisionConflictCarriesCurrentContent(t *testing.T) {
	conflict := &app.RevisionConflictError{Current: app.ContentView{PostID: 7, ContentRevision: 9}}
	h := newServer(t, stubPosts{}, stubContents{err: conflict}, &stubPublic{}, 100)
	rec := do(h, http.MethodPatch, "/v1/blog/posts/7/content", `{"base_revision":1,"order":[]}`, true)
	var p map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &p)
	if rec.Code != http.StatusConflict || p["type"] != "revision_conflict" || p["content_revision"] != float64(9) {
		t.Fatalf("%d %v", rec.Code, p)
	}
}

func TestPublicRoutesAreAnonymousCachedAndLimited(t *testing.T) {
	pub := &stubPublic{}
	h := newServer(t, stubPosts{}, stubContents{}, pub, 2)
	rec := do(h, http.MethodGet, "/v1/blog/public/posts/old", "", false)
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if rec.Code != http.StatusOK || body["slug"] != "new" || body["requested_slug"] != "old" ||
		rec.Header().Get("Cache-Control") != "public, max-age=60" {
		t.Fatalf("get %d %v %q", rec.Code, body, rec.Header().Get("Cache-Control"))
	}
	rec = do(h, http.MethodGet, "/v1/blog/public/posts", "", false)
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	next, _ := body["next_cursor"].(string)
	got, err := httpapi.DecodeCursor(next)
	if err != nil || got.PostID != 5 || got.FirstPublishedAt.Nanosecond() != 123456000 {
		t.Fatalf("cursor %q %+v %v", next, got, err)
	}
	if rec := do(h, http.MethodGet, "/v1/blog/public/tags", "", false); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("third request %d", rec.Code)
	}
}

func TestPublicQueryValidation(t *testing.T) {
	pub := &stubPublic{}
	h := newServer(t, stubPosts{}, stubContents{}, pub, 100)
	for _, path := range []string{"/v1/blog/public/posts?limit=abc", "/v1/blog/public/posts?cursor=***", "/v1/blog/public/index?cursor=bm9wZQ"} {
		if rec := do(h, http.MethodGet, path, "", false); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", path, rec.Code)
		}
	}
	cursor := httpapi.EncodeCursor(app.Cursor{FirstPublishedAt: time.UnixMicro(1_760_000_000_123_456).UTC(), PostID: 99})
	if rec := do(h, http.MethodGet, "/v1/blog/public/index?limit=7&cursor="+cursor, "", false); rec.Code != http.StatusOK ||
		pub.lastLimit != 7 || pub.lastAfter == nil || pub.lastAfter.PostID != 99 {
		t.Fatalf("index %d %+v %d", rec.Code, pub.lastAfter, pub.lastLimit)
	}
}

func TestMalformedPathIDIsNotFound(t *testing.T) {
	h := newServer(t, stubPosts{}, stubContents{}, &stubPublic{}, 100)
	if rec := do(h, http.MethodGet, "/v1/blog/posts/abc", "", true); rec.Code != http.StatusNotFound {
		t.Fatalf("got %d", rec.Code)
	}
	if rec := do(h, http.MethodGet, "/v1/blog/posts/7/versions/0", "", true); rec.Code != http.StatusNotFound {
		t.Fatalf("version 0 got %d", rec.Code)
	}
}
