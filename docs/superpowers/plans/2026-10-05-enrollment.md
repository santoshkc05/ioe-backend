# Enrollment Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an `enrollment` bounded context so students self-enroll in free published courses, managers enroll and cancel anyone, and courseauthoring's lecture gate reads real enrollments instead of the deny-all stub.

**Architecture:** New context `internal/enrollment/{domain,app,adapters/postgres,adapters/httpapi}` on its own `enrollment` schema. Enrollment reads course facts synchronously through a `CourseCatalog` port that `cmd/api` backs with a new `courseauthoring` `CourseService.Facts`; courseauthoring reads enrollment through its existing `EnrollmentQuery` port, backed by enrollment's `AccessQuery`. The two app types are split so wiring has no cycle.

**Tech Stack:** Go 1.27, pgx/v5, sqlc, goose, Watermill SQL outbox (`internal/platform/outbox`), net/http `ServeMux` via `internal/platform/httpserver`.

**Spec:** `docs/superpowers/specs/2026-10-05-enrollment-design.md`

## Global Constraints

- A context never imports another context; only `cmd/api` wires them (`AGENTS.md`).
- Enrollment reads and writes only the `enrollment` schema; no foreign keys into other schemas.
- Outbox messages are written in the same transaction as the state change.
- Wire shape `{id, course_id, user_id, status, enrolled_at, canceled_at?}`; IDs are decimal strings; status `active` or `canceled` (one L).
- Cancel reason: trimmed, at most 500 runes.
- Roster `limit` default 50, range 1–200; `offset` ≥ 0.
- Event names: `enrollment.enrollment.activated`, `enrollment.enrollment.canceled`.
- Path IDs that fail to parse return 404.
- Run `make fmt` before `golangci-lint run`; plan snippets are not guaranteed gofmt-aligned.
- Conventional Commits.
- Gates: `make check`, `make test-integration` (must actually run), `docker compose config`, `make docker-build`, `git diff --check`.

## Review Focus

- `DELETE` with no body and no `Content-Type` (the frontend omits the body when there is no reason) must succeed, not 415/400 — pinned in Task 5 `TestCancelWithoutBody`.
- Two concurrent first enrollments for the same (course, user) must both succeed with one row and one activation event — pinned in Task 4 `TestConcurrentEnrollCreatesOneRow`.
- A canceled student re-enrolling must reuse the row, reset `enrolled_at`, clear the reason, and emit a new activation — pinned in Task 2 `TestEnrollReactivatesCanceled`.
- A non-manager probing an unpublished course (enroll, cancel someone else, roster) must get 404, never 403, so drafts stay hidden — pinned in Task 1 `TestAuthorize*` rows and Task 2 `TestCancelOtherUserHidesUnpublishedCourse`.
- A user listing with zero enrollments must return `[]`, not `null` — pinned in Task 5 `TestListByUserEmptyIsArray`.

---

## File Structure

| File | Responsibility |
|---|---|
| `internal/enrollment/domain/errors.go` | Domain sentinel errors |
| `internal/enrollment/domain/events.go` | `Event`, `EnrollmentActivated`, `EnrollmentCanceled` |
| `internal/enrollment/domain/enrollment.go` | `Enrollment` aggregate and lifecycle |
| `internal/enrollment/domain/access.go` | `CourseFacts` and authorization rules |
| `internal/enrollment/app/errors.go` | App sentinel errors |
| `internal/enrollment/app/ports.go` | `Repository`, `EventPublisher`, `Repos`, `TxRunner`, `CourseCatalog` |
| `internal/enrollment/app/access_query.go` | `AccessQuery` (repository-only read) |
| `internal/enrollment/app/service.go` | `Service` use cases |
| `internal/enrollment/adapters/postgres/postgres.go` | `TxRunner`, outbox publisher |
| `internal/enrollment/adapters/postgres/enrollments.go` | `Repository` implementation |
| `internal/enrollment/adapters/postgres/queries.sql` | sqlc queries |
| `internal/enrollment/adapters/httpapi/httpapi.go` | Handler, routes, error mapping |
| `internal/enrollment/adapters/httpapi/wire.go` | JSON wire types |
| `migrations/00004_enrollment.sql` | Schema |
| `internal/courseauthoring/app/course_service.go` | Add `CourseFacts` and `Facts` |
| `cmd/api/enrollment.go` | Cross-context adapters and `registerEnrollment` |
| `cmd/api/app.go` | Wiring order; drop `Deny` |

---

### Task 1: Enrollment domain and boundary rules

**Files:**
- Create: `internal/enrollment/domain/errors.go`, `events.go`, `enrollment.go`, `access.go`
- Test: `internal/enrollment/domain/enrollment_test.go`, `internal/enrollment/domain/access_test.go`
- Modify: `.golangci.yml`

**Interfaces:**
- Consumes: `auth.Principal`, `auth.RoleRootAdmin` (`internal/platform/auth`), `id.ID` (`internal/platform/id`).
- Produces:
  - `type Status string`; `StatusActive`, `StatusCanceled`; `const MaxReasonRunes = 500`.
  - `type Enrollment struct{ ID, CourseID, UserID id.ID; Status Status; CancelReason string; EnrolledAt, CanceledAt time.Time; Version int64 }`.
  - `func NewEnrollment(enrollmentID, courseID, userID id.ID, now time.Time) (Enrollment, Event)`.
  - `func (e *Enrollment) IsActive() bool`.
  - `func (e *Enrollment) Cancel(reason string, now time.Time) (Event, error)` — nil event when already canceled.
  - `func (e *Enrollment) Reactivate(now time.Time) Event` — nil when already active.
  - `type Event interface{ EventName() string }`; `EnrollmentActivated`, `EnrollmentCanceled`.
  - `type CourseFacts struct{ Published, Free bool; OwnerID id.ID }`; `func (c CourseFacts) IsManagedBy(p auth.Principal) bool`.
  - `func AuthorizeEnroll(p auth.Principal, c CourseFacts, userID id.ID) error`.
  - `func AuthorizeManage(p auth.Principal, c CourseFacts) error` — cancel someone else's enrollment, list roster.
  - `func AuthorizeListUser(p auth.Principal, userID id.ID) error`.
  - Errors: `ErrInvalidReason`, `ErrCourseHidden`, `ErrForbidden`, `ErrPaymentRequired`, `ErrCourseNotPublished`.

- [ ] **Step 1: Write the failing lifecycle test**

`internal/enrollment/domain/enrollment_test.go`:

```go
package domain_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/enrollment/domain"
)

var (
	t0 = time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	t1 = t0.Add(time.Hour)
	t2 = t1.Add(time.Hour)
)

func TestNewEnrollment(t *testing.T) {
	e, ev := domain.NewEnrollment(1, 10, 20, t0)
	if e.ID != 1 || e.CourseID != 10 || e.UserID != 20 || !e.IsActive() || !e.EnrolledAt.Equal(t0) || !e.CanceledAt.IsZero() {
		t.Fatalf("enrollment = %+v", e)
	}
	want := domain.EnrollmentActivated{EnrollmentID: 1, CourseID: 10, UserID: 20, OccurredAt: t0}
	if ev != want || ev.EventName() != "enrollment.enrollment.activated" {
		t.Fatalf("event = %#v", ev)
	}
}

func TestCancel(t *testing.T) {
	e, _ := domain.NewEnrollment(1, 10, 20, t0)
	ev, err := e.Cancel("  moving on  ", t1)
	if err != nil {
		t.Fatal(err)
	}
	if e.Status != domain.StatusCanceled || e.CancelReason != "moving on" || !e.CanceledAt.Equal(t1) {
		t.Fatalf("enrollment = %+v", e)
	}
	want := domain.EnrollmentCanceled{EnrollmentID: 1, CourseID: 10, UserID: 20, Reason: "moving on", OccurredAt: t1}
	if ev != want || ev.EventName() != "enrollment.enrollment.canceled" {
		t.Fatalf("event = %#v", ev)
	}

	ev, err = e.Cancel("again", t2)
	if err != nil || ev != nil || e.CancelReason != "moving on" || !e.CanceledAt.Equal(t1) {
		t.Fatalf("second cancel: ev=%v err=%v e=%+v", ev, err, e)
	}
}

func TestCancelReasonLimit(t *testing.T) {
	e, _ := domain.NewEnrollment(1, 10, 20, t0)
	if _, err := e.Cancel(strings.Repeat("é", domain.MaxReasonRunes+1), t1); !errors.Is(err, domain.ErrInvalidReason) {
		t.Fatalf("err = %v", err)
	}
	if !e.IsActive() {
		t.Fatal("rejected cancel changed state")
	}
	if _, err := e.Cancel(strings.Repeat("é", domain.MaxReasonRunes), t1); err != nil {
		t.Fatalf("500 runes rejected: %v", err)
	}
}

func TestReactivate(t *testing.T) {
	e, _ := domain.NewEnrollment(1, 10, 20, t0)
	if ev := e.Reactivate(t1); ev != nil {
		t.Fatalf("reactivating active emitted %v", ev)
	}
	_, _ = e.Cancel("r", t1)
	ev := e.Reactivate(t2)
	if !e.IsActive() || e.CancelReason != "" || !e.CanceledAt.IsZero() || !e.EnrolledAt.Equal(t2) {
		t.Fatalf("enrollment = %+v", e)
	}
	if ev != (domain.EnrollmentActivated{EnrollmentID: 1, CourseID: 10, UserID: 20, OccurredAt: t2}) {
		t.Fatalf("event = %#v", ev)
	}
}
```

- [ ] **Step 2: Write the failing access test**

`internal/enrollment/domain/access_test.go`:

```go
package domain_test

import (
	"errors"
	"testing"

	"github.com/santoshkc2200/ioe-backend/internal/enrollment/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
)

var (
	owner   = auth.Principal{UserID: 100, Role: auth.RoleInstructor}
	admin   = auth.Principal{UserID: 1, Role: auth.RoleRootAdmin}
	student = auth.Principal{UserID: 200, Role: auth.RoleStudent}
	other   = auth.Principal{UserID: 300, Role: auth.RoleInstructor}

	freePub  = domain.CourseFacts{Published: true, Free: true, OwnerID: 100}
	paidPub  = domain.CourseFacts{Published: true, Free: false, OwnerID: 100}
	draft    = domain.CourseFacts{Published: false, Free: true, OwnerID: 100}
)

func TestAuthorizeEnroll(t *testing.T) {
	cases := []struct {
		name   string
		p      auth.Principal
		c      domain.CourseFacts
		target auth.Principal
		want   error
	}{
		{"self free published", student, freePub, student, nil},
		{"self paid published", student, paidPub, student, domain.ErrPaymentRequired},
		{"self draft", student, draft, student, domain.ErrCourseHidden},
		{"other user as student", student, freePub, other, domain.ErrForbidden},
		{"other user on draft as non-manager", other, draft, student, domain.ErrCourseHidden},
		{"owner enrolls student in paid", owner, paidPub, student, nil},
		{"admin enrolls student in paid", admin, paidPub, student, nil},
		{"owner enrolls self in paid", owner, paidPub, owner, nil},
		{"owner on draft", owner, draft, student, domain.ErrCourseNotPublished},
		{"admin on draft", admin, draft, student, domain.ErrCourseNotPublished},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := domain.AuthorizeEnroll(tc.p, tc.c, tc.target.UserID); !errors.Is(err, tc.want) || (tc.want == nil && err != nil) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestAuthorizeManage(t *testing.T) {
	cases := []struct {
		name string
		p    auth.Principal
		c    domain.CourseFacts
		want error
	}{
		{"owner", owner, paidPub, nil},
		{"owner draft", owner, draft, nil},
		{"admin", admin, draft, nil},
		{"stranger published", other, paidPub, domain.ErrForbidden},
		{"student published", student, freePub, domain.ErrForbidden},
		{"stranger draft", other, draft, domain.ErrCourseHidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := domain.AuthorizeManage(tc.p, tc.c); !errors.Is(err, tc.want) || (tc.want == nil && err != nil) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestAuthorizeListUser(t *testing.T) {
	if err := domain.AuthorizeListUser(student, student.UserID); err != nil {
		t.Fatalf("self: %v", err)
	}
	if err := domain.AuthorizeListUser(admin, student.UserID); err != nil {
		t.Fatalf("admin: %v", err)
	}
	if err := domain.AuthorizeListUser(owner, student.UserID); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("instructor: %v", err)
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/enrollment/domain/`
Expected: FAIL — package `domain` has no non-test Go files / undefined symbols.

- [ ] **Step 4: Implement the domain**

`internal/enrollment/domain/errors.go`:

```go
// Package domain holds the enrollment model.
package domain

import "errors"

var (
	ErrInvalidReason      = errors.New("reason must be at most 500 characters")
	ErrCourseHidden       = errors.New("course not found")
	ErrForbidden          = errors.New("forbidden")
	ErrPaymentRequired    = errors.New("payment required")
	ErrCourseNotPublished = errors.New("course is not published")
)
```

`internal/enrollment/domain/events.go`:

```go
package domain

import (
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

// Event is a domain event written to the outbox under its EventName.
type Event interface {
	EventName() string
}

// EnrollmentActivated is emitted when an enrollment is created or reactivated.
type EnrollmentActivated struct {
	EnrollmentID id.ID     `json:"enrollment_id"`
	CourseID     id.ID     `json:"course_id"`
	UserID       id.ID     `json:"user_id"`
	OccurredAt   time.Time `json:"occurred_at"`
}

func (EnrollmentActivated) EventName() string { return "enrollment.enrollment.activated" }

// EnrollmentCanceled is emitted when an active enrollment is canceled.
type EnrollmentCanceled struct {
	EnrollmentID id.ID     `json:"enrollment_id"`
	CourseID     id.ID     `json:"course_id"`
	UserID       id.ID     `json:"user_id"`
	Reason       string    `json:"reason"`
	OccurredAt   time.Time `json:"occurred_at"`
}

func (EnrollmentCanceled) EventName() string { return "enrollment.enrollment.canceled" }
```

`internal/enrollment/domain/enrollment.go`:

```go
package domain

import (
	"strings"
	"time"
	"unicode/utf8"

	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

type Status string

const (
	StatusActive   Status = "active"
	StatusCanceled Status = "canceled"
)

// MaxReasonRunes bounds a cancellation reason after trimming.
const MaxReasonRunes = 500

// Enrollment is one user's access to one course. There is at most one per (course, user);
// re-enrolling reactivates it.
type Enrollment struct {
	ID           id.ID
	CourseID     id.ID
	UserID       id.ID
	Status       Status
	CancelReason string
	EnrolledAt   time.Time
	CanceledAt   time.Time // zero unless canceled
	Version      int64
}

func NewEnrollment(enrollmentID, courseID, userID id.ID, now time.Time) (Enrollment, Event) {
	e := Enrollment{ID: enrollmentID, CourseID: courseID, UserID: userID, Status: StatusActive, EnrolledAt: now}
	return e, e.activated(now)
}

func (e *Enrollment) IsActive() bool { return e.Status == StatusActive }

// Cancel ends an active enrollment. Canceling a canceled enrollment changes nothing and
// returns no event.
func (e *Enrollment) Cancel(reason string, now time.Time) (Event, error) {
	reason = strings.TrimSpace(reason)
	if utf8.RuneCountInString(reason) > MaxReasonRunes {
		return nil, ErrInvalidReason
	}
	if !e.IsActive() {
		return nil, nil
	}
	e.Status, e.CancelReason, e.CanceledAt = StatusCanceled, reason, now
	return EnrollmentCanceled{EnrollmentID: e.ID, CourseID: e.CourseID, UserID: e.UserID, Reason: reason, OccurredAt: now}, nil
}

// Reactivate restores a canceled enrollment. Reactivating an active enrollment changes
// nothing and returns no event.
func (e *Enrollment) Reactivate(now time.Time) Event {
	if e.IsActive() {
		return nil
	}
	e.Status, e.CancelReason, e.CanceledAt, e.EnrolledAt = StatusActive, "", time.Time{}, now
	return e.activated(now)
}

func (e *Enrollment) activated(now time.Time) Event {
	return EnrollmentActivated{EnrollmentID: e.ID, CourseID: e.CourseID, UserID: e.UserID, OccurredAt: now}
}
```

`internal/enrollment/domain/access.go`:

```go
package domain

import (
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

// CourseFacts is what enrollment knows about a course, supplied by the application layer.
type CourseFacts struct {
	Published bool
	Free      bool
	OwnerID   id.ID
}

// IsManagedBy reports whether p owns the course or is a root admin.
func (c CourseFacts) IsManagedBy(p auth.Principal) bool {
	return p.Role == auth.RoleRootAdmin || p.UserID == c.OwnerID
}

// AuthorizeEnroll decides whether p may enroll userID. Non-managers never learn that an
// unpublished course exists.
func AuthorizeEnroll(p auth.Principal, c CourseFacts, userID id.ID) error {
	if c.IsManagedBy(p) {
		if !c.Published {
			return ErrCourseNotPublished
		}
		return nil
	}
	if !c.Published {
		return ErrCourseHidden
	}
	if p.UserID != userID {
		return ErrForbidden
	}
	if !c.Free {
		return ErrPaymentRequired
	}
	return nil
}

// AuthorizeManage decides whether p may cancel another user's enrollment or list the roster.
func AuthorizeManage(p auth.Principal, c CourseFacts) error {
	if c.IsManagedBy(p) {
		return nil
	}
	if !c.Published {
		return ErrCourseHidden
	}
	return ErrForbidden
}

// AuthorizeListUser decides whether p may list userID's enrollments.
func AuthorizeListUser(p auth.Principal, userID id.ID) error {
	if p.UserID == userID || p.Role == auth.RoleRootAdmin {
		return nil
	}
	return ErrForbidden
}
```

- [ ] **Step 5: Add depguard rules**

In `.golangci.yml`, under `platform-independent-of-contexts.deny` add:

```yaml
            - pkg: github.com/santoshkc2200/ioe-backend/internal/enrollment
              desc: platform must not depend on bounded contexts
```

Add to each of `identity-independent.deny`, `notification-independent.deny`, `courseauthoring-independent.deny`:

```yaml
            - pkg: github.com/santoshkc2200/ioe-backend/internal/enrollment
              desc: bounded contexts must not import each other
```

Add these rules after `courseauthoring-independent`:

```yaml
        enrollment-domain:
          list-mode: strict
          files:
            - "**/internal/enrollment/domain/**"
            - "!$test"
          allow:
            - $gostd
            - github.com/santoshkc2200/ioe-backend/internal/platform/auth
            - github.com/santoshkc2200/ioe-backend/internal/platform/id
        enrollment-app:
          list-mode: strict
          files:
            - "**/internal/enrollment/app/**"
            - "!$test"
          allow:
            - $gostd
            - github.com/santoshkc2200/ioe-backend/internal/enrollment/domain
            - github.com/santoshkc2200/ioe-backend/internal/platform/auth
            - github.com/santoshkc2200/ioe-backend/internal/platform/clock
            - github.com/santoshkc2200/ioe-backend/internal/platform/id
        enrollment-independent:
          list-mode: lax
          files:
            - "**/internal/enrollment/**"
          deny:
            - pkg: github.com/santoshkc2200/ioe-backend/internal/identity
              desc: bounded contexts must not import each other
            - pkg: github.com/santoshkc2200/ioe-backend/internal/notification
              desc: bounded contexts must not import each other
            - pkg: github.com/santoshkc2200/ioe-backend/internal/courseauthoring
              desc: bounded contexts must not import each other
```

- [ ] **Step 6: Run tests and lint**

Run: `go test -race ./internal/enrollment/domain/ && golangci-lint run ./internal/enrollment/...`
Expected: PASS, no lint findings.

- [ ] **Step 7: Commit**

```bash
git add internal/enrollment/domain .golangci.yml
git commit -m "feat(enrollment): add enrollment domain and access rules"
```

---

### Task 2: Enrollment application layer

**Files:**
- Create: `internal/enrollment/app/errors.go`, `ports.go`, `access_query.go`, `service.go`
- Test: `internal/enrollment/app/fakes_test.go`, `internal/enrollment/app/service_test.go`

**Interfaces:**
- Consumes: everything Task 1 produces; `clock.Clock` (`Now() time.Time`), `*id.Generator` (`New() id.ID`).
- Produces:
  - Errors `ErrNotFound`, `ErrDuplicate`, `ErrConcurrentModification`, `ErrInvalidInput`.
  - `Repository` with `FindByCourseAndUser(ctx, courseID, userID id.ID) (domain.Enrollment, bool, error)`, `ListActiveByCourse(ctx, courseID id.ID, limit, offset int) ([]domain.Enrollment, int, error)`, `ListActiveByUser(ctx, userID id.ID) ([]domain.Enrollment, error)`, `Insert(ctx, *domain.Enrollment) error`, `Update(ctx, *domain.Enrollment) error`.
  - `EventPublisher.Publish(ctx, ...domain.Event) error`; `Repos{Enrollments Repository; Events EventPublisher}`; `TxRunner.RunInTx(ctx, func(Repos) error) error`.
  - `CourseCatalog.CourseFacts(ctx, courseID id.ID) (domain.CourseFacts, error)` returning `ErrNotFound` for a missing course.
  - `const DefaultPageLimit = 50`, `const MaxPageLimit = 200`.
  - `NewAccessQuery(tx TxRunner) *AccessQuery`; `(*AccessQuery).IsActivelyEnrolled(ctx, courseID, userID id.ID) (bool, error)`.
  - `NewService(tx TxRunner, courses CourseCatalog, ids *id.Generator, c clock.Clock) *Service` with `Enroll(ctx, p auth.Principal, courseID, userID id.ID) (domain.Enrollment, bool, error)`, `Cancel(ctx, p, courseID, userID id.ID, reason string) (domain.Enrollment, error)`, `ListByCourse(ctx, p, courseID id.ID, limit, offset int) ([]domain.Enrollment, int, error)`, `ListByUser(ctx, p, userID id.ID) ([]domain.Enrollment, error)`.

- [ ] **Step 1: Write the in-memory fakes**

`internal/enrollment/app/fakes_test.go`:

```go
package app_test

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/enrollment/app"
	"github.com/santoshkc2200/ioe-backend/internal/enrollment/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time { return c.now }

type key [2]id.ID // course, user

// memStore is a single-goroutine fake; RunInTx copies state and commits only on success.
type memStore struct {
	mu        sync.Mutex
	rows      map[key]domain.Enrollment
	published []domain.Event
	// race, when set, is committed by the next Insert, which then fails with ErrDuplicate,
	// simulating a concurrent first enrollment that won the unique constraint.
	race *domain.Enrollment
}

func newMemStore() *memStore { return &memStore{rows: map[key]domain.Enrollment{}} }

func (m *memStore) RunInTx(_ context.Context, fn func(app.Repos) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	tx := &memTx{store: m, rows: make(map[key]domain.Enrollment, len(m.rows))}
	for k, v := range m.rows {
		tx.rows[k] = v
	}
	if err := fn(app.Repos{Enrollments: tx, Events: tx}); err != nil {
		return err
	}
	m.rows = tx.rows
	m.published = append(m.published, tx.events...)
	return nil
}

type memTx struct {
	store  *memStore
	rows   map[key]domain.Enrollment
	events []domain.Event
}

func (t *memTx) FindByCourseAndUser(_ context.Context, courseID, userID id.ID) (domain.Enrollment, bool, error) {
	e, ok := t.rows[key{courseID, userID}]
	return e, ok, nil
}

func (t *memTx) active(match func(domain.Enrollment) bool) []domain.Enrollment {
	var out []domain.Enrollment
	for _, e := range t.rows {
		if e.IsActive() && match(e) {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (t *memTx) ListActiveByCourse(_ context.Context, courseID id.ID, limit, offset int) ([]domain.Enrollment, int, error) {
	all := t.active(func(e domain.Enrollment) bool { return e.CourseID == courseID })
	if offset > len(all) {
		offset = len(all)
	}
	end := min(offset+limit, len(all))
	return all[offset:end], len(all), nil
}

func (t *memTx) ListActiveByUser(_ context.Context, userID id.ID) ([]domain.Enrollment, error) {
	return t.active(func(e domain.Enrollment) bool { return e.UserID == userID }), nil
}

func (t *memTx) Insert(_ context.Context, e *domain.Enrollment) error {
	k := key{e.CourseID, e.UserID}
	if r := t.store.race; r != nil {
		t.store.race = nil
		t.store.rows[key{r.CourseID, r.UserID}] = *r
		return app.ErrDuplicate
	}
	if _, ok := t.rows[k]; ok {
		return app.ErrDuplicate
	}
	e.Version = 1
	t.rows[k] = *e
	return nil
}

func (t *memTx) Update(_ context.Context, e *domain.Enrollment) error {
	k := key{e.CourseID, e.UserID}
	cur, ok := t.rows[k]
	if !ok || cur.Version != e.Version {
		return app.ErrConcurrentModification
	}
	e.Version++
	t.rows[k] = *e
	return nil
}

func (t *memTx) Publish(_ context.Context, evs ...domain.Event) error {
	t.events = append(t.events, evs...)
	return nil
}

type catalog map[id.ID]domain.CourseFacts

func (c catalog) CourseFacts(_ context.Context, courseID id.ID) (domain.CourseFacts, error) {
	f, ok := c[courseID]
	if !ok {
		return domain.CourseFacts{}, app.ErrNotFound
	}
	return f, nil
}
```

- [ ] **Step 2: Write the failing service tests**

`internal/enrollment/app/service_test.go`:

```go
package app_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/enrollment/app"
	"github.com/santoshkc2200/ioe-backend/internal/enrollment/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

var (
	ctx     = context.Background()
	t0      = time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	owner   = auth.Principal{UserID: 100, Role: auth.RoleInstructor}
	admin   = auth.Principal{UserID: 1, Role: auth.RoleRootAdmin}
	student = auth.Principal{UserID: 200, Role: auth.RoleStudent}
	other   = auth.Principal{UserID: 300, Role: auth.RoleStudent}
)

const (
	freeCourse  id.ID = 10
	paidCourse  id.ID = 11
	draftCourse id.ID = 12
)

type fixture struct {
	svc   *app.Service
	store *memStore
	clock *fixedClock
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	ids, err := id.NewGenerator(0)
	if err != nil {
		t.Fatal(err)
	}
	store := newMemStore()
	clk := &fixedClock{now: t0}
	courses := catalog{
		freeCourse:  {Published: true, Free: true, OwnerID: owner.UserID},
		paidCourse:  {Published: true, Free: false, OwnerID: owner.UserID},
		draftCourse: {Published: false, Free: true, OwnerID: owner.UserID},
	}
	return fixture{svc: app.NewService(store, courses, ids, clk), store: store, clock: clk}
}

func (f fixture) now() time.Time { return f.clock.now }

func TestEnrollSelfInFreeCourse(t *testing.T) {
	f := newFixture(t)
	e, activated, err := f.svc.Enroll(ctx, student, freeCourse, student.UserID)
	if err != nil || !activated || !e.IsActive() || e.Version != 1 || e.CourseID != freeCourse || e.UserID != student.UserID {
		t.Fatalf("e=%+v activated=%v err=%v", e, activated, err)
	}
	if len(f.store.published) != 1 {
		t.Fatalf("events = %v", f.store.published)
	}
	if _, ok := f.store.published[0].(domain.EnrollmentActivated); !ok {
		t.Fatalf("event = %#v", f.store.published[0])
	}
}

func TestEnrollAlreadyActiveIsIdempotent(t *testing.T) {
	f := newFixture(t)
	first, _, _ := f.svc.Enroll(ctx, student, freeCourse, student.UserID)
	again, activated, err := f.svc.Enroll(ctx, student, freeCourse, student.UserID)
	if err != nil || activated || again.ID != first.ID || again.Version != first.Version {
		t.Fatalf("again=%+v activated=%v err=%v", again, activated, err)
	}
	if len(f.store.published) != 1 {
		t.Fatalf("events = %v", f.store.published)
	}
}

func TestEnrollReactivatesCanceled(t *testing.T) {
	f := newFixture(t)
	first, _, _ := f.svc.Enroll(ctx, student, freeCourse, student.UserID)
	if _, err := f.svc.Cancel(ctx, student, freeCourse, student.UserID, "busy"); err != nil {
		t.Fatal(err)
	}
	f.clock.now = t0.Add(time.Hour)
	e, activated, err := f.svc.Enroll(ctx, student, freeCourse, student.UserID)
	if err != nil || !activated || e.ID != first.ID || !e.IsActive() || e.CancelReason != "" || !e.CanceledAt.IsZero() || !e.EnrolledAt.Equal(f.now()) || e.Version != 3 {
		t.Fatalf("e=%+v activated=%v err=%v", e, activated, err)
	}
	if len(f.store.published) != 3 {
		t.Fatalf("events = %v", f.store.published)
	}
}

func TestEnrollRetriesAfterConcurrentInsert(t *testing.T) {
	f := newFixture(t)
	winner, _ := domain.NewEnrollment(999, freeCourse, student.UserID, t0)
	winner.Version = 1
	f.store.race = &winner
	e, activated, err := f.svc.Enroll(ctx, student, freeCourse, student.UserID)
	if err != nil || activated || e.ID != 999 {
		t.Fatalf("e=%+v activated=%v err=%v", e, activated, err)
	}
	if len(f.store.published) != 0 {
		t.Fatalf("events = %v", f.store.published)
	}
}

func TestEnrollAuthorization(t *testing.T) {
	f := newFixture(t)
	cases := []struct {
		name   string
		p      auth.Principal
		course id.ID
		user   id.ID
		want   error
	}{
		{"paid self", student, paidCourse, student.UserID, domain.ErrPaymentRequired},
		{"draft self", student, draftCourse, student.UserID, domain.ErrCourseHidden},
		{"missing course", student, 404, student.UserID, app.ErrNotFound},
		{"enroll someone else", student, freeCourse, other.UserID, domain.ErrForbidden},
		{"manager on draft", owner, draftCourse, student.UserID, domain.ErrCourseNotPublished},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := f.svc.Enroll(ctx, tc.p, tc.course, tc.user); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
	if _, activated, err := f.svc.Enroll(ctx, owner, paidCourse, student.UserID); err != nil || !activated {
		t.Fatalf("owner comp: activated=%v err=%v", activated, err)
	}
	if _, activated, err := f.svc.Enroll(ctx, admin, paidCourse, other.UserID); err != nil || !activated {
		t.Fatalf("admin comp: activated=%v err=%v", activated, err)
	}
}

func TestCancel(t *testing.T) {
	f := newFixture(t)
	_, _, _ = f.svc.Enroll(ctx, student, freeCourse, student.UserID)
	_, _, _ = f.svc.Enroll(ctx, other, freeCourse, other.UserID)

	if _, err := f.svc.Cancel(ctx, other, freeCourse, student.UserID, ""); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("stranger err = %v", err)
	}
	e, err := f.svc.Cancel(ctx, student, freeCourse, student.UserID, "  done ")
	if err != nil || e.Status != domain.StatusCanceled || e.CancelReason != "done" {
		t.Fatalf("self cancel e=%+v err=%v", e, err)
	}
	again, err := f.svc.Cancel(ctx, student, freeCourse, student.UserID, "x")
	if err != nil || again.Version != e.Version || again.CancelReason != "done" {
		t.Fatalf("repeat cancel e=%+v err=%v", again, err)
	}
	if _, err := f.svc.Cancel(ctx, owner, freeCourse, other.UserID, ""); err != nil {
		t.Fatalf("owner cancel err = %v", err)
	}
	if len(f.store.published) != 4 { // 2 activations + 2 cancellations
		t.Fatalf("events = %v", f.store.published)
	}
	if _, err := f.svc.Cancel(ctx, student, paidCourse, student.UserID, ""); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("no enrollment err = %v", err)
	}
	if _, err := f.svc.Cancel(ctx, owner, 404, student.UserID, ""); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("missing course err = %v", err)
	}
}

func TestCancelRejectsLongReason(t *testing.T) {
	f := newFixture(t)
	_, _, _ = f.svc.Enroll(ctx, student, freeCourse, student.UserID)
	reason := strings.Repeat("x", domain.MaxReasonRunes+1)
	_, err := f.svc.Cancel(ctx, student, freeCourse, student.UserID, reason)
	if !errors.Is(err, app.ErrInvalidInput) || !errors.Is(err, domain.ErrInvalidReason) {
		t.Fatalf("err = %v", err)
	}
}

func TestCancelOtherUserHidesUnpublishedCourse(t *testing.T) {
	f := newFixture(t)
	if _, err := f.svc.Cancel(ctx, other, draftCourse, student.UserID, ""); !errors.Is(err, domain.ErrCourseHidden) {
		t.Fatalf("err = %v", err)
	}
}

func TestListByCourse(t *testing.T) {
	f := newFixture(t)
	for _, uid := range []id.ID{201, 202, 203} {
		if _, _, err := f.svc.Enroll(ctx, owner, freeCourse, uid); err != nil {
			t.Fatal(err)
		}
	}
	_, _ = f.svc.Cancel(ctx, owner, freeCourse, 202, "")

	page, total, err := f.svc.ListByCourse(ctx, owner, freeCourse, 1, 1)
	if err != nil || total != 2 || len(page) != 1 || page[0].UserID != 203 {
		t.Fatalf("page=%+v total=%d err=%v", page, total, err)
	}
	if _, _, err := f.svc.ListByCourse(ctx, student, freeCourse, 50, 0); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("student err = %v", err)
	}
	if _, _, err := f.svc.ListByCourse(ctx, student, draftCourse, 50, 0); !errors.Is(err, domain.ErrCourseHidden) {
		t.Fatalf("draft err = %v", err)
	}
	for _, bad := range [][2]int{{0, 0}, {app.MaxPageLimit + 1, 0}, {10, -1}} {
		if _, _, err := f.svc.ListByCourse(ctx, owner, freeCourse, bad[0], bad[1]); !errors.Is(err, app.ErrInvalidInput) {
			t.Fatalf("limit=%d offset=%d err = %v", bad[0], bad[1], err)
		}
	}
}

func TestListByUser(t *testing.T) {
	f := newFixture(t)
	_, _, _ = f.svc.Enroll(ctx, student, freeCourse, student.UserID)
	_, _, _ = f.svc.Enroll(ctx, owner, paidCourse, student.UserID)
	got, err := f.svc.ListByUser(ctx, student, student.UserID)
	if err != nil || len(got) != 2 {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	if _, err := f.svc.ListByUser(ctx, admin, student.UserID); err != nil {
		t.Fatalf("admin err = %v", err)
	}
	if _, err := f.svc.ListByUser(ctx, owner, student.UserID); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("instructor err = %v", err)
	}
}

func TestAccessQuery(t *testing.T) {
	f := newFixture(t)
	q := app.NewAccessQuery(f.store)
	if ok, err := q.IsActivelyEnrolled(ctx, freeCourse, student.UserID); err != nil || ok {
		t.Fatalf("before: %v %v", ok, err)
	}
	_, _, _ = f.svc.Enroll(ctx, student, freeCourse, student.UserID)
	if ok, err := q.IsActivelyEnrolled(ctx, freeCourse, student.UserID); err != nil || !ok {
		t.Fatalf("enrolled: %v %v", ok, err)
	}
	_, _ = f.svc.Cancel(ctx, student, freeCourse, student.UserID, "")
	if ok, err := q.IsActivelyEnrolled(ctx, freeCourse, student.UserID); err != nil || ok {
		t.Fatalf("canceled: %v %v", ok, err)
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/enrollment/app/`
Expected: FAIL — undefined `app.NewService`, `app.ErrDuplicate`, etc.

- [ ] **Step 4: Implement errors and ports**

`internal/enrollment/app/errors.go`:

```go
package app

import "errors"

var (
	ErrNotFound               = errors.New("not found")
	ErrDuplicate              = errors.New("enrollment already exists")
	ErrConcurrentModification = errors.New("concurrent modification")
	ErrInvalidInput           = errors.New("invalid input")
)
```

`internal/enrollment/app/ports.go`:

```go
// Package app contains the enrollment use cases and the ports they depend on.
package app

import (
	"context"

	"github.com/santoshkc2200/ioe-backend/internal/enrollment/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

// Repository reads and writes enrollments in the current transaction. Insert returns
// ErrDuplicate on the (course_id, user_id) unique constraint and sets e.Version to 1.
// Update returns ErrConcurrentModification when e.Version is stale and increments it on
// success. List methods return active enrollments only.
type Repository interface {
	FindByCourseAndUser(ctx context.Context, courseID, userID id.ID) (domain.Enrollment, bool, error)
	ListActiveByCourse(ctx context.Context, courseID id.ID, limit, offset int) ([]domain.Enrollment, int, error)
	ListActiveByUser(ctx context.Context, userID id.ID) ([]domain.Enrollment, error)
	Insert(ctx context.Context, e *domain.Enrollment) error
	Update(ctx context.Context, e *domain.Enrollment) error
}

// EventPublisher records events in the current transaction.
type EventPublisher interface {
	Publish(ctx context.Context, events ...domain.Event) error
}

// Repos are bound to one transaction.
type Repos struct {
	Enrollments Repository
	Events      EventPublisher
}

// TxRunner commits when fn returns nil and rolls back otherwise.
type TxRunner interface {
	RunInTx(ctx context.Context, fn func(Repos) error) error
}

// CourseCatalog returns ErrNotFound when the course does not exist.
type CourseCatalog interface {
	CourseFacts(ctx context.Context, courseID id.ID) (domain.CourseFacts, error)
}
```

- [ ] **Step 5: Implement AccessQuery and Service**

`internal/enrollment/app/access_query.go`:

```go
package app

import (
	"context"

	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

// AccessQuery answers enrollment questions for other contexts. It depends only on the
// repository so it can be built before the contexts it serves.
type AccessQuery struct{ tx TxRunner }

func NewAccessQuery(tx TxRunner) *AccessQuery { return &AccessQuery{tx: tx} }

func (q *AccessQuery) IsActivelyEnrolled(ctx context.Context, courseID, userID id.ID) (bool, error) {
	var active bool
	err := q.tx.RunInTx(ctx, func(r Repos) error {
		e, found, err := r.Enrollments.FindByCourseAndUser(ctx, courseID, userID)
		active = found && e.IsActive()
		return err
	})
	return active, err
}
```

`internal/enrollment/app/service.go`:

```go
package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/santoshkc2200/ioe-backend/internal/enrollment/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/clock"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

const (
	DefaultPageLimit = 50
	MaxPageLimit     = 200
)

// Service implements the enrollment use cases.
type Service struct {
	tx      TxRunner
	courses CourseCatalog
	ids     *id.Generator
	clock   clock.Clock
}

func NewService(tx TxRunner, courses CourseCatalog, ids *id.Generator, c clock.Clock) *Service {
	return &Service{tx: tx, courses: courses, ids: ids, clock: c}
}

// Enroll makes userID actively enrolled in courseID. activated is false when the user was
// already active.
func (s *Service) Enroll(ctx context.Context, p auth.Principal, courseID, userID id.ID) (domain.Enrollment, bool, error) {
	c, err := s.courses.CourseFacts(ctx, courseID)
	if err != nil {
		return domain.Enrollment{}, false, err
	}
	if err := domain.AuthorizeEnroll(p, c, userID); err != nil {
		return domain.Enrollment{}, false, err
	}
	e, activated, err := s.enroll(ctx, courseID, userID)
	if errors.Is(err, ErrDuplicate) {
		// A concurrent first enrollment won the unique constraint; its row is now visible.
		e, activated, err = s.enroll(ctx, courseID, userID)
	}
	return e, activated, err
}

func (s *Service) enroll(ctx context.Context, courseID, userID id.ID) (domain.Enrollment, bool, error) {
	var (
		e         domain.Enrollment
		activated bool
	)
	err := s.tx.RunInTx(ctx, func(r Repos) error {
		existing, found, err := r.Enrollments.FindByCourseAndUser(ctx, courseID, userID)
		if err != nil {
			return err
		}
		now := s.clock.Now()
		var ev domain.Event
		if found {
			e = existing
			if ev = e.Reactivate(now); ev == nil {
				return nil // already active
			}
			err = r.Enrollments.Update(ctx, &e)
		} else {
			e, ev = domain.NewEnrollment(s.ids.New(), courseID, userID, now)
			err = r.Enrollments.Insert(ctx, &e)
		}
		if err != nil {
			return err
		}
		activated = true
		return r.Events.Publish(ctx, ev)
	})
	if err != nil {
		return domain.Enrollment{}, false, err
	}
	return e, activated, nil
}

// Cancel ends userID's enrollment. The enrolled user may always cancel their own;
// anyone else must manage the course.
func (s *Service) Cancel(ctx context.Context, p auth.Principal, courseID, userID id.ID, reason string) (domain.Enrollment, error) {
	if p.UserID != userID {
		c, err := s.courses.CourseFacts(ctx, courseID)
		if err != nil {
			return domain.Enrollment{}, err
		}
		if err := domain.AuthorizeManage(p, c); err != nil {
			return domain.Enrollment{}, err
		}
	}
	var e domain.Enrollment
	err := s.tx.RunInTx(ctx, func(r Repos) error {
		var (
			found bool
			err   error
		)
		e, found, err = r.Enrollments.FindByCourseAndUser(ctx, courseID, userID)
		if err != nil {
			return err
		}
		if !found {
			return ErrNotFound
		}
		ev, err := e.Cancel(reason, s.clock.Now())
		if err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidInput, err)
		}
		if ev == nil {
			return nil
		}
		if err := r.Enrollments.Update(ctx, &e); err != nil {
			return err
		}
		return r.Events.Publish(ctx, ev)
	})
	if err != nil {
		return domain.Enrollment{}, err
	}
	return e, nil
}

// ListByCourse returns one page of a course's active enrollments and the active total.
func (s *Service) ListByCourse(ctx context.Context, p auth.Principal, courseID id.ID, limit, offset int) ([]domain.Enrollment, int, error) {
	if limit < 1 || limit > MaxPageLimit || offset < 0 {
		return nil, 0, fmt.Errorf("%w: limit must be 1-%d and offset must not be negative", ErrInvalidInput, MaxPageLimit)
	}
	c, err := s.courses.CourseFacts(ctx, courseID)
	if err != nil {
		return nil, 0, err
	}
	if err := domain.AuthorizeManage(p, c); err != nil {
		return nil, 0, err
	}
	var (
		page  []domain.Enrollment
		total int
	)
	err = s.tx.RunInTx(ctx, func(r Repos) error {
		var err error
		page, total, err = r.Enrollments.ListActiveByCourse(ctx, courseID, limit, offset)
		return err
	})
	return page, total, err
}

// ListByUser returns userID's active enrollments.
func (s *Service) ListByUser(ctx context.Context, p auth.Principal, userID id.ID) ([]domain.Enrollment, error) {
	if err := domain.AuthorizeListUser(p, userID); err != nil {
		return nil, err
	}
	var out []domain.Enrollment
	err := s.tx.RunInTx(ctx, func(r Repos) error {
		var err error
		out, err = r.Enrollments.ListActiveByUser(ctx, userID)
		return err
	})
	return out, err
}
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test -race ./internal/enrollment/... && golangci-lint run ./internal/enrollment/...`
Expected: PASS, no lint findings.

- [ ] **Step 7: Commit**

```bash
git add internal/enrollment/app
git commit -m "feat(enrollment): add enrollment use cases"
```

---

### Task 3: Courseauthoring course facts

**Files:**
- Modify: `internal/courseauthoring/app/course_service.go` (after `Get`, around line 120)
- Test: `internal/courseauthoring/app/course_service_test.go`

**Interfaces:**
- Consumes: existing `CourseService`, `Repos.Courses.FindByID`, `domain.StatusPublished`, `domain.Price.IsFree`.
- Produces: `type CourseFacts struct{ Published, Free bool; OwnerID id.ID }`; `func (s *CourseService) Facts(ctx context.Context, courseID id.ID) (CourseFacts, error)` returning `ErrNotFound` for a missing course.

- [ ] **Step 1: Write the failing test**

Append to `internal/courseauthoring/app/course_service_test.go` (package `app_test`; `ctx`, `owner` and `newCourseService` already exist there):

```go
func TestFacts(t *testing.T) {
	svc, _ := newCourseService(t)
	c, err := svc.Create(ctx, owner, app.CreateCourseInput{Title: "Go"})
	if err != nil {
		t.Fatal(err)
	}
	f, err := svc.Facts(ctx, c.ID)
	if err != nil || f != (app.CourseFacts{Published: false, Free: true, OwnerID: owner.UserID}) {
		t.Fatalf("draft facts = %+v, %v", f, err)
	}
	if _, err := svc.SetPrice(ctx, owner, c.ID, 150000, "NPR"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AddLecture(ctx, owner, c.ID, app.AddLectureInput{Title: "L1"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Publish(ctx, owner, c.ID); err != nil {
		t.Fatal(err)
	}
	f, err = svc.Facts(ctx, c.ID)
	if err != nil || f != (app.CourseFacts{Published: true, Free: false, OwnerID: owner.UserID}) {
		t.Fatalf("published facts = %+v, %v", f, err)
	}
	if _, err := svc.Facts(ctx, 424242); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("missing err = %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/courseauthoring/app/ -run TestFacts`
Expected: FAIL — `svc.Facts undefined`.

- [ ] **Step 3: Implement**

Insert after `Get` in `internal/courseauthoring/app/course_service.go`:

```go
// CourseFacts is what other contexts may know about a course without a principal.
type CourseFacts struct {
	Published bool
	Free      bool
	OwnerID   id.ID
}

// Facts returns a course's publication, price and ownership facts for internal callers.
// It applies no authorization and must not be exposed over HTTP.
func (s *CourseService) Facts(ctx context.Context, courseID id.ID) (CourseFacts, error) {
	var f CourseFacts
	err := s.tx.RunInTx(ctx, func(r Repos) error {
		c, err := r.Courses.FindByID(ctx, courseID)
		if err != nil {
			return err
		}
		f = CourseFacts{Published: c.Status == domain.StatusPublished, Free: c.Price.IsFree(), OwnerID: c.OwnerID}
		return nil
	})
	return f, err
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race ./internal/courseauthoring/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/courseauthoring/app
git commit -m "feat(courseauthoring): expose course facts to internal callers"
```

---

### Task 4: Enrollment persistence

**Files:**
- Create: `migrations/00004_enrollment.sql`, `internal/enrollment/adapters/postgres/queries.sql`, `postgres.go`, `enrollments.go`
- Generate: `internal/enrollment/adapters/postgres/sqlcgen/*` (and regenerated `models.go` in the identity and courseauthoring `sqlcgen` packages, which pick up the new table)
- Modify: `sqlc.yaml`
- Test: `internal/enrollment/adapters/postgres/postgres_integration_test.go`

**Interfaces:**
- Consumes: `app.Repository`, `app.Repos`, `app.ErrDuplicate`, `app.ErrConcurrentModification`, `app.NewService`, `app.NewAccessQuery`, `domain.*` from Tasks 1–2; `outbox.Publish(ctx, tx, topic, msg)`; `pgtest.New(t) *pgxpool.Pool`.
- Produces: `postgres.NewTxRunner(pool *pgxpool.Pool) *TxRunner` implementing `app.TxRunner`.

- [ ] **Step 1: Write the migration**

`migrations/00004_enrollment.sql`:

```sql
-- +goose Up
CREATE SCHEMA enrollment;

CREATE TABLE enrollment.enrollments (
  id            bigint PRIMARY KEY,
  course_id     bigint NOT NULL,
  user_id       bigint NOT NULL,
  status        text NOT NULL CHECK (status IN ('active', 'canceled')),
  cancel_reason text NOT NULL DEFAULT '',
  enrolled_at   timestamptz NOT NULL,
  canceled_at   timestamptz,
  version       bigint NOT NULL,
  CONSTRAINT enrollments_course_user_unique UNIQUE (course_id, user_id),
  CONSTRAINT enrollments_canceled_consistent CHECK ((status = 'canceled') = (canceled_at IS NOT NULL))
);

CREATE INDEX enrollments_user_active ON enrollment.enrollments (user_id) WHERE status = 'active';

-- +goose Down
DROP SCHEMA enrollment CASCADE;
```

- [ ] **Step 2: Write the queries and sqlc config**

`internal/enrollment/adapters/postgres/queries.sql`:

```sql
-- name: GetEnrollment :one
SELECT * FROM enrollment.enrollments WHERE course_id = $1 AND user_id = $2;

-- name: InsertEnrollment :exec
INSERT INTO enrollment.enrollments
  (id, course_id, user_id, status, cancel_reason, enrolled_at, canceled_at, version)
VALUES ($1, $2, $3, $4, $5, $6, $7, 1);

-- name: UpdateEnrollment :execrows
UPDATE enrollment.enrollments
SET status = $3, cancel_reason = $4, enrolled_at = $5, canceled_at = $6, version = version + 1
WHERE id = $1 AND version = $2;

-- name: ListActiveEnrollmentsByCourse :many
SELECT * FROM enrollment.enrollments
WHERE course_id = $1 AND status = 'active'
ORDER BY enrolled_at, id
LIMIT sqlc.arg(page_limit)::bigint OFFSET sqlc.arg(page_offset)::bigint;

-- name: CountActiveEnrollmentsByCourse :one
SELECT count(*) FROM enrollment.enrollments WHERE course_id = $1 AND status = 'active';

-- name: ListActiveEnrollmentsByUser :many
SELECT * FROM enrollment.enrollments
WHERE user_id = $1 AND status = 'active'
ORDER BY enrolled_at DESC, id DESC;
```

Append to `sqlc.yaml` under `sql:`:

```yaml
  - engine: postgresql
    schema: migrations
    queries: internal/enrollment/adapters/postgres/queries.sql
    gen:
      go:
        package: sqlcgen
        out: internal/enrollment/adapters/postgres/sqlcgen
        sql_package: pgx/v5
        overrides:
          - db_type: timestamptz
            go_type: time.Time
          - db_type: timestamptz
            nullable: true
            go_type:
              import: time
              type: Time
              pointer: true
```

- [ ] **Step 3: Generate**

Run: `make sqlc && go build ./...`
Expected: success. `internal/enrollment/adapters/postgres/sqlcgen/models.go` defines `EnrollmentEnrollment{ID, CourseID, UserID int64; Status, CancelReason string; EnrolledAt time.Time; CanceledAt *time.Time; Version int64}`. The params structs are `GetEnrollmentParams{CourseID, UserID int64}`, `InsertEnrollmentParams{ID, CourseID, UserID int64; Status, CancelReason string; EnrolledAt time.Time; CanceledAt *time.Time}`, `UpdateEnrollmentParams{ID, Version int64; Status, CancelReason string; EnrolledAt time.Time; CanceledAt *time.Time}`, `ListActiveEnrollmentsByCourseParams{CourseID, PageLimit, PageOffset int64}`. If sqlc emits different field names, use the generated names in Step 5 — do not hand-edit generated code. The identity and courseauthoring `sqlcgen/models.go` files also gain `EnrollmentEnrollment`; commit them, or `make sqlc-check` fails.

- [ ] **Step 4: Write the failing integration tests**

`internal/enrollment/adapters/postgres/postgres_integration_test.go`:

```go
//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/santoshkc2200/ioe-backend/internal/enrollment/adapters/postgres"
	"github.com/santoshkc2200/ioe-backend/internal/enrollment/app"
	"github.com/santoshkc2200/ioe-backend/internal/enrollment/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/clock"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
	"github.com/santoshkc2200/ioe-backend/internal/platform/postgres/pgtest"
)

var ctx = context.Background()

type fixture struct {
	pool *pgxpool.Pool
	tx   *postgres.TxRunner
	ids  *id.Generator
	now  time.Time
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	ids, err := id.NewGenerator(0)
	if err != nil {
		t.Fatal(err)
	}
	pool := pgtest.New(t)
	return fixture{pool: pool, tx: postgres.NewTxRunner(pool), ids: ids, now: time.Now().UTC().Truncate(time.Microsecond)}
}

func (f fixture) outboxCount(t *testing.T, topic string) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(ctx, "SELECT count(*) FROM platform.outbox_messages WHERE payload->>'destination_topic' = $1", topic).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestRoundTripAndVersionConflict(t *testing.T) {
	f := newFixture(t)
	e, ev := domain.NewEnrollment(f.ids.New(), 10, 20, f.now)
	err := f.tx.RunInTx(ctx, func(r app.Repos) error {
		if err := r.Enrollments.Insert(ctx, &e); err != nil {
			return err
		}
		return r.Events.Publish(ctx, ev)
	})
	if err != nil || e.Version != 1 {
		t.Fatalf("insert: v=%d err=%v", e.Version, err)
	}

	stale := e // still at version 1
	_, _ = e.Cancel("bye", f.now.Add(time.Minute))
	if err := f.tx.RunInTx(ctx, func(r app.Repos) error { return r.Enrollments.Update(ctx, &e) }); err != nil || e.Version != 2 {
		t.Fatalf("update: v=%d err=%v", e.Version, err)
	}
	if err := f.tx.RunInTx(ctx, func(r app.Repos) error { return r.Enrollments.Update(ctx, &stale) }); !errors.Is(err, app.ErrConcurrentModification) {
		t.Fatalf("stale update err = %v", err)
	}

	var got domain.Enrollment
	var found bool
	if err := f.tx.RunInTx(ctx, func(r app.Repos) error {
		var err error
		got, found, err = r.Enrollments.FindByCourseAndUser(ctx, 10, 20)
		return err
	}); err != nil || !found {
		t.Fatalf("find: found=%v err=%v", found, err)
	}
	if got != e {
		t.Fatalf("round trip = %+v, want %+v", got, e)
	}
	if n := f.outboxCount(t, "enrollment.enrollment.activated"); n != 1 {
		t.Fatalf("activated events = %d", n)
	}
}

func TestFindMissing(t *testing.T) {
	f := newFixture(t)
	if err := f.tx.RunInTx(ctx, func(r app.Repos) error {
		_, found, err := r.Enrollments.FindByCourseAndUser(ctx, 1, 2)
		if found {
			t.Fatal("found missing enrollment")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestInsertDuplicate(t *testing.T) {
	f := newFixture(t)
	a, _ := domain.NewEnrollment(f.ids.New(), 10, 20, f.now)
	b, _ := domain.NewEnrollment(f.ids.New(), 10, 20, f.now)
	if err := f.tx.RunInTx(ctx, func(r app.Repos) error { return r.Enrollments.Insert(ctx, &a) }); err != nil {
		t.Fatal(err)
	}
	if err := f.tx.RunInTx(ctx, func(r app.Repos) error { return r.Enrollments.Insert(ctx, &b) }); !errors.Is(err, app.ErrDuplicate) {
		t.Fatalf("err = %v", err)
	}
}

func TestCanceledConsistencyConstraint(t *testing.T) {
	f := newFixture(t)
	_, err := f.pool.Exec(ctx, `INSERT INTO enrollment.enrollments (id, course_id, user_id, status, enrolled_at, version)
		VALUES (1, 10, 20, 'canceled', now(), 1)`)
	if err == nil {
		t.Fatal("canceled row without canceled_at accepted")
	}
}

func TestListActive(t *testing.T) {
	f := newFixture(t)
	seed := func(course, user id.ID, at time.Time, cancel bool) {
		e, _ := domain.NewEnrollment(f.ids.New(), course, user, at)
		if cancel {
			_, _ = e.Cancel("", at)
		}
		if err := f.tx.RunInTx(ctx, func(r app.Repos) error { return r.Enrollments.Insert(ctx, &e) }); err != nil {
			t.Fatal(err)
		}
	}
	seed(10, 201, f.now, false)
	seed(10, 202, f.now.Add(time.Second), true)
	seed(10, 203, f.now.Add(2*time.Second), false)
	seed(11, 201, f.now.Add(3*time.Second), false)

	if err := f.tx.RunInTx(ctx, func(r app.Repos) error {
		page, total, err := r.Enrollments.ListActiveByCourse(ctx, 10, 1, 1)
		if err != nil {
			return err
		}
		if total != 2 || len(page) != 1 || page[0].UserID != 203 {
			t.Fatalf("page=%+v total=%d", page, total)
		}
		mine, err := r.Enrollments.ListActiveByUser(ctx, 201)
		if err != nil {
			return err
		}
		if len(mine) != 2 || mine[0].CourseID != 11 {
			t.Fatalf("by user = %+v", mine)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

type oneCourse struct{}

func (oneCourse) CourseFacts(context.Context, id.ID) (domain.CourseFacts, error) {
	return domain.CourseFacts{Published: true, Free: true, OwnerID: 1}, nil
}

func TestConcurrentEnrollCreatesOneRow(t *testing.T) {
	f := newFixture(t)
	svc := app.NewService(f.tx, oneCourse{}, f.ids, clock.System{})
	student := auth.Principal{UserID: 20, Role: auth.RoleStudent}
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, errs[i] = svc.Enroll(ctx, student, 10, student.UserID)
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatalf("enroll err = %v", err)
		}
	}
	var rows int
	if err := f.pool.QueryRow(ctx, "SELECT count(*) FROM enrollment.enrollments").Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("rows = %d", rows)
	}
	if n := f.outboxCount(t, "enrollment.enrollment.activated"); n != 1 {
		t.Fatalf("activated events = %d", n)
	}
	ok, err := app.NewAccessQuery(f.tx).IsActivelyEnrolled(ctx, 10, 20)
	if err != nil || !ok {
		t.Fatalf("access: %v %v", ok, err)
	}
}
```

- [ ] **Step 5: Implement the adapter**

`internal/enrollment/adapters/postgres/postgres.go`:

```go
// Package postgres implements enrollment persistence on the enrollment schema.
package postgres

import (
	"context"
	"encoding/json"

	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/santoshkc2200/ioe-backend/internal/enrollment/adapters/postgres/sqlcgen"
	"github.com/santoshkc2200/ioe-backend/internal/enrollment/app"
	"github.com/santoshkc2200/ioe-backend/internal/enrollment/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/outbox"
)

// TxRunner runs enrollment use cases in one PostgreSQL transaction.
type TxRunner struct{ pool *pgxpool.Pool }

func NewTxRunner(pool *pgxpool.Pool) *TxRunner { return &TxRunner{pool: pool} }

func (r *TxRunner) RunInTx(ctx context.Context, fn func(app.Repos) error) error {
	return pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		return fn(app.Repos{Enrollments: enrollments{q: sqlcgen.New(tx)}, Events: events{tx: tx}})
	})
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

`internal/enrollment/adapters/postgres/enrollments.go`:

```go
package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/santoshkc2200/ioe-backend/internal/enrollment/adapters/postgres/sqlcgen"
	"github.com/santoshkc2200/ioe-backend/internal/enrollment/app"
	"github.com/santoshkc2200/ioe-backend/internal/enrollment/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

const uniqueViolation = "23505"

type enrollments struct{ q *sqlcgen.Queries }

func (r enrollments) FindByCourseAndUser(ctx context.Context, courseID, userID id.ID) (domain.Enrollment, bool, error) {
	row, err := r.q.GetEnrollment(ctx, sqlcgen.GetEnrollmentParams{CourseID: int64(courseID), UserID: int64(userID)})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Enrollment{}, false, nil
	}
	if err != nil {
		return domain.Enrollment{}, false, err
	}
	return toDomain(row), true, nil
}

func (r enrollments) ListActiveByCourse(ctx context.Context, courseID id.ID, limit, offset int) ([]domain.Enrollment, int, error) {
	total, err := r.q.CountActiveEnrollmentsByCourse(ctx, int64(courseID))
	if err != nil {
		return nil, 0, err
	}
	rows, err := r.q.ListActiveEnrollmentsByCourse(ctx, sqlcgen.ListActiveEnrollmentsByCourseParams{
		CourseID: int64(courseID), PageLimit: int64(limit), PageOffset: int64(offset),
	})
	if err != nil {
		return nil, 0, err
	}
	return toDomainAll(rows), int(total), nil
}

func (r enrollments) ListActiveByUser(ctx context.Context, userID id.ID) ([]domain.Enrollment, error) {
	rows, err := r.q.ListActiveEnrollmentsByUser(ctx, int64(userID))
	if err != nil {
		return nil, err
	}
	return toDomainAll(rows), nil
}

func (r enrollments) Insert(ctx context.Context, e *domain.Enrollment) error {
	err := r.q.InsertEnrollment(ctx, sqlcgen.InsertEnrollmentParams{
		ID: int64(e.ID), CourseID: int64(e.CourseID), UserID: int64(e.UserID), Status: string(e.Status),
		CancelReason: e.CancelReason, EnrolledAt: e.EnrolledAt, CanceledAt: optionalTime(e.CanceledAt),
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		return app.ErrDuplicate
	}
	if err != nil {
		return err
	}
	e.Version = 1
	return nil
}

func (r enrollments) Update(ctx context.Context, e *domain.Enrollment) error {
	n, err := r.q.UpdateEnrollment(ctx, sqlcgen.UpdateEnrollmentParams{
		ID: int64(e.ID), Version: e.Version, Status: string(e.Status), CancelReason: e.CancelReason,
		EnrolledAt: e.EnrolledAt, CanceledAt: optionalTime(e.CanceledAt),
	})
	if err != nil {
		return err
	}
	if n == 0 {
		return app.ErrConcurrentModification
	}
	e.Version++
	return nil
}

func toDomain(r sqlcgen.EnrollmentEnrollment) domain.Enrollment {
	e := domain.Enrollment{
		ID: id.ID(r.ID), CourseID: id.ID(r.CourseID), UserID: id.ID(r.UserID), Status: domain.Status(r.Status),
		CancelReason: r.CancelReason, EnrolledAt: r.EnrolledAt.UTC(), Version: r.Version,
	}
	if r.CanceledAt != nil {
		e.CanceledAt = r.CanceledAt.UTC()
	}
	return e
}

func toDomainAll(rows []sqlcgen.EnrollmentEnrollment) []domain.Enrollment {
	out := make([]domain.Enrollment, len(rows))
	for i, r := range rows {
		out[i] = toDomain(r)
	}
	return out
}

func optionalTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}
```

`TestRoundTripAndVersionConflict` compares with `!=`; `f.now` is UTC and truncated to microseconds, so the `.UTC()` calls in `toDomain` make the values equal.

- [ ] **Step 6: Run integration tests**

Run: `go test -race -tags integration ./internal/enrollment/...`
Expected: PASS. This requires a running Docker daemon; if Docker is unavailable, stop and report — do not mark this step done.

- [ ] **Step 7: Run sqlc diff and lint**

Run: `make sqlc-check && golangci-lint run ./...`
Expected: no diff, no findings.

- [ ] **Step 8: Commit**

```bash
git add migrations/00004_enrollment.sql sqlc.yaml internal/enrollment/adapters/postgres internal/identity/adapters/postgres/sqlcgen internal/courseauthoring/adapters/postgres/sqlcgen
git commit -m "feat(enrollment): persist enrollments in postgres"
```

---

### Task 5: Enrollment HTTP API

**Files:**
- Create: `internal/enrollment/adapters/httpapi/httpapi.go`, `wire.go`
- Test: `internal/enrollment/adapters/httpapi/httpapi_test.go`

**Interfaces:**
- Consumes: `app.Service` method set (Task 2), domain errors (Task 1), `app.DefaultPageLimit`, `httpserver.Router.Handle`, `httpserver.Middleware`, `httpserver.DecodeJSON`, `httpserver.WriteJSON`, `problem.Write`, `problem.TypeNotFound`, `auth.PrincipalFrom`.
- Produces: `httpapi.New(svc Service, cfg Config) *Handler`; `httpapi.Config{RequireAuth httpserver.Middleware; Logger *slog.Logger}`; `(*Handler).Register(r *httpserver.Router)`.

- [ ] **Step 1: Write the failing tests**

`internal/enrollment/adapters/httpapi/httpapi_test.go`:

```go
package httpapi_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/enrollment/adapters/httpapi"
	"github.com/santoshkc2200/ioe-backend/internal/enrollment/app"
	"github.com/santoshkc2200/ioe-backend/internal/enrollment/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/httpserver"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
	"github.com/santoshkc2200/ioe-backend/internal/platform/problem"
)

var t0 = time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)

// stub records the last call and returns canned results.
type stub struct {
	enrollment domain.Enrollment
	activated  bool
	list       []domain.Enrollment
	total      int
	err        error

	reason        string
	limit, offset int
	principal     auth.Principal
}

func (s *stub) Enroll(_ context.Context, p auth.Principal, _, _ id.ID) (domain.Enrollment, bool, error) {
	s.principal = p
	return s.enrollment, s.activated, s.err
}

func (s *stub) Cancel(_ context.Context, p auth.Principal, _, _ id.ID, reason string) (domain.Enrollment, error) {
	s.principal, s.reason = p, reason
	return s.enrollment, s.err
}

func (s *stub) ListByCourse(_ context.Context, _ auth.Principal, _ id.ID, limit, offset int) ([]domain.Enrollment, int, error) {
	s.limit, s.offset = limit, offset
	return s.list, s.total, s.err
}

func (s *stub) ListByUser(context.Context, auth.Principal, id.ID) ([]domain.Enrollment, error) {
	return s.list, s.err
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

var active = domain.Enrollment{ID: 1, CourseID: 10, UserID: 200, Status: domain.StatusActive, EnrolledAt: t0, Version: 1}

func TestEnrollStatusAndShape(t *testing.T) {
	s := &stub{enrollment: active, activated: true}
	code, body := call(newServer(s), "POST", "/v1/courses/10/enrollments/200", "")
	if code != http.StatusCreated {
		t.Fatalf("code = %d %s", code, body)
	}
	got := decode[map[string]any](t, body)
	want := map[string]any{"id": "1", "course_id": "10", "user_id": "200", "status": "active", "enrolled_at": "2026-10-05T00:00:00Z"}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("%s = %v, want %v (body %s)", k, got[k], v, body)
		}
	}
	if _, ok := got["canceled_at"]; ok {
		t.Fatalf("canceled_at present on active enrollment: %s", body)
	}

	s.activated = false
	if code, _ := call(newServer(s), "POST", "/v1/courses/10/enrollments/200", ""); code != http.StatusOK {
		t.Fatalf("already active code = %d", code)
	}
}

func TestCancelWithoutBody(t *testing.T) {
	canceled := active
	canceled.Status, canceled.CanceledAt = domain.StatusCanceled, t0.Add(time.Hour)
	s := &stub{enrollment: canceled}
	code, body := call(newServer(s), "DELETE", "/v1/courses/10/enrollments/200", "")
	if code != http.StatusOK || s.reason != "" {
		t.Fatalf("code = %d reason = %q body = %s", code, s.reason, body)
	}
	got := decode[map[string]any](t, body)
	if got["status"] != "canceled" || got["canceled_at"] != "2026-10-05T01:00:00Z" {
		t.Fatalf("body = %s", body)
	}
}

func TestCancelWithReason(t *testing.T) {
	s := &stub{enrollment: active}
	if code, _ := call(newServer(s), "DELETE", "/v1/courses/10/enrollments/200", `{"reason":"busy"}`); code != http.StatusOK || s.reason != "busy" {
		t.Fatalf("code = %d reason = %q", code, s.reason)
	}
	if code, _ := call(newServer(s), "DELETE", "/v1/courses/10/enrollments/200", `{"why":"x"}`); code != http.StatusBadRequest {
		t.Fatalf("unknown field code = %d", code)
	}
}

func TestListByCourse(t *testing.T) {
	s := &stub{list: []domain.Enrollment{active}, total: 7}
	code, body := call(newServer(s), "GET", "/v1/courses/10/enrollments", "")
	if code != http.StatusOK || s.limit != app.DefaultPageLimit || s.offset != 0 {
		t.Fatalf("code=%d limit=%d offset=%d", code, s.limit, s.offset)
	}
	page := decode[struct {
		Enrollments []map[string]any `json:"enrollments"`
		Total       int              `json:"total"`
	}](t, body)
	if page.Total != 7 || len(page.Enrollments) != 1 || page.Enrollments[0]["id"] != "1" {
		t.Fatalf("body = %s", body)
	}
	if code, _ := call(newServer(s), "GET", "/v1/courses/10/enrollments?limit=5&offset=10", ""); code != http.StatusOK || s.limit != 5 || s.offset != 10 {
		t.Fatalf("code=%d limit=%d offset=%d", code, s.limit, s.offset)
	}
	for _, q := range []string{"?limit=x", "?offset=1.5"} {
		code, body := call(newServer(s), "GET", "/v1/courses/10/enrollments"+q, "")
		if code != http.StatusBadRequest || decode[map[string]any](t, body)["type"] != "invalid_input" {
			t.Fatalf("%s: %d %s", q, code, body)
		}
	}
}

func TestListByUserEmptyIsArray(t *testing.T) {
	code, body := call(newServer(&stub{}), "GET", "/v1/users/200/enrollments", "")
	if code != http.StatusOK || strings.TrimSpace(string(body)) != "[]" {
		t.Fatalf("code = %d body = %s", code, body)
	}
	code, body = call(newServer(&stub{list: []domain.Enrollment{active}}), "GET", "/v1/users/200/enrollments", "")
	if code != http.StatusOK || len(decode[[]map[string]any](t, body)) != 1 {
		t.Fatalf("code = %d body = %s", code, body)
	}
}

func TestErrorMapping(t *testing.T) {
	cases := []struct {
		err    error
		status int
		typ    string
	}{
		{app.ErrNotFound, 404, "not_found"},
		{domain.ErrCourseHidden, 404, "not_found"},
		{domain.ErrForbidden, 403, "forbidden"},
		{domain.ErrPaymentRequired, 402, "payment_required"},
		{domain.ErrCourseNotPublished, 409, "course_not_published"},
		{app.ErrConcurrentModification, 409, "concurrent_modification"},
		{app.ErrInvalidInput, 400, "invalid_input"},
	}
	for _, tc := range cases {
		code, body := call(newServer(&stub{err: tc.err}), "POST", "/v1/courses/10/enrollments/200", "")
		if code != tc.status || decode[map[string]any](t, body)["type"] != tc.typ {
			t.Fatalf("%v: %d %s", tc.err, code, body)
		}
	}
}

func TestUnparsablePathIDs(t *testing.T) {
	for _, p := range []string{"/v1/courses/abc/enrollments/200", "/v1/courses/10/enrollments/0", "/v1/courses/x/enrollments", "/v1/users/-1/enrollments"} {
		method := "POST"
		if strings.HasSuffix(p, "enrollments") {
			method = "GET"
		}
		code, body := call(newServer(&stub{}), method, p, "")
		if code != http.StatusNotFound || decode[map[string]any](t, body)["type"] != problem.TypeNotFound {
			t.Fatalf("%s %s: %d %s", method, p, code, body)
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/enrollment/adapters/httpapi/`
Expected: FAIL — undefined `httpapi.New`.

- [ ] **Step 3: Implement wire types**

`internal/enrollment/adapters/httpapi/wire.go`:

```go
package httpapi

import (
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/enrollment/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

type enrollmentWire struct {
	ID         id.ID      `json:"id"`
	CourseID   id.ID      `json:"course_id"`
	UserID     id.ID      `json:"user_id"`
	Status     string     `json:"status"`
	EnrolledAt time.Time  `json:"enrolled_at"`
	CanceledAt *time.Time `json:"canceled_at,omitempty"`
}

type enrollmentPageWire struct {
	Enrollments []enrollmentWire `json:"enrollments"`
	Total       int              `json:"total"`
}

type cancelRequest struct {
	Reason string `json:"reason"`
}

func toWire(e domain.Enrollment) enrollmentWire {
	w := enrollmentWire{ID: e.ID, CourseID: e.CourseID, UserID: e.UserID, Status: string(e.Status), EnrolledAt: e.EnrolledAt}
	if !e.CanceledAt.IsZero() {
		at := e.CanceledAt
		w.CanceledAt = &at
	}
	return w
}

func toWires(es []domain.Enrollment) []enrollmentWire {
	out := make([]enrollmentWire, len(es))
	for i, e := range es {
		out[i] = toWire(e)
	}
	return out
}
```

- [ ] **Step 4: Implement the handler**

`internal/enrollment/adapters/httpapi/httpapi.go`:

```go
// Package httpapi exposes enrollment over HTTP.
package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/santoshkc2200/ioe-backend/internal/enrollment/app"
	"github.com/santoshkc2200/ioe-backend/internal/enrollment/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/httpserver"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
	"github.com/santoshkc2200/ioe-backend/internal/platform/problem"
)

// Service is the enrollment use-case surface the handlers call.
type Service interface {
	Enroll(ctx context.Context, p auth.Principal, courseID, userID id.ID) (domain.Enrollment, bool, error)
	Cancel(ctx context.Context, p auth.Principal, courseID, userID id.ID, reason string) (domain.Enrollment, error)
	ListByCourse(ctx context.Context, p auth.Principal, courseID id.ID, limit, offset int) ([]domain.Enrollment, int, error)
	ListByUser(ctx context.Context, p auth.Principal, userID id.ID) ([]domain.Enrollment, error)
}

type Config struct {
	RequireAuth httpserver.Middleware
	Logger      *slog.Logger
}

type Handler struct {
	svc Service
	cfg Config
}

func New(svc Service, cfg Config) *Handler { return &Handler{svc: svc, cfg: cfg} }

// Register mounts the enrollment routes. Every route requires authentication.
func (h *Handler) Register(r *httpserver.Router) {
	a := func(f http.HandlerFunc) http.Handler { return h.cfg.RequireAuth(f) }
	r.Handle("POST /v1/courses/{courseID}/enrollments/{userID}", a(h.enroll))
	r.Handle("DELETE /v1/courses/{courseID}/enrollments/{userID}", a(h.cancel))
	r.Handle("GET /v1/courses/{courseID}/enrollments", a(h.listByCourse))
	r.Handle("GET /v1/users/{userID}/enrollments", a(h.listByUser))
}

func (h *Handler) enroll(w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "courseID", "userID")
	if !ok {
		return
	}
	e, activated, err := h.svc.Enroll(r.Context(), principal(r), ids[0], ids[1])
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	status := http.StatusOK
	if activated {
		status = http.StatusCreated
	}
	httpserver.WriteJSON(w, status, toWire(e))
}

func (h *Handler) cancel(w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "courseID", "userID")
	if !ok {
		return
	}
	// The body is optional: clients send it only when they have a reason.
	var req cancelRequest
	if r.ContentLength != 0 && !httpserver.DecodeJSON(w, r, &req) {
		return
	}
	e, err := h.svc.Cancel(r.Context(), principal(r), ids[0], ids[1], req.Reason)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toWire(e))
}

func (h *Handler) listByCourse(w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "courseID")
	if !ok {
		return
	}
	limit, ok := queryInt(w, r, "limit", app.DefaultPageLimit)
	if !ok {
		return
	}
	offset, ok := queryInt(w, r, "offset", 0)
	if !ok {
		return
	}
	es, total, err := h.svc.ListByCourse(r.Context(), principal(r), ids[0], limit, offset)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, enrollmentPageWire{Enrollments: toWires(es), Total: total})
}

func (h *Handler) listByUser(w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "userID")
	if !ok {
		return
	}
	es, err := h.svc.ListByUser(r.Context(), principal(r), ids[0])
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toWires(es))
}

// pathIDs parses the named path values. Any parse failure is a 404: a malformed ID
// names no resource.
func pathIDs(w http.ResponseWriter, r *http.Request, names ...string) ([]id.ID, bool) {
	out := make([]id.ID, len(names))
	for i, n := range names {
		v, err := id.Parse(r.PathValue(n))
		if err != nil {
			problem.Write(w, r, http.StatusNotFound, problem.TypeNotFound, "Not Found", "")
			return nil, false
		}
		out[i] = v
	}
	return out, true
}

// queryInt parses an optional integer query parameter; range checks belong to the service.
func queryInt(w http.ResponseWriter, r *http.Request, name string, def int) (int, bool) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return def, true
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		problem.Write(w, r, http.StatusBadRequest, "invalid_input", "Invalid Input", name+" must be an integer")
		return 0, false
	}
	return v, true
}

func principal(r *http.Request) auth.Principal {
	p, _ := auth.PrincipalFrom(r.Context()) // RequireAuth guarantees presence
	return p
}

type errorMapping struct {
	err    error
	status int
	typ    string
	title  string
}

var errorMappings = []errorMapping{
	{app.ErrNotFound, http.StatusNotFound, "not_found", "Not Found"},
	{domain.ErrCourseHidden, http.StatusNotFound, "not_found", "Not Found"},
	{domain.ErrForbidden, http.StatusForbidden, "forbidden", "Forbidden"},
	{domain.ErrPaymentRequired, http.StatusPaymentRequired, "payment_required", "Payment Required"},
	{domain.ErrCourseNotPublished, http.StatusConflict, "course_not_published", "Course Not Published"},
	{app.ErrConcurrentModification, http.StatusConflict, "concurrent_modification", "Concurrent Modification"},
	{app.ErrInvalidInput, http.StatusBadRequest, "invalid_input", "Invalid Input"},
}

func (h *Handler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	for _, m := range errorMappings {
		if errors.Is(err, m.err) {
			detail := ""
			if m.status == http.StatusBadRequest {
				detail = err.Error()
			}
			problem.Write(w, r, m.status, m.typ, m.title, detail)
			return
		}
	}
	h.cfg.Logger.ErrorContext(r.Context(), "enrollment request failed", "error", err)
	problem.Write(w, r, http.StatusInternalServerError, problem.TypeInternal, "Internal Server Error", "")
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test -race ./internal/enrollment/... && golangci-lint run ./internal/enrollment/...`
Expected: PASS, no findings. (`id.Parse` rejects `0`, `-1` and non-digits, so every path in `TestUnparsablePathIDs` returns 404.)

- [ ] **Step 6: Commit**

```bash
git add internal/enrollment/adapters/httpapi
git commit -m "feat(enrollment): expose enrollment over HTTP"
```

---

### Task 6: Wiring, end-to-end test, docs and gates

**Files:**
- Create: `cmd/api/enrollment.go`
- Modify: `cmd/api/app.go`, `cmd/api/e2e_integration_test.go`, `api/openapi.yaml`, `README.md`
- Delete: `internal/courseauthoring/adapters/enrollment/deny.go`

**Interfaces:**
- Consumes: `enrollmentpg.NewTxRunner`, `enrollmentapp.NewAccessQuery`, `enrollmentapp.NewService`, `enrollmenthttp.New`, `enrollmenthttp.Config` (Tasks 2, 4, 5); `courseauthoringapp.CourseService.Facts`, `courseauthoringapp.CourseFacts`, `courseauthoringapp.ErrNotFound` (Task 3).
- Produces: `registerEnrollment(...)` and a changed `registerCourseAuthoring` signature in `package main`.

- [ ] **Step 1: Write the failing end-to-end test**

Append to `cmd/api/e2e_integration_test.go`:

```go
func TestEnrollmentEndToEnd(t *testing.T) {
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
	signIn := func(sub, email string) (string, string) {
		t.Helper()
		tok := google.Sign(t, googletest.Claims(sub, email, "web-client", time.Now()))
		resp, body := c.do(http.MethodPost, "/v1/auth/google", `{"id_token":"`+tok+`"}`, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("sign in %s: %d %v", email, resp.StatusCode, body)
		}
		return body["access_token"].(string), body["user"].(map[string]any)["id"].(string)
	}
	adminTok, _ := signIn("sub-admin", "admin@example.com")
	studentTok, studentID := signIn("sub-student", "student@example.com")
	admin, student := bearer(adminTok), bearer(studentTok)

	// publish creates a published course with one non-preview lecture and returns its IDs.
	publish := func(priced bool) (string, string) {
		t.Helper()
		resp, body := c.do(http.MethodPost, "/v1/courses", `{"title":"Go","description":"d"}`, admin)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("create: %d %v", resp.StatusCode, body)
		}
		courseID := body["id"].(string)
		if priced {
			resp, body = c.do(http.MethodPost, "/v1/courses/"+courseID+"/price", `{"amount_minor":150000,"currency":"NPR"}`, admin)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("price: %d %v", resp.StatusCode, body)
			}
		}
		resp, body = c.do(http.MethodPost, "/v1/courses/"+courseID+"/lectures", `{"title":"L1","text_body":"<p>x</p>"}`, admin)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("lecture: %d %v", resp.StatusCode, body)
		}
		lectures := body["lectures"].([]any)
		lectureID := lectures[len(lectures)-1].(map[string]any)["id"].(string)
		if resp, _ = c.do(http.MethodPost, "/v1/courses/"+courseID+"/publish", "", admin); resp.StatusCode != http.StatusNoContent {
			t.Fatalf("publish: %d", resp.StatusCode)
		}
		return courseID, lectureID
	}
	read := func(courseID, lectureID string) (int, any) {
		resp, body := c.do(http.MethodGet, "/v1/courses/"+courseID+"/lectures/"+lectureID+"/content", "", student)
		return resp.StatusCode, body["type"]
	}

	// Free course: self-enroll unlocks content; cancel locks it again.
	freeID, freeLecture := publish(false)
	if code, typ := read(freeID, freeLecture); code != http.StatusForbidden || typ != "enrollment_required" {
		t.Fatalf("before enroll: %d %v", code, typ)
	}
	resp, body := c.do(http.MethodPost, "/v1/courses/"+freeID+"/enrollments/"+studentID, "", student)
	if resp.StatusCode != http.StatusCreated || body["status"] != "active" || body["user_id"] != studentID {
		t.Fatalf("self enroll: %d %v", resp.StatusCode, body)
	}
	if code, _ := read(freeID, freeLecture); code != http.StatusOK {
		t.Fatalf("enrolled read: %d", code)
	}
	resp, body = c.do(http.MethodDelete, "/v1/courses/"+freeID+"/enrollments/"+studentID, "", student)
	if resp.StatusCode != http.StatusOK || body["status"] != "canceled" {
		t.Fatalf("cancel: %d %v", resp.StatusCode, body)
	}
	if code, typ := read(freeID, freeLecture); code != http.StatusForbidden || typ != "enrollment_required" {
		t.Fatalf("after cancel: %d %v", code, typ)
	}

	// Paid course: the student must pay; a manager can enroll them.
	paidID, paidLecture := publish(true)
	resp, body = c.do(http.MethodPost, "/v1/courses/"+paidID+"/enrollments/"+studentID, "", student)
	if resp.StatusCode != http.StatusPaymentRequired || body["type"] != "payment_required" {
		t.Fatalf("paid self enroll: %d %v", resp.StatusCode, body)
	}
	if resp, body = c.do(http.MethodPost, "/v1/courses/"+paidID+"/enrollments/"+studentID, "", admin); resp.StatusCode != http.StatusCreated {
		t.Fatalf("manager enroll: %d %v", resp.StatusCode, body)
	}
	if code, _ := read(paidID, paidLecture); code != http.StatusOK {
		t.Fatalf("comped read: %d", code)
	}

	resp, body = c.do(http.MethodGet, "/v1/courses/"+paidID+"/enrollments", "", admin)
	if resp.StatusCode != http.StatusOK || body["total"] != float64(1) {
		t.Fatalf("roster: %d %v", resp.StatusCode, body)
	}
	if resp, _ = c.do(http.MethodGet, "/v1/courses/"+paidID+"/enrollments", "", student); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("student roster: %d", resp.StatusCode)
	}

	var activations, cancellations int
	if err := pool.QueryRow(ctx, `SELECT
		count(*) FILTER (WHERE payload->>'destination_topic' = 'enrollment.enrollment.activated'),
		count(*) FILTER (WHERE payload->>'destination_topic' = 'enrollment.enrollment.canceled')
		FROM platform.outbox_messages`).Scan(&activations, &cancellations); err != nil {
		t.Fatal(err)
	}
	if activations != 2 || cancellations != 1 {
		t.Fatalf("activations=%d cancellations=%d", activations, cancellations)
	}
}
```

The user enrollments route returns a bare array, which `client.do` cannot decode into a map. It is covered by the HTTP unit test; do not add it here.

- [ ] **Step 2: Run it to verify it fails**

Run: `go test -race -tags integration ./cmd/api/ -run TestEnrollmentEndToEnd`
Expected: FAIL — the enroll POST returns 404 (no route) or 405.

- [ ] **Step 3: Add the composition file**

`cmd/api/enrollment.go`:

```go
package main

import (
	"context"
	"errors"
	"log/slog"

	courseauthoringapp "github.com/santoshkc2200/ioe-backend/internal/courseauthoring/app"
	enrollmenthttp "github.com/santoshkc2200/ioe-backend/internal/enrollment/adapters/httpapi"
	enrollmentapp "github.com/santoshkc2200/ioe-backend/internal/enrollment/app"
	enrollmentdomain "github.com/santoshkc2200/ioe-backend/internal/enrollment/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/clock"
	"github.com/santoshkc2200/ioe-backend/internal/platform/httpserver"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

// courseCatalog lets enrollment read course facts from course authoring.
type courseCatalog struct{ courses *courseauthoringapp.CourseService }

func (c courseCatalog) CourseFacts(ctx context.Context, courseID id.ID) (enrollmentdomain.CourseFacts, error) {
	f, err := c.courses.Facts(ctx, courseID)
	if errors.Is(err, courseauthoringapp.ErrNotFound) {
		return enrollmentdomain.CourseFacts{}, enrollmentapp.ErrNotFound
	}
	if err != nil {
		return enrollmentdomain.CourseFacts{}, err
	}
	return enrollmentdomain.CourseFacts{Published: f.Published, Free: f.Free, OwnerID: f.OwnerID}, nil
}

func registerEnrollment(r *httpserver.Router, tx enrollmentapp.TxRunner, courses *courseauthoringapp.CourseService, ids *id.Generator, clk clock.Clock, requireAuth httpserver.Middleware, logger *slog.Logger) {
	enrollmenthttp.New(
		enrollmentapp.NewService(tx, courseCatalog{courses: courses}, ids, clk),
		enrollmenthttp.Config{RequireAuth: requireAuth, Logger: logger},
	).Register(r)
}
```

`*enrollmentapp.AccessQuery` already has `IsActivelyEnrolled(ctx, courseID, userID id.ID) (bool, error)`, so it satisfies `courseauthoringapp.EnrollmentQuery` directly; no adapter type is needed for that direction.

- [ ] **Step 4: Rewire `app.go`**

In `cmd/api/app.go`:

Replace the import

```go
	courseauthoringenrollment "github.com/santoshkc2200/ioe-backend/internal/courseauthoring/adapters/enrollment"
```

with

```go
	enrollmentpg "github.com/santoshkc2200/ioe-backend/internal/enrollment/adapters/postgres"
	enrollmentapp "github.com/santoshkc2200/ioe-backend/internal/enrollment/app"
```

(keep imports sorted; `golangci-lint fmt` fixes order).

Replace

```go
	registerCourseAuthoring(router, pool, ids, clk, identityHandler.RequireAuth, logger)
```

with

```go
	enrollmentTx := enrollmentpg.NewTxRunner(pool)
	courses := registerCourseAuthoring(router, pool, ids, clk, enrollmentapp.NewAccessQuery(enrollmentTx), identityHandler.RequireAuth, logger)
	registerEnrollment(router, enrollmentTx, courses, ids, clk, identityHandler.RequireAuth, logger)
```

Replace `registerCourseAuthoring` with:

```go
// registerCourseAuthoring mounts course authoring and returns its course service for
// contexts that read course facts.
func registerCourseAuthoring(r *httpserver.Router, pool *pgxpool.Pool, ids *id.Generator, clk clock.Clock, enrollments courseauthoringapp.EnrollmentQuery, requireAuth httpserver.Middleware, logger *slog.Logger) *courseauthoringapp.CourseService {
	tx := courseauthoringpg.NewTxRunner(pool, clk)
	courses := courseauthoringapp.NewCourseService(tx, ids, clk)
	courseauthoringhttp.New(
		courses,
		courseauthoringapp.NewContentService(tx, ids, enrollments),
		courseauthoringhttp.Config{RequireAuth: requireAuth, ContentLimiter: httpserver.NewRateLimiter(60), Logger: logger},
	).Register(r)
	return courses
}
```

- [ ] **Step 5: Delete the deny adapter**

Run: `git rm internal/courseauthoring/adapters/enrollment/deny.go`

Then: `go build ./... && grep -rn 'adapters/enrollment' --include='*.go' . || true`
Expected: build succeeds; grep prints nothing.

- [ ] **Step 6: Run the end-to-end test**

Run: `go test -race -tags integration ./cmd/api/`
Expected: PASS for `TestEndToEnd`, `TestRoleManagementEndToEnd` and `TestEnrollmentEndToEnd`. `TestEndToEnd` still expects `403 enrollment_required` for an unenrolled student, which remains correct.

- [ ] **Step 7: Document the API**

In `api/openapi.yaml`, add these paths after `/v1/courses/{courseID}/lectures/{lectureID}/content` (before `components:`):

```yaml
  /v1/courses/{courseID}/enrollments/{userID}:
    post:
      summary: Enroll a user in a course
      description: >
        A user may enroll themselves in a published free course; a published paid course
        returns 402 `payment_required`. The course owner or a root admin may enroll any user
        in a published course, free or paid (409 `course_not_published` otherwise).
        Re-enrolling reactivates a canceled enrollment. Non-managers get 404 for unpublished
        courses.
      security:
        - bearer: []
      parameters:
        - $ref: "#/components/parameters/CourseID"
        - $ref: "#/components/parameters/UserID"
      responses:
        "201":
          description: Enrollment created or reactivated
          content:
            application/json:
              schema: { $ref: "#/components/schemas/Enrollment" }
        "200":
          description: The user was already enrolled
          content:
            application/json:
              schema: { $ref: "#/components/schemas/Enrollment" }
        "401": { $ref: "#/components/responses/Problem" }
        "402": { $ref: "#/components/responses/Problem" }
        "403": { $ref: "#/components/responses/Problem" }
        "404": { $ref: "#/components/responses/Problem" }
        "409": { $ref: "#/components/responses/Problem" }
        "405": { $ref: "#/components/responses/MethodNotAllowed" }
        "500": { $ref: "#/components/responses/InternalError" }
    delete:
      summary: Cancel an enrollment
      description: >
        The enrolled user, the course owner, or a root admin. Canceling a canceled enrollment
        returns it unchanged. The body is optional.
      security:
        - bearer: []
      parameters:
        - $ref: "#/components/parameters/CourseID"
        - $ref: "#/components/parameters/UserID"
      requestBody:
        required: false
        content:
          application/json:
            schema: { $ref: "#/components/schemas/CancelEnrollmentRequest" }
      responses:
        "200":
          description: The canceled enrollment
          content:
            application/json:
              schema: { $ref: "#/components/schemas/Enrollment" }
        "400": { $ref: "#/components/responses/Problem" }
        "401": { $ref: "#/components/responses/Problem" }
        "403": { $ref: "#/components/responses/Problem" }
        "404": { $ref: "#/components/responses/Problem" }
        "409": { $ref: "#/components/responses/Problem" }
        "413": { $ref: "#/components/responses/Problem" }
        "415": { $ref: "#/components/responses/Problem" }
        "405": { $ref: "#/components/responses/MethodNotAllowed" }
        "500": { $ref: "#/components/responses/InternalError" }
  /v1/courses/{courseID}/enrollments:
    get:
      summary: List a course's active enrollments
      description: Course owner or root admin. Ordered by enrollment time.
      security:
        - bearer: []
      parameters:
        - $ref: "#/components/parameters/CourseID"
        - name: limit
          in: query
          schema: { type: integer, minimum: 1, maximum: 200, default: 50 }
        - name: offset
          in: query
          schema: { type: integer, minimum: 0, default: 0 }
      responses:
        "200":
          description: One page of active enrollments
          content:
            application/json:
              schema: { $ref: "#/components/schemas/EnrollmentPage" }
        "400": { $ref: "#/components/responses/Problem" }
        "401": { $ref: "#/components/responses/Problem" }
        "403": { $ref: "#/components/responses/Problem" }
        "404": { $ref: "#/components/responses/Problem" }
        "405": { $ref: "#/components/responses/MethodNotAllowed" }
        "500": { $ref: "#/components/responses/InternalError" }
  /v1/users/{userID}/enrollments:
    get:
      summary: List a user's active enrollments
      description: The user themselves or a root admin. Newest first.
      security:
        - bearer: []
      parameters:
        - $ref: "#/components/parameters/UserID"
      responses:
        "200":
          description: Active enrollments
          content:
            application/json:
              schema:
                type: array
                items: { $ref: "#/components/schemas/Enrollment" }
        "401": { $ref: "#/components/responses/Problem" }
        "403": { $ref: "#/components/responses/Problem" }
        "404": { $ref: "#/components/responses/Problem" }
        "405": { $ref: "#/components/responses/MethodNotAllowed" }
        "500": { $ref: "#/components/responses/InternalError" }
```

Add under `components.schemas` (after `CoursePage`):

```yaml
    Enrollment:
      type: object
      required: [id, course_id, user_id, status, enrolled_at]
      properties:
        id: { $ref: "#/components/schemas/ID" }
        course_id: { $ref: "#/components/schemas/ID" }
        user_id: { $ref: "#/components/schemas/ID" }
        status: { type: string, enum: [active, canceled] }
        enrolled_at: { type: string, format: date-time }
        canceled_at: { type: string, format: date-time, description: Present only when canceled }
    EnrollmentPage:
      type: object
      required: [enrollments, total]
      properties:
        enrollments:
          type: array
          items: { $ref: "#/components/schemas/Enrollment" }
        total: { type: integer }
    CancelEnrollmentRequest:
      type: object
      properties:
        reason: { type: string, maxLength: 500 }
```

- [ ] **Step 8: Update the README**

Add after the `## Roles` section in `README.md`:

```markdown
## Enrollment

Students enroll themselves in published free courses with
`POST /v1/courses/{courseID}/enrollments/{theirUserID}`; paid courses answer
`402 payment_required` until payment exists. The course owner or a root admin can enroll anyone
in a published course, free or paid, and list a course's roster with
`GET /v1/courses/{courseID}/enrollments`. Enrolled students read every lecture of a published
course. A student, the owner, or a root admin cancels with `DELETE` on the same path.
```

- [ ] **Step 9: Run every gate**

Run each and confirm:

```sh
make check
make test-integration
docker compose config > /dev/null
make docker-build
git diff --check
```

Expected: all succeed. `make test-integration` must actually run against Docker; if Docker is unavailable, report that instead of claiming a pass.

- [ ] **Step 10: Commit**

```bash
git add cmd/api api/openapi.yaml README.md internal/courseauthoring/adapters/enrollment
git commit -m "feat(api): wire enrollment and gate lectures on real enrollments"
```
