# Enrollment Design

Date: 2026-10-05

## Status

Approved in conversation on 2026-10-05. Pending written-spec review.

## Context

This is sub-project 2 of: platform foundation, courseauthoring, enrollment, payment (eSewa,
Khalti, connectIPS).

Courseauthoring gates non-preview lecture content behind its `EnrollmentQuery` port
(`internal/courseauthoring/app/ports.go`). `cmd/api` wires that port to
`internal/courseauthoring/adapters/enrollment.Deny`, which reports every user as not enrolled, so
students can read only free-preview lectures.

`hitox-backend` (`/Users/hitohospital/personal/dev/hitox/hitox-backend/internal/enrollment`,
about 2.2k lines) implements enrollment with multi-tenancy, subscriptions, Kratos permissions and
compliance hooks. Its `Enroll` checks only that the course is published and ignores the price.

`ioe-frontend` already calls the enrollment API from
`packages/course-core/src/api/enrollment.ts`:

- `POST /v1/courses/{courseID}/enrollments/{userID}` returns an enrollment.
- `DELETE /v1/courses/{courseID}/enrollments/{userID}` with optional `{reason}` returns an enrollment.
- `GET /v1/courses/{courseID}/enrollments` returns `{enrollments, total}`.
- `GET /v1/users/{userID}/enrollments` returns a bare array.
- Enrollment wire shape: `{id, course_id, user_id, status}`, status `active` or `canceled`.

`packages/course-instructor/src/roster/RosterPanel.tsx` shows user ID and status only.

## Goals

- A student enrolls themselves in a published free course and then reads its non-preview lectures.
- A course manager (the owning instructor or a root admin) enrolls any user in a published course,
  free or paid, and cancels any enrollment in it.
- A student cancels their own enrollment.
- Managers list a course's active enrollments; a user (or root admin) lists the user's active
  enrollments.
- Self-enrollment in a paid course fails with a distinct `payment_required` error that the payment
  slice will reuse.
- Every enrollment state change writes an outbox event in the same transaction.

## Non-goals

- Payment, refunds, or recording why a manual enrollment in a paid course was granted.
- Progress tracking, enrollment counts, catalog listing.
- Names or emails in the roster.
- Enrollment expiry, waitlists, capacity limits, bulk enrollment.
- Canceling enrollments when a course is archived.
- Consumers of the new events.

## Decisions

| Topic | Decision |
|---|---|
| Approach | Port Hitox's enrollment core and trim tenancy, subscriptions, compliance |
| Self-enrollment | Published free courses only; published paid course → 402 `payment_required` |
| Manager enrollment | Owner or root admin enrolls any user in a published course, free or paid |
| Cancellation | The enrolled user or a manager; no refunds |
| Roster | User ID and status only, `limit`/`offset` pagination with `total` |
| Course facts | Synchronous `CourseCatalog` port backed by a new courseauthoring query |
| Construction cycle | Split enrollment's app into `AccessQuery` (repository only) and `EnrollmentService` |
| Re-enrollment | One row per (course, user); a canceled row is reactivated |

Course facts are read synchronously instead of projected from events because `SetPrice` emits no
event, so a projection would go stale, and a second outbox consumer would bring the head-of-line
blocking risk recorded in the notification integration spec.

## Domain (`internal/enrollment/domain`)

```go
type Status string

const (
    StatusActive   Status = "active"
    StatusCanceled Status = "canceled"
)

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
```

Operations:

- `NewEnrollment(id, courseID, userID, now)` returns an active enrollment and an
  `EnrollmentActivated` event.
- `Cancel(reason, now)`: the reason is trimmed; more than 500 runes returns `ErrInvalidReason`.
  On an active enrollment it sets `Status`, `CancelReason`, `CanceledAt` and returns an
  `EnrollmentCanceled` event. On a canceled enrollment it changes nothing and returns no event.
- `Reactivate(now)`: on a canceled enrollment it sets `Status` to active, `EnrolledAt` to `now`,
  clears `CancelReason` and `CanceledAt`, and returns an `EnrollmentActivated` event. On an active
  enrollment it changes nothing and returns no event.
- `Rehydrate` is used only by the repository.

Events follow identity's `EventName()` style:

| Name | Payload |
|---|---|
| `enrollment.enrollment.activated` | `enrollment_id`, `course_id`, `user_id`, `occurred_at` |
| `enrollment.enrollment.canceled` | `enrollment_id`, `course_id`, `user_id`, `reason`, `occurred_at` |

### Access rules

`CourseFacts{Published, Free bool, OwnerID id.ID}` is supplied by the application layer. A
manager of a course is its owner or any `root_admin`. The rules are pure functions over
`auth.Principal`, `CourseFacts` and the target user ID:

| Action | Allowed | Otherwise |
|---|---|---|
| Enroll self | course published and free | unpublished → `ErrNotFound`; published paid → `ErrPaymentRequired` |
| Enroll another user | caller is a manager and course published | not a manager and course unpublished → `ErrNotFound`; not a manager → `ErrForbidden`; manager and course unpublished → `ErrCourseNotPublished` |
| Cancel | caller is the target user, or a manager | `ErrForbidden` |
| List course roster | caller is a manager | course unpublished → `ErrNotFound`; otherwise `ErrForbidden` |
| List a user's enrollments | caller is that user, or `root_admin` | `ErrForbidden` |

A manager enrolling themselves follows the manager rule, so an owner can enroll in their own
paid course.

## Application (`internal/enrollment/app`)

Ports:

```go
// Repository reads and writes enrollments in the current transaction.
// Insert returns ErrDuplicate on the (course_id, user_id) unique constraint.
// Update returns ErrConcurrentModification when e.Version is stale and increments it on success.
type Repository interface {
    FindByCourseAndUser(ctx context.Context, courseID, userID id.ID) (domain.Enrollment, bool, error)
    ListActiveByCourse(ctx context.Context, courseID id.ID, limit, offset int) ([]domain.Enrollment, int, error)
    ListActiveByUser(ctx context.Context, userID id.ID) ([]domain.Enrollment, error)
    Insert(ctx context.Context, e *domain.Enrollment) error
    Update(ctx context.Context, e *domain.Enrollment) error
}

type EventPublisher interface {
    Publish(ctx context.Context, events ...domain.Event) error
}

type Repos struct {
    Enrollments Repository
    Events      EventPublisher
}

type TxRunner interface {
    RunInTx(ctx context.Context, fn func(Repos) error) error
}

// CourseCatalog returns ErrCourseNotFound when the course does not exist.
type CourseCatalog interface {
    CourseFacts(ctx context.Context, courseID id.ID) (domain.CourseFacts, error)
}
```

`AccessQuery` depends only on `TxRunner`:

- `IsActivelyEnrolled(ctx, courseID, userID) (bool, error)`.

`EnrollmentService` depends on `TxRunner`, `CourseCatalog`, `*id.Generator` and `clock.Clock`:

- `Enroll(ctx, p, courseID, userID) (domain.Enrollment, EnrollOutcome, error)`. It reads course
  facts outside the transaction and applies the access rule. Inside the transaction it returns an
  existing active enrollment (`OutcomeExisting`), reactivates a canceled one
  (`OutcomeActivated`), or inserts a new one (`OutcomeActivated`), and publishes any event. On
  `ErrDuplicate` from `Insert` (a concurrent first enrollment) it reruns the transaction once.
- `Cancel(ctx, p, courseID, userID, reason) (domain.Enrollment, error)`. When `p.UserID` equals
  `userID` it skips the course lookup. Otherwise it reads course facts and requires a manager; a
  missing course is `ErrNotFound`. No enrollment is `ErrNotFound`.
- `ListByCourse(ctx, p, courseID, limit, offset) ([]domain.Enrollment, int, error)`.
- `ListByUser(ctx, p, userID) ([]domain.Enrollment, error)` returns active enrollments.

`ErrCourseNotFound` from `CourseCatalog` maps to `ErrNotFound`.

Errors: `ErrNotFound`, `ErrForbidden`, `ErrPaymentRequired`, `ErrCourseNotPublished`,
`ErrInvalidInput` (wrapping `ErrInvalidReason`), `ErrConcurrentModification`.

## Courseauthoring changes

- `CourseService.Facts(ctx, courseID) (CourseFacts, error)` with
  `CourseFacts{Published, Free bool, OwnerID id.ID}` in courseauthoring's `app`. It takes no
  principal, is not routed over HTTP, and returns `ErrNotFound` for a missing course.
- Delete `internal/courseauthoring/adapters/enrollment`.

## Persistence (`internal/enrollment/adapters/postgres`)

Migration `migrations/00004_enrollment.sql`:

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

No foreign keys reach other schemas. The unique constraint's index serves
`FindByCourseAndUser`, `IsActivelyEnrolled` and roster scans. Queries are generated with sqlc,
following the courseauthoring and identity packages. `TxRunner` uses `pgx.BeginFunc` and the
Watermill SQL publisher for the outbox, as courseauthoring does. `ListActiveByCourse` orders by
`enrolled_at, id`.

## HTTP API (`internal/enrollment/adapters/httpapi`)

Every route requires identity's `RequireAuth`. Path IDs that fail to parse return 404.

| Route | Request | Success |
|---|---|---|
| `POST /v1/courses/{courseID}/enrollments/{userID}` | none | 201 when activated, 200 when already active; enrollment |
| `DELETE /v1/courses/{courseID}/enrollments/{userID}` | optional `{reason}` | 200; enrollment |
| `GET /v1/courses/{courseID}/enrollments` | `limit` (default 50, 1–200), `offset` (≥ 0) | 200; `{enrollments, total}` |
| `GET /v1/users/{userID}/enrollments` | none | 200; array of enrollments |

Enrollment response: `id`, `course_id`, `user_id` (decimal strings), `status`, `enrolled_at`,
`canceled_at` (omitted unless canceled).

Errors are RFC 9457 problems via `internal/platform/problem`:

| Status | Type | Cause |
|---|---|---|
| 400 | `invalid_input` | bad `limit`/`offset`, malformed body, reason over 500 runes |
| 402 | `payment_required` | self-enrollment in a published paid course |
| 403 | `forbidden` | caller not allowed |
| 404 | `not_found` | course missing or hidden, enrollment missing, unparsable path ID |
| 409 | `course_not_published` | manager enrolls a user in an unpublished course |
| 409 | `concurrent_modification` | stale version |

`api/openapi.yaml` documents every route, schema and error in the same change. README lists the
endpoints.

## Composition (`cmd/api`)

New file `cmd/api/enrollment.go` holds `registerEnrollment` and two adapters:

- `courseCatalog` implements enrollment's `CourseCatalog` over courseauthoring's
  `CourseService.Facts`, mapping `courseauthoringapp.ErrNotFound` to
  `enrollmentapp.ErrCourseNotFound`.
- `enrollmentQuery` implements courseauthoring's `EnrollmentQuery` over enrollment's
  `AccessQuery`.

Wiring order in `app.go`: enrollment postgres `TxRunner` and `AccessQuery`; courseauthoring
(receiving `enrollmentQuery`); `EnrollmentService` (receiving `courseCatalog`); enrollment HTTP
handler `Register`.

`.golangci.yml` gains `enrollment-domain` and `enrollment-app` depguard rules, adds the
enrollment import path to `platform-independent-of-contexts`, and adds it to the deny rule of
every other context. `sqlc.yaml` gains the enrollment package.

## Testing

- Domain: lifecycle (new, cancel, cancel when canceled, reactivate, reactivate when active,
  reason trimming and limit); a table test with one row per access-rule outcome.
- App with in-memory fakes: enroll new, existing, reactivated; `ErrDuplicate` retry; enroll
  outcomes per access rule; cancel by self, manager and stranger; cancel with no enrollment;
  roster pagination; `ListByUser` authorization; `AccessQuery`.
- Postgres (`-tags integration`, `pgtest`): round trip, version compare-and-swap, unique and
  check constraints, outbox row in the same transaction, two concurrent `Enroll` calls produce one
  row and one activation event.
- HTTP: each route's success status and wire shape (bare array vs page object), each error type,
  unparsable path IDs, `limit`/`offset` validation.
- Courseauthoring: `CourseService.Facts` unit test.
- `cmd/api` e2e: an instructor publishes a free course; a student self-enrolls and reads a
  non-preview lecture; the student cancels and gets 403 `enrollment_required`; on a published paid
  course the student gets 402, the instructor enrolls the student, and the student reads; the
  instructor lists the roster.

Gates from `AGENTS.md`: `make check`, `make test-integration` (actually run),
`docker compose config`, `make docker-build`, `git diff --check`.

## Risks

- Archiving a course leaves its enrollments active. Courseauthoring already hides an archived
  course's lectures from students, so access is still denied.
- A demoted instructor keeps manager rights over the courses they own, consistent with the role
  management spec.
- A manual enrollment in a paid course has no payment record. The payment slice must tolerate
  enrollments without one.
- `ContentService` calls `IsActivelyEnrolled` inside its own transaction, and `AccessQuery` opens a
  second transaction on another pool connection. A content read briefly holds two connections.
  Acceptable at the current pool size; revisit if pool exhaustion appears.
- The roster shows user IDs only until a name lookup is added.
