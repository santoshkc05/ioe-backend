package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/payment/adapters/httpapi"
	"github.com/santoshkc2200/ioe-backend/internal/payment/app"
	"github.com/santoshkc2200/ioe-backend/internal/payment/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/httpserver"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
	"github.com/santoshkc2200/ioe-backend/internal/platform/problem"
)

var t0 = time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC)

type stub struct {
	purchase domain.Purchase
	checkout app.Checkout
	err      error

	principal  auth.Principal
	courseID   id.ID
	purchaseID id.ID
	gateway    string
}

func (s *stub) Checkout(_ context.Context, p auth.Principal, courseID id.ID, gateway string) (domain.Purchase, app.Checkout, error) {
	s.principal, s.courseID, s.gateway = p, courseID, gateway
	return s.purchase, s.checkout, s.err
}

func (s *stub) Confirm(_ context.Context, p auth.Principal, purchaseID id.ID) (domain.Purchase, error) {
	s.principal, s.purchaseID = p, purchaseID
	return s.purchase, s.err
}

func (s *stub) Get(_ context.Context, p auth.Principal, purchaseID id.ID) (domain.Purchase, error) {
	s.principal, s.purchaseID = p, purchaseID
	return s.purchase, s.err
}

func fakeAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{UserID: 200, Role: auth.RoleStudent})))
	})
}

func newServer(s *stub) http.Handler {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	r, h := httpserver.NewRouter(httpserver.Options{Logger: logger, AllowedOrigins: []string{"https://app.test"}, ServiceName: "test"})
	httpapi.New(s, httpapi.Config{RequireAuth: fakeAuth, Logger: logger}).Register(r)
	return h
}

func call(h http.Handler, method, path, body string) (int, []byte) {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w.Code, w.Body.Bytes()
}

func decode[T any](t *testing.T, b []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("decode %s: %v", b, err)
	}
	return v
}

var pending = domain.Purchase{ID: 1, UserID: 200, CourseID: 10, Price: domain.Money{AmountMinor: 150000, Currency: "NPR"},
	Gateway: "esewa", GatewayRef: "1", Status: domain.StatusPending, CreatedAt: t0, Version: 1}

func TestCheckoutStatusAndShape(t *testing.T) {
	s := &stub{purchase: pending, checkout: app.Checkout{Method: "POST", URL: "https://pay.test/form", Fields: map[string]string{"signature": "sig"}}}
	code, body := call(newServer(s), "POST", "/v1/courses/10/purchases", `{"gateway":"esewa"}`)
	if code != http.StatusCreated {
		t.Fatalf("code = %d %s", code, body)
	}
	if s.courseID != 10 || s.gateway != "esewa" || s.principal.UserID != 200 {
		t.Fatalf("call = %+v", s)
	}
	got := decode[struct {
		Purchase map[string]any `json:"purchase"`
		Checkout struct {
			Method string            `json:"method"`
			URL    string            `json:"url"`
			Fields map[string]string `json:"fields"`
		} `json:"checkout"`
	}](t, body)
	want := map[string]any{"id": "1", "course_id": "10", "user_id": "200", "amount_minor": float64(150000), "currency": "NPR",
		"gateway": "esewa", "status": "pending", "created_at": "2026-10-07T00:00:00Z", "settled_at": nil, "granted": false}
	if len(got.Purchase) != len(want) {
		t.Fatalf("purchase keys = %v", got.Purchase)
	}
	for k, v := range want {
		if got.Purchase[k] != v {
			t.Fatalf("%s = %v, want %v (body %s)", k, got.Purchase[k], v, body)
		}
	}
	if got.Checkout.Method != "POST" || got.Checkout.URL != "https://pay.test/form" || got.Checkout.Fields["signature"] != "sig" {
		t.Fatalf("checkout = %+v", got.Checkout)
	}
}

func TestConfirmAndGet(t *testing.T) {
	paid := pending
	paid.Status, paid.GatewayTxn, paid.SettledAt, paid.GrantedAt = domain.StatusPaid, "T", t0.Add(time.Minute), t0.Add(time.Minute)
	s := &stub{purchase: paid}
	for _, req := range []struct{ method, path string }{{"POST", "/v1/purchases/1/confirm"}, {"GET", "/v1/purchases/1"}} {
		code, body := call(newServer(s), req.method, req.path, "")
		if code != http.StatusOK || s.purchaseID != 1 {
			t.Fatalf("%s %s: %d %s", req.method, req.path, code, body)
		}
		got := decode[struct {
			Purchase map[string]any `json:"purchase"`
		}](t, body)
		if got.Purchase["status"] != "paid" || got.Purchase["settled_at"] != "2026-10-07T00:01:00Z" || got.Purchase["granted"] != true {
			t.Fatalf("%s: %s", req.path, body)
		}
		if _, ok := got.Purchase["gateway_txn"]; ok {
			t.Fatalf("gateway_txn leaked: %s", body)
		}
	}
}

func TestMalformedIDsAre404(t *testing.T) {
	for _, req := range []struct{ method, path, body string }{
		{"POST", "/v1/courses/abc/purchases", `{"gateway":"esewa"}`},
		{"POST", "/v1/purchases/abc/confirm", ""},
		{"GET", "/v1/purchases/abc", ""},
	} {
		if code, _ := call(newServer(&stub{}), req.method, req.path, req.body); code != http.StatusNotFound {
			t.Fatalf("%s %s: %d", req.method, req.path, code)
		}
	}
}

func TestCheckoutRequiresJSON(t *testing.T) {
	r := httptest.NewRequest("POST", "/v1/courses/10/purchases", strings.NewReader(`gateway=esewa`))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	newServer(&stub{}).ServeHTTP(w, r)
	if w.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("code = %d", w.Code)
	}
	if code, _ := call(newServer(&stub{}), "POST", "/v1/courses/10/purchases", `{"gateway":"esewa","extra":1}`); code != http.StatusBadRequest {
		t.Fatalf("unknown field code = %d", code)
	}
}

func TestErrorMapping(t *testing.T) {
	cases := []struct {
		err    error
		status int
		typ    string
	}{
		{fmt.Errorf("%w: gateway is required", app.ErrInvalidInput), http.StatusBadRequest, "invalid_input"},
		{app.ErrNotFound, http.StatusNotFound, "not_found"},
		{app.ErrCourseFree, http.StatusConflict, "course_free"},
		{app.ErrAlreadyEnrolled, http.StatusConflict, "already_enrolled"},
		{app.ErrAlreadyPurchased, http.StatusConflict, "already_purchased"},
		{fmt.Errorf("%w: timeout", app.ErrGatewayUnavailable), http.StatusServiceUnavailable, "payment_unavailable"},
		{app.ErrConcurrentModification, http.StatusConflict, "concurrent_modification"},
		{errors.New("boom"), http.StatusInternalServerError, problem.TypeInternal},
	}
	for _, c := range cases {
		code, body := call(newServer(&stub{err: c.err}), "POST", "/v1/courses/10/purchases", `{"gateway":"esewa"}`)
		p := decode[problem.Problem](t, body)
		if code != c.status || p.Type != c.typ {
			t.Fatalf("%v: %d %s", c.err, code, body)
		}
		if c.status == http.StatusServiceUnavailable && strings.Contains(string(body), "timeout") {
			t.Fatalf("gateway detail leaked: %s", body)
		}
	}
}
