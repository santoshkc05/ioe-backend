# Progress Design

Date: 2026-10-06

## Status

Approved in conversation on 2026-10-06. Pending written-spec review.

## Context

Students can enroll and read lectures, but nothing records how far they have got.

`ioe-frontend` already implements learner progress against a fixed contract:

- `packages/course-core/src/api/progress.ts` calls three routes:
  - `PUT /v1/courses/{courseID}/lectures/{lectureID}/progress/{userID}` with
    `{state, position_ms}`; `state` is `in_progress` or `completed`.
  - `GET /v1/courses/{courseID}/progress/{userID}` returns
    `{course_id, user_id, last_lecture_id?, completed_lecture_ids, lectures, updated_at?}`, where
    each lecture is `{lecture_id, state, position_ms, updated_at}`.
  - `GET /v1/users/{userID}/progress` returns `{user_id, courses, activity_days}`, where each day
    is `{date, lecture_count}` and `date` is a UTC `YYYY-MM-DD`.
- `packages/course-student/src/learn/useLectureProgress.ts` decides completion on the client (text
  dwell, 90% of a video, a finished flashcard deck, or a manual "mark as done"), throttles writes,
  and queues failed writes for the next page load. It treats `409 enrollment_required` as
  permanent and drops the write.
- `LearnShell.tsx` resumes at `last_lecture_id`; `LectureReader.tsx` resumes video at
  `position_ms`.
- `packages/course-student/src/library/MyLearningPage.tsx` intersects `completed_lecture_ids` with
  the course's current reading order, so lectures removed from a course never count, and draws a
  heatmap from `activity_days`.

`hitox-backend/internal/progress` (about 630 lines) implements this contract with tenancy and a
tenant-wide `enrollment:manage` permission. This slice ports it and trims tenancy, following the
enrollment slice.

## Goals

- An actively enrolled student records per-lecture progress (state and video position) in a
  published course.
- A completed lecture never regresses to `in_progress`.
- The student reads their progress per course, and across courses with daily activity.
- A course manager (the owning instructor or a root admin) reads any student's progress in that
  course.
- Writes reject lectures that are not in the course.

## Non-goals

- Server-side completion rules. The client decides when a lecture is complete.
- Undoing a completion.
- Course-level percentages or progress columns on the enrollment roster. The frontend computes
  completion from the outline; a roster column is a later change.
- Filtering progress for lectures removed from a course. The frontend intersects with the current
  outline.
- Resetting progress when an enrollment is canceled. Progress is kept and shown again on
  reactivation.
- Domain events and certificates.
- Per-block (video, quiz, flashcard) tracking.

## Decisions

| Topic | Decision |
|---|---|
| Approach | Port Hitox `progress` as a new context and trim tenancy; match the frontend contract |
| Context | `internal/progress`, schema `progress` |
| Write | Only the user themselves, active enrollment, published course, lecture in the course |
| No regression | `completed` is never overwritten by `in_progress`; that write is a no-op |
| Read one course | The user themselves, or a manager of the course |
| Read all courses | The user themselves, or `root_admin` (as for `GET /v1/users/{userID}/enrollments`) |
| Missing enrollment | `409 enrollment_required`, as the frontend expects |
| Course facts | Courseauthoring's `Facts` gains the course's lecture IDs |
| Enrollment check | Enrollment's `AccessQuery.IsActivelyEnrolled` through a port |
| Cancel | Progress is kept |
| Events | None |

Two changes from Hitox close gaps there: a manager can no longer write another user's progress,
and the manager check is per course instead of tenant-wide.

## Domain (`internal/progress/domain`)

```go
type LectureState string

const (
    LectureStateInProgress LectureState = "in_progress"
    LectureStateCompleted  LectureState = "completed"
)

type LectureProgress struct {
    LectureID  id.ID
    State      LectureState
    PositionMs int64
    UpdatedAt  time.Time
}

type CourseProgress struct {
    CourseID      id.ID
    UserID        id.ID
    LastLectureID id.ID // zero when nothing was recorded
    Lectures      []LectureProgress // ordered by LectureID
    UpdatedAt     time.Time
}

type ActivityDay struct {
    Date         time.Time // UTC midnight
    LectureCount int
}

// CourseFacts is what progress knows about a course, supplied by the application layer.
type CourseFacts struct {
    Published  bool
    OwnerID    id.ID
    LectureIDs []id.ID
}
```

- `NewLectureProgress(lectureID, state, positionMs, now)` returns `ErrInvalidLectureState` for an
  unknown state and `ErrNegativePosition` for `positionMs < 0`.
- `CourseProgress.CompletedLectureIDs()` returns completed lecture IDs in ID order.
- The no-regression rule is enforced by the repository's upsert, because concurrent writes from
  two tabs must not race it in application code. The domain documents the rule; the integration
  test proves it.

Access rules are pure functions over `auth.Principal`, `CourseFacts` and the target user ID. A
manager is the course owner or any `root_admin`.

| Action | Allowed | Otherwise |
|---|---|---|
| Record | caller is the target user, course published, lecture in `LectureIDs` | other user → `ErrForbidden`; unpublished → `ErrCourseHidden`; lecture not in course → `ErrLectureNotFound` |
| Read one course | caller is the target user, or a manager | not a manager and course unpublished → `ErrCourseHidden`; otherwise `ErrForbidden` |
| Read all courses | caller is the target user, or `root_admin` | `ErrForbidden` |

Reading one's own course progress needs no course lookup, so a student whose course was archived
still reads their progress.

## Application (`internal/progress/app`)

Ports:

```go
// Repository reads and writes progress in the current transaction.
type Repository interface {
    // RecordLecture upserts the lecture row and, when that row changed, sets the course's
    // last lecture and updated time. An in_progress write over a completed row changes nothing.
    RecordLecture(ctx context.Context, courseID, userID id.ID, p domain.LectureProgress) error
    // FindCourse returns false when the user has no progress in the course.
    FindCourse(ctx context.Context, courseID, userID id.ID) (domain.CourseProgress, bool, error)
    // FindByUser returns every course with progress, most recently updated first.
    FindByUser(ctx context.Context, userID id.ID) ([]domain.CourseProgress, error)
    ActivityByUser(ctx context.Context, userID id.ID) ([]domain.ActivityDay, error)
}

type TxRunner interface {
    RunInTx(ctx context.Context, fn func(Repository) error) error
}

// CourseCatalog returns ErrNotFound when the course does not exist.
type CourseCatalog interface {
    CourseFacts(ctx context.Context, courseID id.ID) (domain.CourseFacts, error)
}

type EnrollmentQuery interface {
    IsActivelyEnrolled(ctx context.Context, courseID, userID id.ID) (bool, error)
}
```

`Service` depends on `TxRunner`, `CourseCatalog`, `EnrollmentQuery` and `clock.Clock`:

- `RecordLecture(ctx, p, courseID, lectureID, userID, state, positionMs) error`: validates input
  (`ErrInvalidInput`), reads course facts, applies the record rule, checks active enrollment
  (`ErrEnrollmentRequired`), then runs `Repository.RecordLecture` in a transaction. Course facts
  and the enrollment check run before the transaction opens, because each takes its own pool
  connection (see 32ede6d).
- `CourseProgress(ctx, p, courseID, userID) (domain.CourseProgress, error)`: when `p.UserID` is
  not `userID`, reads course facts and applies the read rule. With no stored progress it returns
  an empty `CourseProgress` for the course and user.
- `UserProgress(ctx, p, userID) ([]domain.CourseProgress, []domain.ActivityDay, error)`.

App errors: `ErrNotFound`, `ErrInvalidInput`, `ErrEnrollmentRequired`. Domain access errors pass
through unchanged.

## Courseauthoring changes

`app.CourseFacts` gains `LectureIDs []id.ID`, in course order. `CourseService.Facts` already loads
the whole aggregate, so this adds no query. Enrollment's `courseCatalog` adapter ignores the new
field. `TestFacts` compares the struct with `!=`, which no longer compiles for a struct with a
slice, and moves to field comparisons.

## Persistence (`internal/progress/adapters/postgres`)

Migration `migrations/00005_progress.sql`:

```sql
-- +goose Up
CREATE SCHEMA progress;

CREATE TABLE progress.course_progress (
  course_id       bigint NOT NULL,
  user_id         bigint NOT NULL,
  last_lecture_id bigint NOT NULL,
  updated_at      timestamptz NOT NULL,
  PRIMARY KEY (course_id, user_id)
);

CREATE INDEX course_progress_user_idx ON progress.course_progress (user_id, updated_at DESC);

CREATE TABLE progress.lecture_progress (
  course_id   bigint NOT NULL,
  user_id     bigint NOT NULL,
  lecture_id  bigint NOT NULL,
  state       text NOT NULL CHECK (state IN ('in_progress', 'completed')),
  position_ms bigint NOT NULL CHECK (position_ms >= 0),
  updated_at  timestamptz NOT NULL,
  PRIMARY KEY (course_id, user_id, lecture_id)
);

CREATE INDEX lecture_progress_user_updated_idx ON progress.lecture_progress (user_id, updated_at);

-- +goose Down
DROP SCHEMA progress CASCADE;
```

Hitox's surrogate `id` and `version` columns are dropped: rows are keyed by (course, user) and
written only by upserts.

Queries (sqlc):

- `UpsertLectureProgress`: `INSERT ... ON CONFLICT (course_id, user_id, lecture_id) DO UPDATE ...
  WHERE lecture_progress.state <> 'completed' OR EXCLUDED.state = 'completed' RETURNING
  lecture_id`. No row returned means a stale `in_progress` write was rejected; the repository
  then returns without touching `course_progress`.
- `UpsertCourseProgress`: sets `last_lecture_id` and `updated_at`.
- `GetCourseProgress`, `ListLectureProgressForCourse`.
- `ListCourseProgressByUser` and `ListLectureProgressByUser`: two queries for all of a user's
  courses, grouped in Go. Hitox ran one lecture query per course.
- `ListActivityDaysByUser`: `(updated_at AT TIME ZONE 'UTC')::date` and
  `count(DISTINCT lecture_id)`, newest first. Hitox used `date_trunc`, which depends on the
  session time zone.

`TxRunner` uses `pgx.BeginFunc`, as the other contexts do. There is no outbox publisher.

## HTTP API (`internal/progress/adapters/httpapi`)

Every route requires identity's `RequireAuth`. Path IDs that fail to parse return 404.

| Route | Request | Success |
|---|---|---|
| `PUT /v1/courses/{courseID}/lectures/{lectureID}/progress/{userID}` | `{state, position_ms}` | 204 |
| `GET /v1/courses/{courseID}/progress/{userID}` | none | 200; `CourseProgress` |
| `GET /v1/users/{userID}/progress` | none | 200; `UserProgress` |

`CourseProgress`: `course_id`, `user_id`, `last_lecture_id` (omitted when zero),
`completed_lecture_ids` (array, never null), `lectures` (array of `{lecture_id, state,
position_ms, updated_at}`, never null), `updated_at` (omitted when nothing was recorded). IDs are
decimal strings.

`UserProgress`: `user_id`, `courses` (array of `CourseProgress`), `activity_days` (array of
`{date, lecture_count}`).

Errors are RFC 9457 problems via `internal/platform/problem`:

| Status | Type | Cause |
|---|---|---|
| 400 | `invalid_input` | malformed body, unknown `state`, negative `position_ms` |
| 403 | `forbidden` | caller not allowed |
| 404 | `not_found` | course missing or hidden, lecture not in course, unparsable path ID |
| 409 | `enrollment_required` | record without an active enrollment |

The frontend mock answers an unknown state with `invalid_state`; the frontend does not read that
code, and the backend uses `invalid_input` like every other context. Courseauthoring's content
read returns `enrollment_required` as 403; progress returns 409 because the frontend's write path
treats only 409 as permanent.

`api/openapi.yaml` documents the routes, schemas and errors in the same change. README lists the
endpoints.

## Composition (`cmd/api`)

New file `cmd/api/progress.go` holds `registerProgress` and two adapters:

- `progressCourseCatalog` over courseauthoring's `CourseService.Facts`, mapping
  `courseauthoringapp.ErrNotFound` to `progressapp.ErrNotFound`.
- `progressEnrollmentQuery` over enrollment's `AccessQuery`.

`.golangci.yml` gains `progress-domain` and `progress-app` depguard rules, adds the progress
import path to `platform-independent-of-contexts`, and adds it to the deny rule of every other
context. `sqlc.yaml` gains the progress package.

## Testing

- Domain: `NewLectureProgress` validation; `CompletedLectureIDs` order; a table test with one row
  per access-rule outcome.
- App with in-memory fakes: record by self; record for another user; unpublished course; lecture
  outside the course; no active enrollment; invalid state and position; read own progress without
  a course lookup; manager and stranger reads; empty progress; user progress authorization.
- Postgres (`-tags integration`, `pgtest`): first write creates both rows; `completed` survives a
  later `in_progress` write and leaves `last_lecture_id` unchanged; `completed` overwrites
  `in_progress`; `FindByUser` grouping and order; activity days bucket by UTC date and count
  distinct lectures.
- HTTP: each route's status and wire shape, empty arrays instead of null, omitted optional fields,
  each error type, unparsable path IDs.
- Courseauthoring: `Facts` returns lecture IDs in course order.
- `cmd/api` e2e: a student enrolls in a free course, records `in_progress` with a position, then
  `completed`; reads course and user progress; the instructor reads the student's progress; after
  cancellation a write gets 409 `enrollment_required`.

Gates from `AGENTS.md`: `make check`, `make test-integration` (actually run),
`docker compose config`, `make docker-build`, `git diff --check`.

## Risks

- `activity_days` counts each lecture on the day of its latest write only, because writes
  overwrite `updated_at`. A lecture studied on Monday and finished on Tuesday counts only on
  Tuesday. Hitox has the same behavior; an append-only activity log would fix it if the heatmap
  needs to be exact.
- `activity_days` is unbounded. A user's row count is bounded by the lectures they touched, so
  this is small; add a date window if it grows.
- Progress rows for removed lectures stay stored and are returned. The frontend filters them.
- A write costs a course lookup, an enrollment lookup and a transaction: three pool connections in
  sequence, never held at once.
