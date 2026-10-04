# Role Management Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let a root admin find users by email prefix and set their role to `student` or `instructor`, with an outbox event per change and no role lost to a concurrent sign-in.

**Architecture:** All changes stay inside the `identity` bounded context plus `cmd/api` wiring. The `User` aggregate owns the role-change rule and emits `UserRoleChanged`; a new `app.AdminService` authorizes root admins and runs the change in one transaction with a row lock; the HTTP adapter adds two `/v1/admin/users` routes. Sign-in takes the same row lock so its full-row update cannot overwrite a role change.

**Tech Stack:** Go 1.27.1, pgx v5, sqlc v1.31.1, Watermill SQL outbox, `net/http` ServeMux, testcontainers PostgreSQL 17 (`pgtest`).

**Spec:** `docs/superpowers/specs/2026-10-04-role-management-design.md`

## Before you start

Work on branch `feat/role-management` (already created; the spec commits are on it). The working tree must be clean before Task 1. Integration tests need a running Docker daemon; never report them as passing unless they ran.

## Global Constraints

- Module `github.com/santoshkc2200/ioe-backend`; no new Go modules.
- No change outside `internal/identity`, `cmd/api`, `api/openapi.yaml`, `README.md`, and generated sqlc code.
- `internal/identity/app` imports only its domain, the standard library, and `platform/{auth,clock,id}`.
- Assignable roles: `student`, `instructor`. `root_admin` stays bootstrap-only (`BOOTSTRAP_ROOT_ADMIN_EMAILS`).
- Event topic `identity.user_role_changed`; JSON fields `user_id`, `previous_role`, `role`, `changed_by`, `occurred_at`.
- Search: trimmed prefix, minimum 3 runes, case-insensitive, `%`, `_`, `\` literal, ordered by email then id, at most 20 results.
- Routes: `GET /v1/admin/users?email=<prefix>` and `PUT /v1/admin/users/{userID}/role`, both `NoStore(RequireAuth(...))`.
- Problems: 403 `forbidden`, 404 `not_found`, 400 `invalid_role`, 400 `email_query_too_short`, 409 `role_not_assignable`.
- No session revocation on role change.
- Conventional Commits.

## Review Focus

- Role sent with different casing (`"Instructor"`) or empty: expect 400 `invalid_role`, never a stored unknown role. Pinned in Task 1's table.
- Email prefix containing `%`, `_` or `\`: expect a literal match, not a wildcard. Pinned in Task 2's search test.
- Sign-in racing a role change on the same user: expect the role change to survive. Pinned in Task 2's lock test.
- Missing `email` query parameter or a 2-character multibyte prefix (`"éé"`, 4 bytes): expect 400 `email_query_too_short`. Pinned in Task 3's search test.
- Root admin changing their own role through the API: expect 409 `role_not_assignable`. Pinned in Task 5's end-to-end test.

---

### Task 1: `User.ChangeRole` and `UserRoleChanged`

**Files:**
- Modify: `internal/identity/domain/user.go`
- Modify: `internal/identity/domain/events.go`
- Test: `internal/identity/domain/domain_test.go`

**Interfaces:**
- Consumes: `auth.Role`, `auth.RoleStudent`, `auth.RoleInstructor`, `auth.RoleRootAdmin` (`internal/platform/auth`), `id.ID`.
- Produces:
  - `var domain.ErrInvalidRole`, `var domain.ErrRoleNotAssignable`
  - `func (u *User) ChangeRole(to auth.Role, by id.ID, now time.Time) (UserRoleChanged, bool, error)`
  - `type domain.UserRoleChanged struct { UserID id.ID; PreviousRole auth.Role; Role auth.Role; ChangedBy id.ID; OccurredAt time.Time }` with `EventName() == "identity.user_role_changed"`

- [ ] **Step 1: Write the failing tests**

Append to `internal/identity/domain/domain_test.go`, and add `"encoding/json"` to its imports:

```go
func TestChangeRole(t *testing.T) {
	by := id.ID(9)
	later := t0.Add(time.Hour)
	cases := []struct {
		name        string
		from, to    auth.Role
		wantRole    auth.Role
		wantChanged bool
		wantErr     error
	}{
		{"promote", auth.RoleStudent, auth.RoleInstructor, auth.RoleInstructor, true, nil},
		{"demote", auth.RoleInstructor, auth.RoleStudent, auth.RoleStudent, true, nil},
		{"same role", auth.RoleInstructor, auth.RoleInstructor, auth.RoleInstructor, false, nil},
		{"grant root admin", auth.RoleStudent, auth.RoleRootAdmin, auth.RoleStudent, false, domain.ErrInvalidRole},
		{"unknown role", auth.RoleStudent, auth.Role("teacher"), auth.RoleStudent, false, domain.ErrInvalidRole},
		{"wrong case", auth.RoleStudent, auth.Role("Instructor"), auth.RoleStudent, false, domain.ErrInvalidRole},
		{"empty role", auth.RoleStudent, auth.Role(""), auth.RoleStudent, false, domain.ErrInvalidRole},
		{"root admin target", auth.RoleRootAdmin, auth.RoleStudent, auth.RoleRootAdmin, false, domain.ErrRoleNotAssignable},
		{"root admin target, invalid role", auth.RoleRootAdmin, auth.RoleRootAdmin, auth.RoleRootAdmin, false, domain.ErrInvalidRole},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			u := domain.NewUser(id.ID(1), domain.GoogleIdentity{Subject: "s"}, t0)
			u.Role = c.from
			ev, changed, err := u.ChangeRole(c.to, by, later)
			if !errors.Is(err, c.wantErr) || changed != c.wantChanged || u.Role != c.wantRole {
				t.Fatalf("role=%s changed=%v err=%v", u.Role, changed, err)
			}
			if !changed {
				if ev != (domain.UserRoleChanged{}) || !u.UpdatedAt.Equal(t0) {
					t.Fatalf("unchanged user mutated: event=%+v updated=%v", ev, u.UpdatedAt)
				}
				return
			}
			want := domain.UserRoleChanged{UserID: u.ID, PreviousRole: c.from, Role: c.to, ChangedBy: by, OccurredAt: later}
			if ev != want || !u.UpdatedAt.Equal(later) {
				t.Fatalf("event=%+v updated=%v", ev, u.UpdatedAt)
			}
		})
	}
}

func TestUserRoleChangedEvent(t *testing.T) {
	var e domain.Event = domain.UserRoleChanged{}
	if e.EventName() != "identity.user_role_changed" {
		t.Fatal(e.EventName())
	}
	b, err := json.Marshal(domain.UserRoleChanged{
		UserID: id.ID(1), PreviousRole: auth.RoleStudent, Role: auth.RoleInstructor, ChangedBy: id.ID(2), OccurredAt: t0,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"user_id":"1","previous_role":"student","role":"instructor","changed_by":"2","occurred_at":"2026-10-04T12:00:00Z"}`
	if string(b) != want {
		t.Fatalf("payload %s", b)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/identity/domain/`
Expected: FAIL to compile with `u.ChangeRole undefined` and `undefined: domain.UserRoleChanged`.

- [ ] **Step 3: Implement**

In `internal/identity/domain/user.go`, add `"errors"` to the imports, and add after the `User` methods:

```go
var (
	ErrInvalidRole       = errors.New("role cannot be assigned")
	ErrRoleNotAssignable = errors.New("user's role cannot be changed")
)

// ChangeRole makes the user a student or an instructor. Root admins come only from bootstrap
// configuration, so their role never changes here. It returns changed=false and no event when
// the user already has the role.
func (u *User) ChangeRole(to auth.Role, by id.ID, now time.Time) (UserRoleChanged, bool, error) {
	if to != auth.RoleStudent && to != auth.RoleInstructor {
		return UserRoleChanged{}, false, ErrInvalidRole
	}
	if u.Role == auth.RoleRootAdmin {
		return UserRoleChanged{}, false, ErrRoleNotAssignable
	}
	if u.Role == to {
		return UserRoleChanged{}, false, nil
	}
	ev := UserRoleChanged{UserID: u.ID, PreviousRole: u.Role, Role: to, ChangedBy: by, OccurredAt: now}
	u.Role = to
	u.UpdatedAt = now
	return ev, true, nil
}
```

In `internal/identity/domain/events.go`, add `"github.com/santoshkc2200/ioe-backend/internal/platform/auth"` to the imports and append:

```go
// UserRoleChanged records a root admin changing a user's role.
type UserRoleChanged struct {
	UserID       id.ID     `json:"user_id"`
	PreviousRole auth.Role `json:"previous_role"`
	Role         auth.Role `json:"role"`
	ChangedBy    id.ID     `json:"changed_by"`
	OccurredAt   time.Time `json:"occurred_at"`
}

func (UserRoleChanged) EventName() string { return "identity.user_role_changed" }
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/identity/domain/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/identity/domain/user.go internal/identity/domain/events.go internal/identity/domain/domain_test.go
git commit -m "feat(identity): add user role change to the domain"
```

---

### Task 2: Locking user lookups and email-prefix search

**Files:**
- Modify: `internal/identity/adapters/postgres/queries.sql`
- Regenerate: `internal/identity/adapters/postgres/sqlcgen/queries.sql.go` (via `make sqlc`)
- Modify: `internal/identity/adapters/postgres/postgres.go`
- Modify: `internal/identity/app/ports.go`
- Modify: `internal/identity/app/service.go` (one call in `signIn`)
- Modify: `internal/identity/app/fakes_test.go`
- Test: `internal/identity/adapters/postgres/postgres_integration_test.go`

**Interfaces:**
- Consumes: `domain.User.ChangeRole` (Task 1).
- Produces, on `app.UserRepository`:
  - `FindByGoogleSubjectForUpdate(ctx context.Context, subject string) (domain.User, error)` (replaces `FindByGoogleSubject`)
  - `FindByIDForUpdate(ctx context.Context, id id.ID) (domain.User, error)`
  - `SearchByEmailPrefix(ctx context.Context, prefix string, limit int32) ([]domain.User, error)`
- Produces, in `app_test` fakes: `memUsers` implements the three methods; `func (m *memStore) lastSearchLimit() int32`.

- [ ] **Step 1: Write the failing integration tests**

In `internal/identity/adapters/postgres/postgres_integration_test.go`:

1. Add `"fmt"` and `"slices"` to the imports.
2. In `TestUserRepository`, replace both `r.Users.FindByGoogleSubject(` calls with `r.Users.FindByGoogleSubjectForUpdate(`.
3. Append:

```go
func TestSearchUsersByEmailPrefix(t *testing.T) {
	ctx := context.Background()
	runner := postgres.NewTxRunner(pgtest.New(t))
	gen := testIDs(t)
	emails := []string{
		"bob@example.com", "Alice@Example.com", "alina@example.com", "ALBERT@example.com",
		"x%1@example.com", "x_2@example.com", "xa3@example.com", `y\z@example.com`, "yaz@example.com",
	}
	err := runner.RunInTx(ctx, func(r app.Repos) error {
		for i, e := range emails {
			u := domain.NewUser(gen.New(), domain.GoogleIdentity{Subject: fmt.Sprintf("sub-search-%d", i), Email: e}, now)
			if err := r.Users.Insert(ctx, u); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	search := func(prefix string, limit int32) []string {
		t.Helper()
		var got []string
		err := runner.RunInTx(ctx, func(r app.Repos) error {
			users, err := r.Users.SearchByEmailPrefix(ctx, prefix, limit)
			for _, u := range users {
				got = append(got, u.Email)
			}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	cases := []struct {
		prefix string
		limit  int32
		want   []string
	}{
		{"AL", 20, []string{"ALBERT@example.com", "Alice@Example.com", "alina@example.com"}},
		{"al", 2, []string{"ALBERT@example.com", "Alice@Example.com"}},
		{"x%", 20, []string{"x%1@example.com"}},
		{"x_", 20, []string{"x_2@example.com"}},
		{`y\`, 20, []string{`y\z@example.com`}},
		{"zzz", 20, nil},
	}
	for _, c := range cases {
		if got := search(c.prefix, c.limit); !slices.Equal(got, c.want) {
			t.Errorf("search %q limit %d = %v, want %v", c.prefix, c.limit, got, c.want)
		}
	}
}

func TestSignInLockWaitsForRoleChange(t *testing.T) {
	ctx := context.Background()
	runner := postgres.NewTxRunner(pgtest.New(t))
	u := newUser("sub-lock")
	if err := runner.RunInTx(ctx, func(r app.Repos) error { return r.Users.Insert(ctx, u) }); err != nil {
		t.Fatal(err)
	}

	locked, release := make(chan struct{}), make(chan struct{})
	roleDone := make(chan error, 1)
	go func() {
		roleDone <- runner.RunInTx(ctx, func(r app.Repos) error {
			x, err := r.Users.FindByIDForUpdate(ctx, u.ID)
			if err != nil {
				return err
			}
			close(locked)
			<-release
			if _, _, err := x.ChangeRole(auth.RoleInstructor, id.ID(1), now); err != nil {
				return err
			}
			return r.Users.Update(ctx, x)
		})
	}()
	select {
	case <-locked:
	case err := <-roleDone:
		t.Fatalf("role change ended before taking the lock: %v", err)
	}

	signInDone := make(chan error, 1)
	go func() {
		signInDone <- runner.RunInTx(ctx, func(r app.Repos) error {
			x, err := r.Users.FindByGoogleSubjectForUpdate(ctx, "sub-lock")
			if err != nil {
				return err
			}
			x.RecordLogin(domain.GoogleIdentity{Subject: "sub-lock", Email: "later@example.com"}, now.Add(time.Minute))
			return r.Users.Update(ctx, x)
		})
	}()
	select {
	case err := <-signInDone:
		t.Fatalf("sign-in did not wait for the role change: %v", err)
	case <-time.After(300 * time.Millisecond):
	}

	close(release)
	if err := <-roleDone; err != nil {
		t.Fatal(err)
	}
	if err := <-signInDone; err != nil {
		t.Fatal(err)
	}
	var got domain.User
	err := runner.RunInTx(ctx, func(r app.Repos) error {
		var err error
		got, err = r.Users.FindByID(ctx, u.ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Role != auth.RoleInstructor || got.Email != "later@example.com" {
		t.Fatalf("after both commits: role=%s email=%s", got.Role, got.Email)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -race -tags integration ./internal/identity/adapters/postgres/`
Expected: FAIL to compile with `r.Users.FindByGoogleSubjectForUpdate undefined` (and the other two new methods).

- [ ] **Step 3: Add the queries and regenerate**

In `internal/identity/adapters/postgres/queries.sql`, replace

```sql
-- name: GetUserByGoogleSub :one
SELECT * FROM identity.users WHERE google_sub = $1;
```

with

```sql
-- name: GetUserByGoogleSubForUpdate :one
SELECT * FROM identity.users WHERE google_sub = $1 FOR UPDATE;
```

and after `-- name: GetUserByID :one ...` add

```sql
-- name: GetUserByIDForUpdate :one
SELECT * FROM identity.users WHERE id = $1 FOR UPDATE;

-- name: SearchUsersByEmailPrefix :many
SELECT * FROM identity.users
WHERE email ILIKE sqlc.arg(pattern)::text ESCAPE '\'
ORDER BY email, id
LIMIT sqlc.arg(max_results);
```

Run: `make sqlc`
Expected: `sqlcgen/queries.sql.go` now has `GetUserByGoogleSubForUpdate(ctx, googleSub string)`, `GetUserByIDForUpdate(ctx, id int64)`, and `SearchUsersByEmailPrefix(ctx, arg SearchUsersByEmailPrefixParams)` with `SearchUsersByEmailPrefixParams{Pattern string; MaxResults int32}`; `GetUserByGoogleSub` is gone.

- [ ] **Step 4: Update the port**

In `internal/identity/app/ports.go`, replace the `UserRepository` declaration with:

```go
// UserRepository returns ErrNotFound for missing users and ErrConflict when Insert
// would duplicate a Google subject. The ForUpdate finders lock the row until the
// transaction ends, so a read-modify-Update cannot overwrite a concurrent change.
type UserRepository interface {
	FindByGoogleSubjectForUpdate(ctx context.Context, subject string) (domain.User, error)
	FindByID(ctx context.Context, id id.ID) (domain.User, error)
	FindByIDForUpdate(ctx context.Context, id id.ID) (domain.User, error)
	// SearchByEmailPrefix matches case-insensitively, treats prefix literally, orders by
	// email, and returns at most limit users.
	SearchByEmailPrefix(ctx context.Context, prefix string, limit int32) ([]domain.User, error)
	Insert(ctx context.Context, u domain.User) error
	Update(ctx context.Context, u domain.User) error
}
```

- [ ] **Step 5: Implement the adapter**

In `internal/identity/adapters/postgres/postgres.go`, add `"strings"` to the imports, replace the `FindByGoogleSubject` method with `FindByGoogleSubjectForUpdate`, and add the other two methods:

```go
func (u users) FindByGoogleSubjectForUpdate(ctx context.Context, sub string) (domain.User, error) {
	row, err := u.q.GetUserByGoogleSubForUpdate(ctx, sub)
	return toUser(row, err)
}

func (u users) FindByIDForUpdate(ctx context.Context, id id.ID) (domain.User, error) {
	row, err := u.q.GetUserByIDForUpdate(ctx, int64(id))
	return toUser(row, err)
}

// likeEscaper makes a string match itself literally in a LIKE pattern with ESCAPE '\'.
var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

func (u users) SearchByEmailPrefix(ctx context.Context, prefix string, limit int32) ([]domain.User, error) {
	rows, err := u.q.SearchUsersByEmailPrefix(ctx, sqlcgen.SearchUsersByEmailPrefixParams{
		Pattern: likeEscaper.Replace(prefix) + "%", MaxResults: limit,
	})
	if err != nil {
		return nil, err
	}
	out := make([]domain.User, 0, len(rows))
	for _, row := range rows {
		x, err := toUser(row, nil)
		if err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, nil
}
```

Note: the "zzz" case expects `nil` from the test helper, which only appends; an empty `out` slice yields a nil `got`, so this matches.

- [ ] **Step 6: Lock the user during sign-in**

In `internal/identity/app/service.go`, in `signIn`, replace

```go
	user, err := r.Users.FindByGoogleSubject(ctx, identity.Subject)
```

with

```go
	// Lock the row: Update writes every column, so an unlocked read could undo a concurrent role change.
	user, err := r.Users.FindByGoogleSubjectForUpdate(ctx, identity.Subject)
```

- [ ] **Step 7: Update the in-memory fakes**

In `internal/identity/app/fakes_test.go`:

1. Add `"strings"` to the imports.
2. Add a field to `memStore`: `searchLimit int32`.
3. Rename `func (u *memUsers) FindByGoogleSubject(` to `func (u *memUsers) FindByGoogleSubjectForUpdate(`.
4. Append:

```go
func (u *memUsers) FindByIDForUpdate(ctx context.Context, id id.ID) (domain.User, error) {
	return u.FindByID(ctx, id)
}

func (u *memUsers) SearchByEmailPrefix(_ context.Context, prefix string, limit int32) ([]domain.User, error) {
	u.store.searchLimit = limit
	var out []domain.User
	for _, x := range u.s.users {
		if strings.HasPrefix(strings.ToLower(x.Email), strings.ToLower(prefix)) {
			out = append(out, x)
		}
	}
	slices.SortFunc(out, func(a, b domain.User) int {
		return strings.Compare(strings.ToLower(a.Email), strings.ToLower(b.Email))
	})
	if len(out) > int(limit) {
		out = out[:limit]
	}
	return out, nil
}

func (m *memStore) lastSearchLimit() int32 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.searchLimit
}
```

(`RunInTx` holds `m.mu` while `fn` runs, so writing `searchLimit` there is race-free.)

- [ ] **Step 8: Run unit and integration tests**

Run: `go test -race ./internal/identity/... && go test -race -tags integration ./internal/identity/adapters/postgres/`
Expected: PASS, including `TestSearchUsersByEmailPrefix` and `TestSignInLockWaitsForRoleChange`.

- [ ] **Step 9: Lint and commit**

Run: `golangci-lint run ./internal/identity/... && make sqlc-check`
Expected: no issues, no sqlc diff.

```bash
git add internal/identity/adapters/postgres internal/identity/app/ports.go internal/identity/app/service.go internal/identity/app/fakes_test.go
git commit -m "feat(identity): lock users on sign-in and search by email prefix"
```

---

### Task 3: `AdminService`

**Files:**
- Create: `internal/identity/app/admin.go`
- Modify: `internal/identity/app/ports.go` (error block)
- Test: `internal/identity/app/admin_test.go`

**Interfaces:**
- Consumes: `UserRepository.FindByIDForUpdate`, `UserRepository.SearchByEmailPrefix`, `UserRepository.Update`, `EventPublisher.Publish` (Task 2); `domain.User.ChangeRole`, `domain.ErrInvalidRole`, `domain.ErrRoleNotAssignable` (Task 1); test fakes `newMemStore`, `memStore.snapshot`, `memStore.lastSearchLimit`, and `t0`, `ctx` from `service_test.go`.
- Produces:
  - `var app.ErrForbidden`, `app.ErrEmailQueryTooShort`, `app.ErrInvalidRole` (= `domain.ErrInvalidRole`), `app.ErrRoleNotAssignable` (= `domain.ErrRoleNotAssignable`)
  - `func NewAdminService(tx TxRunner, c clock.Clock) *AdminService`
  - `func (s *AdminService) SearchUsers(ctx context.Context, p auth.Principal, prefix string) ([]domain.User, error)`
  - `func (s *AdminService) SetRole(ctx context.Context, p auth.Principal, userID id.ID, role auth.Role) (domain.User, error)`

- [ ] **Step 1: Write the failing tests**

Create `internal/identity/app/admin_test.go`:

```go
package app_test

import (
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/identity/app"
	"github.com/santoshkc2200/ioe-backend/internal/identity/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/clock"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

var rootAdmin = auth.Principal{UserID: id.ID(7), Role: auth.RoleRootAdmin}

type adminFixture struct {
	store *memStore
	clock *clock.Fake
	svc   *app.AdminService
}

func newAdminFixture(users ...domain.User) *adminFixture {
	f := &adminFixture{store: newMemStore(), clock: clock.NewFake(t0)}
	for _, u := range users {
		f.store.state.users[u.ID] = u
	}
	f.svc = app.NewAdminService(f.store, f.clock)
	return f
}

func userWithRole(n int64, email string, r auth.Role) domain.User {
	u := domain.NewUser(id.ID(n), domain.GoogleIdentity{Subject: fmt.Sprintf("sub-%d", n), Email: email}, t0)
	u.Role = r
	return u
}

func TestAdminServiceRequiresRootAdmin(t *testing.T) {
	f := newAdminFixture(userWithRole(1, "s@example.com", auth.RoleStudent))
	for _, role := range []auth.Role{auth.RoleStudent, auth.RoleInstructor} {
		p := auth.Principal{UserID: id.ID(1), Role: role}
		if _, err := f.svc.SearchUsers(ctx, p, "s@example"); !errors.Is(err, app.ErrForbidden) {
			t.Errorf("%s search err = %v", role, err)
		}
		if _, err := f.svc.SetRole(ctx, p, id.ID(1), auth.RoleInstructor); !errors.Is(err, app.ErrForbidden) {
			t.Errorf("%s set role err = %v", role, err)
		}
	}
	st := f.store.snapshot()
	if st.users[id.ID(1)].Role != auth.RoleStudent || len(st.events) != 0 {
		t.Fatalf("state changed: %+v events=%d", st.users[id.ID(1)], len(st.events))
	}
}

func TestSetRoleChangesRoleAndPublishes(t *testing.T) {
	f := newAdminFixture(userWithRole(1, "s@example.com", auth.RoleStudent))
	f.clock.Advance(time.Hour)
	later := t0.Add(time.Hour)

	got, err := f.svc.SetRole(ctx, rootAdmin, id.ID(1), auth.RoleInstructor)
	if err != nil {
		t.Fatal(err)
	}
	if got.Role != auth.RoleInstructor || !got.UpdatedAt.Equal(later) {
		t.Fatalf("returned %+v", got)
	}
	st := f.store.snapshot()
	if st.users[id.ID(1)].Role != auth.RoleInstructor {
		t.Fatalf("stored %+v", st.users[id.ID(1)])
	}
	want := domain.UserRoleChanged{UserID: id.ID(1), PreviousRole: auth.RoleStudent, Role: auth.RoleInstructor, ChangedBy: rootAdmin.UserID, OccurredAt: later}
	if len(st.events) != 1 || st.events[0] != want {
		t.Fatalf("events %+v", st.events)
	}
}

func TestSetRoleSameRoleWritesNothing(t *testing.T) {
	f := newAdminFixture(userWithRole(1, "i@example.com", auth.RoleInstructor))
	f.clock.Advance(time.Hour)
	got, err := f.svc.SetRole(ctx, rootAdmin, id.ID(1), auth.RoleInstructor)
	if err != nil {
		t.Fatal(err)
	}
	st := f.store.snapshot()
	if got.Role != auth.RoleInstructor || !st.users[id.ID(1)].UpdatedAt.Equal(t0) || len(st.events) != 0 {
		t.Fatalf("returned %+v stored %+v events %d", got, st.users[id.ID(1)], len(st.events))
	}
}

func TestSetRoleErrors(t *testing.T) {
	cases := []struct {
		name   string
		target id.ID
		role   auth.Role
		want   error
	}{
		{"unknown user", id.ID(99), auth.RoleInstructor, app.ErrNotFound},
		{"root admin target", id.ID(2), auth.RoleStudent, app.ErrRoleNotAssignable},
		{"grant root admin", id.ID(1), auth.RoleRootAdmin, app.ErrInvalidRole},
		{"unknown role", id.ID(1), auth.Role("teacher"), app.ErrInvalidRole},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newAdminFixture(userWithRole(1, "s@example.com", auth.RoleStudent), userWithRole(2, "r@example.com", auth.RoleRootAdmin))
			if _, err := f.svc.SetRole(ctx, rootAdmin, c.target, c.role); !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
			st := f.store.snapshot()
			if len(st.events) != 0 || st.users[id.ID(1)].Role != auth.RoleStudent || st.users[id.ID(2)].Role != auth.RoleRootAdmin {
				t.Fatalf("state changed: %+v events=%d", st.users, len(st.events))
			}
		})
	}
}

func TestSearchUsers(t *testing.T) {
	f := newAdminFixture(
		userWithRole(1, "Alicia@example.com", auth.RoleStudent),
		userWithRole(2, "alice@example.com", auth.RoleInstructor),
		userWithRole(3, "bob@example.com", auth.RoleStudent),
	)
	users, err := f.svc.SearchUsers(ctx, rootAdmin, "  ALI  ")
	if err != nil {
		t.Fatal(err)
	}
	var emails []string
	for _, u := range users {
		emails = append(emails, u.Email)
	}
	if !slices.Equal(emails, []string{"alice@example.com", "Alicia@example.com"}) {
		t.Fatalf("emails %v", emails)
	}
	if got := f.store.lastSearchLimit(); got != 20 {
		t.Fatalf("limit %d", got)
	}

	for _, short := range []string{"", "ab", "  ab  ", "éé"} {
		if _, err := f.svc.SearchUsers(ctx, rootAdmin, short); !errors.Is(err, app.ErrEmailQueryTooShort) {
			t.Errorf("%q err = %v", short, err)
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/identity/app/`
Expected: FAIL to compile with `undefined: app.AdminService` (and the new errors).

- [ ] **Step 3: Add the errors**

In `internal/identity/app/ports.go`, extend the `var (...)` error block:

```go
var (
	ErrInvalidToken       = errors.New("invalid token")
	ErrRefreshReuse       = errors.New("refresh token reuse detected")
	ErrEmailUnverified    = errors.New("email not verified")
	ErrNotFound           = errors.New("not found")
	ErrConflict           = errors.New("conflict")
	ErrForbidden          = errors.New("forbidden")
	ErrEmailQueryTooShort = errors.New("email query too short")
	ErrInvalidRole        = domain.ErrInvalidRole
	ErrRoleNotAssignable  = domain.ErrRoleNotAssignable
)
```

- [ ] **Step 4: Implement `AdminService`**

Create `internal/identity/app/admin.go`:

```go
package app

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/santoshkc2200/ioe-backend/internal/identity/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/clock"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

const (
	minEmailQueryLen       = 3
	maxSearchResults int32 = 20
)

// AdminService implements user management for root admins.
type AdminService struct {
	tx    TxRunner
	clock clock.Clock
}

func NewAdminService(tx TxRunner, c clock.Clock) *AdminService {
	return &AdminService{tx: tx, clock: c}
}

// SearchUsers returns up to 20 users whose email starts with prefix, ignoring case.
func (s *AdminService) SearchUsers(ctx context.Context, p auth.Principal, prefix string) ([]domain.User, error) {
	if p.Role != auth.RoleRootAdmin {
		return nil, ErrForbidden
	}
	prefix = strings.TrimSpace(prefix)
	if utf8.RuneCountInString(prefix) < minEmailQueryLen {
		return nil, ErrEmailQueryTooShort
	}
	var users []domain.User
	err := s.tx.RunInTx(ctx, func(r Repos) error {
		var err error
		users, err = r.Users.SearchByEmailPrefix(ctx, prefix, maxSearchResults)
		return err
	})
	return users, err
}

// SetRole makes the user a student or an instructor and records the change in the outbox.
// Setting the role the user already has succeeds without writing anything. The user sees
// the new role in the access token issued at their next refresh.
func (s *AdminService) SetRole(ctx context.Context, p auth.Principal, userID id.ID, role auth.Role) (domain.User, error) {
	if p.Role != auth.RoleRootAdmin {
		return domain.User{}, ErrForbidden
	}
	var user domain.User
	err := s.tx.RunInTx(ctx, func(r Repos) error {
		var err error
		user, err = r.Users.FindByIDForUpdate(ctx, userID)
		if err != nil {
			return err
		}
		ev, changed, err := user.ChangeRole(role, p.UserID, s.clock.Now())
		if err != nil || !changed {
			return err
		}
		if err := r.Users.Update(ctx, user); err != nil {
			return err
		}
		return r.Events.Publish(ctx, ev)
	})
	if err != nil {
		return domain.User{}, err
	}
	return user, nil
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test -race ./internal/identity/...`
Expected: PASS.

- [ ] **Step 6: Lint and commit**

Run: `golangci-lint run ./internal/identity/...`
Expected: no issues (depguard allows `platform/id` in app; `service.go` already imports it).

```bash
git add internal/identity/app/admin.go internal/identity/app/admin_test.go internal/identity/app/ports.go
git commit -m "feat(identity): add admin service for role changes"
```

---

### Task 4: Admin HTTP routes and wiring

**Files:**
- Create: `internal/identity/adapters/httpapi/admin.go`
- Modify: `internal/identity/adapters/httpapi/httpapi.go`
- Modify: `internal/identity/adapters/httpapi/httpapi_test.go`
- Modify: `cmd/api/app.go`

**Interfaces:**
- Consumes: `app.AdminService` methods and errors (Task 3).
- Produces:
  - `type httpapi.AdminService interface { SearchUsers(ctx context.Context, p auth.Principal, prefix string) ([]domain.User, error); SetRole(ctx context.Context, p auth.Principal, userID id.ID, role auth.Role) (domain.User, error) }`
  - `func httpapi.New(svc SessionService, admin AdminService, verifier AccessTokenVerifier, cfg Config) (*Handler, error)` (new second parameter)
  - Routes `GET /v1/admin/users`, `PUT /v1/admin/users/{userID}/role`.

- [ ] **Step 1: Write the failing tests**

In `internal/identity/adapters/httpapi/httpapi_test.go`:

1. Add fields to `fakeService`:

```go
	adminErr     error
	searchResult []domain.User
	gotPrincipal auth.Principal
	gotPrefix    string
	gotUserID    id.ID
	gotRole      auth.Role
```

2. Add methods after `GetMe`:

```go
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
```

3. Make `fakeVerifier.Verify` accept an admin token:

```go
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
```

and add to the top-level `var (...)` block:

```go
	adminPrincipal = auth.Principal{UserID: id.ID(7), Role: auth.RoleRootAdmin}
	adminHeaders   = map[string]string{"Authorization": "Bearer admin", "Content-Type": "application/json"}
```

4. In `newHandler`, change `httpapi.New(svc, fakeVerifier{}, ...` to `httpapi.New(svc, svc, fakeVerifier{}, ...`.

5. Append the tests:

```go
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/identity/adapters/httpapi/`
Expected: FAIL to compile with `too many arguments in call to httpapi.New`.

- [ ] **Step 3: Implement the adapter**

In `internal/identity/adapters/httpapi/httpapi.go`:

1. Extend the `const` block:

```go
	typeInvalidToken       = "invalid_token"
	typeRefreshReuse       = "refresh_reuse_detected"
	typeEmailUnverified    = "email_unverified"
	typeForbidden          = "forbidden"
	typeInvalidRole        = "invalid_role"
	typeEmailQueryTooShort = "email_query_too_short"
	typeRoleNotAssignable  = "role_not_assignable"
```

2. After the `SessionService` interface add:

```go
// AdminService is the identity user-management service as used by HTTP.
type AdminService interface {
	SearchUsers(ctx context.Context, p auth.Principal, prefix string) ([]domain.User, error)
	SetRole(ctx context.Context, p auth.Principal, userID id.ID, role auth.Role) (domain.User, error)
}
```

3. Add `admin AdminService` to `Handler` after `svc`, change `New` to `func New(svc SessionService, admin AdminService, verifier AccessTokenVerifier, cfg Config) (*Handler, error)`, and set `admin: admin` in the returned struct.

4. At the end of `Register` add:

```go
	admin := func(next http.HandlerFunc) http.Handler { return httpserver.NoStore(h.RequireAuth(next)) }
	r.Handle("GET /v1/admin/users", admin(h.searchUsers))
	r.Handle("PUT /v1/admin/users/{userID}/role", admin(h.setRole))
```

5. In `writeError`, add these cases before `default`:

```go
	case errors.Is(err, app.ErrForbidden):
		problem.Write(w, r, http.StatusForbidden, typeForbidden, "Forbidden", "")
	case errors.Is(err, app.ErrInvalidRole):
		problem.Write(w, r, http.StatusBadRequest, typeInvalidRole, "Invalid Role", "role must be student or instructor")
	case errors.Is(err, app.ErrEmailQueryTooShort):
		problem.Write(w, r, http.StatusBadRequest, typeEmailQueryTooShort, "Email Query Too Short", "email must be at least 3 characters")
	case errors.Is(err, app.ErrRoleNotAssignable):
		problem.Write(w, r, http.StatusConflict, typeRoleNotAssignable, "Role Not Assignable", "")
```

Create `internal/identity/adapters/httpapi/admin.go`:

```go
package httpapi

import (
	"net/http"

	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/httpserver"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
	"github.com/santoshkc2200/ioe-backend/internal/platform/problem"
)

type usersResponse struct {
	Users []userResponse `json:"users"`
}

type setRoleRequest struct {
	Role string `json:"role"`
}

func (h *Handler) searchUsers(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.PrincipalFrom(r.Context()) // RequireAuth guarantees presence
	users, err := h.admin.SearchUsers(r.Context(), p, r.URL.Query().Get("email"))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	out := usersResponse{Users: make([]userResponse, 0, len(users))}
	for _, u := range users {
		out.Users = append(out.Users, toUserResponse(u))
	}
	httpserver.WriteJSON(w, http.StatusOK, out)
}

func (h *Handler) setRole(w http.ResponseWriter, r *http.Request) {
	userID, err := id.Parse(r.PathValue("userID"))
	if err != nil {
		problem.Write(w, r, http.StatusNotFound, problem.TypeNotFound, "Not Found", "")
		return
	}
	var req setRoleRequest
	if !httpserver.DecodeJSON(w, r, &req) {
		return
	}
	p, _ := auth.PrincipalFrom(r.Context()) // RequireAuth guarantees presence
	u, err := h.admin.SetRole(r.Context(), p, userID, auth.Role(req.Role))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toUserResponse(u))
}
```

- [ ] **Step 4: Wire it in `cmd/api`**

In `cmd/api/app.go`, in `buildApp`, replace

```go
	identity := identityapp.NewService(identitypg.NewTxRunner(pool), googleVerifier, tokens, ids, clk, cfg.BootstrapRootAdminEmails)
```

with

```go
	identityTx := identitypg.NewTxRunner(pool)
	identity := identityapp.NewService(identityTx, googleVerifier, tokens, ids, clk, cfg.BootstrapRootAdminEmails)
	identityAdmin := identityapp.NewAdminService(identityTx, clk)
```

change `registerIdentity(router, identity, tokens, cfg, logger)` to `registerIdentity(router, identity, identityAdmin, tokens, cfg, logger)`, and change `registerIdentity` to:

```go
func registerIdentity(r *httpserver.Router, svc *identityapp.Service, admin *identityapp.AdminService, tokens *jwt.Tokens, cfg config.Config, logger *slog.Logger) (*httpapi.Handler, error) {
	h, err := httpapi.New(svc, admin, tokens, httpapi.Config{
```

(the rest of the function is unchanged).

- [ ] **Step 5: Run tests and lint**

Run: `go build ./... && go test -race ./internal/identity/... ./cmd/... && golangci-lint run ./...`
Expected: PASS, no lint issues.

- [ ] **Step 6: Commit**

```bash
git add internal/identity/adapters/httpapi cmd/api/app.go
git commit -m "feat(identity): expose role management over HTTP"
```

---

### Task 5: End-to-end test, OpenAPI, README, and gates

**Files:**
- Modify: `cmd/api/e2e_integration_test.go`
- Modify: `api/openapi.yaml`
- Modify: `README.md`

**Interfaces:**
- Consumes: the two routes (Task 4), `buildApp`, `client.do`, `refreshCookie`, `signingKeyPEM`, `appOrigin`, `googletest.NewIssuer`, `googletest.Claims`, `pgtest.New`.
- Produces: nothing new for code; the documented contract.

- [ ] **Step 1: Extract the shared test config**

In `cmd/api/e2e_integration_test.go`, add:

```go
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
```

and in `TestEndToEnd` replace the `cfg := config.Config{...}` literal with:

```go
	cfg := baseConfig(t, google)
	cfg.NotificationServiceBaseURL = notify.URL
	cfg.NotificationServiceSendAPIKey = notifyKey
```

Run: `go test -race -tags integration -run TestEndToEnd ./cmd/api/`
Expected: PASS (refactor only).

- [ ] **Step 2: Write the end-to-end test**

Append to `cmd/api/e2e_integration_test.go`:

```go
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
```

- [ ] **Step 3: Run the end-to-end tests**

Run: `go test -race -tags integration -run 'TestEndToEnd|TestRoleManagementEndToEnd' ./cmd/api/`
Expected: PASS. If the outbox count query returns 0, inspect one row with `SELECT payload FROM platform.outbox_messages` in a debug print and match the existing `courseauthoring.course.published` query shape used by `TestEndToEnd`; do not loosen the assertion.

- [ ] **Step 4: Document the API**

In `api/openapi.yaml`, insert after the `/v1/me` path (before `/v1/courses:`):

```yaml
  /v1/admin/users:
    get:
      summary: Search users by email prefix
      description: >
        Root admins only. Case-insensitive prefix match on email; `%`, `_` and `\` match
        literally. Returns at most 20 users ordered by email.
      security:
        - bearer: []
      parameters:
        - name: email
          in: query
          required: true
          description: Email prefix; at least 3 characters after trimming whitespace.
          schema: { type: string, minLength: 3 }
      responses:
        "200":
          description: Matching users
          content:
            application/json:
              schema: { $ref: "#/components/schemas/UserList" }
        "400": { $ref: "#/components/responses/Problem" }
        "401": { $ref: "#/components/responses/Problem" }
        "403": { $ref: "#/components/responses/Problem" }
        "405": { $ref: "#/components/responses/MethodNotAllowed" }
        "500": { $ref: "#/components/responses/InternalError" }
  /v1/admin/users/{userID}/role:
    put:
      summary: Set a user's role
      description: >
        Root admins only. Assigns `student` or `instructor`; a root admin's role cannot be
        changed (409 `role_not_assignable`). Setting the current role is a no-op. The user
        receives the new role in the access token issued at their next refresh.
      security:
        - bearer: []
      parameters:
        - $ref: "#/components/parameters/UserID"
      requestBody:
        required: true
        content:
          application/json:
            schema: { $ref: "#/components/schemas/SetRoleRequest" }
      responses:
        "200":
          description: The user with the resulting role
          content:
            application/json:
              schema: { $ref: "#/components/schemas/User" }
        "400": { $ref: "#/components/responses/Problem" }
        "401": { $ref: "#/components/responses/Problem" }
        "403": { $ref: "#/components/responses/Problem" }
        "404": { $ref: "#/components/responses/Problem" }
        "409": { $ref: "#/components/responses/Problem" }
        "413": { $ref: "#/components/responses/Problem" }
        "415": { $ref: "#/components/responses/Problem" }
        "405": { $ref: "#/components/responses/MethodNotAllowed" }
        "500": { $ref: "#/components/responses/InternalError" }
```

Under `components.parameters`, after `OwnerID`, add:

```yaml
    UserID:
      name: userID
      in: path
      required: true
      schema: { $ref: "#/components/schemas/ID" }
```

Under `components.schemas`, after `User`, add:

```yaml
    UserList:
      type: object
      required: [users]
      properties:
        users:
          type: array
          items: { $ref: "#/components/schemas/User" }
    SetRoleRequest:
      type: object
      required: [role]
      properties:
        role: { type: string, enum: [student, instructor] }
```

Run: `ruby -ryaml -e 'YAML.load_file("api/openapi.yaml"); puts "ok"'`
Expected: `ok`.

- [ ] **Step 5: Document the operator workflow**

In `README.md`, insert before `## Notifications`:

```markdown
## Roles

New users are students. Accounts whose email is in `BOOTSTRAP_ROOT_ADMIN_EMAILS` become root
admins at sign-in. A root admin finds a user with `GET /v1/admin/users?email=<prefix>` and
makes them an instructor (or a student again) with `PUT /v1/admin/users/{id}/role`. The user
gets the new role at their next token refresh, within 15 minutes; a demoted instructor keeps
managing the courses they own.
```

- [ ] **Step 6: Run the required gates**

Run each and confirm the stated result before moving on:

```sh
make check            # expect: tidy, lint, sqlc diff, tests, govulncheck, gitleaks all pass
make test-integration # expect: PASS; requires Docker; confirm the integration packages actually ran
docker compose config > /dev/null
make docker-build
git diff --check
```

- [ ] **Step 7: Commit**

```bash
git add cmd/api/e2e_integration_test.go api/openapi.yaml README.md
git commit -m "docs(api): document role management endpoints"
```
