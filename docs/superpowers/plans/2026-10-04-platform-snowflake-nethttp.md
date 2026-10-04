# Platform Foundation (Snowflake IDs, net/http Router) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace UUID entity IDs with Snowflake `int64` IDs project-wide and replace `gorilla/mux` with the standard library `http.ServeMux`, keeping every existing HTTP behavior.

**Architecture:** A new `internal/platform/id` package owns the ID type and generator. Identity (the only context with entities) switches to it, including the JWT `sub`. `internal/platform/httpserver` gets a thin `Router` type that embeds `*http.ServeMux`, renders unmatched requests as problem+json, and names spans by route pattern.

**Tech Stack:** Go 1.27, `github.com/bwmarrin/snowflake`, `net/http` ServeMux patterns, `otelhttp`, pgx v5, sqlc, goose.

**Spec:** `docs/superpowers/specs/2026-10-04-platform-snowflake-nethttp-design.md`

## Global Constraints

- Module path: `github.com/santoshkc2200/ioe-backend`.
- IDs are `id.ID` (`int64`) in Go, `bigint` in PostgreSQL, and a JSON **string** of the decimal value on the wire.
- `SNOWFLAKE_NODE_ID`: integer 0-1023, default `0`; startup fails outside the range.
- Watermill message UUIDs, the request-ID middleware and JWT `jti` keep using `github.com/google/uuid`.
- Every route pattern includes a method (`"GET /v1/me"`), never a bare path.
- 404 and 405 stay `application/problem+json` with types `not_found` and `method_not_allowed`; 405 carries `Allow`.
- `migrations/00002_identity.sql` is edited in place (nothing is deployed).
- Conventional Commits. Each task ends with `make test` passing; the final task runs every gate in `AGENTS.md`.

## Review Focus

- A JSON number sent where an ID is expected (`{"user_id": 123}`) must be rejected, not silently accepted, so clients cannot lose precision above 2^53. Test in Task 1.
- A path that exists under another method (`GET /v1/auth/google`) must return 405 with `Allow: POST`, `Cache-Control: no-store`, and count against the auth rate limit. Test in Task 4.
- A non-canonical path (`/v1//me`, `/v1/me/`) must not 500 and must not bypass auth; ServeMux redirects or 404s. Test in Task 4.
- A token whose `sub` is a UUID (minted before this change) must be rejected as `invalid_token`, not panic. Test in Task 3.
- An ID string with leading `+`, whitespace, or `0` must fail `id.Parse`. Test in Task 1.

---

### Task 0: Clean working tree

**Files:** none.

- [ ] **Step 1: Check the tree**

Run: `git status --short`
Expected: no modified files under `internal/platform/httpserver`, `internal/identity`, or `api/openapi.yaml`. If there are, stop and ask the user to commit their in-progress work first. Do not commit it for them without asking.

---

### Task 1: `platform/id` package

**Files:**
- Create: `internal/platform/id/id.go`
- Create: `internal/platform/id/id_test.go`
- Modify: `go.mod`, `go.sum` (add `github.com/bwmarrin/snowflake v0.3.0`)

**Interfaces:**
- Produces: `type ID int64`; `func Parse(s string) (ID, error)`; `func (ID) String() string`; `func (ID) IsZero() bool`; `func (ID) MarshalJSON() ([]byte, error)`; `func (*ID) UnmarshalJSON([]byte) error`; `type Generator struct`; `func NewGenerator(nodeID int64) (*Generator, error)`; `func (*Generator) New() ID`; `var ErrInvalid error`.

- [ ] **Step 1: Write the failing tests**

```go
package id_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

func TestParse(t *testing.T) {
	good := map[string]id.ID{"1": 1, "1840396745219883008": 1840396745219883008, "9223372036854775807": 9223372036854775807}
	for s, want := range good {
		got, err := id.Parse(s)
		if err != nil || got != want {
			t.Fatalf("Parse(%q) = %d, %v; want %d", s, got, err, want)
		}
	}
	for _, s := range []string{"", "0", "-1", "+1", " 1", "1 ", "01", "1a", "9223372036854775808", "01920000-0000-7000-8000-000000000001"} {
		if _, err := id.Parse(s); !errors.Is(err, id.ErrInvalid) {
			t.Fatalf("Parse(%q) error = %v, want ErrInvalid", s, err)
		}
	}
}

func TestJSONRoundTripAsString(t *testing.T) {
	type doc struct {
		ID id.ID `json:"id"`
	}
	b, err := json.Marshal(doc{ID: 1840396745219883008})
	if err != nil || string(b) != `{"id":"1840396745219883008"}` {
		t.Fatalf("marshal = %s, %v", b, err)
	}
	var d doc
	if err := json.Unmarshal(b, &d); err != nil || d.ID != 1840396745219883008 {
		t.Fatalf("unmarshal = %d, %v", d.ID, err)
	}
}

func TestUnmarshalRejectsNumbersAndBadStrings(t *testing.T) {
	for _, in := range []string{`123`, `"abc"`, `""`, `null`, `"0"`} {
		var v id.ID
		if err := json.Unmarshal([]byte(in), &v); err == nil {
			t.Fatalf("Unmarshal(%s) succeeded with %d", in, v)
		}
	}
}

func TestGenerator(t *testing.T) {
	for _, n := range []int64{-1, 1024} {
		if _, err := id.NewGenerator(n); err == nil {
			t.Fatalf("NewGenerator(%d) succeeded", n)
		}
	}
	g, err := id.NewGenerator(1023)
	if err != nil {
		t.Fatal(err)
	}
	prev := g.New()
	for range 1000 {
		next := g.New()
		if next <= prev || next.IsZero() {
			t.Fatalf("ids not increasing: %d then %d", prev, next)
		}
		prev = next
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/platform/id/`
Expected: FAIL, package `id` has no Go files.

- [ ] **Step 3: Implement**

```go
// Package id provides the Snowflake identifier used for every entity.
package id

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/bwmarrin/snowflake"
)

// ErrInvalid reports a malformed identifier.
var ErrInvalid = errors.New("invalid id")

// ID is a positive Snowflake identifier. It is a JSON string on the wire because
// JavaScript numbers cannot represent every 63-bit value.
type ID int64

// Parse reads a canonical decimal ID: digits only, no sign, no leading zero, positive.
func Parse(s string) (ID, error) {
	if s == "" || s[0] == '0' {
		return 0, fmt.Errorf("%w: %q", ErrInvalid, s)
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, fmt.Errorf("%w: %q", ErrInvalid, s)
		}
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: %q", ErrInvalid, s)
	}
	return ID(v), nil
}

func (i ID) String() string { return strconv.FormatInt(int64(i), 10) }

// IsZero reports whether i is unset.
func (i ID) IsZero() bool { return i == 0 }

func (i ID) MarshalJSON() ([]byte, error) { return strconv.AppendQuote(nil, i.String()), nil }

func (i *ID) UnmarshalJSON(b []byte) error {
	s, err := strconv.Unquote(string(b))
	if err != nil {
		return fmt.Errorf("%w: must be a JSON string", ErrInvalid)
	}
	v, err := Parse(s)
	if err != nil {
		return err
	}
	*i = v
	return nil
}

// Generator mints IDs. Each running process needs a distinct node ID.
type Generator struct{ node *snowflake.Node }

// NewGenerator returns a generator for nodeID, which must be in 0-1023.
func NewGenerator(nodeID int64) (*Generator, error) {
	n, err := snowflake.NewNode(nodeID)
	if err != nil {
		return nil, fmt.Errorf("snowflake node %d: %w", nodeID, err)
	}
	return &Generator{node: n}, nil
}

// New returns a new, time-ordered ID. Safe for concurrent use.
func (g *Generator) New() ID { return ID(g.node.Generate().Int64()) }
```

- [ ] **Step 4: Add the dependency and run tests**

Run: `go get github.com/bwmarrin/snowflake@v0.3.0 && go test -race ./internal/platform/id/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/platform/id go.mod go.sum
git commit -m "feat(platform): add snowflake id package"
```

---

### Task 2: `SNOWFLAKE_NODE_ID` configuration

**Files:**
- Modify: `internal/platform/config/config.go` (struct field, `validate`)
- Modify: `internal/platform/config/config_test.go`
- Modify: `.env.example`, `README.md`

**Interfaces:**
- Produces: `config.Config.SnowflakeNodeID int64`.

- [ ] **Step 1: Write the failing test**

Add to `config_test.go`. Find the existing helper that builds a valid environment map (it is used by the other `LoadFrom` tests) and reuse it; call it `validEnv()` below.

```go
func TestSnowflakeNodeID(t *testing.T) {
	cfg, err := config.LoadFrom(validEnv())
	if err != nil || cfg.SnowflakeNodeID != 0 {
		t.Fatalf("default = %d, %v", cfg.SnowflakeNodeID, err)
	}
	for _, v := range []string{"-1", "1024", "x"} {
		env := validEnv()
		env["SNOWFLAKE_NODE_ID"] = v
		if _, err := config.LoadFrom(env); err == nil || !strings.Contains(err.Error(), "SNOWFLAKE_NODE_ID") {
			t.Fatalf("SNOWFLAKE_NODE_ID=%s: err = %v", v, err)
		}
	}
	env := validEnv()
	env["SNOWFLAKE_NODE_ID"] = "1023"
	if cfg, err := config.LoadFrom(env); err != nil || cfg.SnowflakeNodeID != 1023 {
		t.Fatalf("1023 = %d, %v", cfg.SnowflakeNodeID, err)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/platform/config/ -run TestSnowflakeNodeID`
Expected: FAIL, `cfg.SnowflakeNodeID undefined`.

- [ ] **Step 3: Implement**

In `Config`, after `AuthRateLimitPerMinute`:

```go
	SnowflakeNodeID               int64    `env:"SNOWFLAKE_NODE_ID" envDefault:"0"`
```

In `validate()`, after the `AuthRateLimitPerMinute` check:

```go
	if c.SnowflakeNodeID < 0 || c.SnowflakeNodeID > 1023 {
		errs = append(errs, errors.New("SNOWFLAKE_NODE_ID: must be between 0 and 1023"))
	}
```

A non-numeric value already fails in `env.ParseAsWithOptions`, and `withVarNames` puts the variable name in the error.

In `.env.example`, add:

```sh
# Snowflake node ID (0-1023). Every running replica must use a different value.
SNOWFLAKE_NODE_ID=0
```

In `README.md`, add the same variable to the configuration list with the sentence "Every running replica must use a different value."

- [ ] **Step 4: Run tests**

Run: `go test -race ./internal/platform/config/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/platform/config .env.example README.md
git commit -m "feat(config): add SNOWFLAKE_NODE_ID"
```

---

### Task 3: Identity and principal use Snowflake IDs

One task because the change does not compile in smaller pieces: `auth.Principal.UserID` feeds the JWT adapter, which feeds the HTTP adapter.

**Files:**
- Modify: `migrations/00002_identity.sql`
- Modify: `internal/platform/auth/auth.go`, `auth_test.go`
- Modify: `internal/identity/domain/{user,refresh_token,events}.go`, `domain_test.go`
- Modify: `internal/identity/app/{ports,service}.go`, `service_test.go`, `fakes_test.go`
- Modify: `internal/identity/adapters/jwt/jwt.go`, `jwt_test.go`
- Modify: `internal/identity/adapters/postgres/postgres.go`, `postgres_integration_test.go`
- Regenerate: `internal/identity/adapters/postgres/sqlcgen/*`
- Modify: `internal/identity/adapters/httpapi/httpapi.go`, `httpapi_test.go`
- Modify: `cmd/api/app.go`
- Modify: `.golangci.yml`, `sqlc.yaml`

**Interfaces:**
- Consumes: `id.ID`, `id.Parse`, `*id.Generator` (Task 1); `cfg.SnowflakeNodeID` (Task 2).
- Produces: `auth.Principal{UserID id.ID; Role Role}`; `identityapp.NewService(tx TxRunner, google GoogleVerifier, tokens AccessTokenIssuer, ids *id.Generator, c clock.Clock, rootAdminEmails []string) *Service`; `AccessTokenIssuer.Issue(userID id.ID, role auth.Role)`; `SessionService.GetMe(ctx, userID id.ID)`; `buildApp` creates one `*id.Generator` (`ids`) for every context.

- [ ] **Step 1: Write the failing JWT test**

Add to `jwt_test.go` (reuse the file's existing key and clock helpers; `newTokens(t)` below stands for whatever constructor helper the file already uses):

```go
func TestVerifyRejectsNonSnowflakeSubject(t *testing.T) {
	tokens := newTokens(t)
	raw := signWithSubject(t, tokens, "01920000-0000-7000-8000-000000000001")
	if _, err := tokens.Verify(raw); !errors.Is(err, app.ErrInvalidToken) {
		t.Fatalf("err = %v, want ErrInvalidToken", err)
	}
}

func TestIssueUsesDecimalSubject(t *testing.T) {
	tokens := newTokens(t)
	raw, _, err := tokens.Issue(id.ID(1840396745219883008), auth.RoleInstructor)
	if err != nil {
		t.Fatal(err)
	}
	p, err := tokens.Verify(raw)
	if err != nil || p.UserID != 1840396745219883008 || p.Role != auth.RoleInstructor {
		t.Fatalf("principal = %+v, %v", p, err)
	}
}
```

`signWithSubject` signs a token whose claims are valid except for `sub`. Write it in the test file with `gojwt.NewWithClaims` and the test keys, copying the claim set from `Tokens.Issue`.

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/identity/adapters/jwt/`
Expected: compile failure, `Issue` takes `uuid.UUID`.

- [ ] **Step 3: Change the migration**

In `migrations/00002_identity.sql`, change these column types to `bigint` (no other change):

```sql
  id            bigint PRIMARY KEY,                 -- identity.users
  id                bigint PRIMARY KEY,             -- identity.refresh_tokens
  user_id           bigint NOT NULL REFERENCES identity.users (id) ON DELETE CASCADE,
  family_id         bigint NOT NULL,
```

In `sqlc.yaml`, delete the `db_type: uuid` override (identity no longer has uuid columns). Run `sqlc generate` and confirm `sqlcgen/models.go` now has `int64` for these fields.

- [ ] **Step 4: Change `platform/auth`**

```go
// Principal is the authenticated caller of a request.
type Principal struct {
	UserID id.ID
	Role   Role
}
```

Import `github.com/santoshkc2200/ioe-backend/internal/platform/id`; drop `uuid`. In `auth_test.go` replace `uuid.New()` with `id.ID(42)`.

- [ ] **Step 5: Change identity domain**

In `user.go`, `refresh_token.go` and `events.go`, replace every `uuid.UUID` with `id.ID` and the import accordingly. `UserRegistered.UserID` stays tagged `json:"user_id"`; it now marshals as a string, which is what notification already reads.

In `domain_test.go`, replace `uuid.New()` with distinct literals (`id.ID(1)`, `id.ID(2)`, `id.ID(3)`) at each call site.

- [ ] **Step 6: Change identity app**

`ports.go`: `UserRepository.FindByID(ctx, id id.ID)`, `RefreshTokenRepository.MarkUsed(ctx, id id.ID, at)`, `RevokeFamily(ctx, familyID id.ID, at)`, `AccessTokenIssuer.Issue(userID id.ID, role auth.Role)`.

`service.go`:

```go
type Service struct {
	tx         TxRunner
	google     GoogleVerifier
	tokens     AccessTokenIssuer
	ids        *id.Generator
	clock      clock.Clock
	rootAdmins map[string]struct{}
}

func NewService(tx TxRunner, google GoogleVerifier, tokens AccessTokenIssuer, ids *id.Generator, c clock.Clock, rootAdminEmails []string) *Service {
	// body unchanged; also set ids: ids
}
```

Replace the three `uuid.NewV7()` call sites and the `newIDs` helper:

- in `signIn`, `user = domain.NewUser(s.ids.New(), identity, now)` (delete the `id, err :=` block),
- `token := domain.NewRefreshFamily(s.ids.New(), s.ids.New(), user.ID, hash, now, ua, ip)` (delete the `newIDs()` call and the function),
- in `Refresh`, `next := current.Successor(s.ids.New(), nextHash, now, ua, ip)` (delete the `uuid.NewV7()` block),
- `GetMe(ctx context.Context, userID id.ID)`.

`fakes_test.go` / `service_test.go`: maps keyed by `id.ID`; `fakeIssuer.Issue(id id.ID, ...)`; every `app.NewService(...)` gets `testIDs(t)` as the fourth argument, with this helper in `fakes_test.go`:

```go
func testIDs(t *testing.T) *id.Generator {
	t.Helper()
	g, err := id.NewGenerator(0)
	if err != nil {
		t.Fatal(err)
	}
	return g
}
```

`GetMe(ctx, uuid.New())` becomes `GetMe(ctx, id.ID(999))`.

- [ ] **Step 7: Change the JWT adapter**

```go
func (t *Tokens) Issue(userID id.ID, role auth.Role) (string, time.Duration, error) {
	// unchanged except: Subject: userID.String(),
}
```

In `Verify`, replace `uuid.Parse(c.Subject)` with `id.Parse(c.Subject)`; the existing error wrapping stays. Keep `ID: uuid.NewString()` for `jti`.

- [ ] **Step 8: Change the postgres adapter**

`FindByID(ctx, x id.ID)` calls `u.q.GetUserByID(ctx, int64(x))`. In `Insert`/`Update` pass `ID: int64(x.ID)`. In `toUser`, `ID: id.ID(row.ID)`. In `tokens`, convert `ID`, `UserID`, `FamilyID` with `int64(...)` going in and `id.ID(...)` coming out; `MarkUsed`/`RevokeFamily` take `id.ID`. `events.Publish` keeps `uuid.NewString()` for the message UUID.

In `postgres_integration_test.go`, create `gen := testIDs(t)` (same helper body as Step 6, defined in this file) and replace `uuid.Must(uuid.NewV7())` / `uuid.New()` with `gen.New()`, `stubIssuer.Issue(id.ID, auth.Role)`, and pass `gen` to `app.NewService`.

- [ ] **Step 9: Change the HTTP adapter**

`SessionService.GetMe(ctx, userID id.ID)`. `toUserResponse` already calls `u.ID.String()`. In `httpapi_test.go`: `user = domain.User{ID: id.ID(1840396745219883008), ...}`, `fakeService.GetMe(_ context.Context, id id.ID)`, and any assertion on the `/v1/me` body expects `"id":"1840396745219883008"`.

- [ ] **Step 10: Wire the generator**

In `cmd/api/app.go` `buildApp`, after `clk := clock.System{}`:

```go
	ids, err := id.NewGenerator(cfg.SnowflakeNodeID)
	if err != nil {
		return nil, err
	}
```

and pass `ids` as the fourth argument of `identityapp.NewService`.

- [ ] **Step 11: depguard**

In `.golangci.yml`, add `- github.com/santoshkc2200/ioe-backend/internal/platform/id` to the `allow` lists of `identity-domain` and `identity-app`, and remove `- github.com/google/uuid` from both (neither uses it any more).

- [ ] **Step 12: Run tests**

Run: `go build ./... && make test && make lint`
Expected: PASS. Then `make test-integration` (Docker running): identity postgres tests and the `cmd/api` e2e pass. If Docker is unavailable, say so; do not report integration tests as passing.

- [ ] **Step 13: Commit**

```bash
git add migrations sqlc.yaml .golangci.yml internal/platform/auth internal/identity cmd/api
git commit -m "feat(identity): use snowflake ids for users and refresh tokens"
```

---

### Task 4: net/http router

**Files:**
- Modify: `internal/platform/httpserver/router.go`, `router_test.go`, `health.go`
- Modify: `internal/identity/adapters/httpapi/httpapi.go` (`Register`)
- Modify: `cmd/api/app.go` (`registerIdentity` signature)
- Modify: `go.mod`, `go.sum` (drop `gorilla/mux`, `otelmux`)

**Interfaces:**
- Consumes: `httpserver.Middleware`, `Chain`, `NoStore`, `problem.Write` (existing).
- Produces: `type Router struct{ *http.ServeMux; ... }`; `func NewRouter(o Options) (*Router, http.Handler)`; `func (*Router) WrapUnmatched(prefix string, mw Middleware)`; `func MountHealth(r *Router, ready func(context.Context) error)`; identity `func (*Handler) Register(r *httpserver.Router)`.

- [ ] **Step 1: Rewrite the router tests**

Replace `router_test.go`'s route registrations with ServeMux patterns and add the new cases. Full replacements for the changed tests:

```go
func TestRouterSpanUsesRouteTemplateAndOmitsSecrets(t *testing.T) {
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	r, h := httpserver.NewRouter(options())
	r.HandleFunc("GET /v1/things/{id}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	rq := httptest.NewRequest(http.MethodGet, "/v1/things/123", nil)
	rq.Header.Set("Authorization", "Bearer super-secret")
	rq.Header.Set("Cookie", "ioe_refresh=cookie-secret")
	h.ServeHTTP(httptest.NewRecorder(), rq)

	spans := exp.GetSpans()
	if len(spans) != 1 || spans[0].Name != "GET /v1/things/{id}" {
		t.Fatalf("spans = %+v", spans)
	}
	for _, a := range spans[0].Attributes {
		v := a.Value.Emit()
		if strings.Contains(v, "super-secret") || strings.Contains(v, "cookie-secret") {
			t.Fatalf("attribute %s leaked a secret", a.Key)
		}
	}
}

func TestRouterUnmatchedSpanName(t *testing.T) {
	exp := tracetest.NewInMemoryExporter()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp)))
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	_, h := httpserver.NewRouter(options())
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/nope/123", nil))
	if spans := exp.GetSpans(); len(spans) != 1 || spans[0].Name != "GET unmatched" {
		t.Fatalf("spans = %+v", spans)
	}
}

func TestRouterMethodNotAllowedIsProblemWithAllowHeader(t *testing.T) {
	r, h := httpserver.NewRouter(options())
	r.HandleFunc("GET /v1/items", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	r.HandleFunc("POST /v1/sub/action", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	for path, wantAllow := range map[string]string{"/v1/items": "GET, HEAD", "/v1/sub/action": "POST"} {
		method := http.MethodPost
		if path == "/v1/sub/action" {
			method = http.MethodGet
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(method, path, nil))
		if w.Code != http.StatusMethodNotAllowed || w.Header().Get("Content-Type") != "application/problem+json" {
			t.Fatalf("%s %s: %d %q", method, path, w.Code, w.Header().Get("Content-Type"))
		}
		if got := w.Header().Get("Allow"); got != wantAllow {
			t.Fatalf("%s: allow = %q, want %q", path, got, wantAllow)
		}
		if w.Header().Get("X-Content-Type-Options") != "nosniff" || w.Header().Get("X-Request-ID") == "" {
			t.Fatalf("outer middleware skipped on 405: %v", w.Header())
		}
		var p struct {
			Type   string `json:"type"`
			Status int    `json:"status"`
		}
		if err := json.NewDecoder(w.Body).Decode(&p); err != nil || p.Type != "method_not_allowed" || p.Status != 405 {
			t.Fatalf("problem = %+v, %v", p, err)
		}
	}
}

func TestRouterWrapUnmatchedAppliesOnlyUnderPrefix(t *testing.T) {
	r, h := httpserver.NewRouter(options())
	r.HandleFunc("POST /v1/auth/google", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	r.HandleFunc("GET /v1/other", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	r.WrapUnmatched("/v1/auth/", func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			w.Header().Set("X-Wrapped", "1")
			next.ServeHTTP(w, req)
		})
	})

	cases := []struct {
		method, path string
		code         int
		wrapped      bool
	}{
		{http.MethodGet, "/v1/auth/google", 405, true},
		{http.MethodGet, "/v1/auth/missing", 404, true},
		{http.MethodPost, "/v1/other", 405, false},
		{http.MethodPost, "/v1/auth/google", 200, false},
	}
	for _, c := range cases {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(c.method, c.path, nil))
		if w.Code != c.code || (w.Header().Get("X-Wrapped") == "1") != c.wrapped {
			t.Fatalf("%s %s: code %d wrapped %q", c.method, c.path, w.Code, w.Header().Get("X-Wrapped"))
		}
	}
}

func TestRouterNonCanonicalPathDoesNotBypassRouting(t *testing.T) {
	r, h := httpserver.NewRouter(options())
	r.HandleFunc("GET /v1/me", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	// The mux redirects unclean paths to the canonical one (301) and 404s a trailing
	// slash. Neither may reach the handler directly.
	for p, want := range map[string]int{"/v1//me": http.StatusMovedPermanently, "/v1/./me": http.StatusMovedPermanently, "/v1/me/": http.StatusNotFound} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, p, nil))
		if w.Code != want {
			t.Fatalf("%s: code %d, want %d", p, w.Code, want)
		}
	}
}
```

`TestRouterNotFoundIsProblemWithHeaders` is unchanged. In `TestHealthEndpoints`, `httpserver.MountHealth(r, ...)` is unchanged in shape.

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/platform/httpserver/`
Expected: FAIL to compile (`r.HandleFunc` returns no value / `WrapUnmatched` undefined).

- [ ] **Step 3: Implement the router**

Replace `router.go`:

```go
package httpserver

import (
	"log/slog"
	"net/http"
	"strings"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/trace"

	"github.com/santoshkc2200/ioe-backend/internal/platform/problem"
)

const maxBodyBytes = 1 << 20

// Options configures NewRouter.
type Options struct {
	Logger         *slog.Logger
	AllowedOrigins []string
	ServiceName    string
}

// Router is an http.ServeMux whose unmatched requests become problem+json responses.
// Every pattern must include a method, for example "GET /v1/me".
type Router struct {
	*http.ServeMux
	unmatched []unmatchedRule
}

type unmatchedRule struct {
	prefix string
	mw     Middleware
}

// WrapUnmatched applies mw to 404 and 405 responses for request paths starting with prefix.
// Call it during setup only; it is not safe to call while serving.
func (r *Router) WrapUnmatched(prefix string, mw Middleware) {
	r.unmatched = append(r.unmatched, unmatchedRule{prefix: prefix, mw: mw})
}

// NewRouter returns the router for route registration and the handler to serve.
// The outer chain runs for every request, including 404 and 405.
func NewRouter(o Options) (*Router, http.Handler) {
	r := &Router{ServeMux: http.NewServeMux()}
	traced := otelhttp.NewHandler(http.HandlerFunc(r.dispatch), o.ServiceName,
		otelhttp.WithSpanNameFormatter(func(_ string, req *http.Request) string { return req.Method + " unmatched" }))
	h := Chain(traced,
		RequestID,
		Recover(o.Logger),
		AccessLog(o.Logger),
		SecurityHeaders,
		CORS(o.AllowedOrigins),
		BodyLimit(maxBodyBytes),
	)
	return r, h
}

// dispatch names the span after the matched pattern; unmatched requests keep the
// "<METHOD> unmatched" name and are answered with problem+json.
func (r *Router) dispatch(w http.ResponseWriter, req *http.Request) {
	if _, pattern := r.Handler(req); pattern != "" {
		trace.SpanFromContext(req.Context()).SetName(pattern)
		r.ServeMux.ServeHTTP(w, req)
		return
	}
	var h http.Handler = http.HandlerFunc(r.problemFallback)
	for _, rule := range r.unmatched {
		if strings.HasPrefix(req.URL.Path, rule.prefix) {
			h = rule.mw(h)
		}
	}
	h.ServeHTTP(w, req)
}

// problemFallback runs the mux's built-in 404/405 handler only to learn the status and
// Allow header, then writes problem+json instead of its plain-text body.
func (r *Router) problemFallback(w http.ResponseWriter, req *http.Request) {
	h, _ := r.Handler(req)
	capture := &statusCapture{header: http.Header{}}
	h.ServeHTTP(capture, req)
	if capture.status == http.StatusMethodNotAllowed {
		if allow := capture.header.Get("Allow"); allow != "" {
			w.Header().Set("Allow", allow)
		}
		problem.Write(w, req, http.StatusMethodNotAllowed, problem.TypeMethodNotAllowed, "Method Not Allowed", "")
		return
	}
	problem.Write(w, req, http.StatusNotFound, problem.TypeNotFound, "Not Found", "")
}

type statusCapture struct {
	header http.Header
	status int
}

func (c *statusCapture) Header() http.Header       { return c.header }
func (c *statusCapture) Write(b []byte) (int, error) { return len(b), nil }
func (c *statusCapture) WriteHeader(status int)    { c.status = status }
```

Replace `health.go`'s registration:

```go
// MountHealth registers /healthz (process alive) and /readyz (dependencies reachable).
func MountHealth(r *Router, ready func(context.Context) error) {
	r.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	r.HandleFunc("GET /readyz", func(w http.ResponseWriter, req *http.Request) {
		ctx, cancel := context.WithTimeout(req.Context(), 2*time.Second)
		defer cancel()
		if err := ready(ctx); err != nil {
			WriteJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
			return
		}
		WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
}
```

- [ ] **Step 4: Run platform tests**

Run: `go test -race ./internal/platform/httpserver/`
Expected: PASS.

- [ ] **Step 5: Rewrite identity `Register`**

```go
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
```

Remove the `gorilla/mux` import. The existing `TestAuthRouteMethodNotAllowed` and `TestAuthRouteMethodNotAllowedRateLimited` must pass unchanged; they pin the behavior `WrapUnmatched` preserves.

- [ ] **Step 6: Update composition and dependencies**

In `cmd/api/app.go`, change `registerIdentity(r *mux.Router, ...)` to `registerIdentity(r *httpserver.Router, ...)` and drop the `gorilla/mux` import. Then:

Run: `go mod tidy && grep -n "gorilla\|otelmux" go.mod`
Expected: no output.

- [ ] **Step 7: Run everything**

Run: `make test && make lint && make test-integration`
Expected: PASS (integration requires Docker; if unavailable, say so).

- [ ] **Step 8: Commit**

```bash
git add internal/platform/httpserver internal/identity/adapters/httpapi cmd/api go.mod go.sum
git commit -m "refactor(httpserver): route with net/http ServeMux"
```

---

### Task 5: Gates and docs

**Files:**
- Modify: `AGENTS.md` (mention `internal/platform/id` in the domain import rule)
- Modify: `docs/superpowers/specs/2026-10-04-foundation-identity-design.md` only if it states UUID IDs or gorilla/mux as current behavior (add a one-line "Superseded by 2026-10-04-platform-snowflake-nethttp-design.md" note at those spots)

- [ ] **Step 1: Update AGENTS.md**

Change the domain line to:

```text
  domain/             entities, value objects, events; stdlib + platform/{id,auth} only
```

- [ ] **Step 2: Run every gate**

Run, in order: `make check`, `make test-integration`, `docker compose config`, `make docker-build`, `git diff --check`.
Expected: all succeed. Report each command's result. Integration tests must actually run.

- [ ] **Step 3: Commit**

```bash
git add AGENTS.md docs/superpowers/specs/2026-10-04-foundation-identity-design.md
git commit -m "docs: record snowflake ids and net/http router"
```
