# Role Management Design

Date: 2026-10-04

## Status

Approved in conversation on 2026-10-04. Pending written-spec review.

## Context

Identity assigns every new user the `student` role. The only other path to a role is the
`BOOTSTRAP_ROOT_ADMIN_EMAILS` list, which `Service.applyBootstrap` re-applies on every sign-in.
Nobody can become an `instructor` without editing the database, so course authoring cannot be
used in practice. The foundation-identity spec deferred role management to "a later slice";
this is that slice.

Relevant existing behaviour:

- `Service.Refresh` reloads the user and issues the access token from the stored role, so a
  role change reaches the user at their next refresh, within the 15-minute access-token TTL.
- Courseauthoring's `Course.IsManagedBy` checks ownership, not role. Creating a course requires
  `instructor` or `root_admin`.
- `Service.signIn` reads the user without a lock and `UpdateUser` writes every column,
  including `role`. A sign-in that overlaps a role change can write the old role back.

## Goals

- A root admin finds a user by email prefix and sets their role to `student` or `instructor`.
- Every role change is recorded as an outbox event in the same transaction.
- A concurrent sign-in never loses a role change.

## Non-goals

- Granting or revoking `root_admin` through the API; it stays bootstrap-only.
- Revoking sessions on role change. Revocation would not shorten the 15-minute window, because
  the current access token stays valid until it expires; it would only force a new Google
  sign-in.
- Per-request role lookup for immediate effect.
- Paginated user listing, role filters, profile editing.
- Changes to courseauthoring. A demoted instructor keeps managing the courses they own and can
  no longer create courses; a root admin can archive those courses.
- Consumers of the new event.

## Decisions

| Topic | Decision |
|---|---|
| Finding a user | Case-insensitive email prefix search, minimum 3 characters, at most 20 results |
| Assignable roles | `student` and `instructor` only |
| Root admin as target | Rejected with 409 `role_not_assignable`; also prevents self-demotion |
| Same role | 200, no write, no event |
| Propagation | At the user's next refresh; no session revocation |
| Audit | `identity.user_role_changed` outbox event |
| Lost-update fix | `SELECT ... FOR UPDATE` on the user row in sign-in and in role change |
| Validation status | 400, matching courseauthoring |

## Domain (`internal/identity/domain`)

```go
var (
	ErrInvalidRole       = errors.New("role cannot be assigned")
	ErrRoleNotAssignable = errors.New("user's role cannot be changed")
)

// ChangeRole sets the role to student or instructor. It returns changed=false and no event
// when the role is already set.
func (u *User) ChangeRole(to auth.Role, by id.ID, now time.Time) (UserRoleChanged, bool, error)
```

`ChangeRole` checks in this order:

1. `to` is not `student` or `instructor`: `ErrInvalidRole`.
2. `u.Role` is `root_admin`: `ErrRoleNotAssignable`.
3. `u.Role == to`: zero event, `false`, nil.
4. Otherwise it sets `Role` and `UpdatedAt` and returns the event.

```go
type UserRoleChanged struct {
	UserID       id.ID     `json:"user_id"`
	PreviousRole auth.Role `json:"previous_role"`
	Role         auth.Role `json:"role"`
	ChangedBy    id.ID     `json:"changed_by"`
	OccurredAt   time.Time `json:"occurred_at"`
}

func (UserRoleChanged) EventName() string { return "identity.user_role_changed" }
```

## Application (`internal/identity/app`)

New errors: `ErrForbidden`, plus `ErrInvalidRole` and `ErrRoleNotAssignable` as aliases of the
domain errors, so the HTTP adapter maps only app errors. `ErrEmailQueryTooShort` for prefixes
shorter than 3 characters after trimming.

`UserRepository` gains:

```go
FindByIDForUpdate(ctx context.Context, id id.ID) (domain.User, error)
FindByGoogleSubjectForUpdate(ctx context.Context, subject string) (domain.User, error)
// SearchByEmailPrefix matches case-insensitively, treats prefix literally, and orders by email.
SearchByEmailPrefix(ctx context.Context, prefix string, limit int32) ([]domain.User, error)
```

`FindByGoogleSubjectForUpdate` replaces `FindByGoogleSubject`, whose only caller is
`Service.signIn`; the `GetUserByGoogleSub` query is replaced the same way.

New `AdminService`, built with `NewAdminService(tx TxRunner, c clock.Clock)`:

- `SearchUsers(ctx, p auth.Principal, prefix string) ([]domain.User, error)`: `ErrForbidden`
  unless `p.Role` is `root_admin`; trims `prefix`; `ErrEmailQueryTooShort` when shorter than 3
  runes; returns at most 20 users.
- `SetRole(ctx, p auth.Principal, userID id.ID, role auth.Role) (domain.User, error)`:
  `ErrForbidden` unless `p.Role` is `root_admin`. In one transaction: `FindByIDForUpdate`
  (`ErrNotFound` passes through), `ChangeRole(role, p.UserID, now)`, and when changed,
  `Users.Update` then `Events.Publish`. Returns the resulting user.

An invalid role for an unknown user returns `ErrNotFound`, because the user is loaded first.

## Persistence (`internal/identity/adapters/postgres`)

New sqlc queries, no migration:

```sql
-- name: GetUserByIDForUpdate :one
SELECT * FROM identity.users WHERE id = $1 FOR UPDATE;

-- Replaces GetUserByGoogleSub.
-- name: GetUserByGoogleSubForUpdate :one
SELECT * FROM identity.users WHERE google_sub = $1 FOR UPDATE;

-- name: SearchUsersByEmailPrefix :many
SELECT * FROM identity.users
WHERE email ILIKE sqlc.arg(pattern)::text ESCAPE '\'
ORDER BY email, id
LIMIT sqlc.arg(max_results);
```

The adapter escapes `\`, `%` and `_` in the prefix and appends `%`. `ILIKE` makes the match
case-insensitive without relying on `citext` operator resolution for a `text` pattern. The
table is small; the existing `users_email_idx` is sufficient.

## HTTP API (`internal/identity/adapters/httpapi`)

`New` takes an `AdminService` interface alongside `SessionService`. Both routes use
`NoStore(RequireAuth(...))`.

`GET /v1/admin/users?email=<prefix>` returns `200 {"users": [userResponse...]}`.

`PUT /v1/admin/users/{userID}/role` with `{"role": "instructor"}` returns `200 userResponse`.
A malformed `userID` returns 404. The body is decoded with `httpserver.DecodeJSON`.

| Error | Status | Type |
|---|---|---|
| `ErrForbidden` | 403 | `forbidden` |
| `ErrNotFound` | 404 | `not_found` |
| `ErrInvalidRole` | 400 | `invalid_role` |
| `ErrEmailQueryTooShort` | 400 | `email_query_too_short` |
| `ErrRoleNotAssignable` | 409 | `role_not_assignable` |

`api/openapi.yaml` documents both paths, the request and response schemas, and these problems.

## Composition (`cmd/api`)

Build `identityapp.NewAdminService` with the identity `TxRunner` and clock and pass it to the
identity HTTP handler. No configuration changes.

## Testing

- Domain: table test for `ChangeRole` covering student to instructor, instructor to student,
  no-op, `root_admin` as the requested role, an unknown role, and a `root_admin` target.
- App, with in-memory fakes: non-admin rejected for both methods; unknown user; a change
  publishes exactly one event with the caller as `changed_by`; a no-op writes and publishes
  nothing; prefix trimming and the minimum length; the 20-result cap is passed to the repository.
- Postgres (`-tags integration`): search is case-insensitive, ordered, limited, and treats `%`
  and `_` literally; a transaction holding `GetUserByIDForUpdate` blocks a concurrent
  `GetUserByGoogleSubForUpdate` until commit, and the sign-in then sees the new role.
- HTTP: every row of the error table, response shapes, 401 without a token, malformed ID as 404.
- `cmd/api` e2e: a bootstrap root admin promotes a student; after refresh the student's access
  token carries `instructor` and course creation succeeds.

Gates from `AGENTS.md`: `make check`, `make test-integration` (actually run),
`docker compose config`, `make docker-build`, `git diff --check`.

## Risks

- A demoted instructor keeps full access for up to 15 minutes and keeps managing owned courses
  indefinitely. Accepted for this slice.
- The sign-in transaction now holds a row lock while it issues the refresh token. The lock is
  per user and short; contention only occurs when one user signs in concurrently with
  themselves or with a role change.
