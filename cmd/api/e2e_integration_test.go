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
	"strconv"
	"strings"
	"sync"
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

// baseConfig is a working configuration with notifications disabled.
func baseConfig(t *testing.T, google *googletest.Issuer) config.Config {
	t.Helper()
	return config.Config{
		GoogleClientIDs:          []string{"web-client"},
		GoogleJWKSURL:            google.JWKSURL(),
		JWTIssuer:                "https://api.test",
		JWTAudience:              "ioe",
		JWTSigningKeyPEM:         signingKeyPEM(t),
		JWTSigningKeyID:          "k1",
		AllowedOrigins:           []string{appOrigin},
		CookieSecure:             false,
		AuthRateLimitPerMinute:   1000,
		LogLevel:                 "info",
		BootstrapRootAdminEmails: []string{"admin@example.com"},
	}
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

	cfg := baseConfig(t, google)
	cfg.NotificationServiceBaseURL = notify.URL
	cfg.NotificationServiceSendAPIKey = notifyKey
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

func TestRoleManagementEndToEnd(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pool := pgtest.New(t)
	google := googletest.NewIssuer(t)
	a, err := buildApp(ctx, baseConfig(t, google), slog.New(slog.NewJSONHandler(io.Discard, nil)), pool)
	if err != nil {
		t.Fatal(err)
	}
	defer a.forwarder.Close()
	srv := httptest.NewServer(a.handler)
	defer srv.Close()
	c := client{t: t, base: srv.URL}
	bearer := func(tok string) map[string]string { return map[string]string{"Authorization": "Bearer " + tok} }
	signIn := func(sub, email string) (string, string, *http.Response) {
		t.Helper()
		tok := google.Sign(t, googletest.Claims(sub, email, "web-client", time.Now()))
		resp, body := c.do(http.MethodPost, "/v1/auth/google", `{"id_token":"`+tok+`"}`, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("sign in %s: %d %v", email, resp.StatusCode, body)
		}
		u := body["user"].(map[string]any)
		return body["access_token"].(string), u["id"].(string), resp
	}

	studentAccess, studentID, resp := signIn("sub-student", "student@example.com")
	studentCookie := refreshCookie(t, resp)
	adminAccess, adminID, _ := signIn("sub-admin", "admin@example.com")

	resp, _ = c.do(http.MethodPost, "/v1/courses", `{"title":"Go","description":"d"}`, bearer(studentAccess))
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("student create course before promotion: %d", resp.StatusCode)
	}
	resp, body := c.do(http.MethodGet, "/v1/admin/users?email=stu", "", bearer(studentAccess))
	if resp.StatusCode != http.StatusForbidden || body["type"] != "forbidden" {
		t.Fatalf("student search: %d %v", resp.StatusCode, body)
	}

	resp, body = c.do(http.MethodGet, "/v1/admin/users?email=STUDENT%40", "", bearer(adminAccess))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("admin search: %d %v", resp.StatusCode, body)
	}
	users := body["users"].([]any)
	if len(users) != 1 || users[0].(map[string]any)["id"] != studentID || users[0].(map[string]any)["role"] != "student" {
		t.Fatalf("search result %v", users)
	}

	resp, body = c.do(http.MethodPut, "/v1/admin/users/"+studentID+"/role", `{"role":"instructor"}`, bearer(adminAccess))
	if resp.StatusCode != http.StatusOK || body["role"] != "instructor" {
		t.Fatalf("promote: %d %v", resp.StatusCode, body)
	}
	resp, body = c.do(http.MethodPut, "/v1/admin/users/"+adminID+"/role", `{"role":"student"}`, bearer(adminAccess))
	if resp.StatusCode != http.StatusConflict || body["type"] != "role_not_assignable" {
		t.Fatalf("self demotion: %d %v", resp.StatusCode, body)
	}

	resp, body = c.do(http.MethodPost, "/v1/auth/refresh", "", map[string]string{"Origin": appOrigin, "Cookie": "ioe_refresh=" + studentCookie})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("refresh: %d %v", resp.StatusCode, body)
	}
	instructorAccess := body["access_token"].(string)
	resp, body = c.do(http.MethodGet, "/v1/me", "", bearer(instructorAccess))
	if resp.StatusCode != http.StatusOK || body["role"] != "instructor" {
		t.Fatalf("me after refresh: %d %v", resp.StatusCode, body)
	}
	resp, body = c.do(http.MethodPost, "/v1/courses", `{"title":"Go","description":"d"}`, bearer(instructorAccess))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("instructor create course: %d %v", resp.StatusCode, body)
	}

	var changes int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM platform.outbox_messages WHERE payload->>'destination_topic' = 'identity.user_role_changed'").Scan(&changes); err != nil {
		t.Fatal(err)
	}
	if changes != 1 {
		t.Fatalf("role change events = %d, want 1", changes)
	}
}

func TestEnrollmentEndToEnd(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pool := pgtest.New(t)
	google := googletest.NewIssuer(t)
	a, err := buildApp(ctx, baseConfig(t, google), slog.New(slog.NewJSONHandler(io.Discard, nil)), pool)
	if err != nil {
		t.Fatal(err)
	}
	defer a.forwarder.Close()
	srv := httptest.NewServer(a.handler)
	defer srv.Close()
	c := client{t: t, base: srv.URL}
	bearer := func(tok string) map[string]string { return map[string]string{"Authorization": "Bearer " + tok} }
	signIn := func(sub, email string) (string, string) {
		t.Helper()
		tok := google.Sign(t, googletest.Claims(sub, email, "web-client", time.Now()))
		resp, body := c.do(http.MethodPost, "/v1/auth/google", `{"id_token":"`+tok+`"}`, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("sign in %s: %d %v", email, resp.StatusCode, body)
		}
		return body["access_token"].(string), body["user"].(map[string]any)["id"].(string)
	}
	adminTok, _ := signIn("sub-admin", "admin@example.com")
	studentTok, studentID := signIn("sub-student", "student@example.com")
	admin, student := bearer(adminTok), bearer(studentTok)

	// publish creates a published course with one non-preview lecture and returns its IDs.
	publish := func(priced bool) (string, string) {
		t.Helper()
		resp, body := c.do(http.MethodPost, "/v1/courses", `{"title":"Go","description":"d"}`, admin)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("create: %d %v", resp.StatusCode, body)
		}
		courseID := body["id"].(string)
		if priced {
			resp, body = c.do(http.MethodPost, "/v1/courses/"+courseID+"/price", `{"amount_minor":150000,"currency":"NPR"}`, admin)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("price: %d %v", resp.StatusCode, body)
			}
		}
		resp, body = c.do(http.MethodPost, "/v1/courses/"+courseID+"/lectures", `{"title":"L1","text_body":"<p>x</p>"}`, admin)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("lecture: %d %v", resp.StatusCode, body)
		}
		lectures := body["lectures"].([]any)
		lectureID := lectures[len(lectures)-1].(map[string]any)["id"].(string)
		if resp, _ = c.do(http.MethodPost, "/v1/courses/"+courseID+"/publish", "", admin); resp.StatusCode != http.StatusNoContent {
			t.Fatalf("publish: %d", resp.StatusCode)
		}
		return courseID, lectureID
	}
	read := func(courseID, lectureID string) (int, any) {
		resp, body := c.do(http.MethodGet, "/v1/courses/"+courseID+"/lectures/"+lectureID+"/content", "", student)
		return resp.StatusCode, body["type"]
	}

	// Free course: self-enroll unlocks content; cancel locks it again.
	freeID, freeLecture := publish(false)
	if code, typ := read(freeID, freeLecture); code != http.StatusForbidden || typ != "enrollment_required" {
		t.Fatalf("before enroll: %d %v", code, typ)
	}
	resp, body := c.do(http.MethodPost, "/v1/courses/"+freeID+"/enrollments/"+studentID, "", student)
	if resp.StatusCode != http.StatusCreated || body["status"] != "active" || body["user_id"] != studentID {
		t.Fatalf("self enroll: %d %v", resp.StatusCode, body)
	}
	if code, _ := read(freeID, freeLecture); code != http.StatusOK {
		t.Fatalf("enrolled read: %d", code)
	}
	resp, body = c.do(http.MethodDelete, "/v1/courses/"+freeID+"/enrollments/"+studentID, "", student)
	if resp.StatusCode != http.StatusOK || body["status"] != "canceled" {
		t.Fatalf("cancel: %d %v", resp.StatusCode, body)
	}
	if code, typ := read(freeID, freeLecture); code != http.StatusForbidden || typ != "enrollment_required" {
		t.Fatalf("after cancel: %d %v", code, typ)
	}

	// Paid course: the student must pay; a manager can enroll them.
	paidID, paidLecture := publish(true)
	resp, body = c.do(http.MethodPost, "/v1/courses/"+paidID+"/enrollments/"+studentID, "", student)
	if resp.StatusCode != http.StatusPaymentRequired || body["type"] != "payment_required" {
		t.Fatalf("paid self enroll: %d %v", resp.StatusCode, body)
	}
	if resp, body = c.do(http.MethodPost, "/v1/courses/"+paidID+"/enrollments/"+studentID, "", admin); resp.StatusCode != http.StatusCreated {
		t.Fatalf("manager enroll: %d %v", resp.StatusCode, body)
	}
	if code, _ := read(paidID, paidLecture); code != http.StatusOK {
		t.Fatalf("comped read: %d", code)
	}

	resp, body = c.do(http.MethodGet, "/v1/courses/"+paidID+"/enrollments", "", admin)
	if resp.StatusCode != http.StatusOK || body["total"] != float64(1) {
		t.Fatalf("roster: %d %v", resp.StatusCode, body)
	}
	if resp, _ = c.do(http.MethodGet, "/v1/courses/"+paidID+"/enrollments", "", student); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("student roster: %d", resp.StatusCode)
	}

	var activations, cancellations int
	if err := pool.QueryRow(ctx, `SELECT
		count(*) FILTER (WHERE payload->>'destination_topic' = 'enrollment.enrollment.activated'),
		count(*) FILTER (WHERE payload->>'destination_topic' = 'enrollment.enrollment.canceled')
		FROM platform.outbox_messages`).Scan(&activations, &cancellations); err != nil {
		t.Fatal(err)
	}
	if activations != 2 || cancellations != 1 {
		t.Fatalf("activations=%d cancellations=%d", activations, cancellations)
	}
}

func TestProgressEndToEnd(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pool := pgtest.New(t)
	google := googletest.NewIssuer(t)
	a, err := buildApp(ctx, baseConfig(t, google), slog.New(slog.NewJSONHandler(io.Discard, nil)), pool)
	if err != nil {
		t.Fatal(err)
	}
	defer a.forwarder.Close()
	srv := httptest.NewServer(a.handler)
	defer srv.Close()
	c := client{t: t, base: srv.URL}
	bearer := func(tok string) map[string]string { return map[string]string{"Authorization": "Bearer " + tok} }
	signIn := func(sub, email string) (string, string) {
		t.Helper()
		tok := google.Sign(t, googletest.Claims(sub, email, "web-client", time.Now()))
		resp, body := c.do(http.MethodPost, "/v1/auth/google", `{"id_token":"`+tok+`"}`, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("sign in %s: %d %v", email, resp.StatusCode, body)
		}
		return body["access_token"].(string), body["user"].(map[string]any)["id"].(string)
	}
	adminTok, _ := signIn("sub-admin", "admin@example.com")
	studentTok, studentID := signIn("sub-student", "student@example.com")
	strangerTok, _ := signIn("sub-stranger", "stranger@example.com")
	admin, student, stranger := bearer(adminTok), bearer(studentTok), bearer(strangerTok)

	// A published free course with two lectures.
	resp, body := c.do(http.MethodPost, "/v1/courses", `{"title":"Go","description":"d"}`, admin)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d %v", resp.StatusCode, body)
	}
	courseID := body["id"].(string)
	var lectures []string
	for _, title := range []string{"L1", "L2"} {
		resp, body = c.do(http.MethodPost, "/v1/courses/"+courseID+"/lectures", `{"title":"`+title+`","text_body":"<p>x</p>"}`, admin)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("lecture: %d %v", resp.StatusCode, body)
		}
		ls := body["lectures"].([]any)
		lectures = append(lectures, ls[len(ls)-1].(map[string]any)["id"].(string))
	}
	if resp, _ = c.do(http.MethodPost, "/v1/courses/"+courseID+"/publish", "", admin); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("publish: %d", resp.StatusCode)
	}
	record := func(lectureID, payload string, who map[string]string) (int, any) {
		resp, body := c.do(http.MethodPut, "/v1/courses/"+courseID+"/lectures/"+lectureID+"/progress/"+studentID, payload, who)
		return resp.StatusCode, body["type"]
	}

	if code, typ := record(lectures[0], `{"state":"completed","position_ms":0}`, student); code != http.StatusConflict || typ != "enrollment_required" {
		t.Fatalf("before enroll: %d %v", code, typ)
	}
	if resp, body = c.do(http.MethodPost, "/v1/courses/"+courseID+"/enrollments/"+studentID, "", student); resp.StatusCode != http.StatusCreated {
		t.Fatalf("enroll: %d %v", resp.StatusCode, body)
	}
	if code, _ := record(lectures[0], `{"state":"in_progress","position_ms":4000}`, student); code != http.StatusNoContent {
		t.Fatalf("in_progress: %d", code)
	}
	if code, _ := record(lectures[0], `{"state":"completed","position_ms":9000}`, student); code != http.StatusNoContent {
		t.Fatalf("completed: %d", code)
	}
	if code, _ := record(lectures[1], `{"state":"in_progress","position_ms":10}`, student); code != http.StatusNoContent {
		t.Fatalf("second lecture: %d", code)
	}
	if code, typ := record(lectures[0], `{"state":"completed","position_ms":0}`, admin); code != http.StatusForbidden || typ != "forbidden" {
		t.Fatalf("admin writes for student: %d %v", code, typ)
	}

	resp, body = c.do(http.MethodGet, "/v1/courses/"+courseID+"/progress/"+studentID, "", student)
	completed, _ := body["completed_lecture_ids"].([]any)
	if resp.StatusCode != http.StatusOK || body["last_lecture_id"] != lectures[1] || len(completed) != 1 || completed[0] != lectures[0] {
		t.Fatalf("own progress: %d %v", resp.StatusCode, body)
	}
	if resp, body = c.do(http.MethodGet, "/v1/courses/"+courseID+"/progress/"+studentID, "", admin); resp.StatusCode != http.StatusOK {
		t.Fatalf("manager read: %d %v", resp.StatusCode, body)
	}
	if resp, _ = c.do(http.MethodGet, "/v1/courses/"+courseID+"/progress/"+studentID, "", stranger); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("stranger read: %d", resp.StatusCode)
	}

	resp, body = c.do(http.MethodGet, "/v1/users/"+studentID+"/progress", "", student)
	courses, _ := body["courses"].([]any)
	days, _ := body["activity_days"].([]any)
	if resp.StatusCode != http.StatusOK || len(courses) != 1 || len(days) != 1 || days[0].(map[string]any)["lecture_count"] != float64(2) {
		t.Fatalf("user progress: %d %v", resp.StatusCode, body)
	}

	if resp, _ = c.do(http.MethodDelete, "/v1/courses/"+courseID+"/enrollments/"+studentID, "", student); resp.StatusCode != http.StatusOK {
		t.Fatalf("cancel: %d", resp.StatusCode)
	}
	if code, typ := record(lectures[1], `{"state":"completed","position_ms":0}`, student); code != http.StatusConflict || typ != "enrollment_required" {
		t.Fatalf("after cancel: %d %v", code, typ)
	}
	resp, body = c.do(http.MethodGet, "/v1/courses/"+courseID+"/progress/"+studentID, "", student)
	if completed, _ = body["completed_lecture_ids"].([]any); resp.StatusCode != http.StatusOK || len(completed) != 1 {
		t.Fatalf("kept after cancel: %d %v", resp.StatusCode, body)
	}
}

const mediaKey = "test-media-api-key-0123456789abcdef"

// fakeMediaService implements the subset of the media service API the backend calls.
type fakeMediaService struct {
	mu     sync.Mutex
	next   int64
	assets map[string]string // id -> kind
	auth   []string
}

func newFakeMediaService(t *testing.T) (*fakeMediaService, *httptest.Server) {
	t.Helper()
	f := &fakeMediaService{next: 900000000000000000, assets: map[string]string{}}
	asset := func(w http.ResponseWriter, assetID, kind string) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": assetID, "namespace_id": "ioe", "kind": kind, "status": "ready", "progress_percent": 100,
			"duration_ms": 60000, "width": 1280, "height": 720, "version": 1, "updated_at": time.Now().UTC(),
		})
	}
	find := func(w http.ResponseWriter, r *http.Request) (string, string, bool) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.auth = append(f.auth, r.Header.Get("Authorization")+"|"+r.Header.Get("X-Namespace-ID"))
		assetID := r.PathValue("id")
		kind, ok := f.assets[assetID]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"code":"not_found","detail":"asset not found","status":404}`)
		}
		return assetID, kind, ok
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/assets", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Kind string `json:"kind"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.next++
		assetID := strconv.FormatInt(f.next, 10)
		f.assets[assetID] = body.Kind
		f.auth = append(f.auth, r.Header.Get("Authorization")+"|"+r.Header.Get("X-Namespace-ID"))
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"asset_id": assetID, "namespace_id": "ioe", "upload_id": "up-" + assetID, "part_size": 5242880,
			"part_urls":  []map[string]any{{"part_number": 1, "url": "http://objects.test/" + assetID + "/1"}},
			"expires_at": time.Now().Add(time.Hour).UTC(),
		})
	})
	mux.HandleFunc("POST /v1/assets/{id}/complete", func(w http.ResponseWriter, r *http.Request) {
		if assetID, kind, ok := find(w, r); ok {
			asset(w, assetID, kind)
		}
	})
	mux.HandleFunc("GET /v1/assets/{id}", func(w http.ResponseWriter, r *http.Request) {
		if assetID, kind, ok := find(w, r); ok {
			asset(w, assetID, kind)
		}
	})
	mux.HandleFunc("POST /v1/assets/{id}/delivery", func(w http.ResponseWriter, r *http.Request) {
		assetID, _, ok := find(w, r)
		if !ok {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"visibility": "private", "url": "/v1/delivery/" + assetID + "/master.m3u8?token=t",
			"expires_at": time.Now().Add(15 * time.Minute).UTC(),
			"renditions": []map[string]any{{"name": "poster", "content_type": "image/jpeg", "url": "http://objects.test/" + assetID + "/poster.jpg"}},
		})
	})
	mux.HandleFunc("DELETE /v1/assets/{id}", func(w http.ResponseWriter, r *http.Request) {
		if assetID, _, ok := find(w, r); ok {
			f.mu.Lock()
			delete(f.assets, assetID)
			f.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return f, srv
}

func TestMediaEndToEnd(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pool := pgtest.New(t)
	google := googletest.NewIssuer(t)
	media, mediaSrv := newFakeMediaService(t)
	cfg := baseConfig(t, google)
	cfg.MediaServiceBaseURL = mediaSrv.URL
	cfg.MediaServicePublicURL = "https://media.test"
	cfg.MediaServiceAPIKey = mediaKey
	a, err := buildApp(ctx, cfg, slog.New(slog.NewJSONHandler(io.Discard, nil)), pool)
	if err != nil {
		t.Fatal(err)
	}
	defer a.forwarder.Close()
	srv := httptest.NewServer(a.handler)
	defer srv.Close()
	c := client{t: t, base: srv.URL}
	bearer := func(tok string) map[string]string { return map[string]string{"Authorization": "Bearer " + tok} }
	signIn := func(sub, email string) (string, string) {
		t.Helper()
		tok := google.Sign(t, googletest.Claims(sub, email, "web-client", time.Now()))
		resp, body := c.do(http.MethodPost, "/v1/auth/google", `{"id_token":"`+tok+`"}`, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("sign in %s: %d %v", email, resp.StatusCode, body)
		}
		return body["access_token"].(string), body["user"].(map[string]any)["id"].(string)
	}
	adminTok, _ := signIn("sub-admin", "admin@example.com")
	studentTok, studentID := signIn("sub-student", "student@example.com")
	strangerTok, _ := signIn("sub-stranger", "stranger@example.com")
	admin, student, stranger := bearer(adminTok), bearer(studentTok), bearer(strangerTok)

	newCourse := func(title string) string {
		resp, body := c.do(http.MethodPost, "/v1/courses", `{"title":"`+title+`","description":"d"}`, admin)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("create course: %d %v", resp.StatusCode, body)
		}
		return body["id"].(string)
	}
	addLecture := func(courseID, title string) string {
		resp, body := c.do(http.MethodPost, "/v1/courses/"+courseID+"/lectures", `{"title":"`+title+`","text_body":"<p>x</p>"}`, admin)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("lecture: %d %v", resp.StatusCode, body)
		}
		ls := body["lectures"].([]any)
		return ls[len(ls)-1].(map[string]any)["id"].(string)
	}
	upload := func(courseID string, who map[string]string) (int, string) {
		resp, body := c.do(http.MethodPost, "/v1/courses/"+courseID+"/media/uploads",
			`{"kind":"video","content_type":"video/mp4","filename":"a.mp4","size_bytes":1048576}`, who)
		id, _ := body["asset_id"].(string)
		return resp.StatusCode, id
	}
	putVideo := func(courseID, lectureID, assetID string) (int, any) {
		resp, body := c.do(http.MethodPut, "/v1/courses/"+courseID+"/lectures/"+lectureID+"/content",
			`{"blocks":[{"client_block_id":"v","type":"video","media_asset_id":"`+assetID+`","duration_ms":60000}]}`, admin)
		return resp.StatusCode, body["type"]
	}
	play := func(courseID, lectureID, assetID string, who map[string]string) (int, map[string]any) {
		resp, body := c.do(http.MethodGet, "/v1/courses/"+courseID+"/lectures/"+lectureID+"/media/"+assetID, "", who)
		return resp.StatusCode, body
	}

	courseA, courseB := newCourse("A"), newCourse("B")
	locked, preview := addLecture(courseA, "Locked"), addLecture(courseA, "Preview")

	if code, _ := upload(courseA, student); code != http.StatusNotFound {
		t.Fatalf("student upload to draft: %d", code)
	}
	code, video := upload(courseA, admin)
	if code != http.StatusCreated || video == "" {
		t.Fatalf("upload: %d %q", code, video)
	}
	resp, body := c.do(http.MethodPost, "/v1/media/uploads/"+video+"/complete", `{"parts":[{"part_number":1,"etag":"e1"}]}`, admin)
	if resp.StatusCode != http.StatusOK || body["status"] != "ready" || body["course_id"] != courseA {
		t.Fatalf("complete: %d %v", resp.StatusCode, body)
	}
	if resp, body = c.do(http.MethodGet, "/v1/media/assets/"+video, "", admin); resp.StatusCode != http.StatusOK || body["kind"] != "video" {
		t.Fatalf("status: %d %v", resp.StatusCode, body)
	}
	if resp, _ = c.do(http.MethodGet, "/v1/media/assets/"+video, "", stranger); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("stranger status: %d", resp.StatusCode)
	}
	_, foreign := upload(courseB, admin)

	if code, typ := putVideo(courseA, locked, foreign); code != http.StatusBadRequest || typ != "invalid_media_reference" {
		t.Fatalf("foreign asset: %d %v", code, typ)
	}
	if code, _ := putVideo(courseA, locked, video); code != http.StatusNoContent {
		t.Fatalf("put video: %d", code)
	}
	if resp, _ = c.do(http.MethodPost, "/v1/courses/"+courseA+"/lectures/"+preview+"/free-preview", `{"free_preview":true}`, admin); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("free preview: %d", resp.StatusCode)
	}
	if resp, _ = c.do(http.MethodPost, "/v1/courses/"+courseA+"/publish", "", admin); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("publish: %d", resp.StatusCode)
	}

	if code, body := play(courseA, locked, video, student); code != http.StatusForbidden || body["type"] != "enrollment_required" {
		t.Fatalf("before enroll: %d %v", code, body)
	}
	if code, _ := play(courseA, preview, video, stranger); code != http.StatusNotFound {
		t.Fatalf("unreferenced on preview lecture: %d", code)
	}
	if resp, body = c.do(http.MethodPost, "/v1/courses/"+courseA+"/enrollments/"+studentID, "", student); resp.StatusCode != http.StatusCreated {
		t.Fatalf("enroll: %d %v", resp.StatusCode, body)
	}
	code, body = play(courseA, locked, video, student)
	if code != http.StatusOK || body["status"] != "ready" ||
		body["playback_url"] != "https://media.test/v1/delivery/"+video+"/master.m3u8?token=t" ||
		body["poster_url"] != "http://objects.test/"+video+"/poster.jpg" {
		t.Fatalf("playback: %d %v", code, body)
	}

	if code, _ := putVideo(courseA, preview, video); code != http.StatusNoContent {
		t.Fatalf("put preview video: %d", code)
	}
	// The edit reopened the course as a draft: readers keep the live version until it is republished.
	if code, _ := play(courseA, preview, video, stranger); code != http.StatusNotFound {
		t.Fatalf("draft preview video playback: %d", code)
	}
	if resp, _ = c.do(http.MethodPost, "/v1/courses/"+courseA+"/publish", "", admin); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("republish: %d", resp.StatusCode)
	}
	if code, body := play(courseA, preview, video, stranger); code != http.StatusOK || body["playback_url"] == nil {
		t.Fatalf("free preview playback: %d %v", code, body)
	}
	if code, _ := play(courseA, locked, foreign, student); code != http.StatusNotFound {
		t.Fatalf("foreign asset playback: %d", code)
	}

	resp, body = c.do(http.MethodDelete, "/v1/media/assets/"+video, "", admin)
	if resp.StatusCode != http.StatusConflict || body["type"] != "asset_in_use" {
		t.Fatalf("delete live video: %d %v", resp.StatusCode, body)
	}

	c.do(http.MethodPut, "/v1/courses/"+courseA+"/lectures/"+locked+"/content", `{"blocks":[]}`, admin)
	c.do(http.MethodPut, "/v1/courses/"+courseA+"/lectures/"+preview+"/content", `{"blocks":[]}`, admin)
	c.do(http.MethodPost, "/v1/courses/"+courseA+"/publish", "", admin)

	if resp, _ = c.do(http.MethodDelete, "/v1/media/assets/"+video, "", admin); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: %d", resp.StatusCode)
	}
	if code, _ := play(courseA, locked, video, student); code != http.StatusNotFound {
		t.Fatalf("after delete: %d", code)
	}

	media.mu.Lock()
	defer media.mu.Unlock()
	for _, h := range media.auth {
		if h != "Bearer "+mediaKey+"|ioe" {
			t.Fatalf("media request headers = %q", h)
		}
	}
}

func TestMediaDisabledRoutesAreAbsent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pool := pgtest.New(t)
	google := googletest.NewIssuer(t)
	a, err := buildApp(ctx, baseConfig(t, google), slog.New(slog.NewJSONHandler(io.Discard, nil)), pool)
	if err != nil {
		t.Fatal(err)
	}
	defer a.forwarder.Close()
	srv := httptest.NewServer(a.handler)
	defer srv.Close()
	resp, _ := client{t: t, base: srv.URL}.do(http.MethodGet, "/v1/media/assets/1", "", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

func TestQuizzesEndToEnd(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pool := pgtest.New(t)
	google := googletest.NewIssuer(t)
	a, err := buildApp(ctx, baseConfig(t, google), slog.New(slog.NewJSONHandler(io.Discard, nil)), pool)
	if err != nil {
		t.Fatal(err)
	}
	defer a.forwarder.Close()
	srv := httptest.NewServer(a.handler)
	defer srv.Close()
	c := client{t: t, base: srv.URL}
	bearer := func(tok string) map[string]string { return map[string]string{"Authorization": "Bearer " + tok} }
	signIn := func(sub, email string) (string, string) {
		t.Helper()
		tok := google.Sign(t, googletest.Claims(sub, email, "web-client", time.Now()))
		resp, body := c.do(http.MethodPost, "/v1/auth/google", `{"id_token":"`+tok+`"}`, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("sign in %s: %d %v", email, resp.StatusCode, body)
		}
		return body["access_token"].(string), body["user"].(map[string]any)["id"].(string)
	}
	adminTok, _ := signIn("sub-admin", "admin@example.com")
	studentTok, studentID := signIn("sub-student", "student@example.com")
	strangerTok, strangerID := signIn("sub-stranger", "stranger@example.com")
	admin, student, stranger := bearer(adminTok), bearer(studentTok), bearer(strangerTok)

	resp, body := c.do(http.MethodPost, "/v1/courses", `{"title":"Go","description":"d"}`, admin)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create course: %d %v", resp.StatusCode, body)
	}
	courseID := body["id"].(string)
	var lectures []string
	for _, title := range []string{"L1", "L2"} {
		resp, body = c.do(http.MethodPost, "/v1/courses/"+courseID+"/lectures", `{"title":"`+title+`","text_body":"<p>x</p>"}`, admin)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("lecture: %d %v", resp.StatusCode, body)
		}
		ls := body["lectures"].([]any)
		lectures = append(lectures, ls[len(ls)-1].(map[string]any)["id"].(string))
	}
	quizzesPath := "/v1/courses/" + courseID + "/lectures/" + lectures[0] + "/quizzes"

	// An instructor creates a quiz and references it from the lecture.
	resp, body = c.do(http.MethodPost, quizzesPath, `{"position":0,"questions":[{"prompt":"2+2?","type":"single_choice",`+
		`"explanation":"arith","reference_lecture_id":"","options":[{"label":"4","is_correct":true},{"label":"5","is_correct":false}]}]}`, admin)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create quiz: %d %v", resp.StatusCode, body)
	}
	quizID := body["id"].(string)
	question := body["questions"].([]any)[0].(map[string]any)
	questionID := question["id"].(string)
	correct := question["correct_option_ids"].([]any)[0].(string)
	putQuizBlock := func(lectureID string) (int, any) {
		resp, body := c.do(http.MethodPut, "/v1/courses/"+courseID+"/lectures/"+lectureID+"/content",
			`{"blocks":[{"client_block_id":"q","type":"quiz","quiz_id":"`+quizID+`"}]}`, admin)
		return resp.StatusCode, body["type"]
	}
	if code, typ := putQuizBlock(lectures[0]); code != http.StatusNoContent {
		t.Fatalf("own lecture block: %d %v", code, typ)
	}
	if code, typ := putQuizBlock(lectures[1]); code != http.StatusBadRequest || typ != "invalid_quiz_reference" {
		t.Fatalf("other lecture block: %d %v", code, typ)
	}
	if resp, _ = c.do(http.MethodPost, "/v1/courses/"+courseID+"/publish", "", admin); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("publish: %d", resp.StatusCode)
	}

	list := func(who map[string]string) (int, []map[string]any) {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+quizzesPath, nil)
		if err != nil {
			t.Fatal(err)
		}
		for k, v := range who {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out []map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
	if code, _ := list(student); code != http.StatusConflict {
		t.Fatalf("list before enroll: %d", code)
	}
	if resp, body = c.do(http.MethodPost, "/v1/courses/"+courseID+"/enrollments/"+studentID, "", student); resp.StatusCode != http.StatusCreated {
		t.Fatalf("enroll: %d %v", resp.StatusCode, body)
	}
	if code, qs := list(student); code != http.StatusOK || len(qs) != 1 || qs[0]["id"] != quizID {
		t.Fatalf("list: %d %v", code, qs)
	}

	attempt := `{"user_id":"` + studentID + `","answers":[{"question_id":"` + questionID + `","option_ids":["` + correct + `"]}]}`
	key := map[string]string{"Authorization": student["Authorization"], "Idempotency-Key": "attempt-1"}
	resp, body = c.do(http.MethodPost, "/v1/quizzes/"+quizID+"/attempts", attempt, key)
	if resp.StatusCode != http.StatusCreated || body["recorded"] != true {
		t.Fatalf("record: %d %v", resp.StatusCode, body)
	}
	first := body["id"]
	resp, body = c.do(http.MethodPost, "/v1/quizzes/"+quizID+"/attempts", attempt, key)
	if resp.StatusCode != http.StatusCreated || body["id"] != first {
		t.Fatalf("replay: %d %v first=%v", resp.StatusCode, body, first)
	}
	forStranger := `{"user_id":"` + strangerID + `","answers":[]}`
	if resp, body = c.do(http.MethodPost, "/v1/quizzes/"+quizID+"/attempts", forStranger, student); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("record for another user: %d %v", resp.StatusCode, body)
	}
	if resp, body = c.do(http.MethodPost, "/v1/quizzes/"+quizID+"/attempts", forStranger, stranger); resp.StatusCode != http.StatusConflict || body["type"] != "enrollment_required" {
		t.Fatalf("unenrolled: %d %v", resp.StatusCode, body)
	}

	if resp, _ = c.do(http.MethodDelete, "/v1/quizzes/"+quizID, "", student); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("student delete: %d", resp.StatusCode)
	}
	if resp, _ = c.do(http.MethodDelete, "/v1/quizzes/"+quizID, "", admin); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: %d", resp.StatusCode)
	}
	if code, qs := list(student); code != http.StatusOK || len(qs) != 1 {
		t.Fatalf("after delete: %d %v", code, qs)
	}
}

func TestExamsEndToEnd(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pool := pgtest.New(t)
	google := googletest.NewIssuer(t)
	a, err := buildApp(ctx, baseConfig(t, google), slog.New(slog.NewJSONHandler(io.Discard, nil)), pool)
	if err != nil {
		t.Fatal(err)
	}
	defer a.forwarder.Close()
	srv := httptest.NewServer(a.handler)
	defer srv.Close()
	c := client{t: t, base: srv.URL}
	bearer := func(tok string) map[string]string { return map[string]string{"Authorization": "Bearer " + tok} }
	signIn := func(sub, email string) (string, string) {
		t.Helper()
		tok := google.Sign(t, googletest.Claims(sub, email, "web-client", time.Now()))
		resp, body := c.do(http.MethodPost, "/v1/auth/google", `{"id_token":"`+tok+`"}`, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("sign in %s: %d %v", email, resp.StatusCode, body)
		}
		return body["access_token"].(string), body["user"].(map[string]any)["id"].(string)
	}
	adminTok, _ := signIn("sub-admin", "admin@example.com")
	studentTok, studentID := signIn("sub-student", "student@example.com")
	strangerTok, _ := signIn("sub-stranger", "stranger@example.com")
	admin, student, stranger := bearer(adminTok), bearer(studentTok), bearer(strangerTok)
	list := func(path string, who map[string]string) (int, []map[string]any) {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		for k, v := range who {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out []map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}

	resp, body := c.do(http.MethodPost, "/v1/courses", `{"title":"Go","description":"d"}`, admin)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create course: %d %v", resp.StatusCode, body)
	}
	courseID := body["id"].(string)
	if resp, body = c.do(http.MethodPost, "/v1/courses/"+courseID+"/lectures", `{"title":"L1","text_body":"<p>x</p>"}`, admin); resp.StatusCode != http.StatusCreated {
		t.Fatalf("lecture: %d %v", resp.StatusCode, body)
	}
	if resp, _ = c.do(http.MethodPost, "/v1/courses/"+courseID+"/publish", "", admin); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("publish course: %d", resp.StatusCode)
	}
	examsPath := "/v1/courses/" + courseID + "/exams"
	examBody := func(limit string, retakes bool) string {
		return `{"title":"Final","description":"","position":0,"pass_mark":50,"time_limit_seconds":` + limit +
			`,"retakes_allowed":` + strconv.FormatBool(retakes) + `,"opens_at":null,"closes_at":null,"reveal_policy":"after_attempt",` +
			`"questions":[{"prompt":"2+2?","type":"single_choice","explanation":"arith","points":2,"reference_lecture_id":"",` +
			`"options":[{"label":"4","is_correct":true},{"label":"5","is_correct":false}]}]}`
	}

	// An instructor creates a draft exam; students cannot see it until it is published.
	resp, body = c.do(http.MethodPost, examsPath, examBody("null", false), admin)
	if resp.StatusCode != http.StatusCreated || body["status"] != "draft" {
		t.Fatalf("create exam: %d %v", resp.StatusCode, body)
	}
	examID := body["id"].(string)
	question := body["questions"].([]any)[0].(map[string]any)
	questionID := question["id"].(string)
	options := question["options"].([]any)
	right, wrong := options[0].(map[string]any)["id"].(string), options[1].(map[string]any)["id"].(string)
	if code, _ := list(examsPath, student); code != http.StatusConflict {
		t.Fatalf("list before enroll: %d", code)
	}
	if resp, body = c.do(http.MethodPost, "/v1/courses/"+courseID+"/enrollments/"+studentID, "", student); resp.StatusCode != http.StatusCreated {
		t.Fatalf("enroll: %d %v", resp.StatusCode, body)
	}
	if code, exams := list(examsPath, student); code != http.StatusOK || len(exams) != 0 {
		t.Fatalf("drafts listed: %d %v", code, exams)
	}
	if resp, _ = c.do(http.MethodPost, "/v1/exams/"+examID+"/publish", "", admin); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("publish exam: %d", resp.StatusCode)
	}
	if resp, _ = c.do(http.MethodPost, "/v1/courses/"+courseID+"/publish", "", admin); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("publish course: %d", resp.StatusCode)
	}
	if code, exams := list(examsPath, student); code != http.StatusOK || len(exams) != 1 || exams[0]["availability"] != "open" {
		t.Fatalf("list: %d %v", code, exams)
	}
	resp, body = c.do(http.MethodGet, "/v1/exams/"+examID, "", student)
	if q := body["questions"].([]any)[0].(map[string]any); resp.StatusCode != http.StatusOK || q["correct_option_ids"] != nil {
		t.Fatalf("student exam: %d %v", resp.StatusCode, body)
	}
	if resp, body = c.do(http.MethodGet, "/v1/exams/"+examID, "", stranger); resp.StatusCode != http.StatusConflict || body["type"] != "enrollment_required" {
		t.Fatalf("unenrolled: %d %v", resp.StatusCode, body)
	}

	// The student takes the exam.
	resp, body = c.do(http.MethodPost, "/v1/exams/"+examID+"/attempts", "", student)
	if resp.StatusCode != http.StatusCreated || body["deadline"] != nil {
		t.Fatalf("start: %d %v", resp.StatusCode, body)
	}
	attemptID := body["id"].(string)
	if resp, body = c.do(http.MethodPost, "/v1/exams/"+examID+"/attempts", "", student); resp.StatusCode != http.StatusConflict || body["type"] != "open_attempt_exists" {
		t.Fatalf("second start: %d %v", resp.StatusCode, body)
	}
	answer := func(option string) int {
		resp, _ := c.do(http.MethodPost, "/v1/exam-attempts/"+attemptID+"/answers", `{"question_id":"`+questionID+`","option_ids":["`+option+`"]}`, student)
		return resp.StatusCode
	}
	if code := answer(wrong); code != http.StatusNoContent {
		t.Fatalf("answer: %d", code)
	}
	if code := answer(right); code != http.StatusNoContent {
		t.Fatalf("change answer: %d", code)
	}
	if resp, _ = c.do(http.MethodGet, "/v1/exam-attempts/"+attemptID, "", stranger); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("stranger reads attempt: %d", resp.StatusCode)
	}
	if resp, body = c.do(http.MethodGet, "/v1/exam-attempts/"+attemptID+"/review", "", student); resp.StatusCode != http.StatusConflict || body["type"] != "reveal_attempt_open" {
		t.Fatalf("review while open: %d %v", resp.StatusCode, body)
	}
	resp, body = c.do(http.MethodPost, "/v1/exam-attempts/"+attemptID+"/submit", "", student)
	if resp.StatusCode != http.StatusOK || body["score"] != 100.0 || body["passed"] != true {
		t.Fatalf("submit: %d %v", resp.StatusCode, body)
	}
	resp, body = c.do(http.MethodGet, "/v1/exam-attempts/"+attemptID+"/review", "", student)
	if q := body["questions"].([]any)[0].(map[string]any); resp.StatusCode != http.StatusOK || q["correct_option_ids"].([]any)[0] != right {
		t.Fatalf("review: %d %v", resp.StatusCode, body)
	}
	if resp, body = c.do(http.MethodPost, "/v1/exams/"+examID+"/attempts", "", student); resp.StatusCode != http.StatusConflict || body["type"] != "retakes_not_allowed" {
		t.Fatalf("retake: %d %v", resp.StatusCode, body)
	}

	// In the versioned model, edits succeed (producing a new revision) even after submissions.
	save := func(prompt string, rightCorrect bool) (*http.Response, map[string]any) {
		return c.do(http.MethodPut, "/v1/exams/"+examID, `{"title":"Final","description":"","position":0,"pass_mark":50,`+
			`"time_limit_seconds":null,"retakes_allowed":false,"opens_at":null,"closes_at":null,"reveal_policy":"after_attempt",`+
			`"questions":[{"id":"`+questionID+`","prompt":"`+prompt+`","type":"single_choice","explanation":"arith","points":2,`+
			`"options":[{"id":"`+right+`","label":"4","is_correct":`+strconv.FormatBool(rightCorrect)+`},`+
			`{"id":"`+wrong+`","label":"5","is_correct":`+strconv.FormatBool(!rightCorrect)+`}]}]}`, admin)
	}
	if resp, body = save("What is 2+2?", true); resp.StatusCode != http.StatusOK || body["revision"] != float64(3) {
		t.Fatalf("reword: %d %v", resp.StatusCode, body)
	}
	if code, attempts := list("/v1/exams/"+examID+"/attempts", admin); code != http.StatusOK || len(attempts) != 1 || attempts[0]["still_open"] != false {
		t.Fatalf("attempts: %d %v", code, attempts)
	}
	// Deleting an exam with attempts succeeds (soft-delete).
	if resp, _ = c.do(http.MethodDelete, "/v1/exams/"+examID, "", admin); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: %d", resp.StatusCode)
	}

	// A timed attempt left past its deadline reads back as auto-submitted.
	resp, body = c.do(http.MethodPost, examsPath, examBody("1", true), admin)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create timed exam: %d %v", resp.StatusCode, body)
	}
	timedID := body["id"].(string)
	if resp, _ = c.do(http.MethodPost, "/v1/exams/"+timedID+"/publish", "", admin); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("publish timed: %d", resp.StatusCode)
	}
	if resp, _ = c.do(http.MethodPost, "/v1/courses/"+courseID+"/publish", "", admin); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("publish course timed: %d", resp.StatusCode)
	}
	resp, body = c.do(http.MethodPost, "/v1/exams/"+timedID+"/attempts", "", student)
	if resp.StatusCode != http.StatusCreated || body["deadline"] == nil {
		t.Fatalf("start timed: %d %v", resp.StatusCode, body)
	}
	timedAttempt := body["id"].(string)
	time.Sleep(1500 * time.Millisecond)
	resp, body = c.do(http.MethodGet, "/v1/exam-attempts/"+timedAttempt, "", student)
	if resp.StatusCode != http.StatusOK || body["auto_submitted"] != true || body["score"] != 0.0 {
		t.Fatalf("expired attempt: %d %v", resp.StatusCode, body)
	}
}

// fakeEsewa answers eSewa status requests from a status map keyed by transaction_uuid.
type fakeEsewa struct {
	mu     sync.Mutex
	status map[string]string
}

func (f *fakeEsewa) set(ref, status string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status[ref] = status
}

func (f *fakeEsewa) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	ref := q.Get("transaction_uuid")
	f.mu.Lock()
	status, ok := f.status[ref]
	f.mu.Unlock()
	if !ok {
		status = "NOT_FOUND"
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = fmt.Fprintf(w, `{"product_code":%q,"transaction_uuid":%q,"total_amount":%s,"status":%q,"ref_id":"REF-%s"}`,
		q.Get("product_code"), ref, q.Get("total_amount"), status, ref)
}

func esewaConfig(cfg config.Config, statusURL string) config.Config {
	cfg.EsewaProductCode = "EPAYTEST"
	cfg.EsewaSecretKey = "8gBm/:&EnhH.1/q"
	cfg.EsewaFormURL = "https://rc-epay.esewa.com.np/api/epay/main/v2/form"
	cfg.EsewaStatusURL = statusURL
	cfg.PaymentReturnURL = appOrigin
	return cfg
}

func TestPaymentEndToEnd(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pool := pgtest.New(t)
	google := googletest.NewIssuer(t)
	fake := &fakeEsewa{status: map[string]string{}}
	esewaSrv := httptest.NewServer(fake)
	defer esewaSrv.Close()
	cfg := esewaConfig(baseConfig(t, google), esewaSrv.URL+"/api/epay/transaction/status/")
	a, err := buildApp(ctx, cfg, slog.New(slog.NewJSONHandler(io.Discard, nil)), pool)
	if err != nil {
		t.Fatal(err)
	}
	defer a.forwarder.Close()
	srv := httptest.NewServer(a.handler)
	defer srv.Close()
	c := client{t: t, base: srv.URL}
	bearer := func(tok string) map[string]string { return map[string]string{"Authorization": "Bearer " + tok} }
	signIn := func(sub, email string) string {
		t.Helper()
		tok := google.Sign(t, googletest.Claims(sub, email, "web-client", time.Now()))
		resp, body := c.do(http.MethodPost, "/v1/auth/google", `{"id_token":"`+tok+`"}`, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("sign in %s: %d %v", email, resp.StatusCode, body)
		}
		return body["access_token"].(string)
	}
	admin := bearer(signIn("sub-admin", "admin@example.com"))
	student := bearer(signIn("sub-student", "student@example.com"))
	stranger := bearer(signIn("sub-stranger", "stranger@example.com"))

	publish := func(priced bool) (string, string) {
		t.Helper()
		resp, body := c.do(http.MethodPost, "/v1/courses", `{"title":"Go","description":"d"}`, admin)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("create: %d %v", resp.StatusCode, body)
		}
		courseID := body["id"].(string)
		if priced {
			if resp, body = c.do(http.MethodPost, "/v1/courses/"+courseID+"/price", `{"amount_minor":150000,"currency":"NPR"}`, admin); resp.StatusCode != http.StatusOK {
				t.Fatalf("price: %d %v", resp.StatusCode, body)
			}
		}
		resp, body = c.do(http.MethodPost, "/v1/courses/"+courseID+"/lectures", `{"title":"L1","text_body":"<p>x</p>"}`, admin)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("lecture: %d %v", resp.StatusCode, body)
		}
		lectures := body["lectures"].([]any)
		lectureID := lectures[len(lectures)-1].(map[string]any)["id"].(string)
		if resp, _ = c.do(http.MethodPost, "/v1/courses/"+courseID+"/publish", "", admin); resp.StatusCode != http.StatusNoContent {
			t.Fatalf("publish: %d", resp.StatusCode)
		}
		return courseID, lectureID
	}
	read := func(who map[string]string, courseID, lectureID string) int {
		resp, _ := c.do(http.MethodGet, "/v1/courses/"+courseID+"/lectures/"+lectureID+"/content", "", who)
		return resp.StatusCode
	}
	checkout := func(who map[string]string, courseID string) (*http.Response, map[string]any) {
		return c.do(http.MethodPost, "/v1/courses/"+courseID+"/purchases", `{"gateway":"esewa"}`, who)
	}

	freeID, _ := publish(false)
	if resp, body := checkout(student, freeID); resp.StatusCode != http.StatusConflict || body["type"] != "course_free" {
		t.Fatalf("free checkout: %d %v", resp.StatusCode, body)
	}

	courseID, lectureID := publish(true)
	resp, body := checkout(student, courseID)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("checkout: %d %v", resp.StatusCode, body)
	}
	purchase := body["purchase"].(map[string]any)
	form := body["checkout"].(map[string]any)
	fields := form["fields"].(map[string]any)
	purchaseID := purchase["id"].(string)
	if purchase["status"] != "pending" || purchase["amount_minor"] != float64(150000) || form["method"] != "POST" ||
		form["url"] != "https://rc-epay.esewa.com.np/api/epay/main/v2/form" || fields["total_amount"] != "1500" ||
		fields["transaction_uuid"] != purchaseID || fields["success_url"] != appOrigin+"/payments/"+purchaseID+"/return" ||
		fields["signature"] == "" {
		t.Fatalf("checkout body = %v", body)
	}
	if code := read(student, courseID, lectureID); code != http.StatusForbidden {
		t.Fatalf("read before paying: %d", code)
	}

	confirm := func(who map[string]string, id string) (*http.Response, map[string]any) {
		return c.do(http.MethodPost, "/v1/purchases/"+id+"/confirm", "", who)
	}
	fake.set(purchaseID, "PENDING")
	if resp, body = confirm(student, purchaseID); resp.StatusCode != http.StatusOK || body["purchase"].(map[string]any)["status"] != "pending" {
		t.Fatalf("pending confirm: %d %v", resp.StatusCode, body)
	}
	fake.set(purchaseID, "COMPLETE")
	resp, body = confirm(student, purchaseID)
	if p := body["purchase"].(map[string]any); resp.StatusCode != http.StatusOK || p["status"] != "paid" || p["granted"] != true {
		t.Fatalf("complete confirm: %d %v", resp.StatusCode, body)
	}
	if code := read(student, courseID, lectureID); code != http.StatusOK {
		t.Fatalf("read after paying: %d", code)
	}
	if resp, body = checkout(student, courseID); resp.StatusCode != http.StatusConflict || body["type"] != "already_enrolled" {
		t.Fatalf("second checkout: %d %v", resp.StatusCode, body)
	}
	if resp, _ = c.do(http.MethodGet, "/v1/purchases/"+purchaseID, "", stranger); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("stranger get: %d", resp.StatusCode)
	}
	if resp, _ = c.do(http.MethodGet, "/v1/purchases/"+purchaseID, "", admin); resp.StatusCode != http.StatusOK {
		t.Fatalf("admin get: %d", resp.StatusCode)
	}

	// The stranger pays but never comes back; the reconciler enrolls them.
	resp, body = checkout(stranger, courseID)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("stranger checkout: %d %v", resp.StatusCode, body)
	}
	strangerPurchase := body["purchase"].(map[string]any)["id"].(string)
	fake.set(strangerPurchase, "COMPLETE")
	strangerPurchaseID, err := strconv.ParseInt(strangerPurchase, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "UPDATE payment.purchases SET created_at = created_at - interval '1 hour' WHERE id = $1", strangerPurchaseID); err != nil {
		t.Fatal(err)
	}
	if err := a.payments.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if code := read(stranger, courseID, lectureID); code != http.StatusOK {
		t.Fatalf("stranger read after reconcile: %d", code)
	}

	var initiated, paid int
	if err := pool.QueryRow(ctx, `SELECT
		count(*) FILTER (WHERE payload->>'destination_topic' = 'payment.purchase.initiated'),
		count(*) FILTER (WHERE payload->>'destination_topic' = 'payment.purchase.paid')
		FROM platform.outbox_messages`).Scan(&initiated, &paid); err != nil {
		t.Fatal(err)
	}
	if initiated != 2 || paid != 2 {
		t.Fatalf("initiated=%d paid=%d", initiated, paid)
	}
}

func TestPaymentsDisabledReturn503(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pool := pgtest.New(t)
	google := googletest.NewIssuer(t)
	a, err := buildApp(ctx, baseConfig(t, google), slog.New(slog.NewJSONHandler(io.Discard, nil)), pool)
	if err != nil {
		t.Fatal(err)
	}
	defer a.forwarder.Close()
	srv := httptest.NewServer(a.handler)
	defer srv.Close()
	c := client{t: t, base: srv.URL}
	tok := google.Sign(t, googletest.Claims("sub-student", "student@example.com", "web-client", time.Now()))
	_, body := c.do(http.MethodPost, "/v1/auth/google", `{"id_token":"`+tok+`"}`, nil)
	headers := map[string]string{"Authorization": "Bearer " + body["access_token"].(string)}
	resp, body := c.do(http.MethodPost, "/v1/courses/1/purchases", `{"gateway":"esewa"}`, headers)
	if resp.StatusCode != http.StatusServiceUnavailable || body["type"] != "payment_unavailable" {
		t.Fatalf("disabled checkout: %d %v", resp.StatusCode, body)
	}
}

func quizBody(prompt string) string {
	return `{"position":0,"questions":[{"prompt":"` + prompt + `","type":"single_choice",` +
		`"explanation":"arith","reference_lecture_id":"","options":[{"label":"4","is_correct":true},{"label":"5","is_correct":false}]}]}`
}

func examBody(title string) string {
	return `{"title":"` + title + `","description":"","position":0,"pass_mark":50,"time_limit_seconds":null,` +
		`"retakes_allowed":false,"opens_at":null,"closes_at":null,"reveal_policy":"after_attempt",` +
		`"questions":[{"prompt":"2+2?","type":"single_choice","explanation":"arith","points":2,"reference_lecture_id":"",` +
		`"options":[{"label":"4","is_correct":true},{"label":"5","is_correct":false}]}]}`
}

func mustStatus(t *testing.T, c client, method, path, reqBody string, headers map[string]string, want int) map[string]any {
	t.Helper()
	r, b := c.do(method, path, reqBody, headers)
	if r.StatusCode != want {
		t.Fatalf("%s %s = %d %v, want %d", method, path, r.StatusCode, b, want)
	}
	return b
}

func createQuiz(t *testing.T, c client, path, prompt string, headers map[string]string) string {
	t.Helper()
	b := mustStatus(t, c, http.MethodPost, path, quizBody(prompt), headers, http.StatusCreated)
	return b["id"].(string)
}

func putQuizBlock(t *testing.T, c client, courseID, lectureID, quizID string, headers map[string]string) {
	t.Helper()
	mustStatus(t, c, http.MethodPut, "/v1/courses/"+courseID+"/lectures/"+lectureID+"/content",
		`{"blocks":[{"client_block_id":"q","type":"quiz","quiz_id":"`+quizID+`"}]}`, headers, http.StatusNoContent)
}

func createPublishedExam(t *testing.T, c client, courseID string, headers map[string]string) string {
	t.Helper()
	b := mustStatus(t, c, http.MethodPost, "/v1/courses/"+courseID+"/exams", examBody("Final"), headers, http.StatusCreated)
	examID := b["id"].(string)
	mustStatus(t, c, http.MethodPost, "/v1/exams/"+examID+"/publish", "", headers, http.StatusNoContent)
	mustStatus(t, c, http.MethodPost, "/v1/courses/"+courseID+"/publish", "", headers, http.StatusNoContent)
	return examID
}

func firstPrompt(list []map[string]any) string {
	if len(list) == 0 {
		return ""
	}
	qs, ok := list[0]["questions"].([]any)
	if !ok || len(qs) == 0 {
		return ""
	}
	q, ok := qs[0].(map[string]any)
	if !ok {
		return ""
	}
	p, _ := q["prompt"].(string)
	return p
}

func waitFor(t *testing.T, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("timed out waiting for condition")
}

func TestAssessmentVersioningEndToEnd(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pool := pgtest.New(t)
	google := googletest.NewIssuer(t)
	a, err := buildApp(ctx, baseConfig(t, google), slog.New(slog.NewJSONHandler(io.Discard, nil)), pool)
	if err != nil {
		t.Fatal(err)
	}
	defer a.forwarder.Close()
	go func() { _ = a.forwarder.Run(ctx) }()
	srv := httptest.NewServer(a.handler)
	defer srv.Close()
	c := client{t: t, base: srv.URL}

	bearer := func(tok string) map[string]string { return map[string]string{"Authorization": "Bearer " + tok} }
	signIn := func(sub, email string) (string, string) {
		t.Helper()
		tok := google.Sign(t, googletest.Claims(sub, email, "web-client", time.Now()))
		resp, body := c.do(http.MethodPost, "/v1/auth/google", `{"id_token":"`+tok+`"}`, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("sign in %s: %d %v", email, resp.StatusCode, body)
		}
		return body["access_token"].(string), body["user"].(map[string]any)["id"].(string)
	}
	adminTok, _ := signIn("sub-admin", "admin@example.com")
	studentTok, studentID := signIn("sub-student", "student@example.com")
	admin, student := bearer(adminTok), bearer(studentTok)

	resp, body := c.do(http.MethodPost, "/v1/courses", `{"title":"Go","description":"d"}`, admin)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create course: %d %v", resp.StatusCode, body)
	}
	courseID := body["id"].(string)
	resp, body = c.do(http.MethodPost, "/v1/courses/"+courseID+"/lectures", `{"title":"L1","text_body":"<p>x</p>"}`, admin)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("lecture: %d %v", resp.StatusCode, body)
	}
	ls := body["lectures"].([]any)
	lectureID := ls[len(ls)-1].(map[string]any)["id"].(string)
	quizzesPath := "/v1/courses/" + courseID + "/lectures/" + lectureID + "/quizzes"

	listQuizzes := func(who map[string]string) []map[string]any {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+quizzesPath, nil)
		for k, v := range who {
			req.Header.Set(k, v)
		}
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil
		}
		defer r.Body.Close()
		var out []map[string]any
		_ = json.NewDecoder(r.Body).Decode(&out)
		return out
	}

	// 1. Publish with a quiz; edit it in the draft: students still see the original.
	quizID := createQuiz(t, c, quizzesPath, "2+2?", admin)
	putQuizBlock(t, c, courseID, lectureID, quizID, admin)
	mustStatus(t, c, http.MethodPost, "/v1/courses/"+courseID+"/publish", "", admin, http.StatusNoContent)

	// Enroll student (requires course to be published)
	if resp, body = c.do(http.MethodPost, "/v1/courses/"+courseID+"/enrollments/"+studentID, "", student); resp.StatusCode != http.StatusCreated {
		t.Fatalf("enroll: %d %v", resp.StatusCode, body)
	}

	mustStatus(t, c, http.MethodPut, quizzesPath+"/"+quizID, quizBody("3+3?"), admin, http.StatusOK)
	list := listQuizzes(student)
	if prompt := firstPrompt(list); prompt != "2+2?" {
		t.Fatalf("student sees %q before republish", prompt)
	}

	// 2. Start an exam attempt, change the exam, republish: the attempt keeps its revision and
	//    the student now sees the new quiz.
	examID := createPublishedExam(t, c, courseID, admin)
	_, started := c.do(http.MethodPost, "/v1/exams/"+examID+"/attempts", "", student)
	startedRevision := started["revision"]
	mustStatus(t, c, http.MethodPut, "/v1/exams/"+examID, examBody("Changed"), admin, http.StatusOK)
	mustStatus(t, c, http.MethodPost, "/v1/courses/"+courseID+"/publish", "", admin, http.StatusNoContent)
	list = listQuizzes(student)
	if prompt := firstPrompt(list); prompt != "3+3?" {
		t.Fatalf("student sees %q after republish", prompt)
	}
	attemptID := started["id"].(string)
	_, submitted := c.do(http.MethodPost, "/v1/exam-attempts/"+attemptID+"/submit", "", student)
	if got := submitted["revision"]; got != startedRevision {
		t.Fatalf("graded against revision %v, want %v", got, startedRevision)
	}

	// 3. Delete the quiz, discard the draft: the quiz is back for the manager.
	mustStatus(t, c, http.MethodDelete, "/v1/quizzes/"+quizID, "", admin, http.StatusNoContent)
	mustStatus(t, c, http.MethodPost, "/v1/courses/"+courseID+"/discard-draft", "", admin, http.StatusOK)
	waitFor(t, func() bool { // the restore runs through the outbox
		mine := listQuizzes(admin)
		return firstPrompt(mine) == "3+3?"
	})
}

func TestManualPaymentEndToEnd(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pool := pgtest.New(t)
	google := googletest.NewIssuer(t)

	type sentEmail struct{ key, recipient, subject string }
	sent := make(chan sentEmail, 8)
	notify := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Recipient string `json:"recipient"`
			Subject   string `json:"subject"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		sent <- sentEmail{key: r.Header.Get("Idempotency-Key"), recipient: body.Recipient, subject: body.Subject}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer notify.Close()
	cfg := baseConfig(t, google)
	cfg.NotificationServiceBaseURL = notify.URL
	cfg.NotificationServiceSendAPIKey = notifyKey
	a, err := buildApp(ctx, cfg, slog.New(slog.NewJSONHandler(io.Discard, nil)), pool)
	if err != nil {
		t.Fatal(err)
	}
	defer a.forwarder.Close()
	go func() { _ = a.forwarder.Run(ctx) }()
	srv := httptest.NewServer(a.handler)
	defer srv.Close()
	c := client{t: t, base: srv.URL}
	signIn := func(sub, email string) map[string]string {
		t.Helper()
		tok := google.Sign(t, googletest.Claims(sub, email, "web-client", time.Now()))
		resp, body := c.do(http.MethodPost, "/v1/auth/google", `{"id_token":"`+tok+`"}`, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("sign in %s: %d %v", email, resp.StatusCode, body)
		}
		return map[string]string{"Authorization": "Bearer " + body["access_token"].(string)}
	}
	admin := signIn("sub-admin", "admin@example.com")
	student := signIn("sub-student", "student@example.com")
	_, me := c.do(http.MethodGet, "/v1/me", "", student)
	studentID := me["id"].(string)

	resp, body := c.do(http.MethodPost, "/v1/courses", `{"title":"Go","description":"d"}`, admin)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d %v", resp.StatusCode, body)
	}
	courseID := body["id"].(string)
	if resp, body = c.do(http.MethodPost, "/v1/courses/"+courseID+"/price", `{"amount_minor":150000,"currency":"NPR"}`, admin); resp.StatusCode != http.StatusOK {
		t.Fatalf("price: %d %v", resp.StatusCode, body)
	}
	resp, body = c.do(http.MethodPost, "/v1/courses/"+courseID+"/lectures", `{"title":"L1","text_body":"<p>x</p>"}`, admin)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("lecture: %d %v", resp.StatusCode, body)
	}
	lectures := body["lectures"].([]any)
	lectureID := lectures[len(lectures)-1].(map[string]any)["id"].(string)
	if resp, _ = c.do(http.MethodPost, "/v1/courses/"+courseID+"/publish", "", admin); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("publish: %d", resp.StatusCode)
	}

	record := `{"course_id":"` + courseID + `","amount_minor":120000,"currency":"NPR","method":"cash","reference":"R-77","note":"desk"}`
	if resp, body = c.do(http.MethodPost, "/v1/users/"+studentID+"/purchases", record, student); resp.StatusCode != http.StatusForbidden || body["type"] != "forbidden" {
		t.Fatalf("student record: %d %v", resp.StatusCode, body)
	}
	resp, body = c.do(http.MethodPost, "/v1/users/"+studentID+"/purchases", record, admin)
	if p := body["purchase"].(map[string]any); resp.StatusCode != http.StatusCreated || p["status"] != "paid" || p["granted"] != true ||
		p["manual_method"] != "cash" || p["reference"] != "R-77" || p["course_title"] != "Go" {
		t.Fatalf("record: %d %v", resp.StatusCode, body)
	}
	if resp, body = c.do(http.MethodPost, "/v1/users/"+studentID+"/purchases", record, admin); resp.StatusCode != http.StatusConflict || body["type"] != "already_purchased" {
		t.Fatalf("second record: %d %v", resp.StatusCode, body)
	}
	if resp, _ = c.do(http.MethodGet, "/v1/courses/"+courseID+"/lectures/"+lectureID+"/content", "", student); resp.StatusCode != http.StatusOK {
		t.Fatalf("read after manual payment: %d", resp.StatusCode)
	}

	resp, body = c.do(http.MethodGet, "/v1/me/purchases", "", student)
	items, _ := body["items"].([]any)
	if resp.StatusCode != http.StatusOK || len(items) != 1 || items[0].(map[string]any)["amount_minor"] != float64(120000) {
		t.Fatalf("my purchases: %d %v", resp.StatusCode, body)
	}
	if resp, body = c.do(http.MethodGet, "/v1/users/"+studentID+"/purchases", "", admin); resp.StatusCode != http.StatusOK || len(body["items"].([]any)) != 1 {
		t.Fatalf("admin list: %d %v", resp.StatusCode, body)
	}
	_, adminMe := c.do(http.MethodGet, "/v1/me", "", admin)
	if resp, _ = c.do(http.MethodGet, "/v1/users/"+adminMe["id"].(string)+"/purchases", "", student); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("student reads admin list: %d", resp.StatusCode)
	}

	purchaseID := body["items"].([]any)[0].(map[string]any)["id"].(string)
	refund := `{"reference":"RF-77","note":"duplicate"}`
	if resp, body = c.do(http.MethodPost, "/v1/purchases/"+purchaseID+"/refund", refund, student); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("student refund: %d %v", resp.StatusCode, body)
	}
	resp, body = c.do(http.MethodPost, "/v1/purchases/"+purchaseID+"/refund", refund, admin)
	if p := body["purchase"].(map[string]any); resp.StatusCode != http.StatusOK || p["status"] != "refunded" ||
		p["refund_reference"] != "RF-77" || p["revoke_pending"] != false {
		t.Fatalf("refund: %d %v", resp.StatusCode, body)
	}
	if resp, body = c.do(http.MethodPost, "/v1/purchases/"+purchaseID+"/refund", refund, admin); resp.StatusCode != http.StatusConflict || body["type"] != "not_refundable" {
		t.Fatalf("second refund: %d %v", resp.StatusCode, body)
	}
	if resp, body = c.do(http.MethodGet, "/v1/courses/"+courseID+"/lectures/"+lectureID+"/content", "", student); resp.StatusCode != http.StatusForbidden || body["type"] != "enrollment_required" {
		t.Fatalf("read after refund: %d %v", resp.StatusCode, body)
	}
	if resp, body = c.do(http.MethodPost, "/v1/purchases/"+purchaseID+"/confirm", "", student); resp.StatusCode != http.StatusOK {
		t.Fatalf("confirm refunded manual purchase: %d %v", resp.StatusCode, body)
	}

	deadline := time.After(30 * time.Second)
	var paidSeen, refundedSeen bool
	for !paidSeen || !refundedSeen {
		select {
		case got := <-sent:
			switch {
			case strings.HasPrefix(got.key, "ioe:payment.purchase.paid:"):
				if got.recipient != "student@example.com" || got.subject != "Payment received: Go" || !strings.HasSuffix(got.key, ":paid-v1") {
					t.Fatalf("paid email %+v", got)
				}
				paidSeen = true
			case strings.HasPrefix(got.key, "ioe:payment.purchase.refunded:"):
				if got.recipient != "student@example.com" || got.subject != "Refund issued: Go" || !strings.HasSuffix(got.key, ":refunded-v1") {
					t.Fatalf("refund email %+v", got)
				}
				refundedSeen = true
			}
		case <-deadline:
			t.Fatalf("emails not enqueued: paid=%v refunded=%v", paidSeen, refundedSeen)
		}
	}
}

func TestBlogEndToEnd(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pool := pgtest.New(t)
	google := googletest.NewIssuer(t)
	a, err := buildApp(ctx, baseConfig(t, google), slog.New(slog.NewJSONHandler(io.Discard, nil)), pool)
	if err != nil {
		t.Fatal(err)
	}
	defer a.forwarder.Close()
	srv := httptest.NewServer(a.handler)
	defer srv.Close()
	c := client{t: t, base: srv.URL}
	bearer := func(tok string) map[string]string { return map[string]string{"Authorization": "Bearer " + tok} }
	signIn := func(sub, email string) string {
		t.Helper()
		tok := google.Sign(t, googletest.Claims(sub, email, "web-client", time.Now()))
		resp, body := c.do(http.MethodPost, "/v1/auth/google", `{"id_token":"`+tok+`"}`, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("sign in %s: %d %v", email, resp.StatusCode, body)
		}
		return body["access_token"].(string)
	}
	admin := bearer(signIn("sub-admin", "admin@example.com"))
	student := bearer(signIn("sub-student", "student@example.com"))

	mustStatus(t, c, http.MethodPost, "/v1/blog/posts", `{"title":"Nope"}`, student, http.StatusForbidden)
	post := mustStatus(t, c, http.MethodPost, "/v1/blog/posts", `{"title":"Hello Blog","tags":["Go"]}`, admin, http.StatusCreated)
	postID := post["id"].(string)
	mustStatus(t, c, http.MethodPost, "/v1/blog/posts/"+postID+"/publish", "", admin, http.StatusBadRequest) // empty
	mustStatus(t, c, http.MethodPut, "/v1/blog/posts/"+postID+"/content",
		`{"blocks":[{"client_block_id":"b1","type":"text","body":"<p>hello readers</p>"}]}`, admin, http.StatusOK)
	live := mustStatus(t, c, http.MethodPost, "/v1/blog/posts/"+postID+"/publish", "", admin, http.StatusOK)
	if live["live_version"] != float64(1) {
		t.Fatalf("publish %v", live)
	}
	mustStatus(t, c, http.MethodPut, "/v1/blog/posts/"+postID+"/slug", `{"slug":"hello-again"}`, admin, http.StatusOK)

	got := mustStatus(t, c, http.MethodGet, "/v1/blog/public/posts/hello-blog", "", nil, http.StatusOK)
	if got["slug"] != "hello-again" || got["requested_slug"] != "hello-blog" || got["author_name"] != "Test User" {
		t.Fatalf("public get %v", got)
	}
	list := mustStatus(t, c, http.MethodGet, "/v1/blog/public/posts?tag=go", "", nil, http.StatusOK)
	if items := list["items"].([]any); len(items) != 1 {
		t.Fatalf("list %v", list)
	}
	mustStatus(t, c, http.MethodGet, "/v1/blog/public/index", "", nil, http.StatusOK)
	mustStatus(t, c, http.MethodGet, "/v1/blog/public/tags", "", nil, http.StatusOK)
	mustStatus(t, c, http.MethodPost, "/v1/blog/posts/"+postID+"/unpublish", "", admin, http.StatusOK)
	mustStatus(t, c, http.MethodGet, "/v1/blog/public/posts/hello-again", "", nil, http.StatusNotFound)
}

func TestCatalogSearchEndToEnd(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pool := pgtest.New(t)
	google := googletest.NewIssuer(t)
	a, err := buildApp(ctx, baseConfig(t, google), slog.New(slog.NewJSONHandler(io.Discard, nil)), pool)
	if err != nil {
		t.Fatal(err)
	}
	defer a.forwarder.Close()
	srv := httptest.NewServer(a.handler)
	defer srv.Close()
	c := client{t: t, base: srv.URL}
	bearer := func(tok string) map[string]string { return map[string]string{"Authorization": "Bearer " + tok} }
	signIn := func(sub, email string) string {
		t.Helper()
		tok := google.Sign(t, googletest.Claims(sub, email, "web-client", time.Now()))
		resp, body := c.do(http.MethodPost, "/v1/auth/google", `{"id_token":"`+tok+`"}`, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("sign in %s: %d %v", email, resp.StatusCode, body)
		}
		return body["access_token"].(string)
	}
	admin := bearer(signIn("sub-admin", "admin@example.com"))
	student := bearer(signIn("sub-student", "student@example.com"))

	mustStatus(t, c, http.MethodPost, "/v1/categories", `{"name":"Web"}`, student, http.StatusForbidden)
	web := mustStatus(t, c, http.MethodPost, "/v1/categories", `{"name":"Web Development"}`, admin, http.StatusCreated)
	course := mustStatus(t, c, http.MethodPost, "/v1/courses", `{"title":"Learning Go","description":"a gentle start"}`, admin, http.StatusCreated)
	courseID := course["id"].(string)
	mustStatus(t, c, http.MethodPatch, "/v1/courses/"+courseID,
		`{"title":"Learning Go","description":"a gentle start","category_ids":["`+web["id"].(string)+`"],"tags":["golang","Backend"]}`,
		admin, http.StatusNoContent)
	mustStatus(t, c, http.MethodPost, "/v1/courses/"+courseID+"/lectures", `{"title":"L1","text_body":"<p>x</p>"}`, admin, http.StatusCreated)
	mustStatus(t, c, http.MethodPost, "/v1/courses/"+courseID+"/publish", "", admin, http.StatusNoContent)

	for _, qs := range []string{"q=learn", "q=gentle", "q=golang", "category=web-development", "tag=backend", "q=go&tag=golang"} {
		page := mustStatus(t, c, http.MethodGet, "/v1/courses?"+qs, "", nil, http.StatusOK)
		courses := page["courses"].([]any)
		if len(courses) != 1 || courses[0].(map[string]any)["id"] != courseID {
			t.Fatalf("%s = %v", qs, page)
		}
	}
	if page := mustStatus(t, c, http.MethodGet, "/v1/courses?q=rust", "", nil, http.StatusOK); len(page["courses"].([]any)) != 0 {
		t.Fatalf("rust = %v", page)
	}
	list := mustStatus(t, c, http.MethodGet, "/v1/categories", "", nil, http.StatusOK)["categories"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["course_count"] != float64(1) {
		t.Fatalf("categories = %v", list)
	}
	mustStatus(t, c, http.MethodDelete, "/v1/categories/"+web["id"].(string), "", admin, http.StatusNoContent)
	detail := mustStatus(t, c, http.MethodGet, "/v1/courses/"+courseID, "", nil, http.StatusOK)
	if len(detail["categories"].([]any)) != 0 || len(detail["tags"].([]any)) != 2 {
		t.Fatalf("detail after delete = %v", detail)
	}
}

func TestCertificateEndToEnd(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pool := pgtest.New(t)
	google := googletest.NewIssuer(t)
	a, err := buildApp(ctx, baseConfig(t, google), slog.New(slog.NewJSONHandler(io.Discard, nil)), pool)
	if err != nil {
		t.Fatal(err)
	}
	defer a.forwarder.Close()
	go func() { _ = a.forwarder.Run(ctx) }()
	srv := httptest.NewServer(a.handler)
	defer srv.Close()
	c := client{t: t, base: srv.URL}
	signIn := func(sub, email string) map[string]string {
		t.Helper()
		tok := google.Sign(t, googletest.Claims(sub, email, "web-client", time.Now()))
		resp, body := c.do(http.MethodPost, "/v1/auth/google", `{"id_token":"`+tok+`"}`, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("sign in %s: %d %v", email, resp.StatusCode, body)
		}
		return map[string]string{"Authorization": "Bearer " + body["access_token"].(string)}
	}
	admin := signIn("sub-admin", "admin@example.com")
	student := signIn("sub-student", "student@example.com")
	_, me := c.do(http.MethodGet, "/v1/me", "", student)
	studentID := me["id"].(string)

	// A priced, published course with one lecture.
	courseID := mustStatus(t, c, http.MethodPost, "/v1/courses", `{"title":"Go","description":"d"}`, admin, http.StatusCreated)["id"].(string)
	mustStatus(t, c, http.MethodPost, "/v1/courses/"+courseID+"/price", `{"amount_minor":150000,"currency":"NPR"}`, admin, http.StatusOK)
	lectures := mustStatus(t, c, http.MethodPost, "/v1/courses/"+courseID+"/lectures", `{"title":"L1","text_body":"<p>x</p>"}`, admin, http.StatusCreated)["lectures"].([]any)
	lectureID := lectures[len(lectures)-1].(map[string]any)["id"].(string)
	mustStatus(t, c, http.MethodPost, "/v1/courses/"+courseID+"/publish", "", admin, http.StatusNoContent)

	claim := func(who map[string]string) (int, map[string]any) {
		resp, body := c.do(http.MethodPost, "/v1/courses/"+courseID+"/certificate", "", who)
		return resp.StatusCode, body
	}
	refused := func(who map[string]string, typ string) {
		t.Helper()
		if code, body := claim(who); code != http.StatusConflict || body["type"] != typ {
			t.Fatalf("claim: %d %v, want 409 %s", code, body, typ)
		}
	}

	refused(student, "certificates_disabled")
	mustStatus(t, c, http.MethodPut, "/v1/courses/"+courseID+"/certificate-policy", `{"mode":"completion"}`, student, http.StatusForbidden)
	mustStatus(t, c, http.MethodPut, "/v1/courses/"+courseID+"/certificate-policy", `{"mode":"completion_and_exam"}`, admin, http.StatusBadRequest)
	policy := mustStatus(t, c, http.MethodPut, "/v1/courses/"+courseID+"/certificate-policy", `{"mode":"completion"}`, admin, http.StatusOK)
	if policy["mode"] != "completion" {
		t.Fatalf("policy = %v", policy)
	}

	refused(student, "not_enrolled")
	record := `{"course_id":"` + courseID + `","amount_minor":150000,"currency":"NPR","method":"cash","reference":"R-1","note":"desk"}`
	mustStatus(t, c, http.MethodPost, "/v1/users/"+studentID+"/purchases", record, admin, http.StatusCreated)
	refused(student, "progress_incomplete")
	mustStatus(t, c, http.MethodPut, "/v1/courses/"+courseID+"/lectures/"+lectureID+"/progress/"+studentID,
		`{"state":"completed","position_ms":0}`, student, http.StatusNoContent)

	code, cert := claim(student)
	if code != http.StatusCreated || cert["status"] != "valid" || cert["course_title"] != "Go" || cert["student_name"] == "" {
		t.Fatalf("claim: %d %v", code, cert)
	}
	certCode := cert["code"].(string)
	if code, again := claim(student); code != http.StatusOK || again["code"] != certCode {
		t.Fatalf("second claim: %d %v", code, again)
	}

	// Verification needs no credentials and exposes no ids.
	verify := mustStatus(t, c, http.MethodGet, "/v1/certificates/"+certCode, "", nil, http.StatusOK)
	if verify["status"] != "valid" || verify["course_title"] != "Go" || len(verify) != 5 {
		t.Fatalf("verify = %v", verify)
	}
	mustStatus(t, c, http.MethodGet, "/v1/certificates/"+strings.Repeat("A", 26), "", nil, http.StatusNotFound)
	mustStatus(t, c, http.MethodGet, "/v1/certificates/not-a-code", "", nil, http.StatusNotFound)
	if items := mustStatus(t, c, http.MethodGet, "/v1/me/certificates", "", student, http.StatusOK)["items"].([]any); len(items) != 1 {
		t.Fatalf("my certificates = %v", items)
	}
	mustStatus(t, c, http.MethodGet, "/v1/courses/"+courseID+"/certificate", "", student, http.StatusOK)

	// A refund that ends access revokes the certificate through the outbox.
	purchases := mustStatus(t, c, http.MethodGet, "/v1/users/"+studentID+"/purchases", "", admin, http.StatusOK)["items"].([]any)
	purchaseID := purchases[0].(map[string]any)["id"].(string)
	mustStatus(t, c, http.MethodPost, "/v1/purchases/"+purchaseID+"/refund", `{"reference":"RF-1","note":"duplicate"}`, admin, http.StatusOK)
	waitFor(t, func() bool {
		_, body := c.do(http.MethodGet, "/v1/certificates/"+certCode, "", nil)
		return body["status"] == "revoked"
	})
	mustStatus(t, c, http.MethodGet, "/v1/courses/"+courseID+"/certificate", "", student, http.StatusNotFound)
	refused(student, "not_enrolled")
	if items := mustStatus(t, c, http.MethodGet, "/v1/me/certificates", "", student, http.StatusOK)["items"].([]any); len(items) != 1 ||
		items[0].(map[string]any)["status"] != "revoked" {
		t.Fatalf("my certificates after refund = %v", items)
	}
}
