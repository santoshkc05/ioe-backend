package mediasvc_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/santoshkc2200/ioe-backend/internal/media/adapters/mediasvc"
	"github.com/santoshkc2200/ioe-backend/internal/media/app"
	"github.com/santoshkc2200/ioe-backend/internal/media/domain"
)

const apiKey = "test-media-api-key-0123456789abcdef"

var ctx = context.Background()

type captured struct {
	method, path, auth, namespace, idem, contentType string
	body                                             map[string]any
}

func server(t *testing.T, status int, response string, got *captured) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*got = captured{method: r.Method, path: r.URL.Path, auth: r.Header.Get("Authorization"),
			namespace: r.Header.Get("X-Namespace-ID"), idem: r.Header.Get("Idempotency-Key"),
			contentType: r.Header.Get("Content-Type")}
		b, _ := io.ReadAll(r.Body)
		if len(b) > 0 {
			_ = json.Unmarshal(b, &got.body)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, response)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestCreateImage(t *testing.T) {
	var got captured
	srv := server(t, http.StatusCreated, `{"asset_id":"900","namespace_id":"ioe","upload_id":"u1",
		"upload_url":"https://objects.test/put","expires_at":"2026-10-06T10:00:00Z"}`, &got)
	c := mediasvc.New(srv.URL, "https://media.test", apiKey)
	up, err := c.Create(ctx, app.RemoteCreate{Kind: domain.KindImage, ContentType: "image/png", Filename: "a.png",
		SizeBytes: 10, OwnerID: 7, CourseID: 10, IdempotencyKey: "ioe:upload:1"})
	if err != nil {
		t.Fatal(err)
	}
	if up.AssetID != 900 || up.UploadURL != "https://objects.test/put" || up.UploadID != "u1" || up.ExpiresAt.IsZero() {
		t.Fatalf("upload = %+v", up)
	}
	if got.method != http.MethodPost || got.path != "/v1/assets" || got.auth != "Bearer "+apiKey ||
		got.namespace != "ioe" || got.idem != "ioe:upload:1" || got.contentType != "application/json" {
		t.Fatalf("request = %+v", got)
	}
	b := got.body
	if b["kind"] != "image" || b["visibility"] != "private" || b["owner_id"] != "7" || b["external_ref"] != "course:10" ||
		b["content_type"] != "image/png" || b["filename"] != "a.png" || b["size_bytes"] != float64(10) {
		t.Fatalf("body = %v", b)
	}
	variants, _ := b["image_variants"].([]any)
	if len(variants) != 1 {
		t.Fatalf("variants = %v", b["image_variants"])
	}
	v := variants[0].(map[string]any)
	if v["name"] != "display" || v["width"] != float64(1600) || v["format"] != "webp" || v["quality"] != float64(82) {
		t.Fatalf("variant = %v", v)
	}
}

func TestCreateVideoMultipart(t *testing.T) {
	var got captured
	srv := server(t, http.StatusCreated, `{"asset_id":"901","namespace_id":"ioe","upload_id":"u2","part_size":5242880,
		"part_urls":[{"part_number":1,"url":"https://objects.test/1"}],"expires_at":"2026-10-06T10:00:00Z"}`, &got)
	up, err := mediasvc.New(srv.URL, "https://media.test", apiKey).Create(ctx, app.RemoteCreate{
		Kind: domain.KindVideo, ContentType: "video/mp4", Filename: "a.mp4", SizeBytes: 9, OwnerID: 7, CourseID: 10, IdempotencyKey: "k"})
	if err != nil || up.PartSize != 5242880 || len(up.PartURLs) != 1 || up.PartURLs[0].PartNumber != 1 {
		t.Fatalf("upload = %+v, %v", up, err)
	}
	if _, ok := got.body["image_variants"]; ok {
		t.Fatalf("video request carries image variants: %v", got.body)
	}
}

func TestPartsCompleteGetDelete(t *testing.T) {
	var got captured
	srv := server(t, http.StatusOK, `{"part_urls":[{"part_number":2,"url":"https://objects.test/2"}]}`, &got)
	c := mediasvc.New(srv.URL+"/", "https://media.test", apiKey) // trailing slash is trimmed
	parts, err := c.PresignParts(ctx, 900, []int{2})
	if err != nil || len(parts) != 1 || parts[0].URL != "https://objects.test/2" {
		t.Fatalf("parts = %+v, %v", parts, err)
	}
	if got.path != "/v1/assets/900/parts" || got.body["part_numbers"].([]any)[0] != float64(2) {
		t.Fatalf("request = %+v", got)
	}

	asset := `{"id":"900","namespace_id":"ioe","kind":"video","status":"processing","progress_percent":40,
		"duration_ms":60000,"width":1280,"height":720,"version":2,"updated_at":"2026-10-06T10:00:00Z"}`
	srv = server(t, http.StatusOK, asset, &got)
	c = mediasvc.New(srv.URL, "https://media.test", apiKey)
	a, err := c.Complete(ctx, 900, []app.CompletedPart{{PartNumber: 1, ETag: `"e1"`}})
	if err != nil || a.ID != 900 || a.Status != "processing" || a.ProgressPercent != 40 || a.Width != 1280 {
		t.Fatalf("complete = %+v, %v", a, err)
	}
	p := got.body["parts"].([]any)[0].(map[string]any)
	if got.path != "/v1/assets/900/complete" || p["part_number"] != float64(1) || p["etag"] != `"e1"` {
		t.Fatalf("request = %+v", got)
	}
	if a, err = c.Get(ctx, 900); err != nil || got.method != http.MethodGet || got.path != "/v1/assets/900" || a.DurationMs != 60000 {
		t.Fatalf("get = %+v, %v, %+v", a, err, got)
	}

	srv = server(t, http.StatusNoContent, "", &got)
	if err := mediasvc.New(srv.URL, "https://media.test", apiKey).Delete(ctx, 900); err != nil ||
		got.method != http.MethodDelete || got.path != "/v1/assets/900" {
		t.Fatalf("delete err = %v, %+v", err, got)
	}
}

func TestDeliveryAbsolutizesRelativeURLs(t *testing.T) {
	var got captured
	srv := server(t, http.StatusOK, `{"visibility":"private","url":"/v1/delivery/900/master.m3u8?token=t",
		"expires_at":"2026-10-06T10:15:00Z","renditions":[
		{"name":"poster","content_type":"image/jpeg","url":"https://objects.test/poster.jpg","width":1280,"height":720},
		{"name":"display","content_type":"image/webp","url":"/v1/delivery/900/display","width":1600,"height":900}]}`, &got)
	d, err := mediasvc.New(srv.URL, "https://media.test/", apiKey).Delivery(ctx, 900)
	if err != nil {
		t.Fatal(err)
	}
	if got.method != http.MethodPost || got.path != "/v1/assets/900/delivery" {
		t.Fatalf("request = %+v", got)
	}
	if d.URL != "https://media.test/v1/delivery/900/master.m3u8?token=t" || d.ExpiresAt.IsZero() {
		t.Fatalf("delivery = %+v", d)
	}
	if d.Renditions[0].URL != "https://objects.test/poster.jpg" || d.Renditions[1].URL != "https://media.test/v1/delivery/900/display" ||
		d.Renditions[1].Width != 1600 {
		t.Fatalf("renditions = %+v", d.Renditions)
	}
}

func TestErrorMapping(t *testing.T) {
	cases := []struct {
		status int
		want   error
	}{
		{http.StatusBadRequest, app.ErrRemoteInvalid},
		{http.StatusNotFound, app.ErrRemoteNotFound},
		{http.StatusConflict, app.ErrRemoteConflict},
		{http.StatusRequestEntityTooLarge, app.ErrRemoteTooLarge},
		{http.StatusTooManyRequests, app.ErrRemoteUnavailable},
		{http.StatusInternalServerError, app.ErrRemoteUnavailable},
		{http.StatusUnauthorized, app.ErrRemoteUnavailable},
	}
	for _, tc := range cases {
		var got captured
		srv := server(t, tc.status, `{"code":"x","detail":"filename is required","status":400,"request_id":"r1"}`, &got)
		_, err := mediasvc.New(srv.URL, "https://media.test", apiKey).Get(ctx, 900)
		if !errors.Is(err, tc.want) {
			t.Fatalf("%d: err = %v, want %v", tc.status, err, tc.want)
		}
		if tc.status == http.StatusBadRequest && !strings.Contains(err.Error(), "filename is required") {
			t.Fatalf("400 detail not carried: %v", err)
		}
	}
}

func TestClosedConnectionIsUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		conn, _, _ := w.(http.Hijacker).Hijack()
		_ = conn.Close()
	}))
	defer srv.Close()
	if _, err := mediasvc.New(srv.URL, "https://media.test", apiKey).Get(ctx, 900); !errors.Is(err, app.ErrRemoteUnavailable) {
		t.Fatalf("err = %v", err)
	}
}

func TestMalformedSuccessIsUnavailable(t *testing.T) {
	var got captured
	srv := server(t, http.StatusCreated, `{"asset_id":"not-a-snowflake","expires_at":"2026-10-06T10:00:00Z"}`, &got)
	_, err := mediasvc.New(srv.URL, "https://media.test", apiKey).Create(ctx, app.RemoteCreate{Kind: domain.KindVideo})
	if !errors.Is(err, app.ErrRemoteUnavailable) {
		t.Fatalf("err = %v", err)
	}
}

func TestErrorsNeverContainAPIKey(t *testing.T) {
	for _, status := range []int{400, 401, 404, 409, 413, 500} {
		var got captured
		srv := server(t, status, `{"code":"x","detail":"Bearer `+apiKey+`","request_id":"r"}`, &got)
		_, err := mediasvc.New(srv.URL, "https://media.test", apiKey).Get(ctx, 900)
		if err == nil || strings.Contains(err.Error(), apiKey) {
			t.Fatalf("%d: err = %v", status, err)
		}
	}
}
