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
	if code, body := play(courseA, preview, video, stranger); code != http.StatusOK || body["playback_url"] == nil {
		t.Fatalf("free preview playback: %d %v", code, body)
	}
	if code, _ := play(courseA, locked, foreign, student); code != http.StatusNotFound {
		t.Fatalf("foreign asset playback: %d", code)
	}

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
	if code, qs := list(student); code != http.StatusOK || len(qs) != 0 {
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

	// After a submission, wording edits pass and answer-key edits are refused.
	save := func(prompt string, rightCorrect bool) (*http.Response, map[string]any) {
		return c.do(http.MethodPut, "/v1/exams/"+examID, `{"title":"Final","description":"","position":0,"pass_mark":50,`+
			`"time_limit_seconds":null,"retakes_allowed":false,"opens_at":null,"closes_at":null,"reveal_policy":"after_attempt",`+
			`"questions":[{"id":"`+questionID+`","prompt":"`+prompt+`","type":"single_choice","explanation":"arith","points":2,`+
			`"options":[{"id":"`+right+`","label":"4","is_correct":`+strconv.FormatBool(rightCorrect)+`},`+
			`{"id":"`+wrong+`","label":"5","is_correct":`+strconv.FormatBool(!rightCorrect)+`}]}]}`, admin)
	}
	if resp, body = save("What is 2+2?", true); resp.StatusCode != http.StatusOK || body["locks"].(map[string]any)["submitted_attempt_count"] != 1.0 {
		t.Fatalf("reword: %d %v", resp.StatusCode, body)
	}
	if resp, body = save("What is 2+2?", false); resp.StatusCode != http.StatusConflict || body["type"] != "edit_key_frozen" {
		t.Fatalf("key change: %d %v", resp.StatusCode, body)
	}
	if resp, body = c.do(http.MethodDelete, "/v1/exams/"+examID, "", admin); resp.StatusCode != http.StatusConflict || body["type"] != "exam_has_attempts" {
		t.Fatalf("delete: %d %v", resp.StatusCode, body)
	}
	if code, attempts := list("/v1/exams/"+examID+"/attempts", admin); code != http.StatusOK || len(attempts) != 1 || attempts[0]["still_open"] != false {
		t.Fatalf("attempts: %d %v", code, attempts)
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
