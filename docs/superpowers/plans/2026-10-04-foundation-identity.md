# Backend Foundation, DevSecOps, and Identity Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the `ioe-backend` modular-monolith foundation (platform layer, DevSecOps, OpenTelemetry to OpenObserve) and the identity bounded context (Google sign-in, rotating refresh sessions, `GET /v1/me`).

**Architecture:** One Go module and one binary. Bounded contexts live under `internal/<context>/{domain,app,adapters}` with one PostgreSQL schema each; `internal/platform` holds shared infrastructure; `cmd/api` is the only composition root. depguard enforces import boundaries. Identity events go through a Watermill SQL outbox written in the same transaction as the data.

**Tech Stack:** Go 1.27, gorilla/mux, pgx v5, sqlc 1.31, goose v3, Watermill + watermill-sql v4, golang-jwt v5, keyfunc v3, OpenTelemetry Go SDK 1.47 (OTLP/HTTP), testcontainers-go, golangci-lint v2, govulncheck, gitleaks, Trivy, Syft, cosign, GitHub Actions.

**Spec:** `docs/superpowers/specs/2026-10-04-foundation-identity-design.md`

## Global Constraints

- Module path: `github.com/santoshkc2200/ioe-backend`. Go 1.27.
- Context packages may not import another context; `internal/platform` may not import any context; `domain` imports only stdlib, `github.com/google/uuid`, and `internal/platform/auth`; `app` imports only stdlib, uuid, its own `domain`, `internal/platform/auth`, and `internal/platform/clock`.
- Roles: exactly `student`, `instructor`, `root_admin`. New users are `student`.
- Access token: EdDSA JWT, 15 minutes, header `kid`, claims `iss aud sub role iat exp jti`.
- Refresh token: 32 random bytes, base64url, stored as SHA-256 only; idle lifetime 7 days, family lifetime 30 days; rotation on every use; reuse revokes the family.
- Refresh cookie: `__Secure-ioe_refresh` (or `ioe_refresh` when `COOKIE_SECURE=false`), `HttpOnly; Secure; SameSite=Strict; Path=/v1/auth`.
- `/v1/auth/refresh` and `/v1/auth/logout` require `Origin` in `ALLOWED_ORIGINS`.
- JSON request bodies: `Content-Type: application/json` required, 1 MiB limit, unknown fields rejected.
- Errors: `application/problem+json` with the `type` codes in the spec, plus `unsupported_media_type` (415) and `method_not_allowed` (405).
- Never log or put in telemetry attributes: tokens, cookies, `Authorization`, emails.
- Integration tests use build tag `integration` and need a running Docker daemon. The daemon was not running when this plan was written; start Docker Desktop before Task 6. Never report integration tests as passing unless they ran.
- Conventional Commits. One commit per task minimum.
- Third-party GitHub Actions pinned by full commit SHA; container base images pinned by digest.

## Review Focus

- Two concurrent `POST /v1/auth/refresh` calls with the same cookie: exactly one succeeds and the other returns `refresh_reuse_detected` (row lock `FOR UPDATE`). Test: Task 12 `TestConcurrentRefreshOneWins`.
- A reused refresh token must leave its family revoked even though the request fails; the revocation must commit, not roll back with the error. Tests: Task 9 `TestRefreshReuseRevokesFamily` (fake store rolls back on error) and Task 14 end-to-end.
- Two simultaneous first sign-ins for the same Google account must not produce a 500; the unique-violation path retries once. Test: Task 9 `TestSignInRetriesOnceOnConflict`.
- A cross-site HTML form posting `text/plain` JSON to `/v1/auth/google` (login CSRF) must be rejected with 415 so only CORS-preflighted requests reach sign-in. Tests: Task 4 `TestDecodeJSONRequiresJSONContentType`, Task 13 `TestSignInRejectsNonJSONContentType`.
- A client sending a forged `X-Forwarded-For` directly (not through a trusted proxy) must not change its rate-limit key. Test: Task 4 `TestClientIPIgnoresForwardedForFromUntrustedPeer`.

---

## File Map

```text
go.mod, go.sum
Makefile                         developer and CI entry points
.golangci.yml                    linters, formatters, depguard boundaries
lefthook.yml                     pre-commit hooks
.editorconfig, .gitignore, .dockerignore
README.md, AGENTS.md, CLAUDE.md, SECURITY.md
Dockerfile, docker-compose.yml, .env.example
sqlc.yaml
api/openapi.yaml
migrations/embed.go              embeds *.sql
migrations/00001_platform_outbox.sql
migrations/00002_identity.sql
cmd/api/main.go                  run/serve/migrate entry
cmd/api/app.go                   buildApp composition
cmd/api/e2e_integration_test.go
cmd/keygen/main.go
internal/platform/auth/          Role, Principal, context helpers
internal/platform/clock/         Clock, System, Fake
internal/platform/config/        env loading and validation
internal/platform/logging/       slog handler chain: redaction, correlation, fan-out
internal/platform/telemetry/     OTel providers and shutdown
internal/platform/problem/       RFC 9457 writer
internal/platform/httpserver/    router, middleware, JSON helpers, rate limit, client IP, health, Serve
internal/platform/postgres/      pgx pool with otelpgx
internal/platform/postgres/pgtest/  integration-only disposable database
internal/platform/migrate/       goose runner
internal/platform/outbox/        transactional publish and forwarder
internal/identity/domain/        User, RefreshToken, GoogleIdentity, events
internal/identity/app/           ports, errors, Service use cases
internal/identity/adapters/jwt/         access tokens and key parsing
internal/identity/adapters/google/      Google ID-token verifier
internal/identity/adapters/google/googletest/  test issuer + JWKS server
internal/identity/adapters/postgres/    sqlc queries, repositories, TxRunner, event publisher
internal/identity/adapters/httpapi/     handlers, bearer middleware, metrics
.github/workflows/{ci,codeql,scorecard,release}.yml
.github/dependabot.yml, .github/CODEOWNERS
```

The HTTP adapter directory is `httpapi` (not `http`) so its package name does not shadow `net/http`.

---

### Task 1: Repository scaffold, tooling, and shared auth/clock types

**Files:**
- Create: `go.mod`, `Makefile`, `.golangci.yml`, `lefthook.yml`, `.editorconfig`, `README.md`, `AGENTS.md`, `CLAUDE.md`, `SECURITY.md`
- Modify: `.gitignore`
- Create: `internal/platform/auth/auth.go`, `internal/platform/auth/auth_test.go`
- Create: `internal/platform/clock/clock.go`, `internal/platform/clock/clock_test.go`

**Interfaces:**
- Produces: `auth.Role` (`RoleStudent`, `RoleInstructor`, `RoleRootAdmin`), `auth.ParseRole(string) (Role, error)`, `auth.Principal{UserID uuid.UUID; Role Role}`, `auth.WithPrincipal(ctx, Principal) context.Context`, `auth.PrincipalFrom(ctx) (Principal, bool)`, `clock.Clock` (`Now() time.Time`), `clock.System{}`, `clock.NewFake(time.Time) *clock.Fake` with `Advance(time.Duration)`.

- [ ] **Step 1: Initialize the module**

```bash
go mod init github.com/santoshkc2200/ioe-backend
go get github.com/google/uuid@v1.6.0
```

- [ ] **Step 2: Write the failing tests**

`internal/platform/auth/auth_test.go`:

```go
package auth_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
)

func TestParseRole(t *testing.T) {
	for _, s := range []string{"student", "instructor", "root_admin"} {
		r, err := auth.ParseRole(s)
		if err != nil || string(r) != s {
			t.Fatalf("ParseRole(%q) = %q, %v", s, r, err)
		}
	}
	for _, s := range []string{"", "admin", "Student"} {
		if _, err := auth.ParseRole(s); err == nil {
			t.Fatalf("ParseRole(%q) succeeded, want error", s)
		}
	}
}

func TestPrincipalContext(t *testing.T) {
	if _, ok := auth.PrincipalFrom(context.Background()); ok {
		t.Fatal("empty context reported a principal")
	}
	p := auth.Principal{UserID: uuid.New(), Role: auth.RoleInstructor}
	got, ok := auth.PrincipalFrom(auth.WithPrincipal(context.Background(), p))
	if !ok || got != p {
		t.Fatalf("PrincipalFrom = %+v, %v; want %+v", got, ok, p)
	}
}
```

`internal/platform/clock/clock_test.go`:

```go
package clock_test

import (
	"testing"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/platform/clock"
)

func TestFakeAdvance(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c := clock.NewFake(start)
	c.Advance(time.Hour)
	if got := c.Now(); !got.Equal(start.Add(time.Hour)) {
		t.Fatalf("Now = %v", got)
	}
}

func TestSystemIsUTC(t *testing.T) {
	if loc := (clock.System{}).Now().Location(); loc != time.UTC {
		t.Fatalf("location = %v", loc)
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/platform/...`
Expected: FAIL (`undefined: auth.ParseRole`, `undefined: clock.NewFake`).

- [ ] **Step 4: Implement**

`internal/platform/auth/auth.go`:

```go
// Package auth holds the authenticated principal shared by every bounded context.
package auth

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// Role is a user's single application role.
type Role string

const (
	RoleStudent    Role = "student"
	RoleInstructor Role = "instructor"
	RoleRootAdmin  Role = "root_admin"
)

// ParseRole validates s as a known role.
func ParseRole(s string) (Role, error) {
	switch r := Role(s); r {
	case RoleStudent, RoleInstructor, RoleRootAdmin:
		return r, nil
	default:
		return "", fmt.Errorf("unknown role %q", s)
	}
}

// Principal is the authenticated caller of a request.
type Principal struct {
	UserID uuid.UUID
	Role   Role
}

type principalKey struct{}

// WithPrincipal returns a context carrying p.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// PrincipalFrom returns the principal stored by WithPrincipal.
func PrincipalFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}
```

`internal/platform/clock/clock.go`:

```go
// Package clock abstracts time so use cases are deterministic under test.
package clock

import (
	"sync"
	"time"
)

// Clock reports the current time.
type Clock interface {
	Now() time.Time
}

// System is the real clock, in UTC.
type System struct{}

func (System) Now() time.Time { return time.Now().UTC() }

// Fake is a manually advanced clock for tests.
type Fake struct {
	mu sync.Mutex
	t  time.Time
}

func NewFake(t time.Time) *Fake { return &Fake{t: t} }

func (f *Fake) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.t
}

func (f *Fake) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.t = f.t.Add(d)
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/platform/...`
Expected: PASS.

- [ ] **Step 6: Add tooling and repository files**

`.gitignore` (replace contents):

```gitignore
.env
.DS_Store
/bin/
coverage.out
*.test
```

`.editorconfig`:

```ini
root = true

[*]
charset = utf-8
end_of_line = lf
insert_final_newline = true
trim_trailing_whitespace = true

[*.go]
indent_style = tab

[*.{yml,yaml,json,md,sql}]
indent_style = space
indent_size = 2

[Makefile]
indent_style = tab
```

`Makefile`:

```make
GITLEAKS_VERSION    := v8.30.1
GOVULNCHECK_VERSION := v1.8.0
LEFTHOOK_VERSION    := v2.1.16

.PHONY: run build test test-integration lint fmt vuln secrets tidy-check check hooks

run:
	go run ./cmd/api

build:
	CGO_ENABLED=0 go build -trimpath -o bin/api ./cmd/api

test:
	go test -race ./...

test-integration:
	go test -race -tags integration ./...

lint:
	golangci-lint run ./...

fmt:
	golangci-lint fmt ./...

vuln:
	go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

secrets:
	go run github.com/zricethezav/gitleaks/v8@$(GITLEAKS_VERSION) git --redact --no-banner .

tidy-check:
	go mod tidy -diff

hooks:
	go run github.com/evilmartians/lefthook/v2@$(LEFTHOOK_VERSION) install

check: tidy-check lint test vuln secrets
```

`.golangci.yml`:

```yaml
version: "2"

run:
  timeout: 5m
  build-tags:
    - integration

linters:
  default: none
  enable:
    - bodyclose
    - contextcheck
    - depguard
    - errcheck
    - errorlint
    - gocritic
    - gosec
    - govet
    - ineffassign
    - misspell
    - noctx
    - revive
    - sqlclosecheck
    - staticcheck
    - unused
  settings:
    depguard:
      rules:
        platform-independent-of-contexts:
          list-mode: lax
          files:
            - "**/internal/platform/**"
          deny:
            - pkg: github.com/santoshkc2200/ioe-backend/internal/identity
              desc: platform must not depend on bounded contexts
        identity-domain:
          list-mode: strict
          files:
            - "**/internal/identity/domain/**"
            - "!$test"
          allow:
            - $gostd
            - github.com/google/uuid
            - github.com/santoshkc2200/ioe-backend/internal/platform/auth
        identity-app:
          list-mode: strict
          files:
            - "**/internal/identity/app/**"
            - "!$test"
          allow:
            - $gostd
            - github.com/google/uuid
            - github.com/santoshkc2200/ioe-backend/internal/identity/domain
            - github.com/santoshkc2200/ioe-backend/internal/platform/auth
            - github.com/santoshkc2200/ioe-backend/internal/platform/clock
  exclusions:
    generated: strict
    presets:
      - comments
      - common-false-positives
      - std-error-handling

formatters:
  enable:
    - gofmt
    - goimports
  settings:
    goimports:
      local-prefixes:
        - github.com/santoshkc2200/ioe-backend
```

`lefthook.yml`:

```yaml
pre-commit:
  parallel: true
  commands:
    format:
      glob: "*.go"
      run: golangci-lint fmt --diff ./...
    lint:
      glob: "*.go"
      run: golangci-lint run --new-from-rev=HEAD ./...
    secrets:
      run: go run github.com/zricethezav/gitleaks/v8@v8.30.1 git --pre-commit --staged --redact --no-banner .
```

`AGENTS.md`:

````markdown
# ioe-backend

Backend for the IOE learning management system: single tenant, Google sign-in only,
roles `student`, `instructor`, `root_admin`.

## Architecture

Modular monolith, domain-driven design, hexagonal architecture.

```text
cmd/api/              composition root (the only package that wires everything)
internal/platform/    shared infrastructure; never imports a bounded context
internal/<context>/
  domain/             entities, value objects, events; stdlib + uuid + platform/auth only
  app/                use cases and the ports they own; imports only its domain and platform/{auth,clock}
  adapters/           implementations of app ports (postgres, http, external services)
migrations/           goose SQL; one PostgreSQL schema per context
api/openapi.yaml      public HTTP contract
```

Rules:

- A context never imports another context. When context B needs data from A synchronously,
  B defines a port in `B/app` and `cmd/api` wires an adapter backed by A's application service.
  Asynchronous notifications use domain events through `internal/platform/outbox`.
- A context reads and writes only its own schema.
- Write the outbox message in the same transaction as the state change.
- Adding a context: add `<ctx>-domain` and `<ctx>-app` depguard rules in `.golangci.yml`,
  and add the context's import path to `platform-independent-of-contexts` and to a
  deny rule for every other context.

## Required gates

```sh
make check            # tidy diff, golangci-lint, sqlc diff, go test -race, govulncheck, gitleaks
make test-integration # requires a running Docker daemon
docker compose config
make docker-build
git diff --check
```

Integration tests must not be reported as passing unless they actually ran.
Use Conventional Commits.
````

`CLAUDE.md`:

```markdown
Read and follow `AGENTS.md`.
```

`README.md`:

````markdown
# ioe-backend

Go backend for the IOE learning management system.

## Quick start

```sh
cp .env.example .env
make keygen            # paste the key into .env
make compose-up        # PostgreSQL + OpenObserve
make migrate-up
make run
```

See `AGENTS.md` for architecture rules and required checks, and `api/openapi.yaml` for the HTTP contract.
````

`SECURITY.md`:

```markdown
# Security Policy

Report vulnerabilities privately through GitHub's "Report a vulnerability" button on this
repository (Security → Advisories). Do not open public issues for security problems.

We aim to acknowledge reports within 3 business days.
```

- [ ] **Step 7: Run the gates**

Run: `go mod tidy && make lint test`
Expected: lint reports no issues; tests PASS.

Run: `make vuln secrets`
Expected: `No vulnerabilities found.`; gitleaks `no leaks found`.

- [ ] **Step 8: Commit**

```bash
git add -A
git commit -m "chore: scaffold module, tooling, and shared auth types"
```

---

### Task 2: Configuration

**Files:**
- Create: `internal/platform/config/config.go`, `internal/platform/config/config_test.go`

**Interfaces:**
- Produces: `config.Config` with fields `HTTPAddr string`, `DatabaseURL string`, `GoogleClientIDs []string`, `GoogleJWKSURL string`, `JWTIssuer string`, `JWTAudience string`, `JWTSigningKeyPEM string`, `JWTSigningKeyID string`, `JWTVerifyKeys string`, `AllowedOrigins []string`, `BootstrapRootAdminEmails []string`, `CookieSecure bool`, `AuthRateLimitPerMinute int`, `TrustedProxyCIDRs []string`, `LogLevel string`; `config.Load() (Config, error)`; `config.LoadFrom(map[string]string) (Config, error)`; `config.LoadDatabaseURL() (string, error)`; `(Config) SlogLevel() (slog.Level, error)`; `(Config) TrustedProxies() []netip.Prefix`.

- [ ] **Step 1: Add the dependency**

```bash
go get github.com/caarlos0/env/v11@v11.4.1
```

- [ ] **Step 2: Write the failing tests**

`internal/platform/config/config_test.go`:

```go
package config_test

import (
	"log/slog"
	"strings"
	"testing"

	"github.com/santoshkc2200/ioe-backend/internal/platform/config"
)

func validEnv() map[string]string {
	return map[string]string{
		"DATABASE_URL":       "postgres://ioe:ioe@localhost:5432/ioe",
		"GOOGLE_CLIENT_IDS":  "web.apps.googleusercontent.com, admin.apps.googleusercontent.com",
		"JWT_ISSUER":         "https://api.example.com",
		"JWT_AUDIENCE":       "ioe",
		"JWT_SIGNING_KEY":    "pem",
		"JWT_SIGNING_KEY_ID": "k1",
		"ALLOWED_ORIGINS":    "https://app.example.com, http://localhost:5173",
	}
}

func TestLoadDefaultsAndTrimming(t *testing.T) {
	cfg, err := config.LoadFrom(validEnv())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTPAddr != ":8080" || !cfg.CookieSecure || cfg.AuthRateLimitPerMinute != 30 || cfg.LogLevel != "info" {
		t.Fatalf("defaults not applied: %+v", cfg)
	}
	if cfg.GoogleJWKSURL != "https://www.googleapis.com/oauth2/v3/certs" {
		t.Fatalf("GoogleJWKSURL = %q", cfg.GoogleJWKSURL)
	}
	if got := cfg.GoogleClientIDs[1]; got != "admin.apps.googleusercontent.com" {
		t.Fatalf("list entry not trimmed: %q", got)
	}
	if got := cfg.AllowedOrigins[1]; got != "http://localhost:5173" {
		t.Fatalf("origin not trimmed: %q", got)
	}
	if len(cfg.BootstrapRootAdminEmails) != 0 {
		t.Fatalf("BootstrapRootAdminEmails = %q", cfg.BootstrapRootAdminEmails)
	}
	if lvl, err := cfg.SlogLevel(); err != nil || lvl != slog.LevelInfo {
		t.Fatalf("SlogLevel = %v, %v", lvl, err)
	}
}

func TestLoadNamesMissingVariable(t *testing.T) {
	env := validEnv()
	delete(env, "DATABASE_URL")
	_, err := config.LoadFrom(env)
	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("err = %v, want mention of DATABASE_URL", err)
	}
}

func TestLoadRejectsInvalidValues(t *testing.T) {
	cases := map[string]struct{ key, value string }{
		"origin with path":   {"ALLOWED_ORIGINS", "https://app.example.com/login"},
		"origin no scheme":   {"ALLOWED_ORIGINS", "app.example.com"},
		"bad cidr":           {"TRUSTED_PROXY_CIDRS", "10.0.0.0/33"},
		"zero rate":          {"AUTH_RATE_LIMIT_PER_MINUTE", "0"},
		"bad log level":      {"LOG_LEVEL", "loud"},
		"bad cookie boolean": {"COOKIE_SECURE", "maybe"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			env := validEnv()
			env[tc.key] = tc.value
			_, err := config.LoadFrom(env)
			if err == nil || !strings.Contains(err.Error(), tc.key) {
				t.Fatalf("err = %v, want mention of %s", err, tc.key)
			}
		})
	}
}

func TestTrustedProxies(t *testing.T) {
	env := validEnv()
	env["TRUSTED_PROXY_CIDRS"] = "10.0.0.0/8, 192.168.1.7/32"
	cfg, err := config.LoadFrom(env)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.TrustedProxies(); len(got) != 2 || got[0].String() != "10.0.0.0/8" {
		t.Fatalf("TrustedProxies = %v", got)
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/platform/config/`
Expected: FAIL (`undefined: config.LoadFrom`).

- [ ] **Step 4: Implement**

`internal/platform/config/config.go`:

```go
// Package config loads process configuration from the environment and fails fast on invalid values.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"net/url"
	"os"
	"strings"

	"github.com/caarlos0/env/v11"
)

// Config is the complete API server configuration.
type Config struct {
	HTTPAddr                 string   `env:"HTTP_ADDR" envDefault:":8080"`
	DatabaseURL              string   `env:"DATABASE_URL,required,notEmpty"`
	GoogleClientIDs          []string `env:"GOOGLE_CLIENT_IDS,required,notEmpty" envSeparator:","`
	GoogleJWKSURL            string   `env:"GOOGLE_JWKS_URL" envDefault:"https://www.googleapis.com/oauth2/v3/certs"`
	JWTIssuer                string   `env:"JWT_ISSUER,required,notEmpty"`
	JWTAudience              string   `env:"JWT_AUDIENCE,required,notEmpty"`
	JWTSigningKeyPEM         string   `env:"JWT_SIGNING_KEY,required,notEmpty"`
	JWTSigningKeyID          string   `env:"JWT_SIGNING_KEY_ID,required,notEmpty"`
	JWTVerifyKeys            string   `env:"JWT_VERIFY_KEYS"`
	AllowedOrigins           []string `env:"ALLOWED_ORIGINS,required,notEmpty" envSeparator:","`
	BootstrapRootAdminEmails []string `env:"BOOTSTRAP_ROOT_ADMIN_EMAILS" envSeparator:","`
	CookieSecure             bool     `env:"COOKIE_SECURE" envDefault:"true"`
	AuthRateLimitPerMinute   int      `env:"AUTH_RATE_LIMIT_PER_MINUTE" envDefault:"30"`
	TrustedProxyCIDRs        []string `env:"TRUSTED_PROXY_CIDRS" envSeparator:","`
	LogLevel                 string   `env:"LOG_LEVEL" envDefault:"info"`
}

// Load reads the process environment.
func Load() (Config, error) {
	return LoadFrom(env.ToMap(os.Environ()))
}

// LoadFrom reads configuration from the given variables.
func LoadFrom(environ map[string]string) (Config, error) {
	cfg, err := env.ParseAsWithOptions[Config](env.Options{Environment: environ})
	if err != nil {
		return Config{}, err
	}
	cfg.normalize()
	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// LoadDatabaseURL reads only DATABASE_URL, for commands that need nothing else.
func LoadDatabaseURL() (string, error) {
	u := os.Getenv("DATABASE_URL")
	if u == "" {
		return "", errors.New("DATABASE_URL is required")
	}
	return u, nil
}

// SlogLevel parses LogLevel.
func (c Config) SlogLevel() (slog.Level, error) {
	var l slog.Level
	err := l.UnmarshalText([]byte(c.LogLevel))
	return l, err
}

// TrustedProxies returns the parsed TRUSTED_PROXY_CIDRS. Values were validated by LoadFrom.
func (c Config) TrustedProxies() []netip.Prefix {
	out := make([]netip.Prefix, 0, len(c.TrustedProxyCIDRs))
	for _, s := range c.TrustedProxyCIDRs {
		if p, err := netip.ParsePrefix(s); err == nil {
			out = append(out, p.Masked())
		}
	}
	return out
}

func (c *Config) normalize() {
	c.GoogleClientIDs = cleanList(c.GoogleClientIDs)
	c.AllowedOrigins = cleanList(c.AllowedOrigins)
	c.BootstrapRootAdminEmails = cleanList(c.BootstrapRootAdminEmails)
	c.TrustedProxyCIDRs = cleanList(c.TrustedProxyCIDRs)
}

func (c *Config) validate() error {
	var errs []error
	if len(c.GoogleClientIDs) == 0 {
		errs = append(errs, errors.New("GOOGLE_CLIENT_IDS: at least one client ID is required"))
	}
	if len(c.AllowedOrigins) == 0 {
		errs = append(errs, errors.New("ALLOWED_ORIGINS: at least one origin is required"))
	}
	for _, o := range c.AllowedOrigins {
		if !isOrigin(o) {
			errs = append(errs, fmt.Errorf("ALLOWED_ORIGINS: %q is not an origin (scheme://host[:port])", o))
		}
	}
	for _, s := range c.TrustedProxyCIDRs {
		if _, err := netip.ParsePrefix(s); err != nil {
			errs = append(errs, fmt.Errorf("TRUSTED_PROXY_CIDRS: %q: %w", s, err))
		}
	}
	if c.AuthRateLimitPerMinute <= 0 {
		errs = append(errs, errors.New("AUTH_RATE_LIMIT_PER_MINUTE: must be positive"))
	}
	if _, err := c.SlogLevel(); err != nil {
		errs = append(errs, fmt.Errorf("LOG_LEVEL: %w", err))
	}
	return errors.Join(errs...)
}

func isOrigin(s string) bool {
	u, err := url.Parse(s)
	if err != nil {
		return false
	}
	return (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" &&
		u.Path == "" && u.RawQuery == "" && u.Fragment == "" && u.User == nil
}

func cleanList(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}
```

Note: for `COOKIE_SECURE=maybe` the env library's parse error names the variable; if it does not, wrap errors returned from `ParseAsWithOptions` so the message contains the field's variable name, and keep the test as written.

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/platform/config/`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
go mod tidy
git add -A
git commit -m "feat(platform): add environment configuration with validation"
```

---

### Task 3: Logging with redaction and correlation

**Files:**
- Create: `internal/platform/logging/logging.go`, `internal/platform/logging/logging_test.go`

**Interfaces:**
- Produces: `logging.New(level slog.Leveler, w io.Writer, extra slog.Handler) *slog.Logger` (`extra` may be nil; it receives the same redacted, correlated records as `w`), `logging.WithRequestID(ctx, id string) context.Context`, `logging.RequestID(ctx) string`.

- [ ] **Step 1: Add the dependency**

```bash
go get go.opentelemetry.io/otel/trace@v1.47.0
```

- [ ] **Step 2: Write the failing tests**

`internal/platform/logging/logging_test.go`:

```go
package logging_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/trace"

	"github.com/santoshkc2200/ioe-backend/internal/platform/logging"
)

func TestRedactsSensitiveKeysEverywhere(t *testing.T) {
	var out, extraOut bytes.Buffer
	extra := slog.NewJSONHandler(&extraOut, &slog.HandlerOptions{Level: slog.LevelDebug})
	log := logging.New(slog.LevelInfo, &out, extra)

	log.With("id_token", "secret-1").Info("msg",
		"Authorization", "Bearer secret-2",
		slog.Group("req", "cookie", "secret-3", "path", "/v1/me"),
		"refresh_token", "secret-4",
	)

	for name, buf := range map[string]*bytes.Buffer{"stdout": &out, "extra": &extraOut} {
		s := buf.String()
		for _, secret := range []string{"secret-1", "secret-2", "secret-3", "secret-4"} {
			if strings.Contains(s, secret) {
				t.Errorf("%s output leaked %s: %s", name, secret, s)
			}
		}
		if !strings.Contains(s, "[REDACTED]") || !strings.Contains(s, "/v1/me") {
			t.Errorf("%s output missing redaction marker or safe value: %s", name, s)
		}
	}
}

func TestLevelAppliesToAllOutputs(t *testing.T) {
	var out, extraOut bytes.Buffer
	extra := slog.NewJSONHandler(&extraOut, &slog.HandlerOptions{Level: slog.LevelDebug})
	logging.New(slog.LevelInfo, &out, extra).Debug("hidden")
	if out.Len() != 0 || extraOut.Len() != 0 {
		t.Fatalf("debug record emitted: %q %q", out.String(), extraOut.String())
	}
}

func TestAddsRequestAndTraceIDs(t *testing.T) {
	var out bytes.Buffer
	log := logging.New(slog.LevelInfo, &out, nil)
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    trace.TraceID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16},
		SpanID:     trace.SpanID{1, 2, 3, 4, 5, 6, 7, 8},
		TraceFlags: trace.FlagsSampled,
	})
	ctx := trace.ContextWithSpanContext(logging.WithRequestID(context.Background(), "req-1"), sc)
	log.InfoContext(ctx, "msg")
	s := out.String()
	for _, want := range []string{`"request_id":"req-1"`, `"trace_id":"` + sc.TraceID().String(), `"span_id":"` + sc.SpanID().String()} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %s in %s", want, s)
		}
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/platform/logging/`
Expected: FAIL (`undefined: logging.New`).

- [ ] **Step 4: Implement**

`internal/platform/logging/logging.go`:

```go
// Package logging builds the application slog logger: JSON output, optional extra sink
// (OpenTelemetry), request/trace correlation, and redaction of sensitive attributes.
package logging

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"

	"go.opentelemetry.io/otel/trace"
)

const redacted = "[REDACTED]"

var sensitiveKeys = map[string]struct{}{
	"authorization": {}, "cookie": {}, "set-cookie": {},
	"id_token": {}, "access_token": {}, "refresh_token": {},
	"password": {}, "secret": {},
}

type requestIDKey struct{}

// WithRequestID stores the request ID for log correlation.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, id)
}

// RequestID returns the request ID stored by WithRequestID, or "".
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

// New returns a logger writing JSON to w and, when extra is non-nil, to extra as well.
// Every output receives the same level filtering, correlation fields, and redaction.
func New(level slog.Leveler, w io.Writer, extra slog.Handler) *slog.Logger {
	handlers := []slog.Handler{slog.NewJSONHandler(w, &slog.HandlerOptions{Level: slog.LevelDebug})}
	if extra != nil {
		handlers = append(handlers, extra)
	}
	return slog.New(redactor{next: correlator{next: fanout{level: level, handlers: handlers}}})
}

type fanout struct {
	level    slog.Leveler
	handlers []slog.Handler
}

func (f fanout) Enabled(ctx context.Context, l slog.Level) bool {
	if l < f.level.Level() {
		return false
	}
	for _, h := range f.handlers {
		if h.Enabled(ctx, l) {
			return true
		}
	}
	return false
}

func (f fanout) Handle(ctx context.Context, r slog.Record) error {
	var errs []error
	for _, h := range f.handlers {
		if h.Enabled(ctx, r.Level) {
			errs = append(errs, h.Handle(ctx, r.Clone()))
		}
	}
	return errors.Join(errs...)
}

func (f fanout) WithAttrs(as []slog.Attr) slog.Handler {
	next := make([]slog.Handler, len(f.handlers))
	for i, h := range f.handlers {
		next[i] = h.WithAttrs(as)
	}
	return fanout{level: f.level, handlers: next}
}

func (f fanout) WithGroup(name string) slog.Handler {
	next := make([]slog.Handler, len(f.handlers))
	for i, h := range f.handlers {
		next[i] = h.WithGroup(name)
	}
	return fanout{level: f.level, handlers: next}
}

type correlator struct{ next slog.Handler }

func (c correlator) Enabled(ctx context.Context, l slog.Level) bool { return c.next.Enabled(ctx, l) }

func (c correlator) Handle(ctx context.Context, r slog.Record) error {
	if id := RequestID(ctx); id != "" {
		r.AddAttrs(slog.String("request_id", id))
	}
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		r.AddAttrs(slog.String("trace_id", sc.TraceID().String()), slog.String("span_id", sc.SpanID().String()))
	}
	return c.next.Handle(ctx, r)
}

func (c correlator) WithAttrs(as []slog.Attr) slog.Handler { return correlator{c.next.WithAttrs(as)} }
func (c correlator) WithGroup(name string) slog.Handler    { return correlator{c.next.WithGroup(name)} }

type redactor struct{ next slog.Handler }

func (r redactor) Enabled(ctx context.Context, l slog.Level) bool { return r.next.Enabled(ctx, l) }

func (r redactor) Handle(ctx context.Context, rec slog.Record) error {
	out := slog.NewRecord(rec.Time, rec.Level, rec.Message, rec.PC)
	rec.Attrs(func(a slog.Attr) bool {
		out.AddAttrs(redact(a))
		return true
	})
	return r.next.Handle(ctx, out)
}

func (r redactor) WithAttrs(as []slog.Attr) slog.Handler {
	clean := make([]slog.Attr, len(as))
	for i, a := range as {
		clean[i] = redact(a)
	}
	return redactor{r.next.WithAttrs(clean)}
}

func (r redactor) WithGroup(name string) slog.Handler { return redactor{r.next.WithGroup(name)} }

func redact(a slog.Attr) slog.Attr {
	if _, ok := sensitiveKeys[strings.ToLower(a.Key)]; ok {
		return slog.String(a.Key, redacted)
	}
	v := a.Value.Resolve()
	if v.Kind() == slog.KindGroup {
		group := v.Group()
		clean := make([]slog.Attr, len(group))
		for i, g := range group {
			clean[i] = redact(g)
		}
		return slog.Attr{Key: a.Key, Value: slog.GroupValue(clean...)}
	}
	return slog.Attr{Key: a.Key, Value: v}
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test -race ./internal/platform/logging/`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
go mod tidy
git add -A
git commit -m "feat(platform): add redacting, correlated slog logger"
```

---
### Task 4: Problem responses and HTTP server platform

**Files:**
- Create: `internal/platform/problem/problem.go`, `internal/platform/problem/problem_test.go`
- Create: `internal/platform/httpserver/middleware.go`, `json.go`, `clientip.go`, `ratelimit.go`, `router.go`, `health.go`, `serve.go`
- Test: `internal/platform/httpserver/middleware_test.go`, `json_test.go`, `clientip_test.go`, `ratelimit_test.go`, `router_test.go`, `serve_test.go`

**Interfaces:**
- Consumes: `logging.WithRequestID`, `logging.RequestID` (Task 3).
- Produces:
  - `problem.Write(w http.ResponseWriter, r *http.Request, status int, typ, title, detail string)`; constants `problem.TypeInvalidRequest`, `TypePayloadTooLarge`, `TypeUnsupportedMediaType`, `TypeRateLimited`, `TypeOriginNotAllowed`, `TypeNotFound`, `TypeMethodNotAllowed`, `TypeInternal`.
  - `httpserver.Middleware` (`func(http.Handler) http.Handler`), `httpserver.Chain(h http.Handler, m ...Middleware) http.Handler` (first is outermost).
  - `httpserver.RequestID`, `httpserver.Recover(*slog.Logger)`, `httpserver.AccessLog(*slog.Logger)`, `httpserver.SecurityHeaders`, `httpserver.CORS([]string)`, `httpserver.BodyLimit(int64)`, `httpserver.NoStore` — all `Middleware`.
  - `httpserver.DecodeJSON(w, r, dst any) bool`, `httpserver.WriteJSON(w, status int, v any)`.
  - `httpserver.NewIPResolver([]netip.Prefix) IPResolver`, `(IPResolver) ClientIP(*http.Request) string`.
  - `httpserver.NewRateLimiter(perMinute int) *RateLimiter`, `(*RateLimiter) Allow(key string) bool`, `(*RateLimiter) Middleware(IPResolver) Middleware`.
  - `httpserver.Options{Logger *slog.Logger; AllowedOrigins []string; ServiceName string}`, `httpserver.NewRouter(Options) (*mux.Router, http.Handler)`.
  - `httpserver.MountHealth(r *mux.Router, ready func(context.Context) error)`.
  - `httpserver.Serve(ctx context.Context, ln net.Listener, h http.Handler, logger *slog.Logger) error`.

- [ ] **Step 1: Add dependencies**

```bash
go get github.com/gorilla/mux@v1.8.1 golang.org/x/time@v0.16.0 \
  go.opentelemetry.io/contrib/instrumentation/github.com/gorilla/mux/otelmux@v0.72.0 \
  go.opentelemetry.io/otel@v1.47.0 go.opentelemetry.io/otel/sdk@v1.47.0
```

- [ ] **Step 2: Write the failing problem test**

`internal/platform/problem/problem_test.go`:

```go
package problem_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/santoshkc2200/ioe-backend/internal/platform/logging"
	"github.com/santoshkc2200/ioe-backend/internal/platform/problem"
)

func TestWrite(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/x", nil)
	r = r.WithContext(logging.WithRequestID(r.Context(), "req-9"))
	w := httptest.NewRecorder()

	problem.Write(w, r, http.StatusNotFound, problem.TypeNotFound, "Not Found", "no such thing")

	if w.Code != http.StatusNotFound || w.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("status %d content-type %q", w.Code, w.Header().Get("Content-Type"))
	}
	var p problem.Problem
	if err := json.NewDecoder(w.Body).Decode(&p); err != nil {
		t.Fatal(err)
	}
	want := problem.Problem{Type: "not_found", Title: "Not Found", Status: 404, Detail: "no such thing", Instance: "req-9"}
	if p != want {
		t.Fatalf("got %+v want %+v", p, want)
	}
}
```

- [ ] **Step 3: Run it to verify it fails**

Run: `go test ./internal/platform/problem/`
Expected: FAIL (`undefined: problem.Write`).

- [ ] **Step 4: Implement problem**

`internal/platform/problem/problem.go`:

```go
// Package problem writes RFC 9457 problem+json responses.
package problem

import (
	"encoding/json"
	"net/http"

	"github.com/santoshkc2200/ioe-backend/internal/platform/logging"
)

// Shared problem type codes. Bounded contexts define their own codes alongside these.
const (
	TypeInvalidRequest       = "invalid_request"
	TypePayloadTooLarge      = "payload_too_large"
	TypeUnsupportedMediaType = "unsupported_media_type"
	TypeRateLimited          = "rate_limited"
	TypeOriginNotAllowed     = "origin_not_allowed"
	TypeNotFound             = "not_found"
	TypeMethodNotAllowed     = "method_not_allowed"
	TypeInternal             = "internal"
)

// Problem is an RFC 9457 problem details document. Instance carries the request ID.
type Problem struct {
	Type     string `json:"type"`
	Title    string `json:"title"`
	Status   int    `json:"status"`
	Detail   string `json:"detail,omitempty"`
	Instance string `json:"instance,omitempty"`
}

// Write sends a problem response.
func Write(w http.ResponseWriter, r *http.Request, status int, typ, title, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(Problem{
		Type: typ, Title: title, Status: status, Detail: detail,
		Instance: logging.RequestID(r.Context()),
	})
}
```

Run: `go test ./internal/platform/problem/` — Expected: PASS.

- [ ] **Step 5: Write the failing httpserver tests**

`internal/platform/httpserver/middleware_test.go`:

```go
package httpserver_test

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/santoshkc2200/ioe-backend/internal/platform/httpserver"
	"github.com/santoshkc2200/ioe-backend/internal/platform/logging"
)

func TestRequestIDGeneratedOrPropagated(t *testing.T) {
	var seen string
	h := httpserver.RequestID(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = logging.RequestID(r.Context())
	}))

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Request-ID", "abc-123")
	h.ServeHTTP(w, r)
	if seen != "abc-123" || w.Header().Get("X-Request-ID") != "abc-123" {
		t.Fatalf("valid id not propagated: %q", seen)
	}

	w = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Request-ID", "bad\nid")
	h.ServeHTTP(w, r)
	if seen == "bad\nid" || len(seen) != 36 {
		t.Fatalf("invalid id not replaced: %q", seen)
	}
}

func TestRecoverWritesProblem(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	h := httpserver.Recover(logger)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") }))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Code != http.StatusInternalServerError || !strings.Contains(w.Body.String(), `"type":"internal"`) {
		t.Fatalf("got %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(logs.String(), "boom") {
		t.Fatalf("panic not logged: %s", logs.String())
	}
}

func TestSecurityHeaders(t *testing.T) {
	w := httptest.NewRecorder()
	httpserver.SecurityHeaders(http.NotFoundHandler()).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	want := map[string]string{
		"Strict-Transport-Security": "max-age=63072000; includeSubDomains",
		"X-Content-Type-Options":    "nosniff",
		"Content-Security-Policy":   "default-src 'none'; frame-ancestors 'none'",
		"Referrer-Policy":           "no-referrer",
	}
	for k, v := range want {
		if got := w.Header().Get(k); got != v {
			t.Errorf("%s = %q want %q", k, got, v)
		}
	}
}

func TestCORS(t *testing.T) {
	h := httpserver.CORS([]string{"https://app.example.com"})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))

	pre := httptest.NewRequest(http.MethodOptions, "/v1/auth/google", nil)
	pre.Header.Set("Origin", "https://app.example.com")
	pre.Header.Set("Access-Control-Request-Method", "POST")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, pre)
	if w.Code != http.StatusNoContent ||
		w.Header().Get("Access-Control-Allow-Origin") != "https://app.example.com" ||
		w.Header().Get("Access-Control-Allow-Credentials") != "true" ||
		!strings.Contains(w.Header().Get("Access-Control-Allow-Headers"), "Content-Type") {
		t.Fatalf("allowed preflight: %d %v", w.Code, w.Header())
	}

	evil := httptest.NewRequest(http.MethodOptions, "/v1/auth/google", nil)
	evil.Header.Set("Origin", "https://evil.example")
	evil.Header.Set("Access-Control-Request-Method", "POST")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, evil)
	if w.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("disallowed origin got CORS headers: %v", w.Header())
	}
	if w.Code != http.StatusTeapot {
		t.Fatalf("disallowed preflight should fall through, got %d", w.Code)
	}
}
```

`internal/platform/httpserver/json_test.go`:

```go
package httpserver_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/santoshkc2200/ioe-backend/internal/platform/httpserver"
)

type payload struct {
	Name string `json:"name"`
}

func decode(t *testing.T, contentType, body string, limit int64) (*httptest.ResponseRecorder, bool, payload) {
	t.Helper()
	var p payload
	w := httptest.NewRecorder()
	ok := false
	h := httpserver.BodyLimit(limit)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ok = httpserver.DecodeJSON(w, r, &p)
	}))
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	h.ServeHTTP(w, r)
	return w, ok, p
}

func TestDecodeJSONAcceptsValidObject(t *testing.T) {
	_, ok, p := decode(t, "application/json; charset=utf-8", `{"name":"a"}`, 1<<20)
	if !ok || p.Name != "a" {
		t.Fatalf("ok=%v p=%+v", ok, p)
	}
}

func TestDecodeJSONRequiresJSONContentType(t *testing.T) {
	for _, ct := range []string{"", "text/plain", "application/x-www-form-urlencoded"} {
		w, ok, _ := decode(t, ct, `{"name":"a"}`, 1<<20)
		if ok || w.Code != http.StatusUnsupportedMediaType || !strings.Contains(w.Body.String(), "unsupported_media_type") {
			t.Fatalf("content-type %q: ok=%v code=%d", ct, ok, w.Code)
		}
	}
}

func TestDecodeJSONRejectsBadBodies(t *testing.T) {
	for _, body := range []string{`{"name":"a","extra":1}`, `{"name":`, `{"name":"a"}{"name":"b"}`, `[]`} {
		w, ok, _ := decode(t, "application/json", body, 1<<20)
		if ok || w.Code != http.StatusBadRequest {
			t.Fatalf("body %q: ok=%v code=%d", body, ok, w.Code)
		}
	}
}

func TestDecodeJSONTooLarge(t *testing.T) {
	w, ok, _ := decode(t, "application/json", `{"name":"`+strings.Repeat("a", 100)+`"}`, 16)
	if ok || w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("ok=%v code=%d", ok, w.Code)
	}
}
```

`internal/platform/httpserver/clientip_test.go`:

```go
package httpserver_test

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/santoshkc2200/ioe-backend/internal/platform/httpserver"
)

func req(remote, xff string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = remote
	if xff != "" {
		r.Header.Set("X-Forwarded-For", xff)
	}
	return r
}

func TestClientIPIgnoresForwardedForFromUntrustedPeer(t *testing.T) {
	res := httpserver.NewIPResolver([]netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")})
	if got := res.ClientIP(req("203.0.113.5:4000", "1.2.3.4")); got != "203.0.113.5" {
		t.Fatalf("got %q", got)
	}
}

func TestClientIPUsesRightmostUntrustedHopBehindProxy(t *testing.T) {
	res := httpserver.NewIPResolver([]netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")})
	if got := res.ClientIP(req("10.0.0.2:4000", "1.2.3.4, 198.51.100.7, 10.0.0.9")); got != "198.51.100.7" {
		t.Fatalf("got %q", got)
	}
}

func TestClientIPFallsBackToPeerOnGarbage(t *testing.T) {
	res := httpserver.NewIPResolver([]netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")})
	if got := res.ClientIP(req("10.0.0.2:4000", "not-an-ip")); got != "10.0.0.2" {
		t.Fatalf("got %q", got)
	}
}
```

`internal/platform/httpserver/ratelimit_test.go`:

```go
package httpserver_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/santoshkc2200/ioe-backend/internal/platform/httpserver"
)

func TestRateLimiterMiddleware(t *testing.T) {
	rl := httpserver.NewRateLimiter(2)
	h := rl.Middleware(httpserver.NewIPResolver(nil))(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	codes := make([]int, 0, 3)
	for range 3 {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req("203.0.113.5:1", ""))
		codes = append(codes, w.Code)
		if w.Code == http.StatusTooManyRequests && w.Header().Get("Retry-After") == "" {
			t.Fatal("429 without Retry-After")
		}
	}
	if codes[0] != 200 || codes[1] != 200 || codes[2] != 429 {
		t.Fatalf("codes = %v", codes)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req("203.0.113.6:1", ""))
	if w.Code != http.StatusOK {
		t.Fatalf("other client limited: %d", w.Code)
	}
}
```

`internal/platform/httpserver/router_test.go`:

```go
package httpserver_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/santoshkc2200/ioe-backend/internal/platform/httpserver"
)

func options() httpserver.Options {
	return httpserver.Options{
		Logger:         slog.New(slog.NewJSONHandler(io.Discard, nil)),
		AllowedOrigins: []string{"https://app.example.com"},
		ServiceName:    "test",
	}
}

func TestRouterSpanUsesRouteTemplateAndOmitsSecrets(t *testing.T) {
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	r, h := httpserver.NewRouter(options())
	r.HandleFunc("/v1/things/{id}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}).Methods(http.MethodGet)

	rq := httptest.NewRequest(http.MethodGet, "/v1/things/123", nil)
	rq.Header.Set("Authorization", "Bearer super-secret")
	rq.Header.Set("Cookie", "ioe_refresh=cookie-secret")
	h.ServeHTTP(httptest.NewRecorder(), rq)

	spans := exp.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("spans = %d", len(spans))
	}
	if !strings.Contains(spans[0].Name, "/v1/things/{id}") {
		t.Fatalf("span name %q is not the route template", spans[0].Name)
	}
	for _, a := range spans[0].Attributes {
		v := a.Value.Emit()
		if strings.Contains(v, "super-secret") || strings.Contains(v, "cookie-secret") {
			t.Fatalf("attribute %s leaked a secret", a.Key)
		}
	}
}

func TestRouterNotFoundIsProblemWithHeaders(t *testing.T) {
	_, h := httpserver.NewRouter(options())
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/nope", nil))
	if w.Code != http.StatusNotFound || w.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("%d %q", w.Code, w.Header().Get("Content-Type"))
	}
	if w.Header().Get("X-Content-Type-Options") != "nosniff" || w.Header().Get("X-Request-ID") == "" {
		t.Fatalf("outer middleware skipped on 404: %v", w.Header())
	}
}

func TestHealthEndpoints(t *testing.T) {
	r, h := httpserver.NewRouter(options())
	var readyErr error
	httpserver.MountHealth(r, func(context.Context) error { return readyErr })

	for path, want := range map[string]int{"/healthz": 200, "/readyz": 200} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != want {
			t.Fatalf("%s = %d", path, w.Code)
		}
	}
	readyErr = errors.New("db down")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if w.Code != http.StatusServiceUnavailable || strings.Contains(w.Body.String(), "db down") {
		t.Fatalf("readyz failing: %d %s", w.Code, w.Body.String())
	}
}
```

`internal/platform/httpserver/serve_test.go`:

```go
package httpserver_test

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/platform/httpserver"
)

func TestServeShutsDownGracefully(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- httpserver.Serve(ctx, ln, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}), slog.New(slog.NewJSONHandler(io.Discard, nil)))
	}()

	rq, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+ln.Addr().String(), nil)
	resp, err := http.DefaultClient.Do(rq)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return")
	}
}
```

- [ ] **Step 6: Run tests to verify they fail**

Run: `go test ./internal/platform/httpserver/`
Expected: FAIL (undefined identifiers).

- [ ] **Step 7: Implement httpserver**

`internal/platform/httpserver/middleware.go`:

```go
// Package httpserver provides the HTTP router, middleware, and server lifecycle shared by all contexts.
package httpserver

import (
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/google/uuid"

	"github.com/santoshkc2200/ioe-backend/internal/platform/logging"
	"github.com/santoshkc2200/ioe-backend/internal/platform/problem"
)

// Middleware wraps a handler.
type Middleware func(http.Handler) http.Handler

// Chain applies middleware so the first argument is the outermost.
func Chain(h http.Handler, m ...Middleware) http.Handler {
	for i := len(m) - 1; i >= 0; i-- {
		h = m[i](h)
	}
	return h
}

// RequestID propagates a well-formed X-Request-ID or generates one.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if !validRequestID(id) {
			id = uuid.NewString()
		}
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r.WithContext(logging.WithRequestID(r.Context(), id)))
	})
}

func validRequestID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, c := range id {
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.'
		if !ok {
			return false
		}
	}
	return true
}

// Recover converts panics into 500 problems and logs the stack.
func Recover(logger *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				v := recover()
				if v == nil {
					return
				}
				if v == http.ErrAbortHandler { //nolint:errorlint // sentinel compared by identity, as net/http does
					panic(v)
				}
				logger.ErrorContext(r.Context(), "panic serving request", "panic", v, "stack", string(debug.Stack()))
				problem.Write(w, r, http.StatusInternalServerError, problem.TypeInternal, "Internal Server Error", "")
			}()
			next.ServeHTTP(w, r)
		})
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	n, err := s.ResponseWriter.Write(b)
	s.bytes += n
	return n, err
}

func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// AccessLog logs one line per request. The query string is never logged.
func AccessLog(logger *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w}
			next.ServeHTTP(rec, r)
			if rec.status == 0 {
				rec.status = http.StatusOK
			}
			logger.InfoContext(r.Context(), "http request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", rec.status,
				"bytes", rec.bytes,
				"duration_ms", time.Since(start).Milliseconds(),
			)
		})
	}
}

// SecurityHeaders sets response headers appropriate for a JSON API.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		h.Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

// NoStore forbids caching of the response.
func NoStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// CORS allows credentialed requests from the listed origins only and answers their preflights.
func CORS(origins []string) Middleware {
	allowed := make(map[string]struct{}, len(origins))
	for _, o := range origins {
		allowed[o] = struct{}{}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Add("Vary", "Origin")
			origin := r.Header.Get("Origin")
			if _, ok := allowed[origin]; ok && origin != "" {
				h.Set("Access-Control-Allow-Origin", origin)
				h.Set("Access-Control-Allow-Credentials", "true")
				if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
					h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE")
					h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
					h.Set("Access-Control-Max-Age", "600")
					w.WriteHeader(http.StatusNoContent)
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// BodyLimit caps request bodies at n bytes.
func BodyLimit(n int64) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, n)
			next.ServeHTTP(w, r)
		})
	}
}
```

`internal/platform/httpserver/json.go`:

```go
package httpserver

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"

	"github.com/santoshkc2200/ioe-backend/internal/platform/problem"
)

// DecodeJSON strictly decodes exactly one JSON value into dst. Requiring application/json
// forces browsers to send a CORS preflight, which blocks cross-site form posts.
// On failure it writes a problem response and returns false.
func DecodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mt != "application/json" {
		problem.Write(w, r, http.StatusUnsupportedMediaType, problem.TypeUnsupportedMediaType,
			"Unsupported Media Type", "Content-Type must be application/json")
		return false
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return decodeFailed(w, r, err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("trailing data")
		}
		return decodeFailed(w, r, err)
	}
	return true
}

func decodeFailed(w http.ResponseWriter, r *http.Request, err error) bool {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		problem.Write(w, r, http.StatusRequestEntityTooLarge, problem.TypePayloadTooLarge, "Payload Too Large", "")
		return false
	}
	problem.Write(w, r, http.StatusBadRequest, problem.TypeInvalidRequest, "Invalid Request",
		"request body must be a single JSON object with known fields")
	return false
}

// WriteJSON sends v as a JSON response.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
```

`[]` decodes into a struct with an error (`cannot unmarshal array`), satisfying the test.

`internal/platform/httpserver/clientip.go`:

```go
package httpserver

import (
	"net/http"
	"net/netip"
	"strings"
)

// IPResolver determines the client address, trusting X-Forwarded-For only from known proxies.
type IPResolver struct {
	trusted []netip.Prefix
}

func NewIPResolver(trusted []netip.Prefix) IPResolver { return IPResolver{trusted: trusted} }

// ClientIP returns the peer address, or, when the peer is a trusted proxy, the right-most
// X-Forwarded-For entry that is not itself a trusted proxy.
func (res IPResolver) ClientIP(r *http.Request) string {
	peer := parseRemote(r.RemoteAddr)
	if !peer.IsValid() {
		return ""
	}
	if !res.isTrusted(peer) {
		return peer.String()
	}
	hops := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	for i := len(hops) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			break
		}
		if a = a.Unmap(); !res.isTrusted(a) {
			return a.String()
		}
	}
	return peer.String()
}

func (res IPResolver) isTrusted(a netip.Addr) bool {
	for _, p := range res.trusted {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

func parseRemote(remote string) netip.Addr {
	if ap, err := netip.ParseAddrPort(remote); err == nil {
		return ap.Addr().Unmap()
	}
	if a, err := netip.ParseAddr(remote); err == nil {
		return a.Unmap()
	}
	return netip.Addr{}
}
```

`internal/platform/httpserver/ratelimit.go`:

```go
package httpserver

import (
	"net/http"
	"strconv"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/santoshkc2200/ioe-backend/internal/platform/problem"
)

const (
	rateLimitSweepEvery = time.Minute
	rateLimitIdleTTL    = 10 * time.Minute
)

// RateLimiter is an in-memory, per-key token bucket. It is per process; multi-instance
// deployments need a shared limiter.
type RateLimiter struct {
	mu        sync.Mutex
	perMinute int
	buckets   map[string]*bucket
	lastSweep time.Time
}

type bucket struct {
	lim  *rate.Limiter
	seen time.Time
}

func NewRateLimiter(perMinute int) *RateLimiter {
	return &RateLimiter{perMinute: perMinute, buckets: map[string]*bucket{}, lastSweep: time.Now()}
}

// Allow reports whether one more request for key is permitted now.
func (l *RateLimiter) Allow(key string) bool {
	now := time.Now()
	l.mu.Lock()
	if now.Sub(l.lastSweep) > rateLimitSweepEvery {
		for k, b := range l.buckets {
			if now.Sub(b.seen) > rateLimitIdleTTL {
				delete(l.buckets, k)
			}
		}
		l.lastSweep = now
	}
	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{lim: rate.NewLimiter(rate.Every(time.Minute/time.Duration(l.perMinute)), l.perMinute)}
		l.buckets[key] = b
	}
	b.seen = now
	l.mu.Unlock()
	return b.lim.AllowN(now, 1)
}

// Middleware limits requests per client IP.
func (l *RateLimiter) Middleware(ips IPResolver) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !l.Allow(ips.ClientIP(r)) {
				w.Header().Set("Retry-After", strconv.Itoa(60/l.perMinute+1))
				problem.Write(w, r, http.StatusTooManyRequests, problem.TypeRateLimited, "Too Many Requests", "")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
```

`internal/platform/httpserver/router.go`:

```go
package httpserver

import (
	"log/slog"
	"net/http"

	"github.com/gorilla/mux"
	"go.opentelemetry.io/contrib/instrumentation/github.com/gorilla/mux/otelmux"

	"github.com/santoshkc2200/ioe-backend/internal/platform/problem"
)

const maxBodyBytes = 1 << 20

// Options configures NewRouter.
type Options struct {
	Logger         *slog.Logger
	AllowedOrigins []string
	ServiceName    string
}

// NewRouter returns the router for route registration and the handler to serve.
// The outer chain runs for every request, including 404 and 405. OpenTelemetry
// instrumentation runs inside the router so spans are named by route template.
func NewRouter(o Options) (*mux.Router, http.Handler) {
	r := mux.NewRouter()
	r.Use(otelmux.Middleware(o.ServiceName))
	r.NotFoundHandler = http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		problem.Write(w, req, http.StatusNotFound, problem.TypeNotFound, "Not Found", "")
	})
	r.MethodNotAllowedHandler = http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		problem.Write(w, req, http.StatusMethodNotAllowed, problem.TypeMethodNotAllowed, "Method Not Allowed", "")
	})
	h := Chain(r,
		RequestID,
		Recover(o.Logger),
		AccessLog(o.Logger),
		SecurityHeaders,
		CORS(o.AllowedOrigins),
		BodyLimit(maxBodyBytes),
	)
	return r, h
}
```

`internal/platform/httpserver/health.go`:

```go
package httpserver

import (
	"context"
	"net/http"
	"time"

	"github.com/gorilla/mux"
)

// MountHealth registers /healthz (process alive) and /readyz (dependencies reachable).
func MountHealth(r *mux.Router, ready func(context.Context) error) {
	r.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}).Methods(http.MethodGet)
	r.HandleFunc("/readyz", func(w http.ResponseWriter, req *http.Request) {
		ctx, cancel := context.WithTimeout(req.Context(), 2*time.Second)
		defer cancel()
		if err := ready(ctx); err != nil {
			WriteJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
			return
		}
		WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}).Methods(http.MethodGet)
}
```

`internal/platform/httpserver/serve.go`:

```go
package httpserver

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"
)

const shutdownTimeout = 20 * time.Second

// Serve runs h on ln until ctx is cancelled, then drains in-flight requests.
func Serve(ctx context.Context, ln net.Listener, h http.Handler, logger *slog.Logger) error {
	srv := &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil {
		return err
	}
	if err := <-errc; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
```

- [ ] **Step 8: Run tests and lint**

Run: `go mod tidy && go test -race ./internal/platform/... && make lint`
Expected: PASS; no lint issues. If otelmux names spans `"GET /v1/things/{id}"` or `"/v1/things/{id}"`, both satisfy the test.

- [ ] **Step 9: Commit**

```bash
git add -A
git commit -m "feat(platform): add HTTP router, middleware, problem responses, and server lifecycle"
```

---

### Task 5: OpenTelemetry setup

**Files:**
- Create: `internal/platform/telemetry/telemetry.go`, `internal/platform/telemetry/telemetry_test.go`

**Interfaces:**
- Produces: `telemetry.Setup(ctx context.Context, serviceVersion string) (*Telemetry, error)`; `(*Telemetry).LogHandler slog.Handler` (nil when disabled); `(*Telemetry).Shutdown(ctx) error`.

- [ ] **Step 1: Add dependencies**

```bash
go get go.opentelemetry.io/otel/sdk/metric@v1.47.0 go.opentelemetry.io/otel/sdk/log@v0.23.0 \
  go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp@v1.47.0 \
  go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp@v1.47.0 \
  go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp@v0.23.0 \
  go.opentelemetry.io/contrib/bridges/otelslog@v0.21.0 \
  go.opentelemetry.io/contrib/instrumentation/runtime@v0.72.0
```

If `go get` reports that `sdk/log` and `otlploghttp` need a different matching version, use the versions it proposes and keep them consistent with each other.

- [ ] **Step 2: Write the failing tests**

`internal/platform/telemetry/telemetry_test.go`:

```go
package telemetry_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"go.opentelemetry.io/otel"

	"github.com/santoshkc2200/ioe-backend/internal/platform/telemetry"
)

func TestSetupDisabledWithoutEndpoint(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	tel, err := telemetry.Setup(context.Background(), "test")
	if err != nil {
		t.Fatal(err)
	}
	if tel.LogHandler != nil {
		t.Fatal("log handler set while disabled")
	}
	if err := tel.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSetupDisabledFlagWins(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:1")
	t.Setenv("OTEL_SDK_DISABLED", "true")
	tel, err := telemetry.Setup(context.Background(), "test")
	if err != nil || tel.LogHandler != nil {
		t.Fatalf("tel=%+v err=%v", tel, err)
	}
}

func TestSetupExportsTracesOverOTLPHTTP(t *testing.T) {
	var mu sync.Mutex
	paths := map[string]bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths[r.URL.Path] = true
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", srv.URL+"/api/default")
	t.Setenv("OTEL_SDK_DISABLED", "")

	ctx := context.Background()
	tel, err := telemetry.Setup(ctx, "test")
	if err != nil {
		t.Fatal(err)
	}
	if tel.LogHandler == nil {
		t.Fatal("log handler missing while enabled")
	}
	_, span := otel.Tracer("test").Start(ctx, "unit")
	span.End()
	if err := tel.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !paths["/api/default/v1/traces"] {
		t.Fatalf("no trace export; saw %v", paths)
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/platform/telemetry/`
Expected: FAIL (`undefined: telemetry.Setup`).

- [ ] **Step 4: Implement**

`internal/platform/telemetry/telemetry.go`:

```go
// Package telemetry configures OpenTelemetry traces, metrics, and logs exported over OTLP/HTTP
// (OpenObserve in deployed environments). It is configured by the standard OTEL_* variables.
package telemetry

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/contrib/instrumentation/runtime"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

const defaultServiceName = "ioe-backend"

// Telemetry owns the SDK providers. LogHandler is nil when telemetry is disabled.
type Telemetry struct {
	LogHandler slog.Handler
	shutdown   []func(context.Context) error
}

// Shutdown flushes and stops all providers.
func (t *Telemetry) Shutdown(ctx context.Context) error {
	var errs []error
	for i := len(t.shutdown) - 1; i >= 0; i-- {
		errs = append(errs, t.shutdown[i](ctx))
	}
	return errors.Join(errs...)
}

// Setup installs W3C propagators and, when an OTLP endpoint is configured and
// OTEL_SDK_DISABLED is not "true", global tracer and meter providers plus a log handler.
func Setup(ctx context.Context, serviceVersion string) (*Telemetry, error) {
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	t := &Telemetry{}
	if !enabled() {
		return t, nil
	}
	res, err := resource.New(ctx,
		resource.WithAttributes(
			attribute.String("service.name", defaultServiceName),
			attribute.String("service.version", serviceVersion),
		),
		resource.WithFromEnv(),
		resource.WithTelemetrySDK(),
	)
	if err != nil {
		return nil, err
	}

	te, err := otlptracehttp.New(ctx)
	if err != nil {
		return nil, err
	}
	tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(te), sdktrace.WithResource(res))
	t.shutdown = append(t.shutdown, tp.Shutdown)

	me, err := otlpmetrichttp.New(ctx)
	if err != nil {
		return nil, errors.Join(err, t.Shutdown(ctx))
	}
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(sdkmetric.NewPeriodicReader(me)), sdkmetric.WithResource(res))
	t.shutdown = append(t.shutdown, mp.Shutdown)

	le, err := otlploghttp.New(ctx)
	if err != nil {
		return nil, errors.Join(err, t.Shutdown(ctx))
	}
	lp := sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewBatchProcessor(le)), sdklog.WithResource(res))
	t.shutdown = append(t.shutdown, lp.Shutdown)

	otel.SetTracerProvider(tp)
	otel.SetMeterProvider(mp)
	if err := runtime.Start(runtime.WithMeterProvider(mp)); err != nil {
		return nil, errors.Join(err, t.Shutdown(ctx))
	}
	t.LogHandler = otelslog.NewLogger(defaultServiceName, otelslog.WithLoggerProvider(lp)).Handler()
	return t, nil
}

func enabled() bool {
	if strings.EqualFold(os.Getenv("OTEL_SDK_DISABLED"), "true") {
		return false
	}
	for _, k := range []string{
		"OTEL_EXPORTER_OTLP_ENDPOINT",
		"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT",
		"OTEL_EXPORTER_OTLP_METRICS_ENDPOINT",
		"OTEL_EXPORTER_OTLP_LOGS_ENDPOINT",
	} {
		if os.Getenv(k) != "" {
			return true
		}
	}
	return false
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go mod tidy && go test -race ./internal/platform/telemetry/ && make lint`
Expected: PASS. If the metric or log exporter logs a harmless shutdown error against the test server (it returns empty bodies), the test still passes because only `/v1/traces` is asserted; if `Shutdown` itself returns an error from the metric/log exporter, make the test server reply with `Content-Type: application/x-protobuf` and an empty body.

- [ ] **Step 6: Commit**

```bash
git add -A
git commit -m "feat(platform): export traces, metrics, and logs via OpenTelemetry"
```

---

### Task 6: PostgreSQL pool, migrations, and integration database helper

**Files:**
- Create: `migrations/embed.go`, `migrations/00001_platform_outbox.sql`, `migrations/00002_identity.sql`
- Create: `internal/platform/postgres/postgres.go`
- Create: `internal/platform/migrate/migrate.go`, `internal/platform/migrate/migrate_integration_test.go`
- Create: `internal/platform/postgres/pgtest/pgtest.go`
- Modify: `Makefile` (add `migrate-*` targets)

**Interfaces:**
- Produces: `migrations.FS embed.FS`; `postgres.NewPool(ctx, url string) (*pgxpool.Pool, error)`; `migrate.NewProvider(db *sql.DB) (*goose.Provider, error)`; `migrate.Run(ctx, db *sql.DB, command string, w io.Writer) error` (`up`, `down`, `status`); `pgtest.New(t *testing.T) *pgxpool.Pool` (integration tag; migrated database).

- [ ] **Step 1: Start Docker and add dependencies**

Start Docker Desktop, then confirm: `docker info --format '{{.ServerVersion}}'` prints a version.

```bash
go get github.com/jackc/pgx/v5@v5.11.0 github.com/pressly/goose/v3@v3.28.0 github.com/exaring/otelpgx@v0.12.0 \
  github.com/testcontainers/testcontainers-go@v0.44.0 github.com/testcontainers/testcontainers-go/modules/postgres@v0.44.0
```

- [ ] **Step 2: Write migrations**

`migrations/embed.go`:

```go
// Package migrations embeds the goose SQL migrations for every schema.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
```

`migrations/00001_platform_outbox.sql` (table shapes match watermill-sql v4's PostgreSQL schema and offsets adapters, so the runtime role needs no DDL rights):

```sql
-- +goose Up
CREATE SCHEMA platform;

CREATE TABLE platform.outbox_messages (
  "offset"       BIGSERIAL,
  uuid           VARCHAR(36) NOT NULL,
  created_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  payload        JSON DEFAULT NULL,
  metadata       JSON DEFAULT NULL,
  transaction_id xid8 NOT NULL,
  PRIMARY KEY (transaction_id, "offset")
);

CREATE TABLE platform.outbox_offsets (
  consumer_group                VARCHAR(255) NOT NULL,
  offset_acked                  BIGINT,
  last_processed_transaction_id xid8 NOT NULL,
  PRIMARY KEY (consumer_group)
);

-- +goose Down
DROP SCHEMA platform CASCADE;
```

`migrations/00002_identity.sql`:

```sql
-- +goose Up
CREATE EXTENSION IF NOT EXISTS citext;
CREATE SCHEMA identity;

CREATE TABLE identity.users (
  id            uuid PRIMARY KEY,
  google_sub    text NOT NULL UNIQUE,
  email         citext NOT NULL,
  name          text NOT NULL DEFAULT '',
  avatar_url    text NOT NULL DEFAULT '',
  role          text NOT NULL CHECK (role IN ('student', 'instructor', 'root_admin')),
  created_at    timestamptz NOT NULL,
  updated_at    timestamptz NOT NULL,
  last_login_at timestamptz NOT NULL
);
CREATE INDEX users_email_idx ON identity.users (email);

CREATE TABLE identity.refresh_tokens (
  id                uuid PRIMARY KEY,
  user_id           uuid NOT NULL REFERENCES identity.users (id) ON DELETE CASCADE,
  family_id         uuid NOT NULL,
  token_hash        bytea NOT NULL UNIQUE,
  family_expires_at timestamptz NOT NULL,
  expires_at        timestamptz NOT NULL,
  used_at           timestamptz,
  revoked_at        timestamptz,
  created_at        timestamptz NOT NULL,
  user_agent        text NOT NULL DEFAULT '',
  ip                text NOT NULL DEFAULT ''
);
CREATE INDEX refresh_tokens_family_idx ON identity.refresh_tokens (family_id);
CREATE INDEX refresh_tokens_user_idx ON identity.refresh_tokens (user_id);

-- +goose Down
DROP SCHEMA identity CASCADE;
```

The `citext` extension is left installed on down; it is database-wide and harmless.

- [ ] **Step 3: Write the pool, migration runner, and test helper**

`internal/platform/postgres/postgres.go`:

```go
// Package postgres creates the instrumented pgx connection pool.
package postgres

import (
	"context"

	"github.com/exaring/otelpgx"
	"github.com/jackc/pgx/v5/pgxpool"
)

// NewPool connects to url with OpenTelemetry tracing (SQL text, no arguments) and pool metrics.
func NewPool(ctx context.Context, url string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, err
	}
	cfg.ConnConfig.Tracer = otelpgx.NewTracer()
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := otelpgx.RecordStats(pool); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}
```

`internal/platform/migrate/migrate.go`:

```go
// Package migrate applies the embedded goose migrations.
package migrate

import (
	"context"
	"database/sql"
	"fmt"
	"io"

	"github.com/pressly/goose/v3"

	"github.com/santoshkc2200/ioe-backend/migrations"
)

// NewProvider returns a goose provider over the embedded migrations.
func NewProvider(db *sql.DB) (*goose.Provider, error) {
	return goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
}

// Run executes "up", "down" (one step), or "status", writing results to w.
func Run(ctx context.Context, db *sql.DB, command string, w io.Writer) error {
	p, err := NewProvider(db)
	if err != nil {
		return err
	}
	switch command {
	case "up":
		results, err := p.Up(ctx)
		for _, r := range results {
			fmt.Fprintln(w, r)
		}
		return err
	case "down":
		r, err := p.Down(ctx)
		if r != nil {
			fmt.Fprintln(w, r)
		}
		return err
	case "status":
		statuses, err := p.Status(ctx)
		if err != nil {
			return err
		}
		for _, s := range statuses {
			fmt.Fprintf(w, "%-10s %s\n", s.State, s.Source.Path)
		}
		return nil
	default:
		return fmt.Errorf("unknown migrate command %q (want up, down, or status)", command)
	}
}
```

`internal/platform/postgres/pgtest/pgtest.go`:

```go
//go:build integration

// Package pgtest starts a disposable, fully migrated PostgreSQL for integration tests.
package pgtest

import (
	"context"
	"io"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/santoshkc2200/ioe-backend/internal/platform/migrate"
)

// New starts PostgreSQL 17, applies all migrations, and returns a pool closed at test end.
func New(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	ctr, err := tcpostgres.Run(ctx, "postgres:17-alpine",
		tcpostgres.WithDatabase("ioe"),
		tcpostgres.WithUsername("ioe"),
		tcpostgres.WithPassword("ioe"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(ctr) })

	url, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	db := stdlib.OpenDBFromPool(pool)
	t.Cleanup(func() { _ = db.Close() })
	if err := migrate.Run(ctx, db, "up", io.Discard); err != nil {
		t.Fatalf("migrate up: %v", err)
	}
	return pool
}
```

- [ ] **Step 4: Write the integration test**

`internal/platform/migrate/migrate_integration_test.go`:

```go
//go:build integration

package migrate_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/santoshkc2200/ioe-backend/internal/platform/migrate"
	"github.com/santoshkc2200/ioe-backend/internal/platform/postgres/pgtest"
)

func tableExists(t *testing.T, pool *pgxpool.Pool, name string) bool {
	t.Helper()
	var exists bool
	if err := pool.QueryRow(context.Background(), "SELECT to_regclass($1) IS NOT NULL", name).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	return exists
}

func TestMigrationsUpDownUp(t *testing.T) {
	ctx := context.Background()
	pool := pgtest.New(t)
	for _, tbl := range []string{"platform.outbox_messages", "platform.outbox_offsets", "identity.users", "identity.refresh_tokens"} {
		if !tableExists(t, pool, tbl) {
			t.Fatalf("%s missing after up", tbl)
		}
	}

	p, err := migrate.NewProvider(stdlib.OpenDBFromPool(pool))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.DownTo(ctx, 0); err != nil {
		t.Fatalf("down to 0: %v", err)
	}
	if tableExists(t, pool, "identity.users") || tableExists(t, pool, "platform.outbox_messages") {
		t.Fatal("tables remain after full down")
	}
	if _, err := p.Up(ctx); err != nil {
		t.Fatalf("up again: %v", err)
	}
	if !tableExists(t, pool, "identity.refresh_tokens") {
		t.Fatal("identity.refresh_tokens missing after re-up")
	}
}
```

- [ ] **Step 5: Run the integration test**

Run: `go mod tidy && go test -race -tags integration ./internal/platform/migrate/`
Expected: PASS (pulls `postgres:17-alpine` on first run).

- [ ] **Step 6: Add Makefile targets**

Append to `Makefile` and add the names to `.PHONY`:

```make
migrate-up:
	go run ./cmd/api migrate up

migrate-down:
	go run ./cmd/api migrate down

migrate-status:
	go run ./cmd/api migrate status
```

(`cmd/api` arrives in Task 14; these targets work from then on.)

- [ ] **Step 7: Lint and commit**

Run: `make lint test`
Expected: no issues; unit tests PASS.

```bash
git add -A
git commit -m "feat(platform): add PostgreSQL pool, goose migrations, and integration DB helper"
```

---

### Task 7: Transactional outbox and forwarder

**Files:**
- Create: `internal/platform/outbox/outbox.go`, `internal/platform/outbox/outbox_integration_test.go`

**Interfaces:**
- Consumes: `pgtest.New` (Task 6), migrations `platform.outbox_messages`, `platform.outbox_offsets`.
- Produces: `outbox.Publish(ctx context.Context, tx pgx.Tx, topic string, msgs ...*message.Message) error`; `outbox.NewForwarder(pool *pgxpool.Pool, logger *slog.Logger) (*Forwarder, error)`; `(*Forwarder).Run(ctx) error`; `(*Forwarder).Close() error`; `(*Forwarder).Subscriber() message.Subscriber`.

- [ ] **Step 1: Add dependencies**

```bash
go get github.com/ThreeDotsLabs/watermill@v1.5.3 github.com/ThreeDotsLabs/watermill-sql/v4@v4.1.5
```

- [ ] **Step 2: Write the failing integration test**

`internal/platform/outbox/outbox_integration_test.go`:

```go
//go:build integration

package outbox_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/santoshkc2200/ioe-backend/internal/platform/outbox"
	"github.com/santoshkc2200/ioe-backend/internal/platform/postgres/pgtest"
)

func TestRolledBackPublishWritesNothing(t *testing.T) {
	ctx := context.Background()
	pool := pgtest.New(t)
	errAbort := errors.New("abort")
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if err := outbox.Publish(ctx, tx, "test.topic", message.NewMessage("m1", []byte(`{}`))); err != nil {
			return err
		}
		return errAbort
	})
	if !errors.Is(err, errAbort) {
		t.Fatalf("err = %v", err)
	}
	var n int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM platform.outbox_messages").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("outbox rows = %d after rollback", n)
	}
}

func TestCommittedPublishIsForwardedWithTraceContext(t *testing.T) {
	otel.SetTextMapPropagator(propagation.TraceContext{})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := pgtest.New(t)

	fw, err := outbox.NewForwarder(pool, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer fw.Close()
	msgs, err := fw.Subscriber().Subscribe(ctx, "test.topic")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = fw.Run(ctx) }()

	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    trace.TraceID{0xaa, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15},
		SpanID:     trace.SpanID{1, 2, 3, 4, 5, 6, 7, 8},
		TraceFlags: trace.FlagsSampled,
	})
	pubCtx := trace.ContextWithSpanContext(ctx, sc)
	if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		return outbox.Publish(pubCtx, tx, "test.topic", message.NewMessage("m2", []byte(`{"ok":true}`)))
	}); err != nil {
		t.Fatal(err)
	}

	select {
	case m := <-msgs:
		m.Ack()
		if string(m.Payload) != `{"ok":true}` {
			t.Fatalf("payload = %s", m.Payload)
		}
		if tp := m.Metadata.Get("traceparent"); !strings.Contains(tp, sc.TraceID().String()) {
			t.Fatalf("traceparent = %q", tp)
		}
	case <-ctx.Done():
		t.Fatal("message not forwarded")
	}
}
```

- [ ] **Step 3: Run it to verify it fails**

Run: `go test -tags integration ./internal/platform/outbox/`
Expected: FAIL (`undefined: outbox.Publish`).

- [ ] **Step 4: Implement**

`internal/platform/outbox/outbox.go`:

```go
// Package outbox implements the transactional outbox: messages are written in the caller's
// PostgreSQL transaction and forwarded to in-process subscribers after commit.
package outbox

import (
	"context"
	"errors"
	"log/slog"

	"github.com/ThreeDotsLabs/watermill"
	wsql "github.com/ThreeDotsLabs/watermill-sql/v4/pkg/sql"
	"github.com/ThreeDotsLabs/watermill/components/forwarder"
	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/ThreeDotsLabs/watermill/pubsub/gochannel"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

const (
	forwarderTopic = "outbox"
	consumerGroup  = "forwarder"
)

func schemaAdapter() wsql.DefaultPostgreSQLSchema {
	return wsql.DefaultPostgreSQLSchema{
		GenerateMessagesTableName: func(string) string { return "platform.outbox_messages" },
	}
}

func offsetsAdapter() wsql.DefaultPostgreSQLOffsetsAdapter {
	return wsql.DefaultPostgreSQLOffsetsAdapter{
		GenerateMessagesOffsetsTableName: func(string) string { return "platform.outbox_offsets" },
	}
}

// Publish stores msgs for topic inside tx. They are delivered only if tx commits.
// The trace context of ctx is stored in each message's metadata.
func Publish(ctx context.Context, tx pgx.Tx, topic string, msgs ...*message.Message) error {
	pub, err := wsql.NewPublisher(wsql.TxFromPgx(tx), wsql.PublisherConfig{SchemaAdapter: schemaAdapter()}, watermill.NopLogger{})
	if err != nil {
		return err
	}
	prop := otel.GetTextMapPropagator()
	for _, m := range msgs {
		m.SetContext(ctx)
		prop.Inject(ctx, propagation.MapCarrier(m.Metadata))
	}
	return forwarder.NewPublisher(pub, forwarder.PublisherConfig{ForwarderTopic: forwarderTopic}).Publish(topic, msgs...)
}

// Forwarder moves committed outbox messages to an in-process pub/sub.
type Forwarder struct {
	fw  *forwarder.Forwarder
	out *gochannel.GoChannel
}

func NewForwarder(pool *pgxpool.Pool, logger *slog.Logger) (*Forwarder, error) {
	wl := watermill.NewSlogLogger(logger)
	sub, err := wsql.NewSubscriber(wsql.BeginnerFromPgx(pool), wsql.SubscriberConfig{
		ConsumerGroup:  consumerGroup,
		SchemaAdapter:  schemaAdapter(),
		OffsetsAdapter: offsetsAdapter(),
	}, wl)
	if err != nil {
		return nil, err
	}
	out := gochannel.NewGoChannel(gochannel.Config{}, wl)
	fw, err := forwarder.NewForwarder(sub, out, wl, forwarder.Config{ForwarderTopic: forwarderTopic})
	if err != nil {
		return nil, errors.Join(err, out.Close())
	}
	return &Forwarder{fw: fw, out: out}, nil
}

// Run forwards messages until ctx is cancelled or Close is called.
func (f *Forwarder) Run(ctx context.Context) error { return f.fw.Run(ctx) }

func (f *Forwarder) Close() error { return errors.Join(f.fw.Close(), f.out.Close()) }

// Subscriber exposes forwarded messages to in-process consumers.
func (f *Forwarder) Subscriber() message.Subscriber { return f.out }
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go mod tidy && go test -race -tags integration ./internal/platform/outbox/ && make lint`
Expected: PASS; no lint issues.

- [ ] **Step 6: Commit**

```bash
git add -A
git commit -m "feat(platform): add transactional outbox and forwarder"
```

---
### Task 8: Identity domain

**Files:**
- Create: `internal/identity/domain/user.go`, `internal/identity/domain/refresh_token.go`, `internal/identity/domain/events.go`
- Test: `internal/identity/domain/domain_test.go`

**Interfaces:**
- Consumes: `auth.Role` (Task 1).
- Produces:
  - `domain.GoogleIdentity{Subject, Email string; EmailVerified bool; Name, Picture string}`
  - `domain.User{ID uuid.UUID; GoogleSubject, Email, Name, AvatarURL string; Role auth.Role; CreatedAt, UpdatedAt, LastLoginAt time.Time}`, `domain.NewUser(id uuid.UUID, g GoogleIdentity, now time.Time) User`, `(*User).RecordLogin(g GoogleIdentity, now time.Time)`, `(*User).PromoteToRootAdmin(now time.Time)`
  - `domain.RefreshIdleLifetime`, `domain.RefreshAbsoluteLifetime`
  - `domain.RefreshToken{ID, UserID, FamilyID uuid.UUID; TokenHash []byte; FamilyExpiresAt, ExpiresAt time.Time; UsedAt, RevokedAt *time.Time; CreatedAt time.Time; UserAgent, IP string}`, `domain.NewRefreshFamily(id, familyID, userID uuid.UUID, hash []byte, now time.Time, userAgent, ip string) RefreshToken`, `(RefreshToken).Check(now time.Time) error`, `(RefreshToken).Successor(id uuid.UUID, hash []byte, now time.Time, userAgent, ip string) RefreshToken`
  - `domain.ErrRefreshExpired`, `domain.ErrRefreshRevoked`, `domain.ErrRefreshReused`
  - `domain.Event` (`EventName() string`), `domain.UserRegistered{UserID uuid.UUID; Email string; OccurredAt time.Time}` with event name `identity.user_registered`

- [ ] **Step 1: Write the failing tests**

`internal/identity/domain/domain_test.go`:

```go
package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/santoshkc2200/ioe-backend/internal/identity/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
)

var t0 = time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)

func TestNewUserIsStudent(t *testing.T) {
	id := uuid.New()
	u := domain.NewUser(id, domain.GoogleIdentity{Subject: "s", Email: "a@example.com", Name: "A", Picture: "p"}, t0)
	if u.ID != id || u.Role != auth.RoleStudent || u.GoogleSubject != "s" || u.AvatarURL != "p" {
		t.Fatalf("%+v", u)
	}
	if !u.CreatedAt.Equal(t0) || !u.UpdatedAt.Equal(t0) || !u.LastLoginAt.Equal(t0) {
		t.Fatalf("timestamps %+v", u)
	}
}

func TestRecordLoginUpdatesProfileOnly(t *testing.T) {
	u := domain.NewUser(uuid.New(), domain.GoogleIdentity{Subject: "s", Email: "a@example.com"}, t0)
	later := t0.Add(time.Hour)
	u.RecordLogin(domain.GoogleIdentity{Subject: "s", Email: "b@example.com", Name: "B", Picture: "q"}, later)
	if u.Email != "b@example.com" || u.Name != "B" || u.AvatarURL != "q" || u.Role != auth.RoleStudent {
		t.Fatalf("%+v", u)
	}
	if !u.LastLoginAt.Equal(later) || !u.CreatedAt.Equal(t0) {
		t.Fatalf("timestamps %+v", u)
	}
}

func TestPromoteToRootAdmin(t *testing.T) {
	u := domain.NewUser(uuid.New(), domain.GoogleIdentity{Subject: "s"}, t0)
	u.PromoteToRootAdmin(t0.Add(time.Minute))
	if u.Role != auth.RoleRootAdmin || !u.UpdatedAt.Equal(t0.Add(time.Minute)) {
		t.Fatalf("%+v", u)
	}
}

func TestRefreshFamilyLifetimes(t *testing.T) {
	tok := domain.NewRefreshFamily(uuid.New(), uuid.New(), uuid.New(), []byte("h"), t0, "ua", "ip")
	if !tok.ExpiresAt.Equal(t0.Add(domain.RefreshIdleLifetime)) {
		t.Fatalf("ExpiresAt %v", tok.ExpiresAt)
	}
	if !tok.FamilyExpiresAt.Equal(t0.Add(domain.RefreshAbsoluteLifetime)) {
		t.Fatalf("FamilyExpiresAt %v", tok.FamilyExpiresAt)
	}

	nearEnd := tok.FamilyExpiresAt.Add(-24 * time.Hour)
	next := tok.Successor(uuid.New(), []byte("h2"), nearEnd, "ua", "ip")
	if next.FamilyID != tok.FamilyID || next.UserID != tok.UserID {
		t.Fatalf("successor left family: %+v", next)
	}
	if !next.ExpiresAt.Equal(tok.FamilyExpiresAt) {
		t.Fatalf("successor not capped by family: %v", next.ExpiresAt)
	}
}

func TestRefreshCheck(t *testing.T) {
	base := domain.NewRefreshFamily(uuid.New(), uuid.New(), uuid.New(), []byte("h"), t0, "", "")
	used, revoked := base, base
	at := t0.Add(time.Minute)
	used.UsedAt = &at
	revoked.RevokedAt = &at
	revokedAndUsed := used
	revokedAndUsed.RevokedAt = &at

	cases := []struct {
		name string
		tok  domain.RefreshToken
		now  time.Time
		want error
	}{
		{"fresh", base, t0.Add(time.Hour), nil},
		{"expired", base, base.ExpiresAt, domain.ErrRefreshExpired},
		{"used", used, t0.Add(time.Hour), domain.ErrRefreshReused},
		{"revoked", revoked, t0.Add(time.Hour), domain.ErrRefreshRevoked},
		{"revoked wins over used", revokedAndUsed, t0.Add(time.Hour), domain.ErrRefreshRevoked},
	}
	for _, c := range cases {
		if err := c.tok.Check(c.now); !errors.Is(err, c.want) {
			t.Errorf("%s: got %v want %v", c.name, err, c.want)
		}
	}
}

func TestUserRegisteredName(t *testing.T) {
	var e domain.Event = domain.UserRegistered{}
	if e.EventName() != "identity.user_registered" {
		t.Fatal(e.EventName())
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/identity/domain/`
Expected: FAIL (package has no non-test files / undefined identifiers).

- [ ] **Step 3: Implement**

`internal/identity/domain/user.go`:

```go
// Package domain holds identity entities and rules. It has no infrastructure dependencies.
package domain

import (
	"time"

	"github.com/google/uuid"

	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
)

// GoogleIdentity is the verified content of a Google ID token.
type GoogleIdentity struct {
	Subject       string
	Email         string
	EmailVerified bool
	Name          string
	Picture       string
}

// User is an application account, keyed by Google subject.
type User struct {
	ID            uuid.UUID
	GoogleSubject string
	Email         string
	Name          string
	AvatarURL     string
	Role          auth.Role
	CreatedAt     time.Time
	UpdatedAt     time.Time
	LastLoginAt   time.Time
}

// NewUser creates a student account from a verified Google identity.
func NewUser(id uuid.UUID, g GoogleIdentity, now time.Time) User {
	return User{
		ID:            id,
		GoogleSubject: g.Subject,
		Email:         g.Email,
		Name:          g.Name,
		AvatarURL:     g.Picture,
		Role:          auth.RoleStudent,
		CreatedAt:     now,
		UpdatedAt:     now,
		LastLoginAt:   now,
	}
}

// RecordLogin refreshes profile fields from Google. The account stays keyed by subject.
func (u *User) RecordLogin(g GoogleIdentity, now time.Time) {
	u.Email = g.Email
	u.Name = g.Name
	u.AvatarURL = g.Picture
	u.LastLoginAt = now
	u.UpdatedAt = now
}

// PromoteToRootAdmin grants the root_admin role.
func (u *User) PromoteToRootAdmin(now time.Time) {
	if u.Role == auth.RoleRootAdmin {
		return
	}
	u.Role = auth.RoleRootAdmin
	u.UpdatedAt = now
}
```

`internal/identity/domain/refresh_token.go`:

```go
package domain

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

const (
	// RefreshIdleLifetime is how long a refresh token stays valid without use.
	RefreshIdleLifetime = 7 * 24 * time.Hour
	// RefreshAbsoluteLifetime caps a token family from its first sign-in.
	RefreshAbsoluteLifetime = 30 * 24 * time.Hour
)

var (
	ErrRefreshExpired = errors.New("refresh token expired")
	ErrRefreshRevoked = errors.New("refresh token revoked")
	ErrRefreshReused  = errors.New("refresh token already used")
)

// RefreshToken is one link in a rotating refresh-token family. Only the hash is stored.
type RefreshToken struct {
	ID              uuid.UUID
	UserID          uuid.UUID
	FamilyID        uuid.UUID
	TokenHash       []byte
	FamilyExpiresAt time.Time
	ExpiresAt       time.Time
	UsedAt          *time.Time
	RevokedAt       *time.Time
	CreatedAt       time.Time
	UserAgent       string
	IP              string
}

// NewRefreshFamily starts a family at sign-in.
func NewRefreshFamily(id, familyID, userID uuid.UUID, hash []byte, now time.Time, userAgent, ip string) RefreshToken {
	familyExpiresAt := now.Add(RefreshAbsoluteLifetime)
	return RefreshToken{
		ID:              id,
		UserID:          userID,
		FamilyID:        familyID,
		TokenHash:       hash,
		FamilyExpiresAt: familyExpiresAt,
		ExpiresAt:       earlier(familyExpiresAt, now.Add(RefreshIdleLifetime)),
		CreatedAt:       now,
		UserAgent:       userAgent,
		IP:              ip,
	}
}

// Check reports why the token cannot be exchanged now, or nil. Revocation is checked first
// so a token from an already revoked family never triggers reuse handling again.
func (t RefreshToken) Check(now time.Time) error {
	switch {
	case t.RevokedAt != nil:
		return ErrRefreshRevoked
	case t.UsedAt != nil:
		return ErrRefreshReused
	case !now.Before(t.ExpiresAt):
		return ErrRefreshExpired
	default:
		return nil
	}
}

// Successor returns the rotated token in the same family.
func (t RefreshToken) Successor(id uuid.UUID, hash []byte, now time.Time, userAgent, ip string) RefreshToken {
	return RefreshToken{
		ID:              id,
		UserID:          t.UserID,
		FamilyID:        t.FamilyID,
		TokenHash:       hash,
		FamilyExpiresAt: t.FamilyExpiresAt,
		ExpiresAt:       earlier(t.FamilyExpiresAt, now.Add(RefreshIdleLifetime)),
		CreatedAt:       now,
		UserAgent:       userAgent,
		IP:              ip,
	}
}

func earlier(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
```

`internal/identity/domain/events.go`:

```go
package domain

import (
	"time"

	"github.com/google/uuid"
)

// Event is a domain event published through the outbox.
type Event interface {
	EventName() string
}

// UserRegistered is emitted once, when a Google account first signs in.
type UserRegistered struct {
	UserID     uuid.UUID `json:"user_id"`
	Email      string    `json:"email"`
	OccurredAt time.Time `json:"occurred_at"`
}

func (UserRegistered) EventName() string { return "identity.user_registered" }
```

- [ ] **Step 4: Run tests and lint**

Run: `go test ./internal/identity/domain/ && make lint`
Expected: PASS; no issues (depguard allows these imports).

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "feat(identity): add user and refresh-token domain model"
```

---

### Task 9: Identity application service

**Files:**
- Create: `internal/identity/app/ports.go`, `internal/identity/app/service.go`, `internal/identity/app/secret.go`
- Test: `internal/identity/app/fakes_test.go`, `internal/identity/app/service_test.go`

**Interfaces:**
- Consumes: domain types (Task 8), `auth.Role`, `clock.Clock`.
- Produces:
  - Errors: `app.ErrInvalidToken`, `app.ErrRefreshReuse`, `app.ErrEmailUnverified`, `app.ErrNotFound`, `app.ErrConflict`.
  - Ports: `app.UserRepository{FindByGoogleSubject(ctx, string) (domain.User, error); FindByID(ctx, uuid.UUID) (domain.User, error); Insert(ctx, domain.User) error; Update(ctx, domain.User) error}`; `app.RefreshTokenRepository{Insert(ctx, domain.RefreshToken) error; FindByHashForUpdate(ctx, []byte) (domain.RefreshToken, error); MarkUsed(ctx, uuid.UUID, time.Time) error; RevokeFamily(ctx, uuid.UUID, time.Time) error}`; `app.EventPublisher{Publish(ctx, ...domain.Event) error}`; `app.Repos{Users; Tokens; Events}`; `app.TxRunner{RunInTx(ctx, func(Repos) error) error}`; `app.GoogleVerifier{Verify(ctx, string) (domain.GoogleIdentity, error)}`; `app.AccessTokenIssuer{Issue(uuid.UUID, auth.Role) (string, time.Duration, error)}`.
  - `app.Client{UserAgent, IP string}`; `app.Session{AccessToken string; AccessTokenTTL time.Duration; RefreshToken string; RefreshTokenTTL time.Duration; User domain.User; Created bool}`.
  - `app.NewService(tx TxRunner, google GoogleVerifier, tokens AccessTokenIssuer, c clock.Clock, rootAdminEmails []string) *Service`; methods `SignInWithGoogle(ctx, idToken string, client Client) (Session, error)`, `Refresh(ctx, raw string, client Client) (Session, error)`, `Logout(ctx, raw string) error`, `GetMe(ctx, userID uuid.UUID) (domain.User, error)`.
  - Repository contract: `ErrNotFound` for missing rows; `ErrConflict` for a duplicate Google subject on `Insert`; `FindByHashForUpdate` locks the row until the transaction ends.

- [ ] **Step 1: Write the fakes**

`internal/identity/app/fakes_test.go`:

```go
package app_test

import (
	"bytes"
	"context"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/santoshkc2200/ioe-backend/internal/identity/app"
	"github.com/santoshkc2200/ioe-backend/internal/identity/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
)

type memState struct {
	users  map[uuid.UUID]domain.User
	tokens map[uuid.UUID]domain.RefreshToken
	events []domain.Event
}

func (s *memState) clone() *memState {
	return &memState{users: maps.Clone(s.users), tokens: maps.Clone(s.tokens), events: slices.Clone(s.events)}
}

// memStore commits a transaction's changes only when fn succeeds, like a real database.
type memStore struct {
	mu            sync.Mutex
	state         *memState
	conflictsLeft int
}

func newMemStore() *memStore {
	return &memStore{state: &memState{users: map[uuid.UUID]domain.User{}, tokens: map[uuid.UUID]domain.RefreshToken{}}}
}

func (m *memStore) RunInTx(_ context.Context, fn func(app.Repos) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	tx := m.state.clone()
	if err := fn(app.Repos{Users: &memUsers{s: tx, store: m}, Tokens: memTokens{s: tx}, Events: memEvents{s: tx}}); err != nil {
		return err
	}
	m.state = tx
	return nil
}

func (m *memStore) snapshot() *memState {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state.clone()
}

func (m *memStore) setRole(id uuid.UUID, r auth.Role) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u := m.state.users[id]
	u.Role = r
	m.state.users[id] = u
}

type memUsers struct {
	s     *memState
	store *memStore
}

func (u *memUsers) FindByGoogleSubject(_ context.Context, sub string) (domain.User, error) {
	for _, x := range u.s.users {
		if x.GoogleSubject == sub {
			return x, nil
		}
	}
	return domain.User{}, app.ErrNotFound
}

func (u *memUsers) FindByID(_ context.Context, id uuid.UUID) (domain.User, error) {
	x, ok := u.s.users[id]
	if !ok {
		return domain.User{}, app.ErrNotFound
	}
	return x, nil
}

func (u *memUsers) Insert(_ context.Context, x domain.User) error {
	if u.store.conflictsLeft > 0 {
		u.store.conflictsLeft--
		return app.ErrConflict
	}
	for _, e := range u.s.users {
		if e.GoogleSubject == x.GoogleSubject {
			return app.ErrConflict
		}
	}
	u.s.users[x.ID] = x
	return nil
}

func (u *memUsers) Update(_ context.Context, x domain.User) error {
	if _, ok := u.s.users[x.ID]; !ok {
		return app.ErrNotFound
	}
	u.s.users[x.ID] = x
	return nil
}

type memTokens struct{ s *memState }

func (m memTokens) Insert(_ context.Context, t domain.RefreshToken) error {
	m.s.tokens[t.ID] = t
	return nil
}

func (m memTokens) FindByHashForUpdate(_ context.Context, hash []byte) (domain.RefreshToken, error) {
	for _, t := range m.s.tokens {
		if bytes.Equal(t.TokenHash, hash) {
			return t, nil
		}
	}
	return domain.RefreshToken{}, app.ErrNotFound
}

func (m memTokens) MarkUsed(_ context.Context, id uuid.UUID, at time.Time) error {
	t := m.s.tokens[id]
	t.UsedAt = &at
	m.s.tokens[id] = t
	return nil
}

func (m memTokens) RevokeFamily(_ context.Context, family uuid.UUID, at time.Time) error {
	for id, t := range m.s.tokens {
		if t.FamilyID == family && t.RevokedAt == nil {
			revokedAt := at
			t.RevokedAt = &revokedAt
			m.s.tokens[id] = t
		}
	}
	return nil
}

type memEvents struct{ s *memState }

func (m memEvents) Publish(_ context.Context, evs ...domain.Event) error {
	m.s.events = append(m.s.events, evs...)
	return nil
}

type fakeGoogle map[string]domain.GoogleIdentity

func (f fakeGoogle) Verify(_ context.Context, token string) (domain.GoogleIdentity, error) {
	id, ok := f[token]
	if !ok {
		return domain.GoogleIdentity{}, fmt.Errorf("%w: unknown test token", app.ErrInvalidToken)
	}
	return id, nil
}

type fakeIssuer struct{}

func (fakeIssuer) Issue(id uuid.UUID, r auth.Role) (string, time.Duration, error) {
	return "access:" + id.String() + ":" + string(r), 15 * time.Minute, nil
}
```

- [ ] **Step 2: Write the failing service tests**

`internal/identity/app/service_test.go`:

```go
package app_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/santoshkc2200/ioe-backend/internal/identity/app"
	"github.com/santoshkc2200/ioe-backend/internal/identity/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/clock"
)

var (
	t0     = time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	ctx    = context.Background()
	client = app.Client{UserAgent: "test-agent", IP: "203.0.113.5"}
	google = fakeGoogle{
		"alice":           {Subject: "sub-alice", Email: "alice@example.com", EmailVerified: true, Name: "Alice", Picture: "https://img/alice"},
		"alice-new-email": {Subject: "sub-alice", Email: "alice@new.example", EmailVerified: true, Name: "Alice B"},
		"admin":           {Subject: "sub-admin", Email: "Admin@Example.com", EmailVerified: true},
		"unverified":      {Subject: "sub-bob", Email: "bob@example.com", EmailVerified: false},
	}
)

type fixture struct {
	store *memStore
	clock *clock.Fake
	svc   *app.Service
}

func newFixture(rootAdmins ...string) *fixture {
	f := &fixture{store: newMemStore(), clock: clock.NewFake(t0)}
	f.svc = app.NewService(f.store, google, fakeIssuer{}, f.clock, rootAdmins)
	return f
}

func (f *fixture) signIn(t *testing.T, token string) app.Session {
	t.Helper()
	s, err := f.svc.SignInWithGoogle(ctx, token, client)
	if err != nil {
		t.Fatalf("sign in %q: %v", token, err)
	}
	return s
}

func (f *fixture) refresh(t *testing.T, raw string) app.Session {
	t.Helper()
	s, err := f.svc.Refresh(ctx, raw, client)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	return s
}

func TestSignInCreatesStudentAndPublishesEvent(t *testing.T) {
	f := newFixture()
	s := f.signIn(t, "alice")
	if !s.Created || s.User.Role != auth.RoleStudent || s.User.Email != "alice@example.com" {
		t.Fatalf("%+v", s)
	}
	if s.AccessToken != "access:"+s.User.ID.String()+":student" || s.AccessTokenTTL != 15*time.Minute {
		t.Fatalf("access %q %v", s.AccessToken, s.AccessTokenTTL)
	}
	if s.RefreshToken == "" || s.RefreshTokenTTL != domain.RefreshIdleLifetime {
		t.Fatalf("refresh %q %v", s.RefreshToken, s.RefreshTokenTTL)
	}
	st := f.store.snapshot()
	if len(st.events) != 1 {
		t.Fatalf("events = %d", len(st.events))
	}
	if ev, ok := st.events[0].(domain.UserRegistered); !ok || ev.UserID != s.User.ID || !ev.OccurredAt.Equal(t0) {
		t.Fatalf("event %+v", st.events[0])
	}
	if len(st.tokens) != 1 {
		t.Fatalf("tokens = %d", len(st.tokens))
	}
	for _, tok := range st.tokens {
		if bytes.Contains(tok.TokenHash, []byte(s.RefreshToken)) || len(tok.TokenHash) != 32 {
			t.Fatal("refresh token stored in plaintext or wrong hash size")
		}
		if tok.UserAgent != "test-agent" || tok.IP != "203.0.113.5" {
			t.Fatalf("client metadata %+v", tok)
		}
	}
}

func TestSignInExistingUserMatchedBySubject(t *testing.T) {
	f := newFixture()
	first := f.signIn(t, "alice")
	f.clock.Advance(time.Hour)
	second := f.signIn(t, "alice-new-email")
	if second.Created || second.User.ID != first.User.ID || second.User.Email != "alice@new.example" {
		t.Fatalf("%+v", second.User)
	}
	if !second.User.LastLoginAt.Equal(t0.Add(time.Hour)) || !second.User.CreatedAt.Equal(t0) {
		t.Fatalf("timestamps %+v", second.User)
	}
	st := f.store.snapshot()
	if len(st.users) != 1 || len(st.events) != 1 {
		t.Fatalf("users=%d events=%d", len(st.users), len(st.events))
	}
}

func TestSignInBootstrapsRootAdminCaseInsensitively(t *testing.T) {
	f := newFixture(" admin@example.com ")
	s := f.signIn(t, "admin")
	if s.User.Role != auth.RoleRootAdmin || !strings.HasSuffix(s.AccessToken, ":root_admin") {
		t.Fatalf("%+v %q", s.User, s.AccessToken)
	}
}

func TestExistingUserPromotedWhenAddedToAllowlist(t *testing.T) {
	f := newFixture()
	if s := f.signIn(t, "admin"); s.User.Role != auth.RoleStudent {
		t.Fatalf("role %s", s.User.Role)
	}
	svc := app.NewService(f.store, google, fakeIssuer{}, f.clock, []string{"admin@example.com"})
	s, err := svc.SignInWithGoogle(ctx, "admin", client)
	if err != nil || s.User.Role != auth.RoleRootAdmin {
		t.Fatalf("%+v %v", s.User, err)
	}
}

func TestRootAdminKeptWhenRemovedFromAllowlist(t *testing.T) {
	f := newFixture("admin@example.com")
	f.signIn(t, "admin")
	svc := app.NewService(f.store, google, fakeIssuer{}, f.clock, nil)
	s, err := svc.SignInWithGoogle(ctx, "admin", client)
	if err != nil || s.User.Role != auth.RoleRootAdmin {
		t.Fatalf("%+v %v", s.User, err)
	}
}

func TestSignInRejectsUnverifiedEmail(t *testing.T) {
	f := newFixture()
	_, err := f.svc.SignInWithGoogle(ctx, "unverified", client)
	if !errors.Is(err, app.ErrEmailUnverified) {
		t.Fatalf("err = %v", err)
	}
	if n := len(f.store.snapshot().users); n != 0 {
		t.Fatalf("users = %d", n)
	}
}

func TestSignInRejectsInvalidToken(t *testing.T) {
	f := newFixture()
	if _, err := f.svc.SignInWithGoogle(ctx, "forged", client); !errors.Is(err, app.ErrInvalidToken) {
		t.Fatalf("err = %v", err)
	}
}

func TestSignInRetriesOnceOnConflict(t *testing.T) {
	f := newFixture()
	f.store.conflictsLeft = 1
	s := f.signIn(t, "alice")
	if !s.Created || len(f.store.snapshot().users) != 1 {
		t.Fatalf("%+v", s)
	}
}

func TestSignInGivesUpAfterSecondConflict(t *testing.T) {
	f := newFixture()
	f.store.conflictsLeft = 2
	if _, err := f.svc.SignInWithGoogle(ctx, "alice", client); !errors.Is(err, app.ErrConflict) {
		t.Fatalf("err = %v", err)
	}
}

func TestSignInTruncatesUserAgent(t *testing.T) {
	f := newFixture()
	if _, err := f.svc.SignInWithGoogle(ctx, "alice", app.Client{UserAgent: strings.Repeat("x", 1000)}); err != nil {
		t.Fatal(err)
	}
	for _, tok := range f.store.snapshot().tokens {
		if len(tok.UserAgent) != 512 {
			t.Fatalf("user agent length %d", len(tok.UserAgent))
		}
	}
}

func TestRefreshRotates(t *testing.T) {
	f := newFixture()
	s := f.signIn(t, "alice")
	f.clock.Advance(time.Minute)
	r := f.refresh(t, s.RefreshToken)
	if r.RefreshToken == s.RefreshToken || r.User.ID != s.User.ID || r.AccessToken == "" {
		t.Fatalf("%+v", r)
	}
	f.refresh(t, r.RefreshToken)
}

func TestRefreshUsesCurrentRole(t *testing.T) {
	f := newFixture()
	s := f.signIn(t, "alice")
	f.store.setRole(s.User.ID, auth.RoleInstructor)
	if r := f.refresh(t, s.RefreshToken); !strings.HasSuffix(r.AccessToken, ":instructor") {
		t.Fatalf("access %q", r.AccessToken)
	}
}

func TestRefreshReuseRevokesFamily(t *testing.T) {
	f := newFixture()
	s := f.signIn(t, "alice")
	r1 := f.refresh(t, s.RefreshToken)

	if _, err := f.svc.Refresh(ctx, s.RefreshToken, client); !errors.Is(err, app.ErrRefreshReuse) {
		t.Fatalf("reuse err = %v", err)
	}
	if _, err := f.svc.Refresh(ctx, r1.RefreshToken, client); !errors.Is(err, app.ErrInvalidToken) {
		t.Fatalf("sibling after reuse err = %v (revocation must survive the failed request)", err)
	}
}

func TestRefreshIdleExpiry(t *testing.T) {
	f := newFixture()
	s := f.signIn(t, "alice")
	f.clock.Advance(domain.RefreshIdleLifetime)
	if _, err := f.svc.Refresh(ctx, s.RefreshToken, client); !errors.Is(err, app.ErrInvalidToken) {
		t.Fatalf("err = %v", err)
	}
}

func TestRefreshFamilyAbsoluteExpiry(t *testing.T) {
	f := newFixture()
	tok := f.signIn(t, "alice").RefreshToken
	var last app.Session
	for range 4 {
		f.clock.Advance(6 * 24 * time.Hour)
		last = f.refresh(t, tok)
		tok = last.RefreshToken
	}
	if last.RefreshTokenTTL != 6*24*time.Hour {
		t.Fatalf("TTL at day 24 = %v, want capped at family end", last.RefreshTokenTTL)
	}
	f.clock.Advance(6 * 24 * time.Hour)
	if _, err := f.svc.Refresh(ctx, tok, client); !errors.Is(err, app.ErrInvalidToken) {
		t.Fatalf("day 30 err = %v", err)
	}
}

func TestRefreshUnknownOrEmpty(t *testing.T) {
	f := newFixture()
	for _, raw := range []string{"", "nope"} {
		if _, err := f.svc.Refresh(ctx, raw, client); !errors.Is(err, app.ErrInvalidToken) {
			t.Fatalf("%q: err = %v", raw, err)
		}
	}
}

func TestLogoutRevokesFamily(t *testing.T) {
	f := newFixture()
	s := f.signIn(t, "alice")
	if err := f.svc.Logout(ctx, s.RefreshToken); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Refresh(ctx, s.RefreshToken, client); !errors.Is(err, app.ErrInvalidToken) {
		t.Fatalf("err = %v", err)
	}
}

func TestLogoutUnknownTokenSucceeds(t *testing.T) {
	f := newFixture()
	for _, raw := range []string{"", "nope"} {
		if err := f.svc.Logout(ctx, raw); err != nil {
			t.Fatalf("%q: %v", raw, err)
		}
	}
}

func TestGetMe(t *testing.T) {
	f := newFixture()
	s := f.signIn(t, "alice")
	u, err := f.svc.GetMe(ctx, s.User.ID)
	if err != nil || u.ID != s.User.ID {
		t.Fatalf("%+v %v", u, err)
	}
	if _, err := f.svc.GetMe(ctx, uuid.New()); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/identity/app/`
Expected: FAIL (undefined `app.*`).

- [ ] **Step 4: Implement**

`internal/identity/app/ports.go`:

```go
// Package app contains the identity use cases and the ports they depend on.
package app

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/santoshkc2200/ioe-backend/internal/identity/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
)

var (
	ErrInvalidToken    = errors.New("invalid token")
	ErrRefreshReuse    = errors.New("refresh token reuse detected")
	ErrEmailUnverified = errors.New("email not verified")
	ErrNotFound        = errors.New("not found")
	ErrConflict        = errors.New("conflict")
)

// UserRepository returns ErrNotFound for missing users and ErrConflict when Insert
// would duplicate a Google subject.
type UserRepository interface {
	FindByGoogleSubject(ctx context.Context, subject string) (domain.User, error)
	FindByID(ctx context.Context, id uuid.UUID) (domain.User, error)
	Insert(ctx context.Context, u domain.User) error
	Update(ctx context.Context, u domain.User) error
}

// RefreshTokenRepository returns ErrNotFound for unknown hashes. FindByHashForUpdate
// locks the row until the transaction ends.
type RefreshTokenRepository interface {
	Insert(ctx context.Context, t domain.RefreshToken) error
	FindByHashForUpdate(ctx context.Context, hash []byte) (domain.RefreshToken, error)
	MarkUsed(ctx context.Context, id uuid.UUID, at time.Time) error
	RevokeFamily(ctx context.Context, familyID uuid.UUID, at time.Time) error
}

// EventPublisher records events in the current transaction.
type EventPublisher interface {
	Publish(ctx context.Context, events ...domain.Event) error
}

// Repos are bound to one transaction.
type Repos struct {
	Users  UserRepository
	Tokens RefreshTokenRepository
	Events EventPublisher
}

// TxRunner commits when fn returns nil and rolls back otherwise.
type TxRunner interface {
	RunInTx(ctx context.Context, fn func(Repos) error) error
}

// GoogleVerifier validates a Google ID token. Failures wrap ErrInvalidToken.
type GoogleVerifier interface {
	Verify(ctx context.Context, idToken string) (domain.GoogleIdentity, error)
}

// AccessTokenIssuer creates access tokens and reports their lifetime.
type AccessTokenIssuer interface {
	Issue(userID uuid.UUID, role auth.Role) (string, time.Duration, error)
}
```

`internal/identity/app/secret.go`:

```go
package app

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
)

const refreshTokenBytes = 32

func newRefreshSecret() (raw string, hash []byte, err error) {
	b := make([]byte, refreshTokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", nil, err
	}
	raw = base64.RawURLEncoding.EncodeToString(b)
	return raw, hashRefreshToken(raw), nil
}

func hashRefreshToken(raw string) []byte {
	sum := sha256.Sum256([]byte(raw))
	return sum[:]
}
```

`internal/identity/app/service.go`:

```go
package app

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/santoshkc2200/ioe-backend/internal/identity/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/clock"
)

const maxUserAgentLen = 512

// Client describes the caller for refresh-token bookkeeping.
type Client struct {
	UserAgent string
	IP        string
}

// Session is the result of sign-in or refresh. RefreshToken is the raw secret for the cookie.
type Session struct {
	AccessToken     string
	AccessTokenTTL  time.Duration
	RefreshToken    string
	RefreshTokenTTL time.Duration
	User            domain.User
	Created         bool
}

// Service implements the identity use cases.
type Service struct {
	tx         TxRunner
	google     GoogleVerifier
	tokens     AccessTokenIssuer
	clock      clock.Clock
	rootAdmins map[string]struct{}
}

func NewService(tx TxRunner, google GoogleVerifier, tokens AccessTokenIssuer, c clock.Clock, rootAdminEmails []string) *Service {
	admins := make(map[string]struct{}, len(rootAdminEmails))
	for _, e := range rootAdminEmails {
		if e = normalizeEmail(e); e != "" {
			admins[e] = struct{}{}
		}
	}
	return &Service{tx: tx, google: google, tokens: tokens, clock: c, rootAdmins: admins}
}

// SignInWithGoogle verifies a Google ID token, creates or updates the user, and starts a session.
func (s *Service) SignInWithGoogle(ctx context.Context, idToken string, client Client) (Session, error) {
	identity, err := s.google.Verify(ctx, idToken)
	if err != nil {
		return Session{}, err
	}
	if !identity.EmailVerified {
		return Session{}, ErrEmailUnverified
	}
	var sess Session
	signIn := func(r Repos) error {
		var err error
		sess, err = s.signIn(ctx, r, identity, client)
		return err
	}
	err = s.tx.RunInTx(ctx, signIn)
	if errors.Is(err, ErrConflict) {
		// A concurrent first sign-in created the user; retry once to load it.
		err = s.tx.RunInTx(ctx, signIn)
	}
	if err != nil {
		return Session{}, err
	}
	return s.withAccessToken(sess)
}

func (s *Service) signIn(ctx context.Context, r Repos, identity domain.GoogleIdentity, client Client) (Session, error) {
	now := s.clock.Now()
	user, err := r.Users.FindByGoogleSubject(ctx, identity.Subject)
	created := false
	switch {
	case errors.Is(err, ErrNotFound):
		id, err := uuid.NewV7()
		if err != nil {
			return Session{}, err
		}
		user = domain.NewUser(id, identity, now)
		s.applyBootstrap(&user, now)
		if err := r.Users.Insert(ctx, user); err != nil {
			return Session{}, err
		}
		if err := r.Events.Publish(ctx, domain.UserRegistered{UserID: user.ID, Email: user.Email, OccurredAt: now}); err != nil {
			return Session{}, err
		}
		created = true
	case err != nil:
		return Session{}, err
	default:
		user.RecordLogin(identity, now)
		s.applyBootstrap(&user, now)
		if err := r.Users.Update(ctx, user); err != nil {
			return Session{}, err
		}
	}

	raw, hash, err := newRefreshSecret()
	if err != nil {
		return Session{}, err
	}
	id, familyID, err := newIDs()
	if err != nil {
		return Session{}, err
	}
	ua, ip := clientMeta(client)
	token := domain.NewRefreshFamily(id, familyID, user.ID, hash, now, ua, ip)
	if err := r.Tokens.Insert(ctx, token); err != nil {
		return Session{}, err
	}
	return Session{RefreshToken: raw, RefreshTokenTTL: token.ExpiresAt.Sub(now), User: user, Created: created}, nil
}

// Refresh exchanges a refresh token for a new access token and a rotated refresh token.
// Presenting an already used token revokes its whole family; that revocation is committed
// even though the call fails.
func (s *Service) Refresh(ctx context.Context, raw string, client Client) (Session, error) {
	if raw == "" {
		return Session{}, ErrInvalidToken
	}
	var sess Session
	reused := false
	err := s.tx.RunInTx(ctx, func(r Repos) error {
		now := s.clock.Now()
		current, err := r.Tokens.FindByHashForUpdate(ctx, hashRefreshToken(raw))
		if errors.Is(err, ErrNotFound) {
			return ErrInvalidToken
		}
		if err != nil {
			return err
		}
		if err := current.Check(now); err != nil {
			if errors.Is(err, domain.ErrRefreshReused) {
				reused = true
				return r.Tokens.RevokeFamily(ctx, current.FamilyID, now)
			}
			return ErrInvalidToken
		}
		user, err := r.Users.FindByID(ctx, current.UserID)
		if errors.Is(err, ErrNotFound) {
			return ErrInvalidToken
		}
		if err != nil {
			return err
		}
		if err := r.Tokens.MarkUsed(ctx, current.ID, now); err != nil {
			return err
		}
		nextRaw, nextHash, err := newRefreshSecret()
		if err != nil {
			return err
		}
		id, err := uuid.NewV7()
		if err != nil {
			return err
		}
		ua, ip := clientMeta(client)
		next := current.Successor(id, nextHash, now, ua, ip)
		if err := r.Tokens.Insert(ctx, next); err != nil {
			return err
		}
		sess = Session{RefreshToken: nextRaw, RefreshTokenTTL: next.ExpiresAt.Sub(now), User: user}
		return nil
	})
	if err != nil {
		return Session{}, err
	}
	if reused {
		return Session{}, ErrRefreshReuse
	}
	return s.withAccessToken(sess)
}

// Logout revokes the token's family. Unknown or empty tokens succeed.
func (s *Service) Logout(ctx context.Context, raw string) error {
	if raw == "" {
		return nil
	}
	return s.tx.RunInTx(ctx, func(r Repos) error {
		current, err := r.Tokens.FindByHashForUpdate(ctx, hashRefreshToken(raw))
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		return r.Tokens.RevokeFamily(ctx, current.FamilyID, s.clock.Now())
	})
}

// GetMe returns the user's current record.
func (s *Service) GetMe(ctx context.Context, userID uuid.UUID) (domain.User, error) {
	var user domain.User
	err := s.tx.RunInTx(ctx, func(r Repos) error {
		var err error
		user, err = r.Users.FindByID(ctx, userID)
		return err
	})
	return user, err
}

func (s *Service) withAccessToken(sess Session) (Session, error) {
	token, ttl, err := s.tokens.Issue(sess.User.ID, sess.User.Role)
	if err != nil {
		return Session{}, err
	}
	sess.AccessToken, sess.AccessTokenTTL = token, ttl
	return sess, nil
}

func (s *Service) applyBootstrap(u *domain.User, now time.Time) {
	if _, ok := s.rootAdmins[normalizeEmail(u.Email)]; ok {
		u.PromoteToRootAdmin(now)
	}
}

func normalizeEmail(e string) string { return strings.ToLower(strings.TrimSpace(e)) }

func clientMeta(c Client) (userAgent, ip string) {
	userAgent = c.UserAgent
	if len(userAgent) > maxUserAgentLen {
		userAgent = userAgent[:maxUserAgentLen]
	}
	return userAgent, c.IP
}

func newIDs() (id, familyID uuid.UUID, err error) {
	if id, err = uuid.NewV7(); err != nil {
		return
	}
	familyID, err = uuid.NewV7()
	return
}
```

- [ ] **Step 5: Run tests and lint**

Run: `go test -race ./internal/identity/app/ && make lint`
Expected: PASS; no issues.

- [ ] **Step 6: Prove the depguard boundary**

Temporarily add `import _ "github.com/jackc/pgx/v5"` to `internal/identity/app/ports.go`, run `make lint`.
Expected: depguard error naming `github.com/jackc/pgx/v5` for rule `identity-app`. Remove the import and re-run `make lint` (clean).

- [ ] **Step 7: Commit**

```bash
git add -A
git commit -m "feat(identity): add sign-in, refresh rotation, logout, and current-user use cases"
```

---

### Task 10: Access-token adapter and key generator

**Files:**
- Create: `internal/identity/adapters/jwt/jwt.go`, `internal/identity/adapters/jwt/keys.go`
- Test: `internal/identity/adapters/jwt/jwt_test.go`
- Create: `cmd/keygen/main.go`
- Modify: `Makefile` (add `keygen`)

**Interfaces:**
- Consumes: `app.ErrInvalidToken`, `auth.Principal`, `auth.ParseRole`, `clock.Clock`.
- Produces: `jwt.TTL` (15 minutes); `jwt.Keys{SigningKID string; Signing ed25519.PrivateKey; Verify map[string]ed25519.PublicKey}`; `jwt.ParseKeys(signingPEM, signingKID, verifySpec string) (Keys, error)`; `jwt.New(keys Keys, issuer, audience string, c clock.Clock) *Tokens`; `(*Tokens).Issue(uuid.UUID, auth.Role) (string, time.Duration, error)` (satisfies `app.AccessTokenIssuer`); `(*Tokens).Verify(string) (auth.Principal, error)`.

- [ ] **Step 1: Add the dependency**

```bash
go get github.com/golang-jwt/jwt/v5@v5.3.1
```

- [ ] **Step 2: Write the failing tests**

`internal/identity/adapters/jwt/jwt_test.go`:

```go
package jwt_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"strings"
	"testing"
	"time"

	gojwt "github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/santoshkc2200/ioe-backend/internal/identity/adapters/jwt"
	"github.com/santoshkc2200/ioe-backend/internal/identity/app"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/clock"
)

const (
	issuer   = "https://api.test"
	audience = "ioe"
)

func genKey(t *testing.T) (priv ed25519.PrivateKey, privPEM, pubPEM string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	p8, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	pk, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return priv,
		string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: p8})),
		string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pk}))
}

func newTokens(t *testing.T, c clock.Clock) (*jwt.Tokens, jwt.Keys) {
	t.Helper()
	_, privPEM, _ := genKey(t)
	keys, err := jwt.ParseKeys(privPEM, "k1", "")
	if err != nil {
		t.Fatal(err)
	}
	return jwt.New(keys, issuer, audience, c), keys
}

func TestIssueVerifyRoundTrip(t *testing.T) {
	tokens, _ := newTokens(t, clock.System{})
	id := uuid.New()
	tok, ttl, err := tokens.Issue(id, auth.RoleInstructor)
	if err != nil || ttl != 15*time.Minute {
		t.Fatalf("ttl=%v err=%v", ttl, err)
	}
	header, err := base64.RawURLEncoding.DecodeString(strings.Split(tok, ".")[0])
	if err != nil || !strings.Contains(string(header), `"kid":"k1"`) || !strings.Contains(string(header), `"alg":"EdDSA"`) {
		t.Fatalf("header %s", header)
	}
	p, err := tokens.Verify(tok)
	if err != nil || p != (auth.Principal{UserID: id, Role: auth.RoleInstructor}) {
		t.Fatalf("%+v %v", p, err)
	}
}

func TestVerifyRejects(t *testing.T) {
	c := clock.NewFake(time.Now().UTC())
	tokens, keys := newTokens(t, c)
	id := uuid.New()
	sign := func(m gojwt.SigningMethod, key any, kid string, claims gojwt.MapClaims) string {
		tok := gojwt.NewWithClaims(m, claims)
		tok.Header["kid"] = kid
		s, err := tok.SignedString(key)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	valid := func() gojwt.MapClaims {
		return gojwt.MapClaims{"iss": issuer, "aud": audience, "sub": id.String(), "role": "student",
			"iat": c.Now().Unix(), "exp": c.Now().Add(time.Minute).Unix(), "jti": "j"}
	}
	otherPriv, _, _ := genKey(t)

	cases := map[string]func() string{
		"wrong audience": func() string {
			cl := valid()
			cl["aud"] = "other"
			return sign(gojwt.SigningMethodEdDSA, keys.Signing, "k1", cl)
		},
		"wrong issuer": func() string {
			cl := valid()
			cl["iss"] = "https://evil"
			return sign(gojwt.SigningMethodEdDSA, keys.Signing, "k1", cl)
		},
		"unknown kid": func() string { return sign(gojwt.SigningMethodEdDSA, otherPriv, "k2", valid()) },
		"wrong key":   func() string { return sign(gojwt.SigningMethodEdDSA, otherPriv, "k1", valid()) },
		"bad role": func() string {
			cl := valid()
			cl["role"] = "god"
			return sign(gojwt.SigningMethodEdDSA, keys.Signing, "k1", cl)
		},
		"missing exp": func() string {
			cl := valid()
			delete(cl, "exp")
			return sign(gojwt.SigningMethodEdDSA, keys.Signing, "k1", cl)
		},
		"alg none": func() string {
			return sign(gojwt.SigningMethodNone, gojwt.UnsafeAllowNoneSignatureType, "k1", valid())
		},
		"hmac confusion": func() string {
			return sign(gojwt.SigningMethodHS256, []byte(keys.Verify["k1"]), "k1", valid())
		},
		"tampered": func() string {
			tok := sign(gojwt.SigningMethodEdDSA, keys.Signing, "k1", valid())
			return tok[:len(tok)-2] + "AA"
		},
	}
	for name, mk := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := tokens.Verify(mk()); !errors.Is(err, app.ErrInvalidToken) {
				t.Fatalf("err = %v", err)
			}
		})
	}

	t.Run("expired", func(t *testing.T) {
		tok, _, err := tokens.Issue(id, auth.RoleStudent)
		if err != nil {
			t.Fatal(err)
		}
		c.Advance(16 * time.Minute)
		if _, err := tokens.Verify(tok); !errors.Is(err, app.ErrInvalidToken) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestVerifyAcceptsRotatedKey(t *testing.T) {
	_, oldPriv, oldPub := genKey(t)
	_, newPriv, _ := genKey(t)
	oldKeys, err := jwt.ParseKeys(oldPriv, "old", "")
	if err != nil {
		t.Fatal(err)
	}
	tok, _, err := jwt.New(oldKeys, issuer, audience, clock.System{}).Issue(uuid.New(), auth.RoleStudent)
	if err != nil {
		t.Fatal(err)
	}
	newKeys, err := jwt.ParseKeys(newPriv, "new", "old="+strings.ReplaceAll(oldPub, "\n", `\n`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := jwt.New(newKeys, issuer, audience, clock.System{}).Verify(tok); err != nil {
		t.Fatalf("rotated key rejected: %v", err)
	}
}

func TestParseKeys(t *testing.T) {
	_, privPEM, pubPEM := genKey(t)
	if _, err := jwt.ParseKeys(strings.ReplaceAll(privPEM, "\n", `\n`), "k1", ""); err != nil {
		t.Fatalf("escaped newlines: %v", err)
	}
	bad := map[string][3]string{
		"not pem":           {"nope", "k1", ""},
		"public as private": {pubPEM, "k1", ""},
		"empty kid":         {privPEM, "", ""},
		"entry without =":   {privPEM, "k1", "k2"},
		"duplicate kid":     {privPEM, "k1", "k1=" + pubPEM},
		"bad verify pem":    {privPEM, "k1", "k2=nope"},
	}
	for name, in := range bad {
		if _, err := jwt.ParseKeys(in[0], in[1], in[2]); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/identity/adapters/jwt/`
Expected: FAIL (undefined `jwt.ParseKeys`).

- [ ] **Step 4: Implement**

`internal/identity/adapters/jwt/keys.go`:

```go
package jwt

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
)

// Keys holds the signing key and every public key accepted for verification, by kid.
type Keys struct {
	SigningKID string
	Signing    ed25519.PrivateKey
	Verify     map[string]ed25519.PublicKey
}

// ParseKeys loads a PKCS#8 Ed25519 signing key and optional extra public keys given as
// "kid=PEM" entries separated by ";". PEM text may use literal "\n" for newlines.
func ParseKeys(signingPEM, signingKID, verifySpec string) (Keys, error) {
	if signingKID == "" {
		return Keys{}, errors.New("JWT_SIGNING_KEY_ID is empty")
	}
	priv, err := parsePrivate(signingPEM)
	if err != nil {
		return Keys{}, fmt.Errorf("JWT_SIGNING_KEY: %w", err)
	}
	pub, ok := priv.Public().(ed25519.PublicKey)
	if !ok {
		return Keys{}, errors.New("JWT_SIGNING_KEY: unexpected public key type")
	}
	k := Keys{SigningKID: signingKID, Signing: priv, Verify: map[string]ed25519.PublicKey{signingKID: pub}}
	for _, entry := range strings.Split(verifySpec, ";") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		kid, pemText, ok := strings.Cut(entry, "=")
		if !ok || kid == "" {
			return Keys{}, errors.New("JWT_VERIFY_KEYS: each entry must be kid=PEM")
		}
		if _, dup := k.Verify[kid]; dup {
			return Keys{}, fmt.Errorf("JWT_VERIFY_KEYS: duplicate kid %q", kid)
		}
		pub, err := parsePublic(pemText)
		if err != nil {
			return Keys{}, fmt.Errorf("JWT_VERIFY_KEYS: kid %q: %w", kid, err)
		}
		k.Verify[kid] = pub
	}
	return k, nil
}

func decodePEM(s string) (*pem.Block, error) {
	block, _ := pem.Decode([]byte(strings.ReplaceAll(s, `\n`, "\n")))
	if block == nil {
		return nil, errors.New("no PEM block found")
	}
	return block, nil
}

func parsePrivate(s string) (ed25519.PrivateKey, error) {
	block, err := decodePEM(s)
	if err != nil {
		return nil, err
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	ed, ok := key.(ed25519.PrivateKey)
	if !ok {
		return nil, errors.New("not an Ed25519 private key")
	}
	return ed, nil
}

func parsePublic(s string) (ed25519.PublicKey, error) {
	block, err := decodePEM(s)
	if err != nil {
		return nil, err
	}
	key, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	ed, ok := key.(ed25519.PublicKey)
	if !ok {
		return nil, errors.New("not an Ed25519 public key")
	}
	return ed, nil
}
```

`internal/identity/adapters/jwt/jwt.go`:

```go
// Package jwt issues and verifies the API's EdDSA access tokens.
package jwt

import (
	"errors"
	"fmt"
	"time"

	gojwt "github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/santoshkc2200/ioe-backend/internal/identity/app"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/clock"
)

// TTL is the access-token lifetime.
const TTL = 15 * time.Minute

type claims struct {
	Role string `json:"role"`
	gojwt.RegisteredClaims
}

// Tokens issues and verifies access tokens.
type Tokens struct {
	keys     Keys
	issuer   string
	audience string
	clock    clock.Clock
}

func New(keys Keys, issuer, audience string, c clock.Clock) *Tokens {
	return &Tokens{keys: keys, issuer: issuer, audience: audience, clock: c}
}

// Issue signs an access token for the user.
func (t *Tokens) Issue(userID uuid.UUID, role auth.Role) (string, time.Duration, error) {
	now := t.clock.Now()
	tok := gojwt.NewWithClaims(gojwt.SigningMethodEdDSA, claims{
		Role: string(role),
		RegisteredClaims: gojwt.RegisteredClaims{
			Issuer:    t.issuer,
			Audience:  gojwt.ClaimStrings{t.audience},
			Subject:   userID.String(),
			IssuedAt:  gojwt.NewNumericDate(now),
			ExpiresAt: gojwt.NewNumericDate(now.Add(TTL)),
			ID:        uuid.NewString(),
		},
	})
	tok.Header["kid"] = t.keys.SigningKID
	s, err := tok.SignedString(t.keys.Signing)
	if err != nil {
		return "", 0, err
	}
	return s, TTL, nil
}

// Verify validates an access token. Every failure wraps app.ErrInvalidToken.
func (t *Tokens) Verify(raw string) (auth.Principal, error) {
	var c claims
	_, err := gojwt.ParseWithClaims(raw, &c, t.keyFor,
		gojwt.WithValidMethods([]string{gojwt.SigningMethodEdDSA.Alg()}),
		gojwt.WithIssuer(t.issuer),
		gojwt.WithAudience(t.audience),
		gojwt.WithExpirationRequired(),
		gojwt.WithIssuedAt(),
		gojwt.WithTimeFunc(t.clock.Now),
	)
	if err != nil {
		return auth.Principal{}, fmt.Errorf("%w: %w", app.ErrInvalidToken, err)
	}
	id, err := uuid.Parse(c.Subject)
	if err != nil {
		return auth.Principal{}, fmt.Errorf("%w: subject: %w", app.ErrInvalidToken, err)
	}
	role, err := auth.ParseRole(c.Role)
	if err != nil {
		return auth.Principal{}, fmt.Errorf("%w: %w", app.ErrInvalidToken, err)
	}
	return auth.Principal{UserID: id, Role: role}, nil
}

func (t *Tokens) keyFor(tok *gojwt.Token) (any, error) {
	kid, _ := tok.Header["kid"].(string)
	key, ok := t.keys.Verify[kid]
	if !ok {
		return nil, errors.New("unknown kid")
	}
	return key, nil
}
```

`cmd/keygen/main.go`:

```go
// Command keygen prints a new Ed25519 key pair for JWT_SIGNING_KEY and JWT_VERIFY_KEYS.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"strings"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "keygen:", err)
		os.Exit(1)
	}
}

func run() error {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	p8, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return err
	}
	pk, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return err
	}
	privPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: p8}))
	pubPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pk}))
	oneLine := func(s string) string { return strings.ReplaceAll(strings.TrimSpace(s), "\n", `\n`) }

	fmt.Println("# Keep the private key secret. Single-line values for .env:")
	fmt.Printf("JWT_SIGNING_KEY=%s\n", oneLine(privPEM))
	fmt.Println("# Public key, for JWT_VERIFY_KEYS (kid=PEM) after rotating to a new signing key:")
	fmt.Println(oneLine(pubPEM))
	return nil
}
```

Append to `Makefile` (and `.PHONY`):

```make
keygen:
	go run ./cmd/keygen
```

- [ ] **Step 5: Run tests and lint**

Run: `go mod tidy && go test -race ./internal/identity/adapters/jwt/ && go run ./cmd/keygen | head -2 && make lint`
Expected: PASS; keygen prints a comment and a `JWT_SIGNING_KEY=-----BEGIN PRIVATE KEY-----\n...` line; no lint issues.

- [ ] **Step 6: Commit**

```bash
git add -A
git commit -m "feat(identity): add EdDSA access tokens with key rotation and keygen"
```

---

### Task 11: Google ID-token verifier

**Files:**
- Create: `internal/identity/adapters/google/google.go`
- Create: `internal/identity/adapters/google/googletest/googletest.go`
- Test: `internal/identity/adapters/google/google_test.go`

**Interfaces:**
- Consumes: `app.ErrInvalidToken`, `domain.GoogleIdentity`, `clock.Clock`.
- Produces: `google.DefaultJWKSURL`; `google.NewVerifier(ctx context.Context, jwksURL string, audiences []string, c clock.Clock) (*Verifier, error)`; `(*Verifier).Verify(ctx, string) (domain.GoogleIdentity, error)` (satisfies `app.GoogleVerifier`). Test support: `googletest.NewIssuer(t testing.TB) *Issuer`, `(*Issuer).JWKSURL() string`, `googletest.Claims(sub, email, aud string, now time.Time) gojwt.MapClaims`, `(*Issuer).Sign(t testing.TB, claims gojwt.MapClaims) string`, `(*Issuer).Forge(t testing.TB, claims gojwt.MapClaims) string`.

- [ ] **Step 1: Add the dependency**

```bash
go get github.com/MicahParks/keyfunc/v3@v3.8.2
```

- [ ] **Step 2: Write the test support package**

`internal/identity/adapters/google/googletest/googletest.go`:

```go
// Package googletest issues Google-style RS256 ID tokens and serves their JWKS for tests.
package googletest

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	gojwt "github.com/golang-jwt/jwt/v5"
)

const kid = "test-kid"

// Issuer signs tokens with a test key and serves the matching JWKS.
type Issuer struct {
	server *httptest.Server
	key    *rsa.PrivateKey
}

func NewIssuer(t testing.TB) *Issuer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	enc := base64.RawURLEncoding.EncodeToString
	body, err := json.Marshal(map[string]any{"keys": []map[string]string{{
		"kty": "RSA", "kid": kid, "use": "sig", "alg": "RS256",
		"n": enc(key.N.Bytes()), "e": enc(big.NewInt(int64(key.E)).Bytes()),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return &Issuer{server: srv, key: key}
}

func (i *Issuer) JWKSURL() string { return i.server.URL }

// Claims returns valid Google ID-token claims.
func Claims(sub, email, aud string, now time.Time) gojwt.MapClaims {
	return gojwt.MapClaims{
		"iss": "https://accounts.google.com", "aud": aud, "sub": sub,
		"email": email, "email_verified": true, "name": "Test User", "picture": "https://example.com/p.png",
		"iat": now.Unix(), "exp": now.Add(time.Hour).Unix(),
	}
}

// Sign signs claims with the served key.
func (i *Issuer) Sign(t testing.TB, claims gojwt.MapClaims) string {
	t.Helper()
	return sign(t, i.key, claims)
}

// Forge signs claims with a different key under the same kid.
func (i *Issuer) Forge(t testing.TB, claims gojwt.MapClaims) string {
	t.Helper()
	other, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return sign(t, other, claims)
}

func sign(t testing.TB, key *rsa.PrivateKey, claims gojwt.MapClaims) string {
	tok := gojwt.NewWithClaims(gojwt.SigningMethodRS256, claims)
	tok.Header["kid"] = kid
	s, err := tok.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
```

- [ ] **Step 3: Write the failing tests**

`internal/identity/adapters/google/google_test.go`:

```go
package google_test

import (
	"context"
	"errors"
	"testing"
	"time"

	gojwt "github.com/golang-jwt/jwt/v5"

	"github.com/santoshkc2200/ioe-backend/internal/identity/adapters/google"
	"github.com/santoshkc2200/ioe-backend/internal/identity/adapters/google/googletest"
	"github.com/santoshkc2200/ioe-backend/internal/identity/app"
	"github.com/santoshkc2200/ioe-backend/internal/platform/clock"
)

const aud = "web.apps.googleusercontent.com"

var now = time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)

func setup(t *testing.T, audiences ...string) (*googletest.Issuer, *google.Verifier) {
	t.Helper()
	iss := googletest.NewIssuer(t)
	if len(audiences) == 0 {
		audiences = []string{aud}
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	v, err := google.NewVerifier(ctx, iss.JWKSURL(), audiences, clock.NewFake(now))
	if err != nil {
		t.Fatal(err)
	}
	return iss, v
}

func TestVerifyValid(t *testing.T) {
	iss, v := setup(t)
	id, err := v.Verify(context.Background(), iss.Sign(t, googletest.Claims("sub-1", "a@example.com", aud, now)))
	if err != nil {
		t.Fatal(err)
	}
	if id.Subject != "sub-1" || id.Email != "a@example.com" || !id.EmailVerified || id.Name != "Test User" || id.Picture == "" {
		t.Fatalf("%+v", id)
	}
}

func TestVerifyAcceptedVariants(t *testing.T) {
	iss, v := setup(t, aud, "admin.apps.googleusercontent.com")
	variants := map[string]func(gojwt.MapClaims){
		"bare issuer":            func(c gojwt.MapClaims) { c["iss"] = "accounts.google.com" },
		"second audience":        func(c gojwt.MapClaims) { c["aud"] = "admin.apps.googleusercontent.com" },
		"email_verified string":  func(c gojwt.MapClaims) { c["email_verified"] = "true" },
	}
	for name, mutate := range variants {
		c := googletest.Claims("sub-1", "a@example.com", aud, now)
		mutate(c)
		if id, err := v.Verify(context.Background(), iss.Sign(t, c)); err != nil || !id.EmailVerified {
			t.Errorf("%s: %+v %v", name, id, err)
		}
	}
}

func TestVerifyPassesUnverifiedEmailThrough(t *testing.T) {
	iss, v := setup(t)
	c := googletest.Claims("sub-1", "a@example.com", aud, now)
	c["email_verified"] = false
	id, err := v.Verify(context.Background(), iss.Sign(t, c))
	if err != nil || id.EmailVerified {
		t.Fatalf("%+v %v", id, err)
	}
}

func TestVerifyRejects(t *testing.T) {
	iss, v := setup(t)
	cases := map[string]func() string{
		"wrong audience": func() string {
			return iss.Sign(t, googletest.Claims("s", "a@example.com", "other", now))
		},
		"wrong issuer": func() string {
			c := googletest.Claims("s", "a@example.com", aud, now)
			c["iss"] = "https://evil.example"
			return iss.Sign(t, c)
		},
		"expired": func() string {
			return iss.Sign(t, googletest.Claims("s", "a@example.com", aud, now.Add(-2*time.Hour)))
		},
		"forged signature": func() string {
			return iss.Forge(t, googletest.Claims("s", "a@example.com", aud, now))
		},
		"hmac": func() string {
			s, err := gojwt.NewWithClaims(gojwt.SigningMethodHS256, googletest.Claims("s", "a@example.com", aud, now)).SignedString([]byte("k"))
			if err != nil {
				t.Fatal(err)
			}
			return s
		},
		"missing subject": func() string {
			c := googletest.Claims("s", "a@example.com", aud, now)
			delete(c, "sub")
			return iss.Sign(t, c)
		},
		"missing email": func() string {
			c := googletest.Claims("s", "a@example.com", aud, now)
			delete(c, "email")
			return iss.Sign(t, c)
		},
		"garbage": func() string { return "not.a.jwt" },
	}
	for name, mk := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := v.Verify(context.Background(), mk()); !errors.Is(err, app.ErrInvalidToken) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}
```

- [ ] **Step 4: Run tests to verify they fail**

Run: `go test ./internal/identity/adapters/google/...`
Expected: FAIL (undefined `google.NewVerifier`).

- [ ] **Step 5: Implement**

`internal/identity/adapters/google/google.go`:

```go
// Package google verifies Google ID tokens against Google's published signing keys.
package google

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/MicahParks/keyfunc/v3"
	gojwt "github.com/golang-jwt/jwt/v5"

	"github.com/santoshkc2200/ioe-backend/internal/identity/app"
	"github.com/santoshkc2200/ioe-backend/internal/identity/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/clock"
)

// DefaultJWKSURL serves Google's OAuth 2.0 signing keys.
const DefaultJWKSURL = "https://www.googleapis.com/oauth2/v3/certs"

const clockSkew = 30 * time.Second

var validIssuers = []string{"accounts.google.com", "https://accounts.google.com"}

// Verifier validates Google ID tokens.
type Verifier struct {
	keyfunc   gojwt.Keyfunc
	audiences []string
	clock     clock.Clock
}

// NewVerifier caches the JWKS at jwksURL and refreshes it in the background until ctx ends.
func NewVerifier(ctx context.Context, jwksURL string, audiences []string, c clock.Clock) (*Verifier, error) {
	kf, err := keyfunc.NewDefaultCtx(ctx, []string{jwksURL})
	if err != nil {
		return nil, fmt.Errorf("google jwks: %w", err)
	}
	return &Verifier{keyfunc: kf.Keyfunc, audiences: audiences, clock: c}, nil
}

// flexBool accepts both JSON booleans and the strings "true"/"false" Google has used.
type flexBool bool

func (b *flexBool) UnmarshalJSON(data []byte) error {
	switch string(data) {
	case "true", `"true"`:
		*b = true
	case "false", `"false"`, "null":
		*b = false
	default:
		return fmt.Errorf("invalid boolean %s", data)
	}
	return nil
}

type claims struct {
	Email         string   `json:"email"`
	EmailVerified flexBool `json:"email_verified"`
	Name          string   `json:"name"`
	Picture       string   `json:"picture"`
	gojwt.RegisteredClaims
}

// Verify checks signature, issuer, audience, and expiry. Failures wrap app.ErrInvalidToken.
// An unverified email is returned as data; the use case decides how to treat it.
func (v *Verifier) Verify(_ context.Context, raw string) (domain.GoogleIdentity, error) {
	var c claims
	_, err := gojwt.ParseWithClaims(raw, &c, v.keyfunc,
		gojwt.WithValidMethods([]string{gojwt.SigningMethodRS256.Alg()}),
		gojwt.WithExpirationRequired(),
		gojwt.WithTimeFunc(v.clock.Now),
		gojwt.WithLeeway(clockSkew),
	)
	if err != nil {
		return domain.GoogleIdentity{}, fmt.Errorf("%w: %w", app.ErrInvalidToken, err)
	}
	if !slices.Contains(validIssuers, c.Issuer) {
		return domain.GoogleIdentity{}, fmt.Errorf("%w: issuer", app.ErrInvalidToken)
	}
	if !slices.ContainsFunc(c.Audience, func(a string) bool { return slices.Contains(v.audiences, a) }) {
		return domain.GoogleIdentity{}, fmt.Errorf("%w: audience", app.ErrInvalidToken)
	}
	if c.Subject == "" || c.Email == "" {
		return domain.GoogleIdentity{}, fmt.Errorf("%w: %w", app.ErrInvalidToken, errors.New("missing sub or email"))
	}
	return domain.GoogleIdentity{
		Subject:       c.Subject,
		Email:         c.Email,
		EmailVerified: bool(c.EmailVerified),
		Name:          c.Name,
		Picture:       c.Picture,
	}, nil
}
```

- [ ] **Step 6: Run tests and lint**

Run: `go mod tidy && go test -race ./internal/identity/adapters/google/... && make lint`
Expected: PASS; no issues. If keyfunc refuses the `http://` test URL, pass `keyfunc.Override{}` via `keyfunc.NewDefaultOverrideCtx` only if its options expose an HTTP allowance; otherwise serve the JWKS with `httptest.NewTLSServer` and give the verifier the test server's client through `keyfunc.Override{Client: srv.Client()}` (add an optional `*http.Client` parameter to `NewVerifier` only if this is required).

- [ ] **Step 7: Commit**

```bash
git add -A
git commit -m "feat(identity): verify Google ID tokens against Google JWKS"
```

---
### Task 12: Identity PostgreSQL adapter (sqlc)

**Files:**
- Create: `sqlc.yaml`
- Create: `internal/identity/adapters/postgres/queries.sql`
- Generate: `internal/identity/adapters/postgres/sqlcgen/*.go`
- Create: `internal/identity/adapters/postgres/postgres.go`
- Test: `internal/identity/adapters/postgres/postgres_integration_test.go`
- Modify: `Makefile` (add `sqlc`, `sqlc-check`; add `sqlc-check` to `check`)

**Interfaces:**
- Consumes: `app.Repos` and repository contracts (Task 9), `outbox.Publish` (Task 7), `pgtest.New` (Task 6).
- Produces: `postgres.NewTxRunner(pool *pgxpool.Pool) *TxRunner` with `RunInTx(ctx, func(app.Repos) error) error` (satisfies `app.TxRunner`).

- [ ] **Step 1: Install sqlc locally if missing**

Run: `sqlc version`
Expected: `v1.31.1` (install with `brew install sqlc` if absent).

- [ ] **Step 2: Write sqlc config and queries**

`sqlc.yaml`:

```yaml
version: "2"
sql:
  - engine: postgresql
    schema: migrations
    queries: internal/identity/adapters/postgres/queries.sql
    gen:
      go:
        package: sqlcgen
        out: internal/identity/adapters/postgres/sqlcgen
        sql_package: pgx/v5
        overrides:
          - db_type: uuid
            go_type: github.com/google/uuid.UUID
          - db_type: timestamptz
            go_type: time.Time
          - db_type: timestamptz
            nullable: true
            go_type:
              import: time
              type: Time
              pointer: true
          - db_type: citext
            go_type: string
        rename:
          avatar_url: AvatarURL
          ip: IP
```

`internal/identity/adapters/postgres/queries.sql`:

```sql
-- name: GetUserByGoogleSub :one
SELECT * FROM identity.users WHERE google_sub = $1;

-- name: GetUserByID :one
SELECT * FROM identity.users WHERE id = $1;

-- name: InsertUser :exec
INSERT INTO identity.users (id, google_sub, email, name, avatar_url, role, created_at, updated_at, last_login_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9);

-- name: UpdateUser :exec
UPDATE identity.users
SET email = $2, name = $3, avatar_url = $4, role = $5, updated_at = $6, last_login_at = $7
WHERE id = $1;

-- name: InsertRefreshToken :exec
INSERT INTO identity.refresh_tokens (id, user_id, family_id, token_hash, family_expires_at, expires_at, created_at, user_agent, ip)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9);

-- name: GetRefreshTokenByHashForUpdate :one
SELECT * FROM identity.refresh_tokens WHERE token_hash = $1 FOR UPDATE;

-- name: MarkRefreshTokenUsed :exec
UPDATE identity.refresh_tokens SET used_at = $2 WHERE id = $1;

-- name: RevokeRefreshFamily :exec
UPDATE identity.refresh_tokens SET revoked_at = $2 WHERE family_id = $1 AND revoked_at IS NULL;
```

- [ ] **Step 3: Generate and inspect**

Run: `sqlc generate && ls internal/identity/adapters/postgres/sqlcgen && grep -n "^type\|^func (q" internal/identity/adapters/postgres/sqlcgen/*.go`
Expected: `db.go`, `models.go`, `queries.sql.go`; models `IdentityUser` and `IdentityRefreshToken`; params `InsertUserParams`, `UpdateUserParams`, `InsertRefreshTokenParams`, `MarkRefreshTokenUsedParams`, `RevokeRefreshFamilyParams`. `Email` is `string`, `UsedAt`/`RevokedAt` are `*time.Time`. If sqlc names differ, use the generated names in Step 5 and keep behavior identical.

- [ ] **Step 4: Write the failing integration tests**

`internal/identity/adapters/postgres/postgres_integration_test.go`:

```go
//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/santoshkc2200/ioe-backend/internal/identity/adapters/postgres"
	"github.com/santoshkc2200/ioe-backend/internal/identity/app"
	"github.com/santoshkc2200/ioe-backend/internal/identity/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/clock"
	"github.com/santoshkc2200/ioe-backend/internal/platform/postgres/pgtest"
)

var now = time.Now().UTC().Truncate(time.Microsecond)

func newUser(sub string) domain.User {
	return domain.NewUser(uuid.Must(uuid.NewV7()), domain.GoogleIdentity{Subject: sub, Email: "Mixed@Example.com", Name: "N"}, now)
}

func TestUserRepository(t *testing.T) {
	ctx := context.Background()
	runner := postgres.NewTxRunner(pgtest.New(t))
	u := newUser("sub-1")

	if err := runner.RunInTx(ctx, func(r app.Repos) error { return r.Users.Insert(ctx, u) }); err != nil {
		t.Fatal(err)
	}
	err := runner.RunInTx(ctx, func(r app.Repos) error { return r.Users.Insert(ctx, newUser("sub-1")) })
	if !errors.Is(err, app.ErrConflict) {
		t.Fatalf("duplicate subject err = %v", err)
	}

	err = runner.RunInTx(ctx, func(r app.Repos) error {
		got, err := r.Users.FindByGoogleSubject(ctx, "sub-1")
		if err != nil {
			return err
		}
		if got.ID != u.ID || got.Email != "Mixed@Example.com" || got.Role != auth.RoleStudent || !got.CreatedAt.Equal(now) {
			t.Errorf("found %+v", got)
		}
		got.Email = "new@example.com"
		got.PromoteToRootAdmin(now)
		if err := r.Users.Update(ctx, got); err != nil {
			return err
		}
		byID, err := r.Users.FindByID(ctx, u.ID)
		if err != nil {
			return err
		}
		if byID.Email != "new@example.com" || byID.Role != auth.RoleRootAdmin {
			t.Errorf("after update %+v", byID)
		}
		if _, err := r.Users.FindByID(ctx, uuid.New()); !errors.Is(err, app.ErrNotFound) {
			t.Errorf("missing id err = %v", err)
		}
		if _, err := r.Users.FindByGoogleSubject(ctx, "nope"); !errors.Is(err, app.ErrNotFound) {
			t.Errorf("missing sub err = %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRefreshTokenRepository(t *testing.T) {
	ctx := context.Background()
	runner := postgres.NewTxRunner(pgtest.New(t))
	u := newUser("sub-2")
	famA, famB := uuid.New(), uuid.New()
	a1 := domain.NewRefreshFamily(uuid.New(), famA, u.ID, []byte("hash-a1-0123456789012345678901"), now, "ua", "203.0.113.1")
	a2 := a1.Successor(uuid.New(), []byte("hash-a2-0123456789012345678901"), now, "ua", "203.0.113.1")
	b1 := domain.NewRefreshFamily(uuid.New(), famB, u.ID, []byte("hash-b1-0123456789012345678901"), now, "ua", "")

	err := runner.RunInTx(ctx, func(r app.Repos) error {
		if err := r.Users.Insert(ctx, u); err != nil {
			return err
		}
		for _, tok := range []domain.RefreshToken{a1, a2, b1} {
			if err := r.Tokens.Insert(ctx, tok); err != nil {
				return err
			}
		}
		if err := r.Tokens.MarkUsed(ctx, a1.ID, now); err != nil {
			return err
		}
		return r.Tokens.RevokeFamily(ctx, famA, now)
	})
	if err != nil {
		t.Fatal(err)
	}

	err = runner.RunInTx(ctx, func(r app.Repos) error {
		got, err := r.Tokens.FindByHashForUpdate(ctx, a1.TokenHash)
		if err != nil {
			return err
		}
		if got.UsedAt == nil || got.RevokedAt == nil || got.IP != "203.0.113.1" || !got.FamilyExpiresAt.Equal(a1.FamilyExpiresAt) {
			t.Errorf("a1 %+v", got)
		}
		if got, _ := r.Tokens.FindByHashForUpdate(ctx, a2.TokenHash); got.RevokedAt == nil {
			t.Error("a2 not revoked with its family")
		}
		if got, _ := r.Tokens.FindByHashForUpdate(ctx, b1.TokenHash); got.RevokedAt != nil {
			t.Error("other family revoked")
		}
		if _, err := r.Tokens.FindByHashForUpdate(ctx, []byte("missing")); !errors.Is(err, app.ErrNotFound) {
			t.Errorf("missing err = %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestEventCommitsWithUserOrNotAtAll(t *testing.T) {
	ctx := context.Background()
	pool := pgtest.New(t)
	runner := postgres.NewTxRunner(pool)
	count := func() (users, events int) {
		_ = pool.QueryRow(ctx, "SELECT count(*) FROM identity.users").Scan(&users)
		_ = pool.QueryRow(ctx, "SELECT count(*) FROM platform.outbox_messages").Scan(&events)
		return
	}
	errAbort := errors.New("abort")
	write := func(u domain.User, fail bool) error {
		return runner.RunInTx(ctx, func(r app.Repos) error {
			if err := r.Users.Insert(ctx, u); err != nil {
				return err
			}
			if err := r.Events.Publish(ctx, domain.UserRegistered{UserID: u.ID, Email: u.Email, OccurredAt: now}); err != nil {
				return err
			}
			if fail {
				return errAbort
			}
			return nil
		})
	}
	if err := write(newUser("sub-rollback"), true); !errors.Is(err, errAbort) {
		t.Fatal(err)
	}
	if u, e := count(); u != 0 || e != 0 {
		t.Fatalf("after rollback users=%d events=%d", u, e)
	}
	if err := write(newUser("sub-commit"), false); err != nil {
		t.Fatal(err)
	}
	if u, e := count(); u != 1 || e != 1 {
		t.Fatalf("after commit users=%d events=%d", u, e)
	}
}

type stubGoogle struct{}

func (stubGoogle) Verify(context.Context, string) (domain.GoogleIdentity, error) {
	return domain.GoogleIdentity{Subject: "sub-race", Email: "race@example.com", EmailVerified: true}, nil
}

type stubIssuer struct{}

func (stubIssuer) Issue(uuid.UUID, auth.Role) (string, time.Duration, error) {
	return "access", time.Minute, nil
}

func TestConcurrentRefreshOneWins(t *testing.T) {
	ctx := context.Background()
	svc := app.NewService(postgres.NewTxRunner(pgtest.New(t)), stubGoogle{}, stubIssuer{}, clock.System{}, nil)
	s, err := svc.SignInWithGoogle(ctx, "x", app.Client{})
	if err != nil {
		t.Fatal(err)
	}

	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = svc.Refresh(ctx, s.RefreshToken, app.Client{})
		}()
	}
	wg.Wait()

	ok, reused := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, app.ErrRefreshReuse):
			reused++
		default:
			t.Fatalf("unexpected err %v", err)
		}
	}
	if ok != 1 || reused != 1 {
		t.Fatalf("ok=%d reused=%d", ok, reused)
	}
}
```

The loser's reuse detection also revokes the winner's new token; that is the intended strict reuse policy.

- [ ] **Step 5: Implement the adapter**

`internal/identity/adapters/postgres/postgres.go`:

```go
// Package postgres implements identity persistence on the identity schema.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/santoshkc2200/ioe-backend/internal/identity/adapters/postgres/sqlcgen"
	"github.com/santoshkc2200/ioe-backend/internal/identity/app"
	"github.com/santoshkc2200/ioe-backend/internal/identity/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/outbox"
)

const uniqueViolation = "23505"

// TxRunner runs identity use cases in one PostgreSQL transaction.
type TxRunner struct {
	pool *pgxpool.Pool
}

func NewTxRunner(pool *pgxpool.Pool) *TxRunner { return &TxRunner{pool: pool} }

func (r *TxRunner) RunInTx(ctx context.Context, fn func(app.Repos) error) error {
	return pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		q := sqlcgen.New(tx)
		return fn(app.Repos{Users: users{q}, Tokens: tokens{q}, Events: events{tx}})
	})
}

type users struct{ q *sqlcgen.Queries }

func (u users) FindByGoogleSubject(ctx context.Context, sub string) (domain.User, error) {
	row, err := u.q.GetUserByGoogleSub(ctx, sub)
	return toUser(row, err)
}

func (u users) FindByID(ctx context.Context, id uuid.UUID) (domain.User, error) {
	row, err := u.q.GetUserByID(ctx, id)
	return toUser(row, err)
}

func (u users) Insert(ctx context.Context, x domain.User) error {
	err := u.q.InsertUser(ctx, sqlcgen.InsertUserParams{
		ID: x.ID, GoogleSub: x.GoogleSubject, Email: x.Email, Name: x.Name, AvatarURL: x.AvatarURL,
		Role: string(x.Role), CreatedAt: x.CreatedAt, UpdatedAt: x.UpdatedAt, LastLoginAt: x.LastLoginAt,
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		return fmt.Errorf("%w: google subject exists", app.ErrConflict)
	}
	return err
}

func (u users) Update(ctx context.Context, x domain.User) error {
	return u.q.UpdateUser(ctx, sqlcgen.UpdateUserParams{
		ID: x.ID, Email: x.Email, Name: x.Name, AvatarURL: x.AvatarURL,
		Role: string(x.Role), UpdatedAt: x.UpdatedAt, LastLoginAt: x.LastLoginAt,
	})
}

func toUser(row sqlcgen.IdentityUser, err error) (domain.User, error) {
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.User{}, app.ErrNotFound
	}
	if err != nil {
		return domain.User{}, err
	}
	role, err := auth.ParseRole(row.Role)
	if err != nil {
		return domain.User{}, err
	}
	return domain.User{
		ID: row.ID, GoogleSubject: row.GoogleSub, Email: row.Email, Name: row.Name, AvatarURL: row.AvatarURL,
		Role: role, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, LastLoginAt: row.LastLoginAt,
	}, nil
}

type tokens struct{ q *sqlcgen.Queries }

func (t tokens) Insert(ctx context.Context, x domain.RefreshToken) error {
	return t.q.InsertRefreshToken(ctx, sqlcgen.InsertRefreshTokenParams{
		ID: x.ID, UserID: x.UserID, FamilyID: x.FamilyID, TokenHash: x.TokenHash,
		FamilyExpiresAt: x.FamilyExpiresAt, ExpiresAt: x.ExpiresAt, CreatedAt: x.CreatedAt,
		UserAgent: x.UserAgent, IP: x.IP,
	})
}

func (t tokens) FindByHashForUpdate(ctx context.Context, hash []byte) (domain.RefreshToken, error) {
	row, err := t.q.GetRefreshTokenByHashForUpdate(ctx, hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.RefreshToken{}, app.ErrNotFound
	}
	if err != nil {
		return domain.RefreshToken{}, err
	}
	return domain.RefreshToken{
		ID: row.ID, UserID: row.UserID, FamilyID: row.FamilyID, TokenHash: row.TokenHash,
		FamilyExpiresAt: row.FamilyExpiresAt, ExpiresAt: row.ExpiresAt,
		UsedAt: row.UsedAt, RevokedAt: row.RevokedAt, CreatedAt: row.CreatedAt,
		UserAgent: row.UserAgent, IP: row.IP,
	}, nil
}

func (t tokens) MarkUsed(ctx context.Context, id uuid.UUID, at time.Time) error {
	return t.q.MarkRefreshTokenUsed(ctx, sqlcgen.MarkRefreshTokenUsedParams{ID: id, UsedAt: &at})
}

func (t tokens) RevokeFamily(ctx context.Context, familyID uuid.UUID, at time.Time) error {
	return t.q.RevokeRefreshFamily(ctx, sqlcgen.RevokeRefreshFamilyParams{FamilyID: familyID, RevokedAt: &at})
}

type events struct{ tx pgx.Tx }

// Publish writes each event to the outbox in the current transaction, topic = event name.
func (e events) Publish(ctx context.Context, evs ...domain.Event) error {
	for _, ev := range evs {
		payload, err := json.Marshal(ev)
		if err != nil {
			return err
		}
		msg := message.NewMessage(uuid.NewString(), payload)
		msg.Metadata.Set("event_name", ev.EventName())
		if err := outbox.Publish(ctx, e.tx, ev.EventName(), msg); err != nil {
			return err
		}
	}
	return nil
}
```

- [ ] **Step 6: Run integration tests and lint**

Run: `go mod tidy && go test -race -tags integration ./internal/identity/adapters/postgres/ && make lint`
Expected: PASS; no issues (generated code is excluded from style linters by `generated: strict`).

- [ ] **Step 7: Add sqlc targets**

Append to `Makefile` (and `.PHONY`), and change `check`:

```make
sqlc:
	sqlc generate

sqlc-check:
	sqlc diff

check: tidy-check lint sqlc-check test vuln secrets
```

Run: `make sqlc-check` — Expected: no output, exit 0.

- [ ] **Step 8: Commit**

```bash
git add -A
git commit -m "feat(identity): persist users, refresh tokens, and events in PostgreSQL"
```

---

### Task 13: Identity HTTP adapter and OpenAPI contract

**Files:**
- Create: `internal/identity/adapters/httpapi/httpapi.go`
- Test: `internal/identity/adapters/httpapi/httpapi_test.go`
- Create: `api/openapi.yaml`

**Interfaces:**
- Consumes: `app.Session`, `app.Client`, app errors (Task 9); `domain.User`; `auth.Principal`, `auth.WithPrincipal`, `auth.PrincipalFrom`; httpserver `DecodeJSON`, `WriteJSON`, `NoStore`, `IPResolver`, `RateLimiter`; `problem.Write`.
- Produces: `httpapi.SessionService` interface (the four `*app.Service` methods), `httpapi.AccessTokenVerifier{Verify(string) (auth.Principal, error)}`, `httpapi.Config{CookieSecure bool; AllowedOrigins []string; Logger *slog.Logger; IPs httpserver.IPResolver; AuthLimiter *httpserver.RateLimiter}`, `httpapi.New(SessionService, AccessTokenVerifier, Config) (*Handler, error)`, `(*Handler).Register(*mux.Router)`, `(*Handler).RequireAuth(http.Handler) http.Handler` (for other contexts' protected routes).

- [ ] **Step 1: Write the failing tests**

`internal/identity/adapters/httpapi/httpapi_test.go`:

```go
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

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/santoshkc2200/ioe-backend/internal/identity/adapters/httpapi"
	"github.com/santoshkc2200/ioe-backend/internal/identity/app"
	"github.com/santoshkc2200/ioe-backend/internal/identity/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/httpserver"
)

const origin = "https://app.example.com"

var (
	user = domain.User{ID: uuid.MustParse("01920000-0000-7000-8000-000000000001"), Email: "a@example.com",
		Name: "A", AvatarURL: "https://img/a", Role: auth.RoleStudent}
	session = app.Session{AccessToken: "acc", AccessTokenTTL: 15 * time.Minute, RefreshToken: "ref-1",
		RefreshTokenTTL: 7 * 24 * time.Hour, User: user, Created: true}
	discard = slog.New(slog.NewJSONHandler(io.Discard, nil))
)

type fakeService struct {
	signInErr  error
	refreshErr error
	gotRefresh string
	gotLogout  string
	gotClient  app.Client
}

func (f *fakeService) SignInWithGoogle(_ context.Context, idToken string, c app.Client) (app.Session, error) {
	f.gotClient = c
	if f.signInErr != nil {
		return app.Session{}, f.signInErr
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

func (f *fakeService) GetMe(_ context.Context, id uuid.UUID) (domain.User, error) {
	if id != user.ID {
		return domain.User{}, app.ErrNotFound
	}
	return user, nil
}

type fakeVerifier struct{}

func (fakeVerifier) Verify(tok string) (auth.Principal, error) {
	if tok != "good" {
		return auth.Principal{}, app.ErrInvalidToken
	}
	return auth.Principal{UserID: user.ID, Role: user.Role}, nil
}

func newHandler(t *testing.T, svc *fakeService, secure bool, perMinute int) http.Handler {
	t.Helper()
	r, h := httpserver.NewRouter(httpserver.Options{Logger: discard, AllowedOrigins: []string{origin}, ServiceName: "test"})
	ih, err := httpapi.New(svc, fakeVerifier{}, httpapi.Config{
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

func TestSignInRecordsMetric(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	prev := otel.GetMeterProvider()
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
	t.Cleanup(func() { otel.SetMeterProvider(prev) })

	resp := do(newHandler(t, &fakeService{}, true, 100), http.MethodPost, "/v1/auth/google", `{"id_token":"x"}`, jsonHeader)
	resp.Body.Close()

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "identity.signins" {
				continue
			}
			sum := m.Data.(metricdata.Sum[int64])
			for _, dp := range sum.DataPoints {
				if v, _ := dp.Attributes.Value(attribute.Key("result")); v.AsString() == "created" && dp.Value == 1 {
					return
				}
			}
		}
	}
	t.Fatalf("identity.signins{result=created} not recorded: %+v", rm)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/identity/adapters/httpapi/`
Expected: FAIL (undefined `httpapi.New`).

- [ ] **Step 3: Implement**

`internal/identity/adapters/httpapi/httpapi.go`:

```go
// Package httpapi exposes identity use cases over HTTP.
package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/santoshkc2200/ioe-backend/internal/identity/app"
	"github.com/santoshkc2200/ioe-backend/internal/identity/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/httpserver"
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
	GetMe(ctx context.Context, userID uuid.UUID) (domain.User, error)
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
	svc      SessionService
	verifier AccessTokenVerifier
	cfg      Config
	origins  map[string]struct{}
	signins  metric.Int64Counter
	reuse    metric.Int64Counter
}

func New(svc SessionService, verifier AccessTokenVerifier, cfg Config) (*Handler, error) {
	meter := otel.Meter("github.com/santoshkc2200/ioe-backend/internal/identity")
	signins, err := meter.Int64Counter("identity.signins", metric.WithDescription("Google sign-in attempts by result"))
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
	return &Handler{svc: svc, verifier: verifier, cfg: cfg, origins: origins, signins: signins, reuse: reuse}, nil
}

// Register mounts the identity routes.
func (h *Handler) Register(r *mux.Router) {
	a := r.PathPrefix("/v1/auth").Subrouter()
	a.NotFoundHandler = r.NotFoundHandler
	a.MethodNotAllowedHandler = r.MethodNotAllowedHandler
	a.Use(httpserver.NoStore, mux.MiddlewareFunc(h.cfg.AuthLimiter.Middleware(h.cfg.IPs)))
	a.HandleFunc("/google", h.signIn).Methods(http.MethodPost)
	a.Handle("/refresh", h.requireOrigin(http.HandlerFunc(h.refresh))).Methods(http.MethodPost)
	a.Handle("/logout", h.requireOrigin(http.HandlerFunc(h.logout))).Methods(http.MethodPost)

	r.Handle("/v1/me", httpserver.NoStore(h.RequireAuth(http.HandlerFunc(h.me)))).Methods(http.MethodGet)
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
		return
	}
	if req.IDToken == "" {
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
	result := "rejected"
	switch {
	case err != nil:
	case sess.Created:
		result = "created"
	default:
		result = "existing"
	}
	h.signins.Add(ctx, 1, metric.WithAttributes(attribute.String("result", result)))
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
	http.SetCookie(w, &http.Cookie{
		Name: h.cookieName(), Value: sess.RefreshToken, Path: cookiePath,
		MaxAge: int(sess.RefreshTokenTTL / time.Second), HttpOnly: true,
		Secure: h.cfg.CookieSecure, SameSite: http.SameSiteStrictMode,
	})
}

func (h *Handler) clearRefreshCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: h.cookieName(), Value: "", Path: cookiePath, MaxAge: -1, HttpOnly: true,
		Secure: h.cfg.CookieSecure, SameSite: http.SameSiteStrictMode,
	})
}

func toUserResponse(u domain.User) userResponse {
	return userResponse{ID: u.ID.String(), Email: u.Email, Name: u.Name, AvatarURL: u.AvatarURL, Role: string(u.Role)}
}
```

`httptest.NewRequest` sets `RemoteAddr` to `192.0.2.1:1234`, which `TestSignInSuccess` asserts as the client IP.

- [ ] **Step 4: Run tests and lint**

Run: `go mod tidy && go test -race ./internal/identity/adapters/httpapi/ && make lint`
Expected: PASS; no issues.

- [ ] **Step 5: Write the OpenAPI contract**

`api/openapi.yaml`:

```yaml
openapi: 3.1.0
info:
  title: IOE Backend API
  version: 0.1.0
  description: >
    Single-tenant LMS backend. Authentication exchanges a Google ID token for an
    access token (Bearer, 15 minutes) and a rotating refresh token held in an
    HttpOnly cookie scoped to /v1/auth.
servers:
  - url: https://api.example.com
paths:
  /healthz:
    get:
      summary: Liveness
      responses:
        "200": { $ref: "#/components/responses/Status" }
  /readyz:
    get:
      summary: Readiness (database reachable)
      responses:
        "200": { $ref: "#/components/responses/Status" }
        "503": { $ref: "#/components/responses/Status" }
  /v1/auth/google:
    post:
      summary: Sign in with a Google ID token
      requestBody:
        required: true
        content:
          application/json:
            schema:
              type: object
              additionalProperties: false
              required: [id_token]
              properties:
                id_token: { type: string, minLength: 1 }
      responses:
        "200":
          description: Signed in
          headers:
            Set-Cookie: { $ref: "#/components/headers/RefreshCookie" }
          content:
            application/json:
              schema:
                allOf:
                  - $ref: "#/components/schemas/TokenResponse"
                  - type: object
                    required: [user]
                    properties:
                      user: { $ref: "#/components/schemas/User" }
        "400": { $ref: "#/components/responses/Problem" }
        "401": { $ref: "#/components/responses/Problem" }
        "403": { $ref: "#/components/responses/Problem" }
        "413": { $ref: "#/components/responses/Problem" }
        "415": { $ref: "#/components/responses/Problem" }
        "429": { $ref: "#/components/responses/Problem" }
  /v1/auth/refresh:
    post:
      summary: Rotate the refresh token and issue a new access token
      parameters:
        - $ref: "#/components/parameters/Origin"
      security:
        - refreshCookie: []
      responses:
        "200":
          description: Refreshed
          headers:
            Set-Cookie: { $ref: "#/components/headers/RefreshCookie" }
          content:
            application/json:
              schema: { $ref: "#/components/schemas/TokenResponse" }
        "401":
          description: invalid_token or refresh_reuse_detected; the cookie is cleared
          content:
            application/problem+json:
              schema: { $ref: "#/components/schemas/Problem" }
        "403": { $ref: "#/components/responses/Problem" }
        "429": { $ref: "#/components/responses/Problem" }
  /v1/auth/logout:
    post:
      summary: Revoke the session's refresh-token family
      parameters:
        - $ref: "#/components/parameters/Origin"
      responses:
        "204":
          description: Logged out; the cookie is cleared
        "403": { $ref: "#/components/responses/Problem" }
        "429": { $ref: "#/components/responses/Problem" }
  /v1/me:
    get:
      summary: Current user
      security:
        - bearer: []
      responses:
        "200":
          description: The authenticated user
          content:
            application/json:
              schema: { $ref: "#/components/schemas/User" }
        "401": { $ref: "#/components/responses/Problem" }
        "404": { $ref: "#/components/responses/Problem" }
components:
  securitySchemes:
    bearer:
      type: http
      scheme: bearer
      bearerFormat: JWT
    refreshCookie:
      type: apiKey
      in: cookie
      name: __Secure-ioe_refresh
  parameters:
    Origin:
      name: Origin
      in: header
      required: true
      description: Must exactly match a configured allowed origin.
      schema: { type: string }
  headers:
    RefreshCookie:
      description: >
        __Secure-ioe_refresh=<token>; Path=/v1/auth; Max-Age=<seconds>; HttpOnly; Secure; SameSite=Strict
      schema: { type: string }
  responses:
    Status:
      description: Health status
      content:
        application/json:
          schema:
            type: object
            properties:
              status: { type: string, enum: [ok, unavailable] }
    Problem:
      description: Error
      content:
        application/problem+json:
          schema: { $ref: "#/components/schemas/Problem" }
  schemas:
    TokenResponse:
      type: object
      required: [access_token, token_type, expires_in]
      properties:
        access_token: { type: string }
        token_type: { type: string, const: Bearer }
        expires_in: { type: integer, description: Seconds until the access token expires }
    User:
      type: object
      required: [id, email, name, avatar_url, role]
      properties:
        id: { type: string, format: uuid }
        email: { type: string, format: email }
        name: { type: string }
        avatar_url: { type: string }
        role: { type: string, enum: [student, instructor, root_admin] }
    Problem:
      type: object
      required: [type, title, status]
      properties:
        type:
          type: string
          enum:
            - invalid_request
            - invalid_token
            - refresh_reuse_detected
            - email_unverified
            - origin_not_allowed
            - not_found
            - method_not_allowed
            - payload_too_large
            - unsupported_media_type
            - rate_limited
            - internal
        title: { type: string }
        status: { type: integer }
        detail: { type: string }
        instance: { type: string, description: Request ID }
```

Validate: `npx --yes @redocly/cli@latest lint api/openapi.yaml`
Expected: no errors (warnings about missing `operationId`/license are acceptable; add `operationId`s if the linter reports them as errors).

- [ ] **Step 6: Commit**

```bash
git add -A
git commit -m "feat(identity): expose sign-in, refresh, logout, and me over HTTP"
```

---

### Task 14: Composition root and end-to-end test

**Files:**
- Create: `cmd/api/main.go`, `cmd/api/app.go`
- Test: `cmd/api/e2e_integration_test.go`

**Interfaces:**
- Consumes: everything above.
- Produces: binary `api` with `api` (serve) and `api migrate up|down|status`; `buildApp(ctx, config.Config, *slog.Logger, *pgxpool.Pool) (*application, error)` with fields `handler http.Handler`, `forwarder *outbox.Forwarder`.

- [ ] **Step 1: Add the dependency**

```bash
go get golang.org/x/sync@latest
```

- [ ] **Step 2: Write the failing end-to-end test**

`cmd/api/e2e_integration_test.go`:

```go
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

	resp, body := c.do(http.MethodGet, "/readyz", "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("readyz %d", resp.StatusCode)
	}

	resp, body = c.do(http.MethodPost, "/v1/auth/google", `{"id_token":"`+idToken+`"}`, nil)
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
```

- [ ] **Step 3: Run it to verify it fails**

Run: `go test -tags integration ./cmd/api/`
Expected: FAIL (`undefined: buildApp`).

- [ ] **Step 4: Implement**

`cmd/api/app.go`:

```go
package main

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/gorilla/mux"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/santoshkc2200/ioe-backend/internal/identity/adapters/google"
	"github.com/santoshkc2200/ioe-backend/internal/identity/adapters/httpapi"
	"github.com/santoshkc2200/ioe-backend/internal/identity/adapters/jwt"
	identitypg "github.com/santoshkc2200/ioe-backend/internal/identity/adapters/postgres"
	identityapp "github.com/santoshkc2200/ioe-backend/internal/identity/app"
	"github.com/santoshkc2200/ioe-backend/internal/platform/clock"
	"github.com/santoshkc2200/ioe-backend/internal/platform/config"
	"github.com/santoshkc2200/ioe-backend/internal/platform/httpserver"
	"github.com/santoshkc2200/ioe-backend/internal/platform/outbox"
)

const serviceName = "ioe-backend"

type application struct {
	handler   http.Handler
	forwarder *outbox.Forwarder
}

// buildApp wires every bounded context. ctx bounds background work such as JWKS refresh.
func buildApp(ctx context.Context, cfg config.Config, logger *slog.Logger, pool *pgxpool.Pool) (*application, error) {
	clk := clock.System{}

	keys, err := jwt.ParseKeys(cfg.JWTSigningKeyPEM, cfg.JWTSigningKeyID, cfg.JWTVerifyKeys)
	if err != nil {
		return nil, err
	}
	tokens := jwt.New(keys, cfg.JWTIssuer, cfg.JWTAudience, clk)
	googleVerifier, err := google.NewVerifier(ctx, cfg.GoogleJWKSURL, cfg.GoogleClientIDs, clk)
	if err != nil {
		return nil, err
	}
	identity := identityapp.NewService(identitypg.NewTxRunner(pool), googleVerifier, tokens, clk, cfg.BootstrapRootAdminEmails)

	router, handler := httpserver.NewRouter(httpserver.Options{
		Logger: logger, AllowedOrigins: cfg.AllowedOrigins, ServiceName: serviceName,
	})
	httpserver.MountHealth(router, pool.Ping)
	if err := registerIdentity(router, identity, tokens, cfg, logger); err != nil {
		return nil, err
	}

	fw, err := outbox.NewForwarder(pool, logger)
	if err != nil {
		return nil, err
	}
	return &application{handler: handler, forwarder: fw}, nil
}

func registerIdentity(r *mux.Router, svc *identityapp.Service, tokens *jwt.Tokens, cfg config.Config, logger *slog.Logger) error {
	h, err := httpapi.New(svc, tokens, httpapi.Config{
		CookieSecure:   cfg.CookieSecure,
		AllowedOrigins: cfg.AllowedOrigins,
		Logger:         logger,
		IPs:            httpserver.NewIPResolver(cfg.TrustedProxies()),
		AuthLimiter:    httpserver.NewRateLimiter(cfg.AuthRateLimitPerMinute),
	})
	if err != nil {
		return err
	}
	h.Register(r)
	return nil
}
```

`cmd/api/main.go`:

```go
// Command api runs the IOE backend HTTP server and its database migrations.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"golang.org/x/sync/errgroup"

	"github.com/santoshkc2200/ioe-backend/internal/platform/config"
	"github.com/santoshkc2200/ioe-backend/internal/platform/httpserver"
	"github.com/santoshkc2200/ioe-backend/internal/platform/logging"
	"github.com/santoshkc2200/ioe-backend/internal/platform/migrate"
	"github.com/santoshkc2200/ioe-backend/internal/platform/postgres"
	"github.com/santoshkc2200/ioe-backend/internal/platform/telemetry"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout io.Writer) error {
	switch {
	case len(args) == 0:
		return serve(ctx)
	case args[0] == "migrate":
		return runMigrate(ctx, args[1:], stdout)
	default:
		return fmt.Errorf("unknown command %q (usage: api [migrate up|down|status])", args[0])
	}
}

func runMigrate(ctx context.Context, args []string, w io.Writer) error {
	if len(args) != 1 {
		return errors.New("usage: api migrate up|down|status")
	}
	url, err := config.LoadDatabaseURL()
	if err != nil {
		return err
	}
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return err
	}
	defer pool.Close()
	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()
	return migrate.Run(ctx, db, args[0], w)
}

func serve(parent context.Context) error {
	ctx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	level, _ := cfg.SlogLevel()

	tel, err := telemetry.Setup(ctx, version)
	if err != nil {
		return fmt.Errorf("telemetry: %w", err)
	}
	defer func() {
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = tel.Shutdown(sctx)
	}()
	logger := logging.New(level, os.Stdout, tel.LogHandler)
	slog.SetDefault(logger)

	pool, err := postgres.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	defer pool.Close()

	a, err := buildApp(ctx, cfg, logger, pool)
	if err != nil {
		return err
	}
	defer a.forwarder.Close()

	ln, err := net.Listen("tcp", cfg.HTTPAddr)
	if err != nil {
		return err
	}
	logger.InfoContext(ctx, "server started", "addr", ln.Addr().String(), "version", version)

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return httpserver.Serve(gctx, ln, a.handler, logger) })
	g.Go(func() error {
		if err := a.forwarder.Run(gctx); err != nil && gctx.Err() == nil {
			return fmt.Errorf("outbox forwarder: %w", err)
		}
		return nil
	})
	err = g.Wait()
	logger.InfoContext(context.WithoutCancel(ctx), "server stopped")
	return err
}
```

- [ ] **Step 5: Run the end-to-end test and all gates**

Run: `go mod tidy && go test -race -tags integration ./cmd/api/ && go build ./... && make lint test`
Expected: PASS; build succeeds; no lint issues.

Run: `go run ./cmd/api migrate nope; echo "exit=$?"`
Expected: `error: DATABASE_URL is required` (or `unknown migrate command` when `DATABASE_URL` is set) and `exit=1`.

Run: `env -i PATH="$PATH" HOME="$HOME" go run ./cmd/api; echo "exit=$?"`
Expected: `error: config: env: required environment variable "DATABASE_URL" is not set` (other missing variables listed too) and `exit=1`.

- [ ] **Step 6: Commit**

```bash
git add -A
git commit -m "feat: wire API server, migrations command, and end-to-end test"
```

---

### Task 15: Container image and local stack

**Files:**
- Create: `Dockerfile`, `.dockerignore`, `docker-compose.yml`, `.env.example`
- Modify: `Makefile` (add `docker-build`, `compose-up`, `compose-down`)

**Interfaces:**
- Consumes: `cmd/api` binary and its `migrate` subcommand; `/readyz`.
- Produces: image `ioe-backend:local`; compose services `postgres`, `openobserve`, and (profile `app`) `migrate`, `api`.

- [ ] **Step 1: Resolve pinned versions and digests**

```bash
go run github.com/google/go-containerregistry/cmd/crane@latest digest golang:1.27-alpine
go run github.com/google/go-containerregistry/cmd/crane@latest digest gcr.io/distroless/static-debian12:nonroot
go run github.com/google/go-containerregistry/cmd/crane@latest digest postgres:17-alpine
gh release list -R openobserve/openobserve --exclude-pre-releases -L 1
```

Record the three `sha256:` digests and the newest stable OpenObserve tag (for example `v0.x.y`). Use them in place of `GOLANG_DIGEST`, `DISTROLESS_DIGEST`, `POSTGRES_DIGEST`, and `OPENOBSERVE_TAG` below. If `golang:1.27-alpine` does not exist, use the newest `golang:1.27.x-alpine` tag.

- [ ] **Step 2: Write the Dockerfile and ignore file**

`Dockerfile`:

```dockerfile
# syntax=docker/dockerfile:1

FROM golang:1.27-alpine@GOLANG_DIGEST AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download && go mod verify
COPY . .
ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/api ./cmd/api

FROM gcr.io/distroless/static-debian12:nonroot@DISTROLESS_DIGEST
COPY --from=build /out/api /api
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/api"]
```

`.dockerignore`:

```gitignore
.git
.github
.env
.env.*
!.env.example
bin
docs
coverage.out
*.test
**/*_test.go
```

- [ ] **Step 3: Write the compose file and example environment**

`docker-compose.yml`:

```yaml
name: ioe-backend

services:
  postgres:
    image: postgres:17-alpine@POSTGRES_DIGEST
    environment:
      POSTGRES_DB: ioe
      POSTGRES_USER: ioe
      POSTGRES_PASSWORD: ioe
    ports:
      - "127.0.0.1:5432:5432"
    volumes:
      - pgdata:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U ioe -d ioe"]
      interval: 2s
      timeout: 3s
      retries: 30

  openobserve:
    image: public.ecr.aws/zinclabs/openobserve:OPENOBSERVE_TAG
    environment:
      ZO_ROOT_USER_EMAIL: ${ZO_ROOT_USER_EMAIL:-root@example.com}
      ZO_ROOT_USER_PASSWORD: ${ZO_ROOT_USER_PASSWORD:-Complexpass#123}
      ZO_DATA_DIR: /data
    ports:
      - "127.0.0.1:5080:5080"
    volumes:
      - o2data:/data

  migrate:
    profiles: [app]
    build: .
    command: ["migrate", "up"]
    environment:
      DATABASE_URL: postgres://ioe:ioe@postgres:5432/ioe?sslmode=disable
    depends_on:
      postgres:
        condition: service_healthy

  api:
    profiles: [app]
    build: .
    env_file: .env
    environment:
      DATABASE_URL: postgres://ioe:ioe@postgres:5432/ioe?sslmode=disable
      HTTP_ADDR: ":8080"
      OTEL_EXPORTER_OTLP_ENDPOINT: http://openobserve:5080/api/default
    read_only: true
    security_opt:
      - no-new-privileges:true
    cap_drop: [ALL]
    ports:
      - "127.0.0.1:8080:8080"
    depends_on:
      migrate:
        condition: service_completed_successfully
      openobserve:
        condition: service_started

volumes:
  pgdata:
  o2data:
```

`.env.example`:

```sh
# Development values only. Copy to .env (git-ignored) and adjust.
HTTP_ADDR=:8080
DATABASE_URL=postgres://ioe:ioe@localhost:5432/ioe?sslmode=disable

GOOGLE_CLIENT_IDS=replace-me.apps.googleusercontent.com
JWT_ISSUER=http://localhost:8080
JWT_AUDIENCE=ioe
# Generate with: make keygen
JWT_SIGNING_KEY=
JWT_SIGNING_KEY_ID=dev-1
JWT_VERIFY_KEYS=

ALLOWED_ORIGINS=http://localhost:5173,http://localhost:5174,http://localhost:3000
BOOTSTRAP_ROOT_ADMIN_EMAILS=
COOKIE_SECURE=false
AUTH_RATE_LIMIT_PER_MINUTE=30
TRUSTED_PROXY_CIDRS=
LOG_LEVEL=debug

# Local OpenObserve (docker compose). Basic auth for root@example.com:Complexpass#123.
OTEL_SERVICE_NAME=ioe-backend
OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:5080/api/default
OTEL_EXPORTER_OTLP_HEADERS=Authorization=Basic%20cm9vdEBleGFtcGxlLmNvbTpDb21wbGV4cGFzcyMxMjM=
OTEL_RESOURCE_ATTRIBUTES=deployment.environment=local
```

`OTEL_EXPORTER_OTLP_HEADERS` values are URL-encoded per the OpenTelemetry spec, so the space after `Basic` is `%20`.

Append to `Makefile` (and `.PHONY`):

```make
docker-build:
	docker build --build-arg VERSION=$$(git describe --tags --always --dirty) -t ioe-backend:local .

compose-up:
	docker compose up -d --wait postgres openobserve

compose-down:
	docker compose down
```

- [ ] **Step 4: Verify the image and stack**

Run: `docker compose config --quiet && echo compose-ok`
Expected: `compose-ok`.

Run: `make docker-build && docker run --rm ioe-backend:local; echo "exit=$?"`
Expected: build succeeds; container prints the missing-config error and `exit=1`. Then `docker image inspect ioe-backend:local --format '{{.Config.User}}'` prints `nonroot:nonroot`.

Run the full stack smoke test:

```bash
cp .env.example .env
KEY=$(go run ./cmd/keygen | sed -n 's/^JWT_SIGNING_KEY=//p')
sed -i '' "s|^JWT_SIGNING_KEY=.*|JWT_SIGNING_KEY=${KEY}|" .env
docker compose --profile app up -d --build --wait
curl -fsS localhost:8080/readyz
curl -s -o /dev/null -w '%{http_code}\n' -X POST -H 'Content-Type: application/json' -d '{"id_token":"bad"}' localhost:8080/v1/auth/google
sleep 10
curl -fsS -u 'root@example.com:Complexpass#123' 'http://localhost:5080/api/default/streams?type=traces' | grep -o '"name":"[^"]*"' | head
docker compose --profile app down
```

Expected: `{"status":"ok"}`; `401`; the OpenObserve stream listing includes a traces stream (typically `"name":"default"`). If the traces listing is empty, check `docker compose logs api` for exporter errors before proceeding.

- [ ] **Step 5: Commit**

```bash
git add Dockerfile .dockerignore docker-compose.yml .env.example Makefile
git commit -m "build: add distroless image and local compose stack with OpenObserve"
```

---

### Task 16: CI, supply-chain, and repository security configuration

**Files:**
- Create: `.github/workflows/ci.yml`, `.github/workflows/codeql.yml`, `.github/workflows/scorecard.yml`, `.github/workflows/release.yml`
- Create: `.github/dependabot.yml`, `.github/CODEOWNERS`

**Interfaces:**
- Consumes: Makefile targets, Dockerfile.
- Produces: CI jobs `lint`, `test`, `integration`, `security`, `image`; release publishing `ghcr.io/santoshkc2200/ioe-backend`.

- [ ] **Step 1: Write the workflows (version tags first; pinned in Step 3)**

`.github/workflows/ci.yml`:

```yaml
name: ci

on:
  pull_request:
  push:
    branches: [main]

permissions:
  contents: read

concurrency:
  group: ci-${{ github.ref }}
  cancel-in-progress: true

jobs:
  lint:
    runs-on: ubuntu-24.04
    steps:
      - uses: step-security/harden-runner@v2
        with:
          egress-policy: audit
      - uses: actions/checkout@v5
        with:
          persist-credentials: false
      - uses: actions/setup-go@v6
        with:
          go-version-file: go.mod
      - uses: golangci/golangci-lint-action@v8
        with:
          version: v2.13.2
      - uses: sqlc-dev/setup-sqlc@v4
        with:
          sqlc-version: "1.31.1"
      - run: sqlc diff
      - run: go mod tidy -diff
      - run: go mod verify

  test:
    runs-on: ubuntu-24.04
    steps:
      - uses: step-security/harden-runner@v2
        with:
          egress-policy: audit
      - uses: actions/checkout@v5
        with:
          persist-credentials: false
      - uses: actions/setup-go@v6
        with:
          go-version-file: go.mod
      - run: go test -race -coverprofile=coverage.out ./...
      - uses: actions/upload-artifact@v4
        with:
          name: coverage
          path: coverage.out

  integration:
    runs-on: ubuntu-24.04
    steps:
      - uses: step-security/harden-runner@v2
        with:
          egress-policy: audit
      - uses: actions/checkout@v5
        with:
          persist-credentials: false
      - uses: actions/setup-go@v6
        with:
          go-version-file: go.mod
      - run: go test -race -tags integration ./...

  security:
    runs-on: ubuntu-24.04
    steps:
      - uses: step-security/harden-runner@v2
        with:
          egress-policy: audit
      - uses: actions/checkout@v5
        with:
          persist-credentials: false
          fetch-depth: 0
      - uses: actions/setup-go@v6
        with:
          go-version-file: go.mod
      - run: go run golang.org/x/vuln/cmd/govulncheck@v1.8.0 ./...
      - run: go run github.com/zricethezav/gitleaks/v8@v8.30.1 git --redact --no-banner .

  image:
    runs-on: ubuntu-24.04
    steps:
      - uses: step-security/harden-runner@v2
        with:
          egress-policy: audit
      - uses: actions/checkout@v5
        with:
          persist-credentials: false
      - uses: docker/setup-buildx-action@v3
      - uses: docker/build-push-action@v6
        with:
          context: .
          load: true
          push: false
          tags: ioe-backend:ci
      - uses: aquasecurity/trivy-action@0.33.1
        with:
          image-ref: ioe-backend:ci
          severity: HIGH,CRITICAL
          ignore-unfixed: true
          exit-code: "1"
      - uses: anchore/sbom-action@v0
        with:
          image: ioe-backend:ci
          format: spdx-json
          output-file: sbom.spdx.json
          upload-artifact: false
      - uses: actions/upload-artifact@v4
        with:
          name: sbom
          path: sbom.spdx.json
```

`.github/workflows/codeql.yml`:

```yaml
name: codeql

on:
  pull_request:
  push:
    branches: [main]
  schedule:
    - cron: "17 3 * * 1"

permissions:
  contents: read

jobs:
  analyze:
    runs-on: ubuntu-24.04
    permissions:
      contents: read
      security-events: write
    steps:
      - uses: step-security/harden-runner@v2
        with:
          egress-policy: audit
      - uses: actions/checkout@v5
        with:
          persist-credentials: false
      - uses: actions/setup-go@v6
        with:
          go-version-file: go.mod
      - uses: github/codeql-action/init@v3
        with:
          languages: go
          build-mode: autobuild
      - uses: github/codeql-action/analyze@v3
```

`.github/workflows/scorecard.yml`:

```yaml
name: scorecard

on:
  push:
    branches: [main]
  schedule:
    - cron: "29 4 * * 2"

permissions: read-all

jobs:
  analysis:
    runs-on: ubuntu-24.04
    permissions:
      security-events: write
      id-token: write
      contents: read
      actions: read
    steps:
      - uses: step-security/harden-runner@v2
        with:
          egress-policy: audit
      - uses: actions/checkout@v5
        with:
          persist-credentials: false
      - uses: ossf/scorecard-action@v2
        with:
          results_file: results.sarif
          results_format: sarif
          publish_results: true
      - uses: github/codeql-action/upload-sarif@v3
        with:
          sarif_file: results.sarif
```

`.github/workflows/release.yml`:

```yaml
name: release

on:
  push:
    tags: ["v*"]

permissions:
  contents: read

jobs:
  image:
    runs-on: ubuntu-24.04
    permissions:
      contents: read
      packages: write
      id-token: write
      attestations: write
    env:
      IMAGE: ghcr.io/${{ github.repository }}
    steps:
      - uses: step-security/harden-runner@v2
        with:
          egress-policy: audit
      - uses: actions/checkout@v5
        with:
          persist-credentials: false
      - uses: docker/setup-qemu-action@v3
      - uses: docker/setup-buildx-action@v3
      - uses: docker/login-action@v3
        with:
          registry: ghcr.io
          username: ${{ github.actor }}
          password: ${{ secrets.GITHUB_TOKEN }}
      - id: meta
        uses: docker/metadata-action@v5
        with:
          images: ${{ env.IMAGE }}
          tags: |
            type=semver,pattern={{version}}
            type=sha
      - id: build
        uses: docker/build-push-action@v6
        with:
          context: .
          platforms: linux/amd64,linux/arm64
          push: true
          tags: ${{ steps.meta.outputs.tags }}
          labels: ${{ steps.meta.outputs.labels }}
          build-args: VERSION=${{ github.ref_name }}
          provenance: mode=max
          sbom: true
      - uses: sigstore/cosign-installer@v3
      - name: Sign image
        env:
          DIGEST: ${{ steps.build.outputs.digest }}
        run: cosign sign --yes "${IMAGE}@${DIGEST}"
      - uses: actions/attest-build-provenance@v3
        with:
          subject-name: ${{ env.IMAGE }}
          subject-digest: ${{ steps.build.outputs.digest }}
          push-to-registry: true
```

`.github/dependabot.yml`:

```yaml
version: 2
updates:
  - package-ecosystem: gomod
    directory: /
    schedule:
      interval: weekly
    groups:
      go-dependencies:
        patterns: ["*"]
  - package-ecosystem: github-actions
    directory: /
    schedule:
      interval: weekly
    groups:
      actions:
        patterns: ["*"]
  - package-ecosystem: docker
    directory: /
    schedule:
      interval: weekly
  - package-ecosystem: docker-compose
    directory: /
    schedule:
      interval: weekly
```

`.github/CODEOWNERS`:

```text
* @santoshkc2200
```

- [ ] **Step 2: Lint the workflows**

Run: `go run github.com/rhysd/actionlint/cmd/actionlint@latest`
Expected: no output.

- [ ] **Step 3: Update to current majors and pin every action by SHA**

Run: `go run github.com/suzuki-shunsuke/pinact/v3/cmd/pinact@v3.10.1 run -u`
Expected: every `uses:` becomes `owner/repo@<40-hex-sha> # vX.Y.Z`. Then:

Run: `grep -hE '^\s*-?\s*uses:' .github/workflows/*.yml | grep -vE '@[0-9a-f]{40} # v'`
Expected: no output (nothing left unpinned).

For `aquasecurity/trivy-action`, confirm the pinned SHA matches a release listed at `https://github.com/aquasecurity/trivy-action/releases` (its tags were force-pushed in a 2026 supply-chain incident; trust only a SHA you have checked against the release page).

Run `actionlint` again — Expected: no output.

- [ ] **Step 4: Commit**

```bash
git add .github
git commit -m "ci: add hardened CI, CodeQL, Scorecard, signed releases, and Dependabot"
```

---

### Task 17: Final verification and documentation sync

**Files:**
- Modify: `docs/superpowers/specs/2026-10-04-foundation-identity-design.md` (record implementation deviations)
- Modify: `README.md` if any command changed

- [ ] **Step 1: Run every gate from a clean state**

```bash
git status --porcelain            # expect empty
go clean -testcache
make check
make test-integration
docker compose config --quiet
make docker-build
git diff --check
```

Expected: all succeed. Record the actual output summary of each (pass counts for tests, `No vulnerabilities found.`, `no leaks found`).

- [ ] **Step 2: Residue scan**

Run: `grep -rnE "TODO|FIXME|GOLANG_DIGEST|DISTROLESS_DIGEST|POSTGRES_DIGEST|OPENOBSERVE_TAG" --exclude-dir=.git --exclude-dir=docs . || echo clean`
Expected: `clean`.

Run: `grep -rn "tenant" --include='*.go' --include='*.sql' --include='*.yaml' . || echo clean`
Expected: `clean`.

- [ ] **Step 3: Sync the spec with implementation details**

Update the spec so it matches what was built:
- Error table: add `415 unsupported_media_type` (JSON content type required, blocks cross-site form posts) and `405 method_not_allowed`.
- Repository layout: HTTP adapter directory is `internal/identity/adapters/httpapi`.
- Platform: there is no `WithTx` helper; the identity `TxRunner` uses `pgx.BeginFunc` directly.
- Telemetry: metric `identity.signins` and `identity.refresh_reuse_detected` are recorded by the HTTP adapter.

- [ ] **Step 4: Commit**

```bash
git add -A
git commit -m "docs: sync design spec with implementation"
```
