# Catalog Design

Date: 2026-10-06

## Status

Approved in conversation on 2026-10-06. Pending written-spec review.

## Context

Students can enroll in published courses, but they can only reach a course if they already know
its ID. `POST /v1/courses` creates courses, `GET /v1/users/{ownerID}/courses` lists one owner's
courses, and `GET /v1/courses/{courseID}` reads one course. Every courseauthoring route requires
a bearer token. The courseauthoring and enrollment specs both list catalog listing as a non-goal.

`GET /v1/courses/{courseID}` already hides drafts and archived courses from non-managers through
`visible()` in `internal/courseauthoring/app/course_service.go`. The migration
`00003_courseauthoring.sql` already has `courses_published_idx (id) WHERE status = 'published'`.

`ioe-frontend` already has a catalog client, ported from hitox
(`packages/course-core/src/api/catalog.ts`):

- `listPublishedCourses` calls `GET /v1/courses` and reads only `courses` from
  `{courses, total}`.
- Items carry `lecture_count`, `section_count`, `category_ids`, `category_names`, `tags`,
  `created_at` and `updated_at` in addition to the course fields.
- `getPublishedCourse` calls `GET /v1/catalog/courses/{id}`. Search and categories call
  `/v1/catalog/courses/search`, `/v1/catalog/categories` and
  `/v1/catalog/categories/{id}/courses`.
- `courseApiRequest` always sends `Authorization: Bearer <token>`.

The backend contract in this spec leads. The frontend migrates to it as a follow-up (see
Frontend follow-up).

## Goals

- Anyone, signed in or not, lists published courses newest first, filtered by level and by free
  or paid, one page at a time.
- Anyone reads a published course's outline (sections and lecture titles) without signing in.
- Signed-in managers keep seeing their drafts and archived courses through the detail endpoint.
- Lecture content stays gated exactly as today.

## Non-goals

- Search, categories, tags.
- Instructor names. Courses expose `owner_id` only.
- Ordering by publication time. There is no `published_at` column.
- Public free-preview lecture content.
- HTTP caching headers.
- A total count of matching courses.

## Decisions

| Topic | Decision |
|---|---|
| Audience | Public: no sign-in needed for the list or the detail of a published course |
| Owner | `courseauthoring`; no new context, no events, no projection |
| List route | `GET /v1/courses` (POST on the same path still creates courses) |
| Filters | `level` (`beginner`, `intermediate`, `advanced`) and `price` (`free`, `paid`) |
| Order | `id DESC`; Snowflake IDs sort by creation time |
| Pagination | Keyset: `limit` (default 20, max 50) and an opaque `cursor`; `next_cursor` only when more remain |
| Item shape | `CourseSummary`: course fields plus `lecture_count` and `section_count`; no outline |
| Detail | `GET /v1/courses/{courseID}` becomes optionally authenticated |
| Invalid token | A present but invalid bearer token is `401` on public routes, never a downgrade to anonymous |
| Rate limit | 120 requests per minute per client IP, shared by both public routes |

A separate `catalog` context with a read model was rejected: it needs per-course domain events
that courseauthoring does not emit (`SetPrice`, `UpdateDetails`) and a second outbox consumer.
Reading the `courseauthoring` schema from another context breaks the one-schema-per-context rule.

Ordering is by creation, not by publication: a course drafted long ago and published today
appears below newer courses. Adding `published_at` needs a migration and a backfill and is left
for when the frontend needs it.

Keyset pagination is stable while courses are published or archived between page requests; offset
pagination would skip or repeat courses.

## HTTP

### `GET /v1/courses`

Public. No `Authorization` header is needed; when one is present it must be valid (see
Authentication).

Query parameters:

| Name | Values | Default |
|---|---|---|
| `level` | `beginner`, `intermediate`, `advanced` | any level, including unset |
| `price` | `free`, `paid` | both |
| `limit` | 1 to 50 | 20 |
| `cursor` | the `next_cursor` of a previous page | first page |

`price=free` means `price_amount_minor = 0`; `price=paid` means `price_amount_minor > 0`.
Any other value of any parameter, including an empty `level`, is `400` with problem type
`invalid_input`. Unknown parameters are ignored.

`200` body (`CatalogPage`):

```json
{
  "courses": [
    {
      "id": "1844674407370955",
      "owner_id": "1844674407370001",
      "title": "Circuit Analysis",
      "description": "…",
      "level": "beginner",
      "thumbnail_url": "https://…",
      "price": { "amount_minor": 0, "currency": "" },
      "is_free": true,
      "lecture_count": 12,
      "section_count": 3,
      "created_at": "2026-10-01T09:00:00Z",
      "updated_at": "2026-10-03T12:00:00Z"
    }
  ],
  "next_cursor": "1844674407370955"
}
```

`courses` is always an array, empty when nothing matches. `next_cursor` is omitted on the last
page. The cursor is the last returned course ID; the contract calls it opaque so it can change.
A cursor that is not a valid ID is `400 invalid_input`. A valid ID that matches no course is not
an error: the page holds the published courses with smaller IDs.

`429` with `Retry-After` when the client IP exceeds the limit.

### `GET /v1/courses/{courseID}`

Response unchanged (`Course`). Authentication becomes optional:

| Caller | Draft or archived | Published |
|---|---|---|
| Anonymous | `404` | `200` |
| Signed-in non-manager | `404` | `200` |
| Owner or root admin | `200` | `200` |

The other courseauthoring routes, including `/content`, still require authentication.

### Authentication

`identity/adapters/httpapi` gains `OptionalAuth`, next to `RequireAuth`:

- No `Authorization` header: call the next handler with no principal in the context.
- A header that is not `Bearer <token>`, or a token that fails verification: `401` with the same
  `WWW-Authenticate` header and problem type as `RequireAuth`.
- A valid token: put the principal in the context, as `RequireAuth` does.

Both middlewares share one function that parses and verifies the header.

Courseauthoring's `principal(r)` returns the zero `auth.Principal` when the context holds none.
`visible()` already treats the zero principal as a non-manager, because
`Course.IsManagedBy` is true only for `root_admin` or a matching non-zero owner ID. The plan
adds a test for it. `principal(r)`'s comment, which says `RequireAuth` guarantees a principal,
is updated.

## Application (`internal/courseauthoring/app`)

```go
type PriceFilter string

const (
    PriceAny  PriceFilter = ""
    PriceFree PriceFilter = "free"
    PricePaid PriceFilter = "paid"
)

// CatalogQuery selects one page of published courses. After is zero for the first page.
type CatalogQuery struct {
    Level string // empty means any level
    Price PriceFilter
    Limit int
    After id.ID
}

// CourseSummary is a published course without its outline.
type CourseSummary struct {
    ID           id.ID
    OwnerID      id.ID
    Title        string
    Description  string
    Level        string
    ThumbnailURL string
    Price        domain.Price
    LectureCount int
    SectionCount int
    CreatedAt    time.Time
    UpdatedAt    time.Time
}

type CatalogPage struct {
    Courses []CourseSummary
    Next    id.ID // zero on the last page
}
```

`CourseRepository` gains:

```go
// ListPublished returns up to q.Limit published courses with ID below q.After (any ID when
// q.After is zero) matching the filters, ordered by ID descending.
ListPublished(ctx context.Context, q CatalogQuery) ([]CourseSummary, error)
```

`CourseService.ListPublished(ctx, q CatalogQuery) (CatalogPage, error)` takes no principal:

1. Validates `q`: `Limit` 1 to 50, `Level` one of the three non-empty levels or empty, `Price`
   one of the three filters. Otherwise `ErrInvalidInput`.
2. Calls the repository with `Limit+1` inside `RunInTx`, like `Get`.
3. When it receives `Limit+1` rows, drops the last and sets `Next` to the ID of the last kept row.

The HTTP handler applies the default limit of 20 when `limit` is absent; the service always
receives an explicit limit.

## Persistence (`internal/courseauthoring/adapters/postgres`)

One sqlc query. No migration.

```sql
-- name: ListPublishedCourses :many
SELECT c.id, c.owner_id, c.title, c.description, c.level, c.thumbnail_url,
       c.price_amount_minor, c.price_currency, c.created_at, c.updated_at,
       (SELECT count(*) FROM courseauthoring.lectures l WHERE l.course_id = c.id) AS lecture_count,
       (SELECT count(*) FROM courseauthoring.sections s WHERE s.course_id = c.id) AS section_count
FROM courseauthoring.courses c
WHERE c.status = 'published'
  AND (sqlc.arg(after)::bigint = 0 OR c.id < sqlc.arg(after))
  AND (sqlc.arg(level)::text = '' OR c.level = sqlc.arg(level))
  AND (sqlc.arg(price)::text = ''
       OR (sqlc.arg(price) = 'free' AND c.price_amount_minor = 0)
       OR (sqlc.arg(price) = 'paid' AND c.price_amount_minor > 0))
ORDER BY c.id DESC
LIMIT sqlc.arg(row_limit);
```

`courses_published_idx` serves the scan and the order. The counts use the existing
`lectures_course_idx` and the `UNIQUE (course_id, title)` index on sections, at most 51 times per
request.

## HTTP adapter (`internal/courseauthoring/adapters/httpapi`)

`Config` gains:

```go
OptionalAuth   httpserver.Middleware
IPs            httpserver.IPResolver
CatalogLimiter *httpserver.RateLimiter
```

Routes:

```go
public := func(f http.HandlerFunc) http.Handler {
    return h.cfg.CatalogLimiter.Middleware(h.cfg.IPs)(h.cfg.OptionalAuth(f))
}
r.Handle("GET /v1/courses", public(h.listCatalog))
r.Handle("GET /v1/courses/{courseID}", public(h.getCourse))
```

The rate limit runs before token verification, so invalid tokens count against the IP.
`listCatalog` parses the query parameters into `app.CatalogQuery`, maps `ErrInvalidInput` to
`400` through the existing `writeError`, and writes `catalogPageWire`. The handler's `CourseService`
interface gains `ListPublished`.

`Register`'s comment changes from "Every route requires authentication" to name the two public
routes.

## Wiring (`cmd/api`)

`registerCourseAuthoring` receives `identityHandler.OptionalAuth`, the shared `IPResolver`, and
`httpserver.NewRateLimiter(120)`. The limit is a fixed value, like the content limiter's 60.

## Contract and docs

- `api/openapi.yaml`: add `get` to `/v1/courses` with no `security` requirement, the four
  parameters, `CatalogPage` and `CourseSummary` schemas, and `400`, `405`, `429`, `500`
  responses. Change `/v1/courses/{courseID}` `get` to `security: [{}, {bearer: []}]` and say that
  anonymous callers see published courses only.
- `README.md`: a Catalog section saying who can list and read courses and that lecture content
  still needs enrollment or a free preview.

## Testing

- App unit tests with the existing fakes: invalid limit, level and price return
  `ErrInvalidInput`; `Limit+1` rows give a `Next`; `Limit` rows or fewer give none.
- Postgres integration test: only published courses appear; `level` and `price` filters;
  counts match the outline; walking pages with `limit=2` over five courses returns each course
  once, in ID order, with no `next_cursor` on the last page.
- Identity HTTP tests for `OptionalAuth`: no header passes with no principal; a malformed header
  and an invalid token are `401`; a valid token sets the principal.
- Courseauthoring HTTP tests: anonymous list; bad parameters are `400`; anonymous detail of a
  published course is `200` and of a draft is `404`; an invalid token on either route is `401`;
  the limiter returns `429`; `POST /v1/courses` without a token is still `401`.

## Frontend follow-up

Not part of this slice; recorded for `ioe-frontend`:

- `getPublishedCourse` moves to `GET /v1/courses/{id}` and its `Course` shape.
- `listPublishedCourses` reads `next_cursor` instead of `total`, and passes `level`, `price`,
  `cursor`.
- `courseApiRequest` omits `Authorization` when the user is signed out, instead of sending an
  empty or missing token.
- Search, categories and tags have no backend; remove or hide them until a slice adds them.

## Risks

- The in-memory rate limiter is per replica. Multi-instance rate limiting is already a non-goal
  in the foundation spec.
- Public routes expose `owner_id`. It is already visible to any signed-in user through
  `GET /v1/courses/{id}`, and it is an opaque Snowflake ID, not a name or email.
