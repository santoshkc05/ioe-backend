# Platform Foundation: Snowflake IDs and net/http Router

Date: 2026-10-04

## Status

Approved in conversation on 2026-10-04. Pending written-spec review.

## Context

`ioe-backend` is about to gain several bounded contexts ported from `hitox-backend` (courseauthoring first, then enrollment and payment). hitox identifies every entity with a Snowflake `int64` and the ported code, schemas and frontend wire shapes assume it. `ioe-backend` currently uses UUIDv7 for identity entities and `gorilla/mux` for routing.

The project is not deployed and holds no production data, so existing migrations may be edited in place.

This is sub-project 0 of: platform foundation, courseauthoring (`2026-10-04-courseauthoring-design.md`), enrollment, payment.

## Goals

- One entity-ID type across every context: Snowflake `int64`, `BIGINT` in PostgreSQL, a decimal string on the wire.
- Route with the standard library `http.ServeMux` (Go 1.22+ patterns) and drop `gorilla/mux`.
- Keep every existing HTTP behavior: problem+json 404 and 405 with `Allow`, CORS, security headers, request IDs, access logs, OpenTelemetry spans named by route.

## Non-goals

- Changing watermill outbox message UUIDs or JWT `jti` values. They are message and token identifiers, not entity IDs.
- Coordinated node-ID assignment. The node ID is configuration.

## Decisions

| Topic | Decision |
|---|---|
| ID library | `github.com/bwmarrin/snowflake` (default epoch) |
| ID type | `internal/platform/id.ID` (`int64`) |
| Wire format | JSON string of the decimal value; JavaScript numbers cannot hold 63 bits |
| Generator | Concrete `*id.Generator` injected like `clock.Clock`; no global state, no interface |
| Node ID | `SNOWFLAKE_NODE_ID`, 0-1023, default 0; must be unique per running replica |
| Identity migration | Edit `migrations/00002_identity.sql` in place (not deployed) |
| Router | `http.ServeMux`; `otelhttp` replaces `otelmux` |

## Package `internal/platform/id`

```go
type ID int64

func Parse(s string) (ID, error)           // decimal; rejects empty, signs, non-digits, overflow, zero
func (i ID) String() string
func (i ID) IsZero() bool
func (i ID) MarshalJSON() ([]byte, error)  // "123"
func (i *ID) UnmarshalJSON(b []byte) error // accepts only a JSON string

type Generator struct{ node *snowflake.Node }
func NewGenerator(nodeID int64) (*Generator, error) // error outside 0-1023
func (g *Generator) New() ID
```

Domain and app packages of every context may import `internal/platform/id`; each `<ctx>-domain` and `<ctx>-app` depguard allow list gains it.

PostgreSQL stores IDs as `BIGINT`. sqlc keeps generating `int64`; adapters convert with `id.ID(x)` / `int64(x)` in their existing row-mapping functions.

## Identity changes

- `identity.users.id`, `refresh_tokens.id`, `refresh_tokens.user_id`, `refresh_tokens.family_id` become `BIGINT` in `00002_identity.sql`.
- `domain.User.ID`, `RefreshToken.ID/UserID/FamilyID`, and `UserRegistered.UserID` become `id.ID`. `UserRegistered` stays JSON-compatible: `user_id` was already a string.
- `app.Service` takes an `*id.Generator` and replaces `uuid.NewV7()` calls.
- `auth.Principal.UserID` becomes `id.ID`. The JWT `sub` claim is `ID.String()`; verification uses `id.Parse`.
- `AccessTokenIssuer.Issue` and `GetMe` take `id.ID`.
- The notification context keeps working unchanged because it reads `user_id` as an opaque string.

## Router (`internal/platform/httpserver`)

`NewRouter(o Options) (*http.ServeMux, http.Handler)`:

- Every route registers with a method pattern (`mux.Handle("POST /v1/auth/google", h)`); a pattern without a method is not allowed. Handlers read `r.PathValue("name")`.
- The returned handler runs the existing outer chain (`RequestID`, `Recover`, `AccessLog`, `SecurityHeaders`, `CORS`, body limit), then `otelhttp`, then a `problemFallback` wrapper, then the mux.
- `problemFallback` calls `mux.Handler(r)`. A non-empty pattern dispatches normally. An empty pattern means no route matched: it runs the mux's own handler against a capturing `ResponseWriter`, keeps the status and `Allow` header, discards the plain-text body, and writes problem+json (`not_found` or `method_not_allowed`). `AllowedMethods` and its method-probing loop are deleted.
- `otelhttp` starts the span before routing, so `problemFallback` renames it from the pattern `mux.Handler` returned: `trace.SpanFromContext(ctx).SetName(pattern)` (patterns already start with the method, e.g. `"GET /v1/me"`). Unmatched requests are named `"<METHOD> unmatched"`.
- `MountHealth` and identity's `Register` take `*http.ServeMux`. Identity wraps protected routes individually with `RequireAuth`, `requireOrigin` and `NoStore`, as today.
- `go.mod` drops `github.com/gorilla/mux` and `otelmux`.

## Configuration

| Variable | Default | Notes |
|---|---|---|
| `SNOWFLAKE_NODE_ID` | `0` | 0-1023; startup fails outside the range. Documented in `.env.example` and README. |

## Testing

- `platform/id`: parse table (valid, empty, negative, overflow, zero, non-digit), JSON round trip as string, JSON number rejected, generator range check, monotonic IDs from one generator.
- `platform/httpserver`: problem+json 404; 405 with correct `Allow`; a matched route still served; span name uses the route pattern (in-memory span exporter); existing middleware tests keep passing.
- Identity: domain, app and HTTP tests updated to `id.ID`; JWT round trip with a Snowflake `sub`; postgres integration tests against the edited migration.
- `cmd/api` e2e sign-in, refresh, logout and `/v1/me` still pass.

## Prerequisite

Uncommitted work in `internal/platform/httpserver/router.go`, `router_test.go` and the identity HTTP adapter must be committed before this change starts, because the router rewrite replaces those files' routing code.

## Risks

- Two replicas sharing a node ID can mint duplicate IDs. Mitigation: the node ID is required to be unique per replica in deployment docs; primary-key constraints turn a collision into a failed insert rather than silent corruption.
- `mux.Handler(r)` is called twice per unmatched request. Matched requests pay one extra lookup. Acceptable at this scale.
