package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/santoshkc2200/ioe-backend/internal/identity/adapters/httpapi"
	"github.com/santoshkc2200/ioe-backend/internal/identity/app"
	"github.com/santoshkc2200/ioe-backend/internal/identity/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/httpserver"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

const origin = "https://app.example.com"

var (
	user = domain.User{ID: id.ID(1840396745219883008), Email: "a@example.com",
		Name: "A", AvatarURL: "https://img/a", Role: auth.RoleStudent}
	session = app.Session{AccessToken: "acc", AccessTokenTTL: 15 * time.Minute, RefreshToken: "ref-1",
		RefreshTokenTTL: 7 * 24 * time.Hour, User: user, Created: true}
	discard        = slog.New(slog.NewJSONHandler(io.Discard, nil))
	adminPrincipal = auth.Principal{UserID: id.ID(7), Role: auth.RoleRootAdmin}
	adminHeaders   = map[string]string{"Authorization": "Bearer admin", "Content-Type": "application/json"}
)

type fakeService struct {
	signInErr    error
	signInSess   *app.Session
	refreshErr   error
	gotRefresh   string
	gotLogout    string
	gotClient    app.Client
	adminErr     error
	searchResult []domain.User
	gotPrincipal auth.Principal
	gotPrefix    string
	gotUserID    id.ID
	gotRole      auth.Role
}

func (f *fakeService) SignInWithGoogle(_ context.Context, _ string, c app.Client) (app.Session, error) {
	f.gotClient = c
	if f.signInErr != nil {
		return app.Session{}, f.signInErr
	}
	if f.signInSess != nil {
		return *f.signInSess, nil
	}
	return session, nil
}

func (f *fakeService) Refresh(_ context.Context, raw string, _ app.Client) (app.Session, error) {
	f.gotRefresh = raw
	if f.refreshErr != nil {
		return app.Session{}, f.refreshErr
	}
	s := session
	s.RefreshToken, s.Created = "ref-2", false
	return s, nil
}

func (f *fakeService) Logout(_ context.Context, raw string) error {
	f.gotLogout = raw
	return nil
}

func (f *fakeService) GetMe(_ context.Context, uid id.ID) (domain.User, error) {
	if uid != user.ID {
		return domain.User{}, app.ErrNotFound
	}
	return user, nil
}

func (f *fakeService) SearchUsers(_ context.Context, p auth.Principal, prefix string) ([]domain.User, error) {
	f.gotPrincipal, f.gotPrefix = p, prefix
	return f.searchResult, f.adminErr
}

func (f *fakeService) SetRole(_ context.Context, p auth.Principal, userID id.ID, role auth.Role) (domain.User, error) {
	f.gotPrincipal, f.gotUserID, f.gotRole = p, userID, role
	if f.adminErr != nil {
		return domain.User{}, f.adminErr
	}
	u := user
	u.Role = role
	return u, nil
}

type fakeVerifier struct{}

func (fakeVerifier) Verify(tok string) (auth.Principal, error) {
	switch tok {
	case "good":
		return auth.Principal{UserID: user.ID, Role: user.Role}, nil
	case "admin":
		return adminPrincipal, nil
	default:
		return auth.Principal{}, app.ErrInvalidToken
	}
}

func newHandler(t *testing.T, svc *fakeService, secure bool, perMinute int) http.Handler {
	t.Helper()
	r, h := httpserver.NewRouter(httpserver.Options{Logger: discard, AllowedOrigins: []string{origin}, ServiceName: "test"})
	ih, err := httpapi.New(svc, svc, fakeVerifier{}, httpapi.Config{
		CookieSecure: secure, AllowedOrigins: []string{origin}, Logger: discard,
		IPs: httpserver.NewIPResolver(nil), AuthLimiter: httpserver.NewRateLimiter(perMinute),
	})
	if err != nil {
		t.Fatal(err)
	}
	ih.Register(r)
	return h
}

func do(h http.Handler, method, path, body string, headers map[string]string) *http.Response {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w.Result()
}

func problemType(t *testing.T, resp *http.Response) string {
	t.Helper()
	if ct := resp.Header.Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("content-type %q", ct)
	}
	var p struct{ Type string }
	if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
		t.Fatal(err)
	}
	return p.Type
}

func cookie(resp *http.Response, name string) *http.Cookie {
	for _, c := range resp.Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

var jsonHeader = map[string]string{"Content-Type": "application/json"}

func TestSignInSuccess(t *testing.T) {
	svc := &fakeService{}
	resp := do(newHandler(t, svc, true, 100), http.MethodPost, "/v1/auth/google", `{"id_token":"x"}`,
		map[string]string{"Content-Type": "application/json", "User-Agent": "ua-1"})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("%d %v", resp.StatusCode, resp.Header)
	}
	var body struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		ExpiresIn   int    `json:"expires_in"`
		User        struct {
			ID, Email, Name, Role string
			AvatarURL             string `json:"avatar_url"`
		} `json:"user"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.AccessToken != "acc" || body.TokenType != "Bearer" || body.ExpiresIn != 900 ||
		body.User.ID != user.ID.String() || body.User.Role != "student" || body.User.AvatarURL != "https://img/a" {
		t.Fatalf("%+v", body)
	}
	c := cookie(resp, "__Secure-ioe_refresh")
	if c == nil || c.Value != "ref-1" || !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteStrictMode ||
		c.Path != "/v1/auth" || c.MaxAge != 7*24*3600 {
		t.Fatalf("cookie %+v", c)
	}
	if svc.gotClient.UserAgent != "ua-1" || svc.gotClient.IP != "192.0.2.1" {
		t.Fatalf("client %+v", svc.gotClient)
	}
}

func TestSignInInsecureCookieForLocalDevelopment(t *testing.T) {
	resp := do(newHandler(t, &fakeService{}, false, 100), http.MethodPost, "/v1/auth/google", `{"id_token":"x"}`, jsonHeader)
	defer resp.Body.Close()
	c := cookie(resp, "ioe_refresh")
	if c == nil || c.Secure || !c.HttpOnly {
		t.Fatalf("cookie %+v", resp.Cookies())
	}
}

func TestSignInRejectsNonJSONContentType(t *testing.T) {
	resp := do(newHandler(t, &fakeService{}, true, 100), http.MethodPost, "/v1/auth/google", `{"id_token":"x"}`,
		map[string]string{"Content-Type": "text/plain"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnsupportedMediaType || problemType(t, resp) != "unsupported_media_type" {
		t.Fatalf("%d", resp.StatusCode)
	}
}

func TestSignInRequestValidation(t *testing.T) {
	for _, body := range []string{`{}`, `{"id_token":""}`, `{"id_token":"x","extra":1}`, `not json`} {
		resp := do(newHandler(t, &fakeService{}, true, 100), http.MethodPost, "/v1/auth/google", body, jsonHeader)
		if resp.StatusCode != http.StatusBadRequest || problemType(t, resp) != "invalid_request" {
			t.Fatalf("%q: %d", body, resp.StatusCode)
		}
		resp.Body.Close()
	}
}

func TestSignInErrorMapping(t *testing.T) {
	cases := []struct {
		err    error
		status int
		typ    string
	}{
		{app.ErrInvalidToken, 401, "invalid_token"},
		{app.ErrEmailUnverified, 403, "email_unverified"},
		{errors.New("db down"), 500, "internal"},
	}
	for _, c := range cases {
		resp := do(newHandler(t, &fakeService{signInErr: c.err}, true, 100), http.MethodPost, "/v1/auth/google", `{"id_token":"x"}`, jsonHeader)
		if resp.StatusCode != c.status || problemType(t, resp) != c.typ {
			t.Fatalf("%v: %d", c.err, resp.StatusCode)
		}
		resp.Body.Close()
	}
}

func TestRefreshRequiresAllowedOrigin(t *testing.T) {
	h := newHandler(t, &fakeService{}, true, 100)
	for _, o := range []string{"", "https://evil.example"} {
		resp := do(h, http.MethodPost, "/v1/auth/refresh", "", map[string]string{"Origin": o, "Cookie": "__Secure-ioe_refresh=ref-1"})
		if resp.StatusCode != http.StatusForbidden || problemType(t, resp) != "origin_not_allowed" {
			t.Fatalf("origin %q: %d", o, resp.StatusCode)
		}
		resp.Body.Close()
	}
}

func TestRefreshWithoutCookie(t *testing.T) {
	resp := do(newHandler(t, &fakeService{}, true, 100), http.MethodPost, "/v1/auth/refresh", "", map[string]string{"Origin": origin})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized || problemType(t, resp) != "invalid_token" {
		t.Fatalf("%d", resp.StatusCode)
	}
}

func TestRefreshRotatesCookie(t *testing.T) {
	svc := &fakeService{}
	resp := do(newHandler(t, svc, true, 100), http.MethodPost, "/v1/auth/refresh", "",
		map[string]string{"Origin": origin, "Cookie": "__Secure-ioe_refresh=ref-1"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || svc.gotRefresh != "ref-1" {
		t.Fatalf("%d %q", resp.StatusCode, svc.gotRefresh)
	}
	if c := cookie(resp, "__Secure-ioe_refresh"); c == nil || c.Value != "ref-2" {
		t.Fatalf("cookie %+v", resp.Cookies())
	}
}

func TestRefreshReuseClearsCookie(t *testing.T) {
	resp := do(newHandler(t, &fakeService{refreshErr: app.ErrRefreshReuse}, true, 100), http.MethodPost, "/v1/auth/refresh", "",
		map[string]string{"Origin": origin, "Cookie": "__Secure-ioe_refresh=ref-1"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized || problemType(t, resp) != "refresh_reuse_detected" {
		t.Fatalf("%d", resp.StatusCode)
	}
	if c := cookie(resp, "__Secure-ioe_refresh"); c == nil || c.MaxAge != -1 {
		t.Fatalf("cookie not cleared: %+v", resp.Cookies())
	}
}

func TestLogout(t *testing.T) {
	svc := &fakeService{}
	h := newHandler(t, svc, true, 100)
	resp := do(h, http.MethodPost, "/v1/auth/logout", "", map[string]string{"Origin": origin, "Cookie": "__Secure-ioe_refresh=ref-1"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent || svc.gotLogout != "ref-1" {
		t.Fatalf("%d %q", resp.StatusCode, svc.gotLogout)
	}
	if c := cookie(resp, "__Secure-ioe_refresh"); c == nil || c.MaxAge != -1 {
		t.Fatalf("cookie not cleared")
	}
	resp = do(h, http.MethodPost, "/v1/auth/logout", "", map[string]string{"Origin": origin})
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("logout without cookie: %d", resp.StatusCode)
	}
}

func TestMe(t *testing.T) {
	h := newHandler(t, &fakeService{}, true, 100)
	for _, authz := range []string{"", "Basic good", "Bearer bad", "Bearer"} {
		resp := do(h, http.MethodGet, "/v1/me", "", map[string]string{"Authorization": authz})
		if resp.StatusCode != http.StatusUnauthorized || resp.Header.Get("WWW-Authenticate") == "" {
			t.Fatalf("%q: %d", authz, resp.StatusCode)
		}
		resp.Body.Close()
	}
	resp := do(h, http.MethodGet, "/v1/me", "", map[string]string{"Authorization": "Bearer good"})
	defer resp.Body.Close()
	var body struct{ ID, Email, Role string }
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || body.ID != user.ID.String() || body.Role != "student" ||
		resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("%d %+v", resp.StatusCode, body)
	}
}

func TestAuthRoutesRateLimited(t *testing.T) {
	h := newHandler(t, &fakeService{}, true, 2)
	var last *http.Response
	for range 3 {
		last = do(h, http.MethodPost, "/v1/auth/google", `{"id_token":"x"}`, jsonHeader)
		last.Body.Close()
	}
	if last.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("third request %d", last.StatusCode)
	}
}

func TestAuthRouteMethodNotAllowed(t *testing.T) {
	h := newHandler(t, &fakeService{}, true, 100)
	resp := do(h, http.MethodGet, "/v1/auth/google", "", nil)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("code = %d, want 405", resp.StatusCode)
	}
	if pType := problemType(t, resp); pType != "method_not_allowed" {
		t.Fatalf("problem type = %q, want method_not_allowed", pType)
	}
	if allow := resp.Header.Get("Allow"); allow != "POST" {
		t.Fatalf("allow = %q, want POST", allow)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("cache-control = %q, want no-store", cc)
	}
}

func TestAuthRouteMethodNotAllowedRateLimited(t *testing.T) {
	h := newHandler(t, &fakeService{}, true, 2)
	var last *http.Response
	for range 3 {
		last = do(h, http.MethodGet, "/v1/auth/google", "", nil)
		last.Body.Close()
	}
	if last.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("third request %d, want 429", last.StatusCode)
	}
}

func collectSignInMetrics(t *testing.T, reader *sdkmetric.ManualReader) map[string]int64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	counts := make(map[string]int64)
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "identity.signins" {
				continue
			}
			sum := m.Data.(metricdata.Sum[int64])
			for _, dp := range sum.DataPoints {
				if v, ok := dp.Attributes.Value(attribute.Key("result")); ok {
					counts[v.AsString()] += dp.Value
				}
			}
		}
	}
	return counts
}

func TestSignInRecordsMetric(t *testing.T) {
	cases := []struct {
		name       string
		service    *fakeService
		body       string
		headers    map[string]string
		wantResult string
	}{
		{
			name:       "created",
			service:    &fakeService{},
			body:       `{"id_token":"x"}`,
			headers:    jsonHeader,
			wantResult: "created",
		},
		{
			name: "existing",
			service: &fakeService{
				signInErr: nil,
			},
			body:       `{"id_token":"x"}`,
			headers:    jsonHeader,
			wantResult: "existing",
		},
		{
			name:       "rejected invalid token",
			service:    &fakeService{signInErr: app.ErrInvalidToken},
			body:       `{"id_token":"x"}`,
			headers:    jsonHeader,
			wantResult: "rejected",
		},
		{
			name:       "rejected unverified email",
			service:    &fakeService{signInErr: app.ErrEmailUnverified},
			body:       `{"id_token":"x"}`,
			headers:    jsonHeader,
			wantResult: "rejected",
		},
		{
			name:       "invalid bad json",
			service:    &fakeService{},
			body:       `{"not json`,
			headers:    jsonHeader,
			wantResult: "invalid",
		},
		{
			name:       "invalid missing id_token",
			service:    &fakeService{},
			body:       `{"id_token":""}`,
			headers:    jsonHeader,
			wantResult: "invalid",
		},
		{
			name:       "invalid unsupported media type",
			service:    &fakeService{},
			body:       `{"id_token":"x"}`,
			headers:    map[string]string{"Content-Type": "text/plain"},
			wantResult: "invalid",
		},
		{
			name:       "error internal unexpected",
			service:    &fakeService{signInErr: errors.New("db explosion")},
			body:       `{"id_token":"x"}`,
			headers:    jsonHeader,
			wantResult: "error",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reader := sdkmetric.NewManualReader()
			prev := otel.GetMeterProvider()
			otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
			defer otel.SetMeterProvider(prev)

			svc := tc.service
			if tc.wantResult == "existing" {
				s := session
				s.Created = false
				svc = &fakeService{signInSess: &s}
			}

			resp := do(newHandler(t, svc, true, 100), http.MethodPost, "/v1/auth/google", tc.body, tc.headers)
			resp.Body.Close()

			counts := collectSignInMetrics(t, reader)
			if got := counts[tc.wantResult]; got != 1 {
				t.Fatalf("result=%q count=%d, want 1; all counts=%v", tc.wantResult, got, counts)
			}
		})
	}
}

const rolePath = "/v1/admin/users/1840396745219883008/role"

func TestSearchUsers(t *testing.T) {
	svc := &fakeService{searchResult: []domain.User{user}}
	resp := do(newHandler(t, svc, true, 100), http.MethodGet, "/v1/admin/users?email=a%40ex", "", adminHeaders)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	want := `{"users":[{"id":"1840396745219883008","email":"a@example.com","name":"A","avatar_url":"https://img/a","role":"student"}]}` + "\n"
	if resp.StatusCode != http.StatusOK || string(b) != want || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
	if svc.gotPrefix != "a@ex" || svc.gotPrincipal != adminPrincipal {
		t.Fatalf("prefix %q principal %+v", svc.gotPrefix, svc.gotPrincipal)
	}
}

func TestSearchUsersNoMatchesIsEmptyArray(t *testing.T) {
	resp := do(newHandler(t, &fakeService{}, true, 100), http.MethodGet, "/v1/admin/users?email=zzz", "", adminHeaders)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(b) != "{\"users\":[]}\n" {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
}

func TestSetRole(t *testing.T) {
	svc := &fakeService{}
	resp := do(newHandler(t, svc, true, 100), http.MethodPut, rolePath, `{"role":"instructor"}`, adminHeaders)
	defer resp.Body.Close()
	var body struct{ ID, Role string }
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || body.ID != user.ID.String() || body.Role != "instructor" ||
		resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("%d %+v", resp.StatusCode, body)
	}
	if svc.gotUserID != user.ID || svc.gotRole != auth.RoleInstructor || svc.gotPrincipal != adminPrincipal {
		t.Fatalf("got %v %q %+v", svc.gotUserID, svc.gotRole, svc.gotPrincipal)
	}
}

func TestAdminRoutesRequireAuth(t *testing.T) {
	h := newHandler(t, &fakeService{}, true, 100)
	for _, req := range []struct{ method, path, body string }{
		{http.MethodGet, "/v1/admin/users?email=abc", ""},
		{http.MethodPut, rolePath, `{"role":"instructor"}`},
	} {
		resp := do(h, req.method, req.path, req.body, jsonHeader)
		if resp.StatusCode != http.StatusUnauthorized || problemType(t, resp) != "invalid_token" {
			t.Errorf("%s %s: %d", req.method, req.path, resp.StatusCode)
		}
		resp.Body.Close()
	}
}

func TestSetRoleMalformedIDIsNotFound(t *testing.T) {
	svc := &fakeService{}
	resp := do(newHandler(t, svc, true, 100), http.MethodPut, "/v1/admin/users/abc/role", `{"role":"instructor"}`, adminHeaders)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound || problemType(t, resp) != "not_found" || !svc.gotUserID.IsZero() {
		t.Fatalf("%d called=%v", resp.StatusCode, !svc.gotUserID.IsZero())
	}
}

func TestAdminErrorMapping(t *testing.T) {
	cases := []struct {
		err    error
		status int
		typ    string
	}{
		{app.ErrForbidden, http.StatusForbidden, "forbidden"},
		{app.ErrNotFound, http.StatusNotFound, "not_found"},
		{app.ErrInvalidRole, http.StatusBadRequest, "invalid_role"},
		{app.ErrEmailQueryTooShort, http.StatusBadRequest, "email_query_too_short"},
		{app.ErrRoleNotAssignable, http.StatusConflict, "role_not_assignable"},
		{errors.New("boom"), http.StatusInternalServerError, "internal"},
	}
	for _, c := range cases {
		h := newHandler(t, &fakeService{adminErr: c.err}, true, 100)
		for _, req := range []struct{ method, path, body string }{
			{http.MethodGet, "/v1/admin/users?email=abc", ""},
			{http.MethodPut, rolePath, `{"role":"instructor"}`},
		} {
			resp := do(h, req.method, req.path, req.body, adminHeaders)
			if resp.StatusCode != c.status || problemType(t, resp) != c.typ {
				t.Errorf("%v on %s: %d", c.err, req.method, resp.StatusCode)
			}
			resp.Body.Close()
		}
	}
}
