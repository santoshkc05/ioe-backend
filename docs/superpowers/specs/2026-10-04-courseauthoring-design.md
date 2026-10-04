# Course Authoring Bounded Context Design

Date: 2026-10-04

## Status

Approved in conversation on 2026-10-04. Pending written-spec review.

## Context

`ioe-frontend` (`../ioe-frontend`) is a pnpm monorepo whose course packages (`packages/course-core`, `course-instructor`, `course-student`, `content-*`) were ported from the Hitox frontend. `packages/course-core/src/api/courses.ts` calls Hitox's `courseauthoring` HTTP API almost verbatim: `/v1/courses/...` routes, snake_case JSON, string IDs.

`hitox-backend` (`/Users/hitohospital/personal/dev/hitox/hitox-backend`) implements that API in `internal/courseauthoring` (about 18.8k lines including tests) on top of `internal/shared/contentblocks`, `shared/publication` and `shared/kernel`. It is multi-tenant, uses Kratos-backed RBAC permissions, and has grown a review workflow, versioned publication, and a mobile content track.

This spec ports the course/lecture authoring core into `ioe-backend` as sub-project 1 of: platform foundation (`2026-10-04-platform-snowflake-nethttp-design.md`, a prerequisite), courseauthoring, enrollment, payment (eSewa, Khalti, connectIPS).

## Goals

- Instructors and root admins create and edit courses, sections and lectures, and author lecture content as ordered blocks, through the API the frontend already calls.
- Keep Hitox's autosave protocol (`PATCH .../content` with `base_revision`, state-based diff, 409 with current content) so the frontend editor works unchanged.
- Publish and archive courses. Students read published outlines and free-preview lectures now, and enrolled lectures once enrollment exists.
- Single tenant, Snowflake IDs, `auth.Principal` roles, NPR prices.

## Non-goals

- Review workflow (submit, approve, request changes, unpublish, reviews trail, `/v1/settings/course-publishing`).
- Versioned publication (`view=live` snapshots, `/versions`, `discard-draft`).
- Mobile track, course visibility, lecture-content dirty sweeper, per-lecture domain events.
- Validating `media_asset_id` or `quiz_id` references against media or assessment contexts; they are stored as opaque IDs.
- Catalog listing of published courses, enrollment, payment.

## Decisions

| Topic | Decision |
|---|---|
| Approach | Port Hitox code and trim; do not rewrite |
| Shared block code | `internal/platform/contentblocks`, ported from Hitox `shared/contentblocks` |
| Status lifecycle | `draft` → `published` → `archived`; archive from draft or published; terminal |
| Edits after publish | Allowed and immediately visible (no versioning in this slice) |
| Currency | NPR only; `amount_minor` in paisa |
| Authorization | In the app layer, from `auth.Principal`: owner or `root_admin` manages |
| Student content access | `EnrollmentQuery` port; fail-closed stub until enrollment ships |
| Errors | problem+json; `type` carries Hitox's error codes; 409 content conflict adds extension members |
| Events | `courseauthoring.course.published`, `courseauthoring.course.archived` only |

## Architecture

```text
internal/platform/contentblocks/   Title, TextBody, VideoRef, ImageRef, FlashcardDeck, Block,
                                   block kinds, JSONB payload codec, rich-text sanitizer
internal/courseauthoring/
  domain/      Course aggregate, Section, Lecture, LectureContent, Price, events, errors
  app/         CourseService, LectureContentService, ports, views (DTOs)
  adapters/postgres/   sqlc queries + sqlcgen, TxRunner, repositories, outbox publisher
  adapters/httpapi/    handlers, wire DTOs, error mapping, Register(*httpserver.Router)
migrations/00003_courseauthoring.sql
```

### Dependency rules

- `courseauthoring-domain` (strict): `$gostd`, `platform/id`, `platform/auth`, `platform/contentblocks`.
- `courseauthoring-app` (strict): `$gostd`, its domain, `platform/id`, `platform/auth`, `platform/clock`, `platform/contentblocks`.
- `platform/contentblocks` imports only `$gostd`, `platform/id`, `github.com/microcosm-cc/bluemonday`, `golang.org/x/net/html`.
- Add `internal/courseauthoring` to `platform-independent-of-contexts`, and deny rules between courseauthoring and each of identity and notification, both directions.

## Porting map

| Hitox source | Destination | Treatment |
|---|---|---|
| `shared/contentblocks/*` | `platform/contentblocks` | Port; `kernel.ID` → `id.ID`; keep tests |
| `shared/kernel` (Money, Aggregate, events, tenant, clock) | — | Not ported. `Price` lives in courseauthoring domain; time from `platform/clock`; events follow identity's `EventName()` style |
| `shared/publication` | — | Not ported; three-state status inline in `Course` |
| `courseauthoring/domain/{course,lecture,section,value_objects,errors,events}.go` | `courseauthoring/domain` | Port; drop tenant, governance, visibility, live version, mobile; keep tests that survive |
| `domain/{mobile_chapter,course_versions,access}.go` | — | Drop |
| `application/{course_service,lecture_content_service,dto,errors,ports}.go` | `courseauthoring/app` | Port; drop versions, reviews, policy, mobile, media/quiz/user/authz ports; add principal-based authorization |
| `application/{mobile_track_service,version_document}.go` | — | Drop |
| `adapters/postgres/{course_repository,lecture_content_repository,unit_of_work,helpers,errors}.go`, `queries/courses.sql` | `courseauthoring/adapters/postgres` | Port to the identity `TxRunner` pattern, no tenant columns |
| `adapters/postgres/{dirty_sweeper,event_tracker,course_review_repository,course_version_repository,lecture_access_repository,mobile_track_repository}.go` | — | Drop |
| `adapters/httpapi/{handlers_courses,dto,respond,router}.go` | `courseauthoring/adapters/httpapi` | Port to `net/http` patterns and problem+json |
| `adapters/httpapi/{rate_limiter,handlers_mobile_track}.go` | — | Drop; use `httpserver.RateLimiter` |

## Domain (`internal/courseauthoring/domain`)

### Course aggregate

Fields: `ID id.ID`, `OwnerID id.ID`, `Title contentblocks.Title`, `Description string`, `Level`, `ThumbnailURL string`, `Price`, `Status`, `Sections []Section`, `Lectures []Lecture`, `Version int64`, `CreatedAt`, `UpdatedAt time.Time`.

Invariants:

- Title 1-200 characters after trimming (`contentblocks.Title`).
- `Level` is `""`, `beginner`, `intermediate` or `advanced`.
- `ThumbnailURL` is `""` or an absolute `http`/`https` URL.
- `Status` transitions: `Publish` only from `draft` and only with at least one lecture (`ErrCourseHasNoLectures`); `Archive` from `draft` or `published`; anything else is `ErrInvalidStatusTransition`.
- Every mutation on an archived course fails with `ErrCourseNotEditable`.
- Section titles are unique within a course (`ErrDuplicateSectionTitle`).
- Lecture `order` is a dense 0-based sequence across the whole course; reorder must name exactly the current lecture set.
- Removing a section moves its lectures to unsectioned (`SectionID` zero).
- Every mutation sets `UpdatedAt` from the injected clock.

Operations (ported from Hitox `course.go`): `NewCourse`, `UpdateDetails`, `SetPrice`, `AddSection`, `RenameSection`, `RemoveSection`, `AddLecture`, `RenameLecture`, `RemoveLecture`, `ReorderLectures`, `MoveLectureToSection`, `SetLectureFreePreview`, `Publish`, `Archive`, `IsManagedBy(auth.Principal)`. `Rehydrate` is used only by the repository.

The aggregate does not hold block content. Hitox's aggregate did, which forced its `Save` to diff and rewrite every lecture's blocks under row locks on every structural edit. Here content is read and written only through `LectureContentRepository`, so structural writes never touch `lecture_blocks`.

### Price

`Price{AmountMinor int64, Currency string}`. `NewPrice(amountMinor, currency)`:

- `amountMinor < 0` → `ErrInvalidPrice`.
- `amountMinor == 0` → free; the stored currency is `""` regardless of input.
- `amountMinor > 0` → currency must be `"NPR"`, else `ErrUnsupportedCurrency`.

`IsFree()` reports `AmountMinor == 0`.

### Lecture and content

`Lecture{ID, SectionID id.ID (zero = unsectioned), Title, FreePreview bool, Order int, HasText, HasVideo bool}`. `HasText`/`HasVideo` are read-only facts the repository hydrates from `lecture_blocks`; a new lecture's flags come from its initial content.

`LectureContent` (`NewLectureContent(blocks)`) is the validated value the app builds before writing blocks. It is an ordered `[]contentblocks.Block`. Block kinds: `text` (sanitized rich text, at most 1 MiB), `video` (URL or `media_asset_id`, plus `duration_ms`), `image`, `flashcard` (1-40 cards), `quiz` (opaque `quiz_id`). Each block has a server `ID id.ID` and a `ClientBlockID` (at most 64 characters, unique within the lecture). Validation, limits and sanitization are exactly Hitox's.

`LectureContent.HasText()` / `HasVideo()` derive from the blocks. `AddLecture` with initial content adds the lecture to the aggregate, saves it, then writes the blocks with `ReplaceBlocks` in the same transaction. The legacy request fields `text_body`, `video_url` and `video_duration_ms` are converted into a text block and a video block; the legacy response fields mirror the first text and first video block.

### Events

Written to the outbox in the same transaction as the state change:

| Event | Payload |
|---|---|
| `courseauthoring.course.published` | `course_id`, `owner_id`, `price_amount_minor`, `price_currency`, `occurred_at` |
| `courseauthoring.course.archived` | `course_id`, `owner_id`, `occurred_at` |

No consumer exists yet; enrollment and catalog are the intended consumers.

## Application (`internal/courseauthoring/app`)

### Ports

```go
type CourseRepository interface {
    FindByID(ctx context.Context, id id.ID) (domain.Course, error)          // ErrNotFound
    ListByOwner(ctx context.Context, ownerID id.ID) ([]domain.Course, error)
    Insert(ctx context.Context, c domain.Course) error
    Update(ctx context.Context, c domain.Course) error                       // ErrConcurrentModification on version mismatch; bumps Version
}

type LectureContentRepository interface { // ported from Hitox; no tenant parameter
    FindLecture(ctx, courseID, lectureID id.ID) (LectureContentHeader, error)
    FindLectureForUpdate(ctx, courseID, lectureID id.ID) (LectureContentHeader, error)
    ListBlocks(ctx, lectureID id.ID) ([]contentblocks.Block, error)
    ApplyPatch(ctx, courseID, lectureID id.ID, baseRevision int64, plan BlockWritePlan) (int64, error) // ErrRevisionConflict
    ReplaceBlocks(ctx, courseID, lectureID id.ID, blocks []contentblocks.Block) (int64, error)         // bumps revision
}

type EventPublisher interface { Publish(ctx context.Context, events ...domain.Event) error }

type Repos struct { Courses CourseRepository; Contents LectureContentRepository; Events EventPublisher }
type TxRunner interface { RunInTx(ctx context.Context, fn func(Repos) error) error }

type EnrollmentQuery interface {
    IsActivelyEnrolled(ctx context.Context, courseID, userID id.ID) (bool, error)
}
```

Services take `*id.Generator` and `clock.Clock`.

### Authorization

Every use case takes the caller's `auth.Principal`. A *manager* is the course owner or any `root_admin`.

| Use case | Rule | Failure |
|---|---|---|
| Create course | role `instructor` or `root_admin`; `owner_id` absent or equal to the caller | `ErrForbidden` |
| List courses by owner | caller is that owner or `root_admin` | `ErrForbidden` |
| Get course | manager: any status; anyone else: `published` only | `ErrNotFound` (drafts and archived courses are not revealed) |
| Any structural or content write, publish, archive | manager | `ErrNotFound` if the caller cannot see the course, otherwise `ErrForbidden` |
| Get lecture content | manager; otherwise course `published` and (lecture `free_preview` or `EnrollmentQuery` true) | `ErrNotFound` if the course is not visible; `ErrEnrollmentRequired` otherwise |

Until enrollment ships, `cmd/api` wires `EnrollmentQuery` to an adapter that always returns `false`.

### Content writes

- `PATCH` (Hitox `LectureContentService.Patch`): lock the lecture row with `FindLectureForUpdate` first, so concurrent patches on one lecture serialize and the loser sees a revision mismatch with a consistent current view; validate sizes, duplicate IDs, order/delete overlap; check `set(order) == (existing ∪ upserts) − deletes` (`ErrBlockSetMismatch`); new blocks get Snowflake IDs; `ApplyPatch` compares and swaps `content_revision`. A conflict returns `ErrRevisionConflict` together with the current content view so the handler can return it. Missing `base_revision` is `ErrRevisionRequired`.
- `PUT` replaces the whole block list under `FindLectureForUpdate` and bumps `content_revision`, so a stale PATCH from another tab conflicts.
- Both check that the course is a manager's and not archived. Neither loads the full aggregate nor bumps `courses.version`.

## Persistence

`migrations/00003_courseauthoring.sql` (goose, schema `courseauthoring`):

```sql
CREATE SCHEMA courseauthoring;

CREATE TABLE courseauthoring.courses (
  id                 bigint PRIMARY KEY,
  owner_id           bigint NOT NULL,
  title              text NOT NULL,
  description        text NOT NULL DEFAULT '',
  level              text NOT NULL DEFAULT '' CHECK (level IN ('', 'beginner', 'intermediate', 'advanced')),
  thumbnail_url      text NOT NULL DEFAULT '' CHECK (thumbnail_url = '' OR thumbnail_url ~ '^https?://'),
  status             text NOT NULL CHECK (status IN ('draft', 'published', 'archived')),
  price_amount_minor bigint NOT NULL DEFAULT 0 CHECK (price_amount_minor >= 0),
  price_currency     text NOT NULL DEFAULT '',
  version            bigint NOT NULL,
  created_at         timestamptz NOT NULL,
  updated_at         timestamptz NOT NULL,
  CONSTRAINT courses_price_consistent CHECK (
    (price_amount_minor = 0 AND price_currency = '') OR
    (price_amount_minor > 0 AND price_currency = 'NPR'))
);
CREATE INDEX courses_owner_idx ON courseauthoring.courses (owner_id);
CREATE INDEX courses_published_idx ON courseauthoring.courses (id) WHERE status = 'published';

CREATE TABLE courseauthoring.sections (
  id         bigint PRIMARY KEY,
  course_id  bigint NOT NULL REFERENCES courseauthoring.courses (id) ON DELETE CASCADE,
  title      text NOT NULL,
  sort_order integer NOT NULL,
  UNIQUE (course_id, title)
);

CREATE TABLE courseauthoring.lectures (
  id               bigint PRIMARY KEY,
  course_id        bigint NOT NULL REFERENCES courseauthoring.courses (id) ON DELETE CASCADE,
  section_id       bigint REFERENCES courseauthoring.sections (id) ON DELETE SET NULL,
  title            text NOT NULL,
  free_preview     boolean NOT NULL DEFAULT false,
  sort_order       integer NOT NULL,
  content_revision bigint NOT NULL DEFAULT 0,
  created_at       timestamptz NOT NULL,
  updated_at       timestamptz NOT NULL
);
CREATE INDEX lectures_course_idx ON courseauthoring.lectures (course_id, sort_order);

CREATE TABLE courseauthoring.lecture_blocks (
  id              bigint PRIMARY KEY,
  lecture_id      bigint NOT NULL REFERENCES courseauthoring.lectures (id) ON DELETE CASCADE,
  course_id       bigint NOT NULL,
  kind            text NOT NULL CHECK (kind IN ('text', 'video', 'quiz', 'image', 'flashcard')),
  position        integer NOT NULL,
  client_block_id text NOT NULL CHECK (length(client_block_id) BETWEEN 1 AND 64),
  payload         jsonb NOT NULL,
  created_at      timestamptz NOT NULL,
  updated_at      timestamptz NOT NULL,
  UNIQUE (lecture_id, client_block_id),
  CONSTRAINT lecture_blocks_position_unique UNIQUE (lecture_id, position) DEFERRABLE INITIALLY DEFERRED
);

-- +goose Down
DROP SCHEMA courseauthoring CASCADE;
```

- `owner_id` has no foreign key to `identity.users`; a context writes and references only its own schema.
- `courses.version` guards structural writes; `lectures.content_revision` guards content writes.
- The repository saves an aggregate by updating the course row with `WHERE version = $n`, then upserting sections, upserting lectures (never touching `content_revision`), deleting lectures not in the aggregate, and deleting sections not in the aggregate, in that order, in one transaction. It never writes `lecture_blocks`.
- `sqlc.yaml` gains a second `sql` entry: `internal/courseauthoring/adapters/postgres/queries.sql` → `sqlcgen`.

## HTTP API (`internal/courseauthoring/adapters/httpapi`)

All routes are wrapped with identity's `RequireAuth`. Request and response shapes match `ioe-frontend/packages/course-core/src/api/courses.ts`. Request bodies use `httpserver.DecodeJSON` (unknown fields rejected).

| Method and path | Request | Success |
|---|---|---|
| `POST /v1/courses` | `{owner_id?, title, description}` | `201` course |
| `GET /v1/users/{ownerID}/courses` | — | `200 {courses, total}` |
| `GET /v1/courses/{courseID}` | `?view=live\|draft` accepted and ignored | `200` course |
| `PATCH /v1/courses/{courseID}` | `{title, description, thumbnail_url, level}` | `204` |
| `POST /v1/courses/{courseID}/price` | `{amount_minor, currency}` | `200` course |
| `POST /v1/courses/{courseID}/publish` | — | `204` |
| `POST /v1/courses/{courseID}/archive` | — | `204` |
| `POST /v1/courses/{courseID}/sections` | `{title}` | `201` course |
| `PATCH /v1/courses/{courseID}/sections/{sectionID}` | `{title}` | `204` |
| `DELETE /v1/courses/{courseID}/sections/{sectionID}` | — | `204` |
| `POST /v1/courses/{courseID}/lectures` | `{title, text_body?, video_url?, video_duration_ms?}` | `201` course |
| `PATCH /v1/courses/{courseID}/lectures/{lectureID}` | `{title}` | `204` |
| `DELETE /v1/courses/{courseID}/lectures/{lectureID}` | — | `204` |
| `PUT /v1/courses/{courseID}/lectures-order` | `{lecture_ids}` | `204` |
| `POST /v1/courses/{courseID}/lectures/{lectureID}/section` | `{section_id}` (`""` = unsectioned) | `204` |
| `POST /v1/courses/{courseID}/lectures/{lectureID}/free-preview` | `{free_preview}` | `204` |
| `GET /v1/courses/{courseID}/lectures/{lectureID}/content` | `?view=` ignored | `200` lecture content |
| `PUT /v1/courses/{courseID}/lectures/{lectureID}/content` | `{blocks}` or legacy `{text_body, video_url, video_duration_ms}` | `204` |
| `PATCH /v1/courses/{courseID}/lectures/{lectureID}/content` | `{base_revision, order, upserts, deletes}` | `200 {content_revision}` |

Course response: `id`, `owner_id`, `title`, `description`, `status`, `price {amount_minor, currency}`, `is_free`, `sections [{id, title, order}]`, `lectures [{id, section_id?, title, has_text, has_video, free_preview, order}]`, `thumbnail_url`, `level`, `created_at`, `updated_at`. Governance and versioning fields are omitted; the frontend treats them as optional.

Lecture content response: `lecture_id`, `course_id`, `title`, `text_body`, `video_url`, `video_duration_ms`, `free_preview`, `content_revision`, `blocks` (Hitox's `ContentBlockWire` shape).

All IDs are JSON strings. Path IDs that fail `id.Parse` return `404 not_found`.

`PUT` and `PATCH .../content` are rate-limited with `httpserver.RateLimiter` at 60 requests per minute per `(user, lecture)` key. Over the limit: `429 rate_limited` with `Retry-After: 60`.

### Errors

problem+json. `type` carries the machine code, reusing Hitox's codes so frontend branches keep matching:

| Status | `type` |
|---|---|
| 400 | `invalid_request` (malformed JSON, platform), `invalid_input` (domain validation), `unsafe_content`, `invalid_client_block_id`, `duplicate_client_block_id`, `order_delete_overlap`, `block_set_mismatch`, `patch_too_large`, `lecture_content_revision_required`, `duplicate_title`, `empty_course`, `unsupported_currency` |
| 403 | `forbidden`, `enrollment_required` |
| 404 | `not_found` |
| 409 | `concurrent_modification`, `course_not_editable`, `invalid_transition`, `revision_conflict` |
| 429 | `rate_limited` |

The `revision_conflict` 409 for `PATCH .../content` adds the lecture content response fields as RFC 9457 extension members next to `type`, `title`, `status`, `instance`. This needs a platform helper, `problem.WriteWithExtensions(w, r, status, typ, title, detail string, ext map[string]any)`, which rejects extension keys that collide with standard members.

`api/openapi.yaml` documents every route, schema and error in the same change.

## Composition (`cmd/api`)

`registerCourseAuthoring` builds the postgres `TxRunner`, the two services (with the shared `*id.Generator` and clock), the deny-all `EnrollmentQuery` adapter, and the HTTP handler, then calls `Register(router)`. The identity handler's `RequireAuth` is passed in as middleware; courseauthoring does not import identity.

## Frontend follow-up (outside this repository)

`packages/course-core/src/api/http.ts` reads `problem.code`. Change it to read `problem.type ?? problem.code`. No other frontend change is required for this slice.

## Testing

- `platform/contentblocks`: Hitox's block, codec, richdoc and sanitize tests, ported.
- Domain: ported `course_test`, `lecture_content_blocks_test` and `value_objects_test`, minus governance, visibility and version cases; new tests for `Price`, the three-state lifecycle, and the archived-is-read-only rule.
- App: ported `course_service_test` and `lecture_content_service_test` with in-memory fakes; one test per authorization-table row, including the deny-all enrollment adapter.
- Postgres (`-tags integration`, `pgtest`): aggregate round trip, version conflict, PATCH revision compare-and-swap, deferrable reorder, two concurrent PATCHes with exactly one winner (ported `lecture_content_concurrency_test`), section delete sets `section_id` null.
- HTTP: response shapes against the frontend wire fields, each error code, the 409 extension members, 429 with `Retry-After`, path-ID parse failure as 404.
- `cmd/api` e2e: instructor creates a course, adds a section and a lecture, patches content, publishes; a student reads the outline, reads a free-preview lecture, and gets `403 enrollment_required` on another lecture.

Gates from `AGENTS.md`: `make check`, `make test-integration` (actually run), `docker compose config`, `make docker-build`, `git diff --check`.

## Risks

- Edits to a published course are live immediately. Students can see half-finished edits. Versioned publication is the planned fix.
- `media_asset_id` and `quiz_id` are unchecked. A block can reference an asset or quiz that never exists; the reader must tolerate it. Validation arrives with the media and assessment contexts.
- The in-memory rate limiter is per process. With several replicas the effective limit multiplies. Acceptable while the backend runs as one instance.
- The deny-all enrollment adapter makes paid and free non-preview lectures unreadable to students until sub-project 2 lands.
