# Backend Foundation, DevSecOps, and Identity Context Design

Date: 2026-10-04

## Status

Approved in conversation on 2026-10-04. Pending written-spec review.

## Context

`ioe-backend` is an empty directory. It will host the backend for a single-tenant learning management system whose users are root admins, instructors, and students. The frontend monorepo (`../ioe-frontend`) authenticates with Google only (OIDC + PKCE in the browser) and has no tenant concepts. Its auth driver calls a `getCurrentUserFn(token)` hook whose backend contract is not yet defined.

`initial_project_layout.md` sets the architectural direction: domain-driven design with hexagonal architecture in a modular monolith; bounded contexts communicate through explicit interfaces (synchronous) or domain/application events (asynchronous) and never touch another context's internals or tables; consuming modules own the interfaces they depend on; dependencies are wired centrally; shared infrastructure lives in a platform layer; the Watermill transactional outbox and saga orchestration are the consistency tools. Mandated tools: Go 1.27, sqlc, pgx, gorilla/mux.

The full product spans many contexts (identity, profile, course authoring/catalog/enrollment/progress/assessment, content, blog, social, feed, settings, media and notification integrations). This spec covers only the first sub-project.

## Goals

- Establish the repository skeleton, module layout, and boundary rules every later bounded context follows.
- Provide a platform layer: configuration, logging, telemetry, PostgreSQL, migrations, HTTP server and middleware, transactional outbox, problem+json errors.
- Deliver the identity bounded context end to end: Google sign-in, user records with roles, backend-issued sessions, refresh, logout, and current-user lookup.
- Establish DevSecOps: linting, static and dependency security scanning, secret scanning, container scanning, SBOM, signed images with provenance, hardened CI, dependency updates.
- Export traces, metrics, and logs through OpenTelemetry to OpenObserve.

## Non-goals

- Any bounded context other than identity.
- Role-management endpoints (granting or revoking `instructor`). A later slice owns them.
- Saga orchestration. Identity has no multi-step consistency requirement; saga infrastructure is added with the first context that needs it.
- Integration with the media or notification services.
- Outbox consumers. Identity publishes events; nothing consumes them yet.
- Deployment manifests or a specific hosting target beyond a published container image.
- Prometheus scrape endpoints.
- Multi-instance rate limiting.

## Decisions

| Topic | Decision |
|---|---|
| Module path | `github.com/santoshkc2200/ioe-backend` |
| Layout | Per-context hexagonal packages: `internal/<context>/{domain,app,adapters}` |
| Data isolation | One PostgreSQL schema per context (`identity`, `platform`) |
| Boundary enforcement | `depguard` rules in golangci-lint, run in CI |
| Database | PostgreSQL 17, pgx v5, sqlc for queries |
| Migrations | goose, SQL files under `migrations/` |
| HTTP | gorilla/mux under `/v1`, RFC 9457 problem+json errors |
| Auth model | Backend session exchange of a Google ID token |
| Roles | Exactly one role per user: `student` (default), `instructor`, `root_admin` |
| Root admin bootstrap | Email allowlist from `BOOTSTRAP_ROOT_ADMIN_EMAILS`, applied on every sign-in |
| Access token | EdDSA (Ed25519) JWT, 15 minutes, returned in the JSON body |
| Refresh token | Opaque, rotating, stored hashed, delivered in an HttpOnly cookie |
| Events | Watermill SQL outbox in schema `platform` |
| Telemetry | OpenTelemetry SDK, OTLP/HTTP to OpenObserve |
| CI / registry | GitHub Actions, GHCR |

The API and frontends are served from the same registrable domain (for example `api.example.com` and `app.example.com`), which makes `SameSite=Strict` cookies viable.

## Architecture

### Repository layout

```text
cmd/api/                     composition root: load config, build adapters, wire use cases, run server
cmd/keygen/                  development Ed25519 key generator
internal/platform/
  config/                    environment parsing and validation (fail fast)
  logging/                   slog JSON handler with redaction
  telemetry/                 OpenTelemetry SDK setup and shutdown
  postgres/                  pgx pool, transaction helper
  migrate/                   goose runner (embedded migrations)
  httpserver/                mux router, middleware, server lifecycle
  problem/                   RFC 9457 problem+json writer
  outbox/                    Watermill SQL publisher (in-transaction) and forwarder
  auth/                      Principal type and context accessors shared by all contexts
  clock/                     Clock interface for deterministic tests
internal/identity/
  domain/                    User, Role, GoogleIdentity, RefreshToken, domain events
  app/                       use cases and the ports they own
  adapters/postgres/         sqlc queries, generated code, repository implementations
  adapters/google/           Google ID-token verifier
  adapters/jwt/              access-token issuer and verifier
  adapters/http/             handlers and bearer-auth middleware
migrations/                  goose SQL migrations
api/openapi.yaml             public HTTP contract
docs/superpowers/specs/      design documents
```

### Dependency rules

- `domain` imports only the standard library, `github.com/google/uuid`, and `internal/platform/auth` (for the shared `Role` type).
- `app` imports its own `domain` and `internal/platform/{auth,clock}`.
- `adapters` implement `app` ports and may import platform packages.
- A bounded context never imports another context's `domain`, `app`, or `adapters`. When a later context needs identity data synchronously, it defines its own port and `cmd/api` wires an adapter backed by identity's application service.
- `internal/platform` never imports a bounded context.
- `cmd/api` is the only package that imports everything.

`depguard` encodes these rules so CI rejects violations.

### Cross-context authentication

`internal/platform/auth` defines:

```go
type Principal struct {
    UserID uuid.UUID
    Role   Role
}

func WithPrincipal(ctx context.Context, p Principal) context.Context
func PrincipalFrom(ctx context.Context) (Principal, bool)
```

Identity's HTTP adapter exports a bearer-auth middleware that verifies the access token and stores a `Principal`. Later contexts read the `Principal` and enforce their own authorization rules. `Role` lives in `platform/auth` because every context reads it; identity's domain uses the same type.

### Transactions and events

The platform `postgres` package provides `WithTx(ctx, func(pgx.Tx) error) error`. Identity repositories accept a `pgx.Tx`-backed querier. When sign-in creates a user, the user row and the `identity.UserRegistered` outbox message are written in the same transaction through Watermill's SQL publisher. A forwarder goroutine in `cmd/api` moves outbox messages to the Watermill Go-channel pub/sub. No subscriber exists yet; the forwarder proves the pipeline and gives later contexts an attachment point.

## Identity Context

### Domain

- `User`: `ID` (UUIDv7), `GoogleSubject`, `Email`, `Name`, `AvatarURL`, `Role`, `CreatedAt`, `UpdatedAt`, `LastLoginAt`.
- Users are keyed by Google `sub`. Email is updated from the verified token on each sign-in and is never used as an identity key.
- `RefreshToken`: `ID`, `UserID`, `FamilyID`, `TokenHash`, `FamilyExpiresAt`, `ExpiresAt`, `UsedAt`, `RevokedAt`, `CreatedAt`, `UserAgent`, `IP`.
- Events: `UserRegistered{UserID, Email, OccurredAt}`.

### Application ports (owned by `app`)

- `UserRepository`: find by Google subject, find by ID, insert, update login details and role.
- `RefreshTokenRepository`: insert, find by hash, mark used, revoke family, revoke by hash.
- `GoogleVerifier`: verify an ID token and return the verified identity claims.
- `AccessTokenIssuer`: issue a token for a user ID and role.
- `EventPublisher`: publish domain events inside the current transaction.
- `TxRunner`: run a function in a transaction.

### Use cases

**SignInWithGoogle(idToken, userAgent, ip)**

1. Verify the ID token: signature against Google's JWKS, `aud` in the configured client IDs, `iss` equal to `accounts.google.com` or `https://accounts.google.com`, unexpired, `email_verified == true`.
2. In one transaction: find the user by `sub`; create it with role `student` and publish `UserRegistered` if absent; otherwise update email, name, avatar, and `last_login_at`.
3. If the email (case-insensitive) is in the root-admin allowlist, set role `root_admin`. Removing an email from the allowlist does not demote an existing root admin.
4. Create a refresh token in a new family with `family_expires_at` 30 days from now and `expires_at` 7 days from now.
5. Return the access token, its lifetime, the user, and the raw refresh token.

**Refresh(rawRefreshToken)**

1. Hash the token and look it up. Unknown, expired, or revoked: fail with `invalid_token`.
2. If the token was already used: revoke its whole family and fail with `refresh_reuse_detected`.
3. Otherwise, in one transaction (holding a row lock on the presented token): mark it used and insert a successor in the same family whose expiry is the lesser of `family_expires_at` and 7 days from now.

The reuse case commits the family revocation before returning the error; the revocation must not be rolled back with the failed request.
4. Load the user to pick up the current role, then issue a new access token.

**Logout(rawRefreshToken)**

Revoke the token's family if the token exists. Always succeed.

**GetMe(principal)**

Return the user's profile and role. Fail with `not_found` if the user no longer exists.

### HTTP contract

All routes are under `/v1`. Errors use `application/problem+json` with stable `type` codes.

| Method and path | Auth | Request | Success |
|---|---|---|---|
| `POST /v1/auth/google` | none | `{"id_token": "..."}` | 200 `{access_token, token_type: "Bearer", expires_in, user}` + refresh cookie |
| `POST /v1/auth/refresh` | refresh cookie + Origin | empty | 200 `{access_token, token_type, expires_in}` + rotated cookie |
| `POST /v1/auth/logout` | refresh cookie + Origin | empty | 204 + cleared cookie |
| `GET /v1/me` | bearer | none | 200 `{id, email, name, avatar_url, role}` |
| `GET /healthz` | none | none | 200 when the process runs |
| `GET /readyz` | none | none | 200 when the database answers a ping, else 503 |

`user` has the same shape as `GET /v1/me`.

Error codes:

| Status | `type` | When |
|---|---|---|
| 400 | `invalid_request` | malformed JSON, unknown fields, missing fields |
| 401 | `invalid_token` | any ID-token, access-token, or refresh-token verification failure |
| 401 | `refresh_reuse_detected` | an already-used refresh token was presented |
| 403 | `email_unverified` | Google reports the email as unverified |
| 403 | `origin_not_allowed` | missing or disallowed `Origin` on cookie-authenticated routes |
| 404 | `not_found` | `/v1/me` user missing |
| 413 | `payload_too_large` | body over 1 MiB |
| 429 | `rate_limited` | auth rate limit exceeded |
| 500 | `internal` | unexpected error; detail logged, not returned |

Verification failures return a generic message; the specific reason is logged at debug level without token contents.

`api/openapi.yaml` documents this contract.

### Tokens

**Access token**: JWT signed with Ed25519 (`alg: EdDSA`), header `kid`. Claims: `iss` (configured issuer), `aud` (configured audience), `sub` (user ID), `role`, `iat`, `exp` (15 minutes), `jti`. The verifier accepts a set of public keys keyed by `kid` so keys can rotate. The middleware trusts the role in the token and does not query the database per request; a role change takes effect within 15 minutes.

**Refresh token**: 32 bytes from `crypto/rand`, base64url-encoded. Only the SHA-256 hash is stored. Families rotate on every use; reuse revokes the family.

**Cookie**: name `__Secure-ioe_refresh`, attributes `HttpOnly; Secure; SameSite=Strict; Path=/v1/auth; Max-Age=<remaining lifetime>`. The `__Host-` prefix is not used because it requires `Path=/`.

**CSRF**: `/v1/auth/refresh` and `/v1/auth/logout` require an `Origin` header exactly matching an entry in `ALLOWED_ORIGINS`.

### Database schema

```sql
CREATE SCHEMA identity;
CREATE EXTENSION IF NOT EXISTS citext;

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
  id          uuid PRIMARY KEY,
  user_id     uuid NOT NULL REFERENCES identity.users (id) ON DELETE CASCADE,
  family_id   uuid NOT NULL,
  token_hash  bytea NOT NULL UNIQUE,
  family_expires_at timestamptz NOT NULL,
  expires_at  timestamptz NOT NULL,
  used_at     timestamptz,
  revoked_at  timestamptz,
  created_at  timestamptz NOT NULL,
  user_agent  text NOT NULL DEFAULT '',
  ip          text NOT NULL DEFAULT ''
);
CREATE INDEX refresh_tokens_family_idx ON identity.refresh_tokens (family_id);
```

Email is not unique: Google `sub` is the identity, and an email can move between Google accounts.

`family_expires_at` records the absolute 30-day family deadline so each rotated token's expiry can be capped by it. A new family's first token expires at the lesser of the family deadline and 7 days from issue. `ip` is stored as text produced by the server's own client-IP resolver, which avoids `inet` codec handling in sqlc; it is informational only.

The `platform` schema holds the Watermill outbox table, created by a goose migration rather than Watermill's auto-initialization so the application role needs no DDL rights at runtime.

Every migration has a working `Down` section.

## Platform

### Configuration

Environment variables, parsed once at startup. Missing or invalid required values stop the process with a message naming the variable.

| Variable | Required | Purpose |
|---|---|---|
| `HTTP_ADDR` | no (`:8080`) | listen address |
| `DATABASE_URL` | yes | PostgreSQL connection string |
| `GOOGLE_CLIENT_IDS` | yes | comma-separated accepted `aud` values |
| `JWT_ISSUER`, `JWT_AUDIENCE` | yes | access-token claims |
| `JWT_SIGNING_KEY` | yes | Ed25519 private key, PEM |
| `JWT_SIGNING_KEY_ID` | yes | `kid` of the signing key |
| `JWT_VERIFY_KEYS` | no | extra PEM public keys for rotation, as `kid=pem` entries separated by `;` |
| `GOOGLE_JWKS_URL` | no (`https://www.googleapis.com/oauth2/v3/certs`) | Google signing keys; overridden only in tests |
| `ALLOWED_ORIGINS` | yes | CORS and CSRF origin allowlist |
| `BOOTSTRAP_ROOT_ADMIN_EMAILS` | no | comma-separated emails |
| `COOKIE_SECURE` | no (`true`) | `false` only for local HTTP development |
| `AUTH_RATE_LIMIT_PER_MINUTE` | no (`30`) | per-IP limit on `/v1/auth/*` |
| `TRUSTED_PROXY_CIDRS` | no | CIDRs whose `X-Forwarded-For` is trusted for client IP |
| `LOG_LEVEL` | no (`info`) | slog level |
| `OTEL_*` | no | standard OpenTelemetry SDK variables |

PEM values may use literal `\n` sequences in place of newlines so they fit in single-line environment files.

When `COOKIE_SECURE=false` the cookie drops the `__Secure-` prefix and the `Secure` attribute, because browsers reject a `__Secure-` cookie without `Secure`.

Migrations run through `cmd/api migrate up|down|status`, not automatically at server start.

### HTTP server

Middleware order, outermost first: request ID, panic recovery, access logging, security headers, CORS, body limit, then the router. OpenTelemetry server instrumentation runs as router middleware because it needs the matched route template for span names; unmatched requests (404/405) still receive headers, CORS handling, and logging from the outer chain. Route-level middleware adds rate limiting (auth routes) and bearer auth (protected routes).

- Server timeouts: read header 5 s, read 15 s, write 30 s, idle 60 s.
- Graceful shutdown on SIGINT/SIGTERM: stop accepting connections, drain for up to 20 s, stop the forwarder, flush telemetry, close the pool.
- Security headers on all responses: `Strict-Transport-Security: max-age=63072000; includeSubDomains`, `X-Content-Type-Options: nosniff`, `Content-Security-Policy: default-src 'none'; frame-ancestors 'none'`, `Referrer-Policy: no-referrer`. Auth responses also send `Cache-Control: no-store`.
- CORS: only `ALLOWED_ORIGINS`, credentials allowed, methods `GET, POST, PUT, PATCH, DELETE`, headers `Authorization, Content-Type`.
- JSON decoding rejects unknown fields and trailing data.
- Rate limiting: in-memory per-IP token bucket. Client IP comes from `X-Forwarded-For` only when the peer is in `TRUSTED_PROXY_CIDRS`. This limiter is per instance; multi-instance deployments need a shared limiter (recorded as a risk).

### Logging

`log/slog` with a JSON handler on stdout, bridged to OpenTelemetry logs. A redaction handler in front of both outputs replaces values of keys named `authorization`, `cookie`, `set-cookie`, `id_token`, `access_token`, `refresh_token`, `password`, and `secret` with `[REDACTED]`. Each record carries `request_id`, and `trace_id`/`span_id` when a span is active.

### Telemetry (OpenTelemetry to OpenObserve)

`internal/platform/telemetry` initializes tracer, meter, and logger providers with OTLP/HTTP exporters, configured by the standard variables: `OTEL_EXPORTER_OTLP_ENDPOINT` (OpenObserve, e.g. `https://observe.example.com/api/<org>`), `OTEL_EXPORTER_OTLP_HEADERS` (`Authorization=Basic <base64>`), `OTEL_SERVICE_NAME` (default `ioe-backend`), `OTEL_RESOURCE_ATTRIBUTES`. With `OTEL_SDK_DISABLED=true` or no endpoint, providers are no-ops. Shutdown flushes all providers. W3C `tracecontext` and `baggage` propagators are installed.

- Traces: `otelmux` server spans named by route template, never the raw path; `otelpgx` database spans recording SQL text without arguments; outbox messages carry trace context in metadata so the forwarder and future consumers continue the trace.
- Metrics: HTTP server metrics, Go runtime metrics, pgx pool statistics, and the counters `identity.signins` (attribute `result`: `created`, `existing`, `rejected`) and `identity.refresh_reuse_detected`.
- Logs: the slog bridge described above.
- Sensitive data: span and metric attributes never include tokens, cookies, emails, or the `Authorization` header. The HTTP instrumentation is configured to omit request headers.
- Outside local development, the endpoint uses HTTPS.

### Errors

`internal/platform/problem` writes RFC 9457 documents with `type`, `title`, `status`, `detail`, and `instance` (the request ID). Application errors are typed in `app`; the HTTP adapter maps them to problems. Unmapped errors become `internal` and are logged with the request ID.

## DevSecOps

### Local development

- `docker-compose.yml`: PostgreSQL 17 with healthcheck, OpenObserve (pinned version, UI on `:5080`), and the API (optional profile). Images pinned by version.
- `.env.example` documents every variable with development-only values, including local OpenObserve root credentials. `.env` is git-ignored.
- `Makefile` targets: `run`, `build`, `test`, `test-integration`, `lint`, `vuln`, `secrets`, `sqlc`, `sqlc-check`, `migrate-up`, `migrate-down`, `keygen`, `docker-build`, `compose-up`, `compose-down`, `check` (all non-integration gates).
- `lefthook.yml` pre-commit: `gofmt`/`goimports` check, golangci-lint on changed packages, gitleaks on staged changes.

### Static analysis

golangci-lint v2 with `govet`, `staticcheck`, `errcheck`, `gosec`, `revive`, `bodyclose`, `sqlclosecheck`, `noctx`, `errorlint`, `contextcheck`, `depguard`, `gocritic`, and `misspell`. Generated sqlc code is excluded from style linters.

### CI (`.github/workflows/ci.yml`, on pull requests and pushes to `main`)

Jobs:

1. **lint**: golangci-lint; `sqlc diff` to reject stale generated code; `go mod tidy` diff; `go mod verify`.
2. **test**: `go test -race -coverprofile` over all packages; coverage uploaded as an artifact.
3. **integration**: `go test -tags integration` using testcontainers-go PostgreSQL; migrations applied up, down, and up again.
4. **security**: `govulncheck ./...`; gitleaks over full history.
5. **image**: Docker build; Trivy image scan failing on fixable HIGH or CRITICAL vulnerabilities; Syft SPDX SBOM uploaded as an artifact.

### Other workflows

- `codeql.yml`: Go CodeQL analysis on pull requests, `main`, and weekly.
- `scorecard.yml`: OpenSSF Scorecard weekly, results uploaded to code scanning.
- `release.yml` on `v*` tags: multi-arch (`linux/amd64`, `linux/arm64`) build, push to `ghcr.io/santoshkc2200/ioe-backend`, SLSA provenance and SBOM attestations via `docker/build-push-action`, keyless cosign signature using GitHub OIDC.

### Workflow hardening

- Every third-party action pinned by full commit SHA with a version comment.
- Top-level `permissions: contents: read`; jobs request more only when required (`packages: write`, `id-token: write`, `security-events: write`).
- `step-security/harden-runner` in audit mode on every job.
- No `pull_request_target` triggers. `persist-credentials: false` on checkout.

### Dependencies

`.github/dependabot.yml`: weekly grouped updates for `gomod`, `github-actions`, and `docker`.

### Container image

Multi-stage `Dockerfile`:

- Builder: `golang:1.27` pinned by digest; `go mod download` cached; `CGO_ENABLED=0 go build -trimpath -ldflags="-s -w"`.
- Runtime: `gcr.io/distroless/static-debian12:nonroot` pinned by digest; `USER nonroot:nonroot`; one binary; no shell; compatible with a read-only root filesystem.
- `.dockerignore` excludes `.git`, `.env*` except `.env.example`, docs, and test data.

Health is served by `/healthz` and `/readyz` for orchestrator probes; the image declares no `HEALTHCHECK` because distroless has no shell or curl.

### Repository files

`README.md`, `AGENTS.md` (architecture rules and required gates), `CLAUDE.md` (points to `AGENTS.md`), `SECURITY.md` (private vulnerability reporting), `.github/CODEOWNERS`, `.gitignore`, `.editorconfig`. The directory is initialized as a Git repository on branch `main`; commits follow Conventional Commits.

## Testing Strategy

### Unit

- Domain: role validation; refresh-token expiry and rotation rules.
- Application, with in-memory fakes for every port and a fixed clock:
  - new Google user is created as `student` and `UserRegistered` is published;
  - existing user is matched by `sub` and profile fields update; changed email does not create a second user;
  - allowlisted email becomes `root_admin`; removal from the allowlist does not demote;
  - unverified email is rejected;
  - refresh rotates and returns a new access token with the current role;
  - reused refresh token revokes the family and fails;
  - expired and revoked refresh tokens fail;
  - logout revokes the family and succeeds for unknown tokens.
- JWT adapter: round trip; wrong key, wrong `kid`, wrong `aud`/`iss`, expired, and `alg: none` rejected.
- Google adapter: tokens signed by a local test key and served from an `httptest` JWKS endpoint; wrong audience, wrong issuer, expired, bad signature rejected.
- HTTP adapter: request validation; problem+json shapes; cookie name and attributes; `Origin` enforcement; `Cache-Control: no-store`; bearer middleware; rate limiting returns 429.
- Platform: config validation errors name the variable; redaction handler hides sensitive keys; security headers present; telemetry test with an in-memory span exporter asserts the route-template span name and the absence of sensitive attributes.

### Integration (`-tags integration`, testcontainers-go)

- Migrations up, down, up.
- Repository behavior against real PostgreSQL, including unique `google_sub`, `citext` email lookup, family revocation, and outbox row written in the same transaction as the user (rolled back together).
- End-to-end through the HTTP server with a stub Google JWKS: sign in, `GET /v1/me`, refresh, reuse rejected and family revoked, logout.

### Required gates

```sh
make check            # fmt check, golangci-lint, sqlc diff, go test -race, govulncheck, gitleaks
make test-integration # requires Docker
docker compose config
make docker-build
git diff --check
```

Integration tests are reported as passing only when actually run with Docker available.

## Risks and Mitigations

- **Google token endpoint requires a client secret for web clients.** The browser PKCE exchange may fail for a "Web application" OAuth client. Mitigation: this design takes an ID token, which works regardless of how the frontend obtained it; a backend code-exchange endpoint can be added later without changing sessions.
- **Role changes lag by up to 15 minutes** because roles are carried in access tokens. Accepted; a later role-management slice can revoke the user's refresh families to force re-authentication.
- **In-memory rate limiting is per instance.** Accepted for a single instance; replace with a shared limiter before scaling out.
- **Cookie flow requires same-site deployment.** If the API and frontends end up on different registrable domains, the refresh cookie flow must be revisited.
- **Tool versions.** Go 1.27 base images, golangci-lint v2, and sqlc must support Go 1.27; versions are pinned and verified during implementation.

## Acceptance Criteria

- The repository builds a single `cmd/api` binary and a distroless non-root image.
- `depguard` rejects cross-context and platform-to-context imports.
- Google sign-in, refresh with rotation and reuse detection, logout, and `GET /v1/me` behave as specified and are covered by unit and integration tests.
- New users are `student`; allowlisted emails become `root_admin`.
- User creation and `UserRegistered` are committed atomically through the outbox.
- Traces, metrics, and logs reach a local OpenObserve via OTLP when configured, and the service runs with telemetry disabled when not configured.
- No secrets, tokens, cookies, or emails appear in logs or telemetry attributes.
- CI, CodeQL, Scorecard, release, and Dependabot configurations exist with SHA-pinned actions and least-privilege permissions.
- All required gates pass locally.
