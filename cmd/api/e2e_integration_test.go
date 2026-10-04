//go:build integration

package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/identity/adapters/google/googletest"
	"github.com/santoshkc2200/ioe-backend/internal/platform/config"
	"github.com/santoshkc2200/ioe-backend/internal/platform/postgres/pgtest"
)

const (
	appOrigin = "https://app.test"
	notifyKey = "c3Nzc3Nzc3Nzc3Nzc3Nzc3Nzc3Nzc3Nzc3Nzc3Nzc3M"
)

func signingKeyPEM(t *testing.T) string {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	p8, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: p8}))
}

type client struct {
	t    *testing.T
	base string
}

func (c client) do(method, path, body string, headers map[string]string) (*http.Response, map[string]any) {
	c.t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, c.base+path, strings.NewReader(body))
	if err != nil {
		c.t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp, out
}

func refreshCookie(t *testing.T, resp *http.Response) string {
	t.Helper()
	for _, ck := range resp.Cookies() {
		if ck.Name == "ioe_refresh" {
			return ck.Value
		}
	}
	t.Fatal("no refresh cookie")
	return ""
}

func TestEndToEnd(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pool := pgtest.New(t)
	google := googletest.NewIssuer(t)

	type sentEmail struct{ key, recipient, auth string }
	sent := make(chan sentEmail, 4)
	notify := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Recipient string `json:"recipient"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		sent <- sentEmail{key: r.Header.Get("Idempotency-Key"), recipient: body.Recipient, auth: r.Header.Get("Authorization")}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer notify.Close()

	cfg := config.Config{
		GoogleClientIDs:               []string{"web-client"},
		GoogleJWKSURL:                 google.JWKSURL(),
		JWTIssuer:                     "https://api.test",
		JWTAudience:                   "ioe",
		JWTSigningKeyPEM:              signingKeyPEM(t),
		JWTSigningKeyID:               "k1",
		AllowedOrigins:                []string{appOrigin},
		CookieSecure:                  false,
		AuthRateLimitPerMinute:        1000,
		LogLevel:                      "info",
		NotificationServiceBaseURL:    notify.URL,
		NotificationServiceSendAPIKey: notifyKey,
		BootstrapRootAdminEmails:      []string{"admin@example.com"},
	}
	a, err := buildApp(ctx, cfg, slog.New(slog.NewJSONHandler(io.Discard, nil)), pool)
	if err != nil {
		t.Fatal(err)
	}
	defer a.forwarder.Close()
	go func() { _ = a.forwarder.Run(ctx) }()
	srv := httptest.NewServer(a.handler)
	defer srv.Close()
	c := client{t: t, base: srv.URL}
	idToken := google.Sign(t, googletest.Claims("sub-e2e", "e2e@example.com", "web-client", time.Now()))
	origin := map[string]string{"Origin": appOrigin}
	withCookie := func(v string) map[string]string {
		return map[string]string{"Origin": appOrigin, "Cookie": "ioe_refresh=" + v}
	}

	resp, _ := c.do(http.MethodGet, "/readyz", "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("readyz %d", resp.StatusCode)
	}

	resp, body := c.do(http.MethodPost, "/v1/auth/google", `{"id_token":"`+idToken+`"}`, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("sign in %d %v", resp.StatusCode, body)
	}
	access, _ := body["access_token"].(string)
	cookie1 := refreshCookie(t, resp)

	resp, body = c.do(http.MethodGet, "/v1/me", "", map[string]string{"Authorization": "Bearer " + access})
	if resp.StatusCode != http.StatusOK || body["email"] != "e2e@example.com" || body["role"] != "student" {
		t.Fatalf("me %d %v", resp.StatusCode, body)
	}

	resp, _ = c.do(http.MethodPost, "/v1/auth/refresh", "", withCookie(cookie1))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("refresh %d", resp.StatusCode)
	}
	cookie2 := refreshCookie(t, resp)

	resp, body = c.do(http.MethodPost, "/v1/auth/refresh", "", withCookie(cookie1))
	if resp.StatusCode != http.StatusUnauthorized || body["type"] != "refresh_reuse_detected" {
		t.Fatalf("reuse %d %v", resp.StatusCode, body)
	}
	resp, body = c.do(http.MethodPost, "/v1/auth/refresh", "", withCookie(cookie2))
	if resp.StatusCode != http.StatusUnauthorized || body["type"] != "invalid_token" {
		t.Fatalf("family not revoked: %d %v", resp.StatusCode, body)
	}

	resp, _ = c.do(http.MethodPost, "/v1/auth/google", `{"id_token":"`+idToken+`"}`, nil)
	cookie3 := refreshCookie(t, resp)
	resp, _ = c.do(http.MethodPost, "/v1/auth/logout", "", withCookie(cookie3))
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("logout %d", resp.StatusCode)
	}
	resp, _ = c.do(http.MethodPost, "/v1/auth/refresh", "", withCookie(cookie3))
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("refresh after logout %d", resp.StatusCode)
	}
	resp, _ = c.do(http.MethodPost, "/v1/auth/refresh", "", origin)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("refresh without cookie %d", resp.StatusCode)
	}

	var events int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM platform.outbox_messages").Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 1 {
		t.Fatalf("outbox messages = %d, want 1 (one registration)", events)
	}

	adminToken := google.Sign(t, googletest.Claims("sub-admin", "admin@example.com", "web-client", time.Now()))
	resp, body = c.do(http.MethodPost, "/v1/auth/google", `{"id_token":"`+adminToken+`"}`, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("admin sign in %d %v", resp.StatusCode, body)
	}
	adminAuth := map[string]string{"Authorization": "Bearer " + body["access_token"].(string)}
	studentAuth := map[string]string{"Authorization": "Bearer " + access}

	resp, body = c.do(http.MethodPost, "/v1/courses", `{"title":"Go","description":"d"}`, adminAuth)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create course %d %v", resp.StatusCode, body)
	}
	courseID := body["id"].(string)
	resp, body = c.do(http.MethodPost, "/v1/courses/"+courseID+"/lectures", `{"title":"Free","text_body":"<p>free</p>"}`, adminAuth)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("add lecture %d %v", resp.StatusCode, body)
	}
	resp, body = c.do(http.MethodPost, "/v1/courses/"+courseID+"/lectures", `{"title":"Paid","text_body":"<p>paid</p>"}`, adminAuth)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("add second lecture %d %v", resp.StatusCode, body)
	}
	lectures := body["lectures"].([]any)
	freeID := lectures[0].(map[string]any)["id"].(string)
	paidID := lectures[1].(map[string]any)["id"].(string)
	resp, _ = c.do(http.MethodPost, "/v1/courses/"+courseID+"/lectures/"+freeID+"/free-preview", `{"free_preview":true}`, adminAuth)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("free preview %d", resp.StatusCode)
	}

	resp, body = c.do(http.MethodGet, "/v1/courses/"+courseID+"/lectures/"+paidID+"/content", "", adminAuth)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("read content %d %v", resp.StatusCode, body)
	}
	rev := body["content_revision"].(float64)
	first := body["blocks"].([]any)[0].(map[string]any)["client_block_id"].(string)
	patch := fmt.Sprintf(`{"base_revision":%d,"order":["%s","n1"],"upserts":[{"client_block_id":"n1","type":"text","body":"<p>more</p>"}],"deletes":[]}`, int64(rev), first)
	resp, body = c.do(http.MethodPatch, "/v1/courses/"+courseID+"/lectures/"+paidID+"/content", patch, adminAuth)
	if resp.StatusCode != http.StatusOK || body["content_revision"].(float64) != rev+1 {
		t.Fatalf("patch %d %v", resp.StatusCode, body)
	}

	resp, _ = c.do(http.MethodGet, "/v1/courses/"+courseID, "", studentAuth)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("student sees draft: %d", resp.StatusCode)
	}
	resp, _ = c.do(http.MethodPost, "/v1/courses/"+courseID+"/publish", "", adminAuth)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("publish %d", resp.StatusCode)
	}
	resp, _ = c.do(http.MethodGet, "/v1/courses/"+courseID, "", studentAuth)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("student outline %d", resp.StatusCode)
	}
	resp, _ = c.do(http.MethodGet, "/v1/courses/"+courseID+"/lectures/"+freeID+"/content", "", studentAuth)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("free preview read %d", resp.StatusCode)
	}
	resp, body = c.do(http.MethodGet, "/v1/courses/"+courseID+"/lectures/"+paidID+"/content", "", studentAuth)
	if resp.StatusCode != http.StatusForbidden || body["type"] != "enrollment_required" {
		t.Fatalf("paid read %d %v", resp.StatusCode, body)
	}

	var published int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM platform.outbox_messages WHERE payload->>'destination_topic' = 'courseauthoring.course.published'").Scan(&published); err != nil {
		t.Fatal(err)
	}
	if published != 1 {
		t.Fatalf("published events = %d, want 1", published)
	}

	select {
	case got := <-sent:
		if got.recipient != "e2e@example.com" || got.auth != "Bearer "+notifyKey ||
			!strings.HasPrefix(got.key, "ioe:identity.user_registered:") || !strings.HasSuffix(got.key, ":welcome-v1") {
			t.Fatalf("welcome email %+v", got)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("welcome email not enqueued")
	}
	select {
	case got := <-sent:
		if got.recipient != "admin@example.com" {
			t.Fatalf("second welcome email %+v", got)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("admin welcome email not enqueued")
	}
	select {
	case got := <-sent:
		t.Fatalf("unexpected third email %+v", got)
	case <-time.After(2 * time.Second):
	}
}
