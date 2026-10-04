package notifysvc_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/santoshkc2200/ioe-backend/internal/notification/adapters/notifysvc"
	"github.com/santoshkc2200/ioe-backend/internal/notification/app"
)

const apiKey = "test-send-key"

var _ app.Mailer = (*notifysvc.Client)(nil)

type captured struct {
	method, path, auth, key, contentType string
	body                                 map[string]any
}

func newServer(t *testing.T, status int, contentType, body string) (*httptest.Server, *captured) {
	t.Helper()
	c := &captured{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.method, c.path = r.Method, r.URL.Path
		c.auth, c.key, c.contentType = r.Header.Get("Authorization"), r.Header.Get("Idempotency-Key"), r.Header.Get("Content-Type")
		_ = json.NewDecoder(r.Body).Decode(&c.body)
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("X-Request-ID", "req-1")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, c
}

var email = app.Email{To: "a@example.com", Subject: "Welcome to IOE", Text: "Hello,", HTML: "<p>Hello,</p>"}

func TestEnqueueSendsContractRequest(t *testing.T) {
	srv, c := newServer(t, http.StatusAccepted, "application/json", `{"notification":{"id":"x"},"idempotent_replay":false}`)
	if err := notifysvc.New(srv.URL, apiKey).Enqueue(context.Background(), email, "key-1"); err != nil {
		t.Fatal(err)
	}
	if c.method != http.MethodPost || c.path != "/v1/notifications" {
		t.Fatalf("%s %s", c.method, c.path)
	}
	if c.auth != "Bearer "+apiKey || c.key != "key-1" || c.contentType != "application/json" {
		t.Fatalf("headers auth=%q key=%q type=%q", c.auth, c.key, c.contentType)
	}
	want := map[string]any{"recipient": "a@example.com", "subject": "Welcome to IOE", "text_body": "Hello,", "html_body": "<p>Hello,</p>"}
	if len(c.body) != len(want) {
		t.Fatalf("body %v", c.body)
	}
	for k, v := range want {
		if c.body[k] != v {
			t.Fatalf("body[%s] = %v, want %v", k, c.body[k], v)
		}
	}
}

func TestEnqueueOmitsEmptyHTML(t *testing.T) {
	srv, c := newServer(t, http.StatusAccepted, "application/json", `{}`)
	plain := email
	plain.HTML = ""
	if err := notifysvc.New(srv.URL, apiKey).Enqueue(context.Background(), plain, "key-1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.body["html_body"]; ok {
		t.Fatalf("html_body sent: %v", c.body)
	}
}

func TestEnqueueJoinsBaseURLWithPathPrefix(t *testing.T) {
	srv, c := newServer(t, http.StatusAccepted, "application/json", `{}`)
	if err := notifysvc.New(srv.URL+"/notify/", apiKey).Enqueue(context.Background(), email, "key-1"); err != nil {
		t.Fatal(err)
	}
	if c.path != "/notify/v1/notifications" {
		t.Fatalf("path %q", c.path)
	}
}

func TestEnqueueMapsStatuses(t *testing.T) {
	cases := []struct {
		status          int
		contentType     string
		body            string
		wantPermanent   bool
		wantErrContains string
	}{
		{http.StatusConflict, "application/problem+json", `{"type":"idempotency-conflict"}`, true, "idempotency-conflict"},
		{http.StatusBadRequest, "application/problem+json", `{"type":"validation"}`, true, "validation"},
		{http.StatusUnauthorized, "application/problem+json", `{"type":"unauthorized"}`, true, "unauthorized"},
		{http.StatusTooManyRequests, "application/problem+json", `{"type":"rate-limited"}`, false, "429"},
		{http.StatusInternalServerError, "application/problem+json", `{"type":"internal"}`, false, "500"},
		{http.StatusBadGateway, "text/html", `<html><body>Bad Gateway</body></html>`, false, "502"},
	}
	for _, tc := range cases {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			srv, _ := newServer(t, tc.status, tc.contentType, tc.body)
			err := notifysvc.New(srv.URL, apiKey).Enqueue(context.Background(), email, "key-1")
			if err == nil {
				t.Fatal("expected error")
			}
			if errors.Is(err, app.ErrPermanent) != tc.wantPermanent {
				t.Fatalf("permanent = %v, want %v (%v)", !tc.wantPermanent, tc.wantPermanent, err)
			}
			msg := err.Error()
			if !strings.Contains(msg, tc.wantErrContains) || !strings.Contains(msg, "req-1") {
				t.Fatalf("error %q lacks %q or request id", msg, tc.wantErrContains)
			}
			if strings.Contains(msg, apiKey) || strings.Contains(msg, email.To) || strings.Contains(msg, "Bad Gateway") {
				t.Fatalf("error leaks secret, recipient, or body: %q", msg)
			}
		})
	}
}

func TestEnqueueNetworkErrorIsRetryable(t *testing.T) {
	srv, _ := newServer(t, http.StatusAccepted, "application/json", `{}`)
	url := srv.URL
	srv.Close()
	err := notifysvc.New(url, apiKey).Enqueue(context.Background(), email, "key-1")
	if err == nil || errors.Is(err, app.ErrPermanent) {
		t.Fatalf("err = %v, want retryable error", err)
	}
	if strings.Contains(err.Error(), apiKey) {
		t.Fatalf("error leaks key: %v", err)
	}
}
