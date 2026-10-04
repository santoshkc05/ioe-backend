// Package httpapi exposes identity use cases over HTTP.
package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/santoshkc2200/ioe-backend/internal/identity/app"
	"github.com/santoshkc2200/ioe-backend/internal/identity/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/httpserver"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
	"github.com/santoshkc2200/ioe-backend/internal/platform/problem"
)

const (
	typeInvalidToken    = "invalid_token"
	typeRefreshReuse    = "refresh_reuse_detected"
	typeEmailUnverified = "email_unverified"

	secureCookieName   = "__Secure-ioe_refresh"
	insecureCookieName = "ioe_refresh"
	cookiePath         = "/v1/auth"
)

// SessionService is the identity application service as used by HTTP.
type SessionService interface {
	SignInWithGoogle(ctx context.Context, idToken string, client app.Client) (app.Session, error)
	Refresh(ctx context.Context, raw string, client app.Client) (app.Session, error)
	Logout(ctx context.Context, raw string) error
	GetMe(ctx context.Context, userID id.ID) (domain.User, error)
}

// AccessTokenVerifier validates bearer tokens.
type AccessTokenVerifier interface {
	Verify(token string) (auth.Principal, error)
}

// Config configures the HTTP adapter.
type Config struct {
	CookieSecure   bool
	AllowedOrigins []string
	Logger         *slog.Logger
	IPs            httpserver.IPResolver
	AuthLimiter    *httpserver.RateLimiter
}

// Handler serves identity routes.
type Handler struct {
	svc           SessionService
	verifier      AccessTokenVerifier
	cfg           Config
	origins       map[string]struct{}
	signInCounter metric.Int64Counter
	reuse         metric.Int64Counter
}

func New(svc SessionService, verifier AccessTokenVerifier, cfg Config) (*Handler, error) {
	meter := otel.Meter("github.com/santoshkc2200/ioe-backend/internal/identity")
	signInCounter, err := meter.Int64Counter("identity.signins", metric.WithDescription("Google sign-in attempts by result"))
	if err != nil {
		return nil, err
	}
	reuse, err := meter.Int64Counter("identity.refresh_reuse_detected", metric.WithDescription("Refresh-token reuse detections"))
	if err != nil {
		return nil, err
	}
	origins := make(map[string]struct{}, len(cfg.AllowedOrigins))
	for _, o := range cfg.AllowedOrigins {
		origins[o] = struct{}{}
	}
	return &Handler{svc: svc, verifier: verifier, cfg: cfg, origins: origins, signInCounter: signInCounter, reuse: reuse}, nil
}

// Register mounts the identity routes. Auth routes and their 404/405 responses are
// no-store and rate-limited per client IP.
func (h *Handler) Register(r *httpserver.Router) {
	limiter := h.cfg.AuthLimiter.Middleware(h.cfg.IPs)
	authRoute := func(next http.Handler) http.Handler { return httpserver.NoStore(limiter(next)) }
	r.WrapUnmatched("/v1/auth/", authRoute)
	r.Handle("POST /v1/auth/google", authRoute(http.HandlerFunc(h.signIn)))
	r.Handle("POST /v1/auth/refresh", authRoute(h.requireOrigin(http.HandlerFunc(h.refresh))))
	r.Handle("POST /v1/auth/logout", authRoute(h.requireOrigin(http.HandlerFunc(h.logout))))
	r.Handle("GET /v1/me", httpserver.NoStore(h.RequireAuth(http.HandlerFunc(h.me))))
}

// RequireAuth rejects requests without a valid bearer access token and stores the principal.
func (h *Handler) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
		if !ok || !strings.EqualFold(scheme, "Bearer") || token == "" {
			h.unauthorized(w, r)
			return
		}
		p, err := h.verifier.Verify(token)
		if err != nil {
			h.cfg.Logger.DebugContext(r.Context(), "access token rejected", "reason", err.Error())
			h.unauthorized(w, r)
			return
		}
		next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), p)))
	})
}

func (h *Handler) unauthorized(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
	problem.Write(w, r, http.StatusUnauthorized, typeInvalidToken, "Invalid Token", "")
}

func (h *Handler) requireOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := h.origins[r.Header.Get("Origin")]; !ok {
			problem.Write(w, r, http.StatusForbidden, problem.TypeOriginNotAllowed, "Origin Not Allowed", "")
			return
		}
		next.ServeHTTP(w, r)
	})
}

type signInRequest struct {
	IDToken string `json:"id_token"`
}

type userResponse struct {
	ID        string `json:"id"`
	Email     string `json:"email"`
	Name      string `json:"name"`
	AvatarURL string `json:"avatar_url"`
	Role      string `json:"role"`
}

type tokenResponse struct {
	AccessToken string        `json:"access_token"`
	TokenType   string        `json:"token_type"`
	ExpiresIn   int           `json:"expires_in"`
	User        *userResponse `json:"user,omitempty"`
}

func (h *Handler) signIn(w http.ResponseWriter, r *http.Request) {
	var req signInRequest
	if !httpserver.DecodeJSON(w, r, &req) {
		h.recordSignInResult(r.Context(), "invalid")
		return
	}
	if req.IDToken == "" {
		h.recordSignInResult(r.Context(), "invalid")
		problem.Write(w, r, http.StatusBadRequest, problem.TypeInvalidRequest, "Invalid Request", "id_token is required")
		return
	}
	sess, err := h.svc.SignInWithGoogle(r.Context(), req.IDToken, h.client(r))
	h.recordSignIn(r.Context(), sess, err)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.setRefreshCookie(w, sess)
	u := toUserResponse(sess.User)
	httpserver.WriteJSON(w, http.StatusOK, tokenResponse{
		AccessToken: sess.AccessToken, TokenType: "Bearer", ExpiresIn: int(sess.AccessTokenTTL.Seconds()), User: &u,
	})
}

func (h *Handler) refresh(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(h.cookieName())
	if err != nil || c.Value == "" {
		h.writeError(w, r, app.ErrInvalidToken)
		return
	}
	sess, err := h.svc.Refresh(r.Context(), c.Value, h.client(r))
	if err != nil {
		if errors.Is(err, app.ErrRefreshReuse) {
			h.reuse.Add(r.Context(), 1)
		}
		if errors.Is(err, app.ErrRefreshReuse) || errors.Is(err, app.ErrInvalidToken) {
			h.clearRefreshCookie(w)
		}
		h.writeError(w, r, err)
		return
	}
	h.setRefreshCookie(w, sess)
	httpserver.WriteJSON(w, http.StatusOK, tokenResponse{
		AccessToken: sess.AccessToken, TokenType: "Bearer", ExpiresIn: int(sess.AccessTokenTTL.Seconds()),
	})
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(h.cookieName()); err == nil && c.Value != "" {
		if err := h.svc.Logout(r.Context(), c.Value); err != nil {
			h.writeError(w, r, err)
			return
		}
	}
	h.clearRefreshCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) me(w http.ResponseWriter, r *http.Request) {
	p, ok := auth.PrincipalFrom(r.Context())
	if !ok {
		h.unauthorized(w, r)
		return
	}
	u, err := h.svc.GetMe(r.Context(), p.UserID)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toUserResponse(u))
}

func (h *Handler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, app.ErrRefreshReuse):
		problem.Write(w, r, http.StatusUnauthorized, typeRefreshReuse, "Refresh Token Reuse Detected", "")
	case errors.Is(err, app.ErrInvalidToken):
		h.cfg.Logger.DebugContext(r.Context(), "token rejected", "reason", err.Error())
		problem.Write(w, r, http.StatusUnauthorized, typeInvalidToken, "Invalid Token", "")
	case errors.Is(err, app.ErrEmailUnverified):
		problem.Write(w, r, http.StatusForbidden, typeEmailUnverified, "Email Not Verified", "")
	case errors.Is(err, app.ErrNotFound):
		problem.Write(w, r, http.StatusNotFound, problem.TypeNotFound, "Not Found", "")
	default:
		h.cfg.Logger.ErrorContext(r.Context(), "identity request failed", "error", err)
		problem.Write(w, r, http.StatusInternalServerError, problem.TypeInternal, "Internal Server Error", "")
	}
}

func (h *Handler) recordSignIn(ctx context.Context, sess app.Session, err error) {
	var result string
	switch {
	case errors.Is(err, app.ErrInvalidToken) || errors.Is(err, app.ErrEmailUnverified):
		result = "rejected"
	case err != nil:
		result = "error"
	case sess.Created:
		result = "created"
	default:
		result = "existing"
	}
	h.recordSignInResult(ctx, result)
}

func (h *Handler) recordSignInResult(ctx context.Context, result string) {
	h.signInCounter.Add(ctx, 1, metric.WithAttributes(attribute.String("result", result)))
}

func (h *Handler) client(r *http.Request) app.Client {
	return app.Client{UserAgent: r.UserAgent(), IP: h.cfg.IPs.ClientIP(r)}
}

func (h *Handler) cookieName() string {
	if h.cfg.CookieSecure {
		return secureCookieName
	}
	return insecureCookieName
}

func (h *Handler) setRefreshCookie(w http.ResponseWriter, sess app.Session) {
	//nolint:gosec // Secure=false is intentional for local HTTP development (COOKIE_SECURE=false).
	http.SetCookie(w, &http.Cookie{
		Name: h.cookieName(), Value: sess.RefreshToken, Path: cookiePath,
		MaxAge: int(sess.RefreshTokenTTL / time.Second), HttpOnly: true,
		Secure: h.cfg.CookieSecure, SameSite: http.SameSiteStrictMode,
	})
}

func (h *Handler) clearRefreshCookie(w http.ResponseWriter) {
	//nolint:gosec // Secure=false is intentional for local HTTP development (COOKIE_SECURE=false).
	http.SetCookie(w, &http.Cookie{
		Name: h.cookieName(), Value: "", Path: cookiePath, MaxAge: -1, HttpOnly: true,
		Secure: h.cfg.CookieSecure, SameSite: http.SameSiteStrictMode,
	})
}

func toUserResponse(u domain.User) userResponse {
	return userResponse{ID: u.ID.String(), Email: u.Email, Name: u.Name, AvatarURL: u.AvatarURL, Role: string(u.Role)}
}
