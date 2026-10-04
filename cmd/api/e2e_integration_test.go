//go:build integration

package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
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

const appOrigin = "https://app.test"

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

	cfg := config.Config{
		GoogleClientIDs:        []string{"web-client"},
		GoogleJWKSURL:          google.JWKSURL(),
		JWTIssuer:              "https://api.test",
		JWTAudience:            "ioe",
		JWTSigningKeyPEM:       signingKeyPEM(t),
		JWTSigningKeyID:        "k1",
		AllowedOrigins:         []string{appOrigin},
		CookieSecure:           false,
		AuthRateLimitPerMinute: 1000,
		LogLevel:               "info",
	}
	a, err := buildApp(ctx, cfg, slog.New(slog.NewJSONHandler(io.Discard, nil)), pool)
	if err != nil {
		t.Fatal(err)
	}
	defer a.forwarder.Close()
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
}
