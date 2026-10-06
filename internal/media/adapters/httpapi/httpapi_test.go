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

	"github.com/santoshkc2200/ioe-backend/internal/media/adapters/httpapi"
	"github.com/santoshkc2200/ioe-backend/internal/media/app"
	"github.com/santoshkc2200/ioe-backend/internal/media/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/httpserver"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

var t0 = time.Date(2026, 10, 6, 9, 0, 0, 0, time.UTC)

type stub struct {
	err      error
	upload   app.Upload
	view     app.AssetView
	playback app.Playback

	courseID, lectureID, assetID id.ID
	input                        app.UploadInput
	partNumbers                  []int
	parts                        []app.CompletedPart
	principal                    auth.Principal
}

func (s *stub) CreateUpload(_ context.Context, p auth.Principal, courseID id.ID, in app.UploadInput) (app.Upload, error) {
	s.principal, s.courseID, s.input = p, courseID, in
	return s.upload, s.err
}

func (s *stub) PresignParts(_ context.Context, _ auth.Principal, assetID id.ID, n []int) ([]app.UploadPart, error) {
	s.assetID, s.partNumbers = assetID, n
	return []app.UploadPart{{PartNumber: 2, URL: "https://objects.test/2"}}, s.err
}

func (s *stub) Complete(_ context.Context, _ auth.Principal, assetID id.ID, parts []app.CompletedPart) (app.AssetView, error) {
	s.assetID, s.parts = assetID, parts
	return s.view, s.err
}

func (s *stub) Status(_ context.Context, _ auth.Principal, assetID id.ID) (app.AssetView, error) {
	s.assetID = assetID
	return s.view, s.err
}

func (s *stub) Delete(_ context.Context, _ auth.Principal, assetID id.ID) error {
	s.assetID = assetID
	return s.err
}

func (s *stub) Resolve(_ context.Context, _ auth.Principal, courseID, lectureID, assetID id.ID) (app.Playback, error) {
	s.courseID, s.lectureID, s.assetID = courseID, lectureID, assetID
	return s.playback, s.err
}

func fakeAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{UserID: 100, Role: auth.RoleInstructor})))
	})
}

func newServer(s *stub, perMinute int) http.Handler {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	r, h := httpserver.NewRouter(httpserver.Options{Logger: logger, AllowedOrigins: []string{"https://app.test"}, ServiceName: "test"})
	httpapi.New(s, httpapi.Config{RequireAuth: fakeAuth, CreateLimiter: httpserver.NewRateLimiter(perMinute), Logger: logger}).Register(r)
	return h
}

func call(h http.Handler, method, path, body string) (*httptest.ResponseRecorder, map[string]any) {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w, out
}

var readyView = app.AssetView{
	Asset:  domain.Asset{ID: 900, CourseID: 10, Kind: domain.KindVideo},
	Remote: app.RemoteAsset{ID: 900, Status: "ready", ProgressPercent: 100, DurationMs: 60000, Width: 1280, Height: 720, UpdatedAt: t0},
}

func TestCreateUpload(t *testing.T) {
	s := &stub{upload: app.Upload{AssetID: 900, UploadID: "u", PartSize: 5, PartURLs: []app.UploadPart{{PartNumber: 1, URL: "https://objects.test/1"}}, ExpiresAt: t0}}
	w, body := call(newServer(s, 30), http.MethodPost, "/v1/courses/10/media/uploads",
		`{"kind":"video","content_type":"video/mp4","filename":"a.mp4","size_bytes":9}`)
	if w.Code != http.StatusCreated || body["asset_id"] != "900" || body["upload_id"] != "u" || body["part_size"] != float64(5) {
		t.Fatalf("%d %v", w.Code, body)
	}
	if _, ok := body["upload_url"]; ok {
		t.Fatalf("empty upload_url serialized: %v", body)
	}
	if s.courseID != 10 || s.input != (app.UploadInput{Kind: "video", ContentType: "video/mp4", Filename: "a.mp4", SizeBytes: 9}) || s.principal.UserID != 100 {
		t.Fatalf("stub = %+v", s)
	}
}

func TestCreateUploadRateLimited(t *testing.T) {
	h := newServer(&stub{}, 1)
	body := `{"kind":"video","content_type":"video/mp4","filename":"a.mp4","size_bytes":9}`
	if w, _ := call(h, http.MethodPost, "/v1/courses/10/media/uploads", body); w.Code != http.StatusCreated {
		t.Fatalf("first = %d", w.Code)
	}
	if w, b := call(h, http.MethodPost, "/v1/courses/10/media/uploads", body); w.Code != http.StatusTooManyRequests || b["type"] != "rate_limited" {
		t.Fatalf("second = %d %v", w.Code, b)
	}
}

func TestPartsCompleteStatusDelete(t *testing.T) {
	s := &stub{view: readyView}
	h := newServer(s, 30)
	w, body := call(h, http.MethodPost, "/v1/media/uploads/900/parts", `{"part_numbers":[2]}`)
	if w.Code != http.StatusOK || len(body["part_urls"].([]any)) != 1 || s.assetID != 900 || s.partNumbers[0] != 2 {
		t.Fatalf("parts %d %v", w.Code, body)
	}
	w, body = call(h, http.MethodPost, "/v1/media/uploads/900/complete", `{"parts":[{"part_number":1,"etag":"e"}]}`)
	if w.Code != http.StatusOK || body["id"] != "900" || body["course_id"] != "10" || body["status"] != "ready" ||
		body["kind"] != "video" || body["progress_percent"] != float64(100) || s.parts[0] != (app.CompletedPart{PartNumber: 1, ETag: "e"}) {
		t.Fatalf("complete %d %v", w.Code, body)
	}
	if w, body = call(h, http.MethodPost, "/v1/media/uploads/900/complete", `{}`); w.Code != http.StatusOK || len(s.parts) != 0 {
		t.Fatalf("complete without parts %d %v", w.Code, body)
	}
	if w, body = call(h, http.MethodGet, "/v1/media/assets/900", ""); w.Code != http.StatusOK || body["duration_ms"] != float64(60000) {
		t.Fatalf("status %d %v", w.Code, body)
	}
	if w, _ = call(h, http.MethodDelete, "/v1/media/assets/900", ""); w.Code != http.StatusNoContent {
		t.Fatalf("delete %d", w.Code)
	}
}

func TestPlayback(t *testing.T) {
	s := &stub{playback: app.Playback{ID: 900, Kind: domain.KindVideo, Status: "ready", URL: "https://media.test/m3u8",
		PosterURL: "https://objects.test/p.jpg", DurationMs: 60000, Width: 1280, Height: 720, ExpiresAt: t0}}
	h := newServer(s, 30)
	w, body := call(h, http.MethodGet, "/v1/courses/10/lectures/20/media/900", "")
	if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-store" || body["playback_url"] != "https://media.test/m3u8" ||
		body["poster_url"] != "https://objects.test/p.jpg" || body["expires_at"] == nil || body["url"] != nil {
		t.Fatalf("video %d %v", w.Code, body)
	}
	if s.courseID != 10 || s.lectureID != 20 || s.assetID != 900 {
		t.Fatalf("ids = %+v", s)
	}
	s.playback = app.Playback{ID: 901, Kind: domain.KindImage, Status: "ready", URL: "https://objects.test/d.webp", Width: 1600, Height: 900, ExpiresAt: t0}
	if w, body = call(h, http.MethodGet, "/v1/courses/10/lectures/20/media/901", ""); w.Code != http.StatusOK ||
		body["url"] != "https://objects.test/d.webp" || body["playback_url"] != nil || body["width"] != float64(1600) {
		t.Fatalf("image %d %v", w.Code, body)
	}
	s.playback = app.Playback{ID: 900, Kind: domain.KindVideo, Status: "processing"}
	if w, body = call(h, http.MethodGet, "/v1/courses/10/lectures/20/media/900", ""); w.Code != http.StatusOK ||
		len(body) != 3 || body["status"] != "processing" {
		t.Fatalf("processing %d %v", w.Code, body)
	}
}

func TestErrorMapping(t *testing.T) {
	cases := []struct {
		err    error
		status int
		typ    string
	}{
		{app.ErrNotFound, 404, "not_found"},
		{app.ErrRemoteNotFound, 404, "not_found"},
		{app.ErrForbidden, 403, "forbidden"},
		{app.ErrEnrollmentRequired, 403, "enrollment_required"},
		{app.ErrCourseNotEditable, 409, "course_not_editable"},
		{app.ErrRemoteConflict, 409, "asset_state_conflict"},
		{app.ErrInvalidInput, 400, "invalid_input"},
		{app.ErrRemoteInvalid, 400, "invalid_input"},
		{app.ErrRemoteTooLarge, 413, "payload_too_large"},
		{app.ErrRemoteUnavailable, 502, "media_unavailable"},
		{io.ErrUnexpectedEOF, 500, "internal"},
	}
	for _, c := range cases {
		w, body := call(newServer(&stub{err: c.err}, 30), http.MethodGet, "/v1/courses/10/lectures/20/media/900", "")
		if w.Code != c.status || body["type"] != c.typ {
			t.Fatalf("%v: %d %v", c.err, w.Code, body)
		}
	}
}

func TestMalformedIDsAreNotFound(t *testing.T) {
	h := newServer(&stub{}, 30)
	for _, path := range []string{"/v1/media/assets/abc", "/v1/courses/x/lectures/20/media/900"} {
		if w, _ := call(h, http.MethodGet, path, ""); w.Code != http.StatusNotFound {
			t.Fatalf("%s: %d", path, w.Code)
		}
	}
}
