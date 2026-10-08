# Catalog Search, Categories and Tags Design

Date: 2026-10-08

## Status

Approved in conversation on 2026-10-08. Pending written-spec review.

## Context

`GET /v1/courses` lists live courses newest first, filtered by `level` and `price`, with keyset
pagination (`internal/courseauthoring/adapters/postgres/queries.sql`, `ListPublishedCourses`).
It reads each course's live version from `courseauthoring.course_versions`, never the working
copy. The catalog spec listed search, categories and tags as non-goals.

`ioe-frontend` has catalog controls for search, categories and tags with no backend. Its client
expects `category_ids`, `category_names` and `tags` on course items, and calls
`/v1/catalog/courses/search`, `/v1/catalog/categories` and `/v1/catalog/categories/{id}/courses`.
As in the catalog slice, the backend contract here leads and the frontend migrates to it.

Blog already has the same tag rule (`internal/blog/domain/tag.go`) and a title-to-slug function
(`internal/blog/domain/slug.go`).

## Goals

- Anyone searches live courses by text, ranked by relevance, combined with the existing filters.
- A root admin maintains a list of categories; an instructor puts a course in up to 3 of them.
- An instructor gives a course up to 10 free-form tags.
- Anyone filters live courses by category or by tag, and lists categories.
- Categories and tags of a published course change only through publication, like its title.

## Non-goals

- Per-language search configurations, stemming, typo tolerance. Text search is English-only in
  practice and uses the language-neutral `simple` configuration so other languages can be added
  later by changing one generated-column expression.
- Facet counts per filter, a total count of matches.
- Ordering by publication time.
- An admin-curated tag vocabulary.
- The `/v1/catalog/...` paths the frontend currently calls.
- Frontend changes (see Frontend follow-up).

## Decisions

| Topic | Decision |
|---|---|
| Owner | `courseauthoring`; no new context, no events, no projection |
| Categories | Root-admin-managed list; 0 to 3 per course |
| Tags | Free-form, normalized (shared rule with blog); 0 to 10 per course |
| Review | Categories and tags are part of the working copy and are snapshotted into each version |
| Category delete | Removes the category from working copies and from every version, by `ON DELETE CASCADE` |
| Search | PostgreSQL full text, `simple` configuration, stored generated `tsvector` on `course_versions` |
| Search weights | title `A`, tags `B`, description `C` |
| Query parsing | `websearch_to_tsquery('simple', q)`; the last word also matches as a prefix |
| Order | Without `q`: `id DESC` (unchanged). With `q`: `ts_rank DESC, id DESC` |
| Shared code | Tag rule moves to `internal/platform/tags`, slug derivation to `internal/platform/slug` |

A separate `catalog` context with a read model fed by publication events was rejected for the
same reasons as in the catalog spec: a second outbox consumer and replication lag for no gain at
this size. An external search engine was rejected as a new service to run and keep in sync.

Blocking category deletion while courses use it was rejected: old versions would keep most
categories undeletable forever. A version's categories are labels, not reviewed content.

## Shared platform packages

- `internal/platform/tags`: `New(raw) (string, error)` trims and lowercases, then requires
  1 to 32 characters matching `^[a-z0-9]+(-[a-z0-9]+)*$`. `NewList(raw []string)` normalizes,
  drops duplicates keeping first-occurrence order, and rejects more than `Max = 10`. Errors:
  `ErrInvalid`, `ErrTooMany`.
- `internal/platform/slug`: `From(text string, maxLen int) string` lowercases, removes dots,
  replaces runs of other non-`[a-z0-9]` characters with `-`, trims `-`, truncates to `maxLen`
  without a trailing `-`. It may return an empty or short string; callers decide the fallback.
- Blog's `NewTag`/`NewTags` and `SlugFromTitle` call these and map `tags.ErrInvalid` and
  `tags.ErrTooMany` to `ErrInvalidTag` and `ErrTooManyTags`. Blog behavior and wire errors do not
  change. The `kebab` pattern blog still needs for `NewSlug` stays in blog.
- Both packages depend only on the standard library. `.golangci.yml` allows them in
  `<ctx>-domain` rules for blog and courseauthoring.

## Domain (`courseauthoring/domain`)

`Category`:

```go
type Category struct {
    ID        id.ID
    Name      string
    Slug      string
    CreatedAt time.Time
}
```

- `NewCategory(id, name, now)`: name trimmed, 1 to 60 characters, else `ErrInvalidCategoryName`.
  `Slug` is `slug.From(name, 64)`; when it is shorter than 2 characters it is
  `"category-" + id.String()`.
- `Rename(name)` applies the same rules and re-derives the slug.

`Course` gains `CategoryIDs []id.ID` and `Tags []string`:

- `SetClassification(categoryIDs []id.ID, tags []string, now)` calls `BeginEdit` (same status
  rules as `UpdateDetails`: a published course becomes a draft with changes, an archived course
  is rejected), drops duplicate IDs keeping order, rejects more than 3 with
  `ErrTooManyCategories`, and normalizes tags with `tags.NewList`, mapping its errors to
  `ErrInvalidTag` and `ErrTooManyTags`.
- Publishing copies both fields into the version; `DiscardDraft` restores both from live.
- The course version read model (`LiveVersion` reads, `view=live`) carries both fields.

## Application (`courseauthoring/app`)

- `DetailsInput` gains `CategoryIDs *[]id.ID` and `Tags *[]string`. Nil leaves the field
  unchanged. `UpdateDetails` applies details and, when either is non-nil, `SetClassification`
  with the given values merged over the current ones, in the existing `mutate` transaction.
- New port `CategoryRepository`:
  - `Insert(ctx, Category) error`; a duplicate name (case-insensitive) or slug is
    `ErrCategoryExists`.
  - `Update(ctx, Category) error`; same conflict rule; missing is `ErrNotFound`.
  - `Delete(ctx, id) error`; missing is `ErrNotFound`.
  - `Get(ctx, id) (Category, error)`.
  - `List(ctx) ([]CategoryWithCount, error)` ordered by name; `CourseCount` counts courses
    whose live version has the category.
  - `ExistAll(ctx, ids []id.ID) (bool, error)`.
  It is reachable from `Repos` so `UpdateDetails` checks `ExistAll` inside its transaction. A
  missing category is `ErrUnknownCategory`. A category deleted between the check and the write
  fails the foreign key; the adapter maps that violation to `ErrUnknownCategory`.
- New `CategoryService`: `Create(p, name)`, `Rename(p, id, name)`, `Delete(p, id)` require
  `auth.RoleRootAdmin`, else `ErrForbidden`. `List()` is public.
- `CatalogQuery` gains `Q string`, `CategorySlug string`, `Tag string`, and the cursor gains an
  optional rank. `ListPublished` validates:
  - `Q` trimmed, at most 200 characters; an empty `Q` means no text search. The HTTP adapter
    rejects a `q` that is empty after trimming, so whitespace never reaches the application.
  - `Tag`, when set, is normalized with `tags.New`; an invalid tag returns an empty page.
  - A category slug matching no category returns an empty page.
- `CourseSummary` and `Course` reads gain `Categories []CategoryRef` (`ID`, `Name`, `Slug`) and
  `Tags []string`. Public reads come from the live version; managers' working-copy reads from
  the working copy.

## Cursor

- Without `q`, the cursor stays the last course ID, as today. Existing cursors keep working.
- With `q`, the cursor is base64url of JSON `{"id": "<id>", "rank": <float32>, "q": "<hash>"}`,
  where `hash` is the first 8 bytes of SHA-256 of the trimmed `q`, hex-encoded. The next page
  takes rows where `(rank, id) < (cursor.rank, cursor.id)`, comparing the same `ts_rank` value
  the query computes, cast to `real`.
- A cursor that does not decode, a ranked cursor used without `q`, or a `q` hash mismatch is
  `ErrInvalidInput` (`400 invalid_input`), matching today's invalid-cursor handling.

## PostgreSQL

Migration `00014_courseauthoring_catalog_search.sql`:

```sql
CREATE TABLE courseauthoring.categories (
  id         bigint PRIMARY KEY,
  name       text NOT NULL,
  slug       text NOT NULL UNIQUE,
  created_at timestamptz NOT NULL
);
CREATE UNIQUE INDEX categories_name_key ON courseauthoring.categories (lower(name));

ALTER TABLE courseauthoring.courses ADD COLUMN tags text[] NOT NULL DEFAULT '{}';

CREATE TABLE courseauthoring.course_categories (
  course_id   bigint NOT NULL REFERENCES courseauthoring.courses (id) ON DELETE CASCADE,
  category_id bigint NOT NULL REFERENCES courseauthoring.categories (id) ON DELETE CASCADE,
  position    integer NOT NULL,
  PRIMARY KEY (course_id, category_id)
);

ALTER TABLE courseauthoring.course_versions
  ADD COLUMN tags text[] NOT NULL DEFAULT '{}',
  ADD COLUMN search tsvector GENERATED ALWAYS AS (
    setweight(to_tsvector('simple', title), 'A') ||
    setweight(to_tsvector('simple', array_to_string(tags, ' ')), 'B') ||
    setweight(to_tsvector('simple', description), 'C')) STORED;

CREATE TABLE courseauthoring.course_version_categories (
  course_id   bigint NOT NULL,
  number      integer NOT NULL,
  category_id bigint NOT NULL REFERENCES courseauthoring.categories (id) ON DELETE CASCADE,
  position    integer NOT NULL,
  PRIMARY KEY (course_id, number, category_id),
  FOREIGN KEY (course_id, number)
    REFERENCES courseauthoring.course_versions (course_id, number) ON DELETE CASCADE
);
CREATE INDEX course_version_categories_category_idx
  ON courseauthoring.course_version_categories (category_id);

CREATE INDEX course_versions_search_idx ON courseauthoring.course_versions USING gin (search);
CREATE INDEX course_versions_tags_idx ON courseauthoring.course_versions USING gin (tags);
```

`position` keeps the instructor's category order. Existing rows get empty tags, no categories,
and a computed `search`. The down migration drops the new tables, indexes and columns.

Queries:

- `ListPublishedCourses` gains `q`, `category_slug` and `tag` arguments. Text matching uses
  `websearch_to_tsquery('simple', q)` combined with `to_tsquery('simple', last_word || ':*')`
  by `&&`, where the application passes `last_word` already reduced to `[a-z0-9]` characters
  (empty means no prefix term). A `q` that yields an empty tsquery matches nothing. The tag
  filter is `v.tags @> ARRAY[tag]`; the category filter joins `course_version_categories` and
  `categories` on slug. Rank is `ts_rank(v.search, query)::real`.
- Course and version saves write `tags`, `course_categories` and, at publish,
  `course_version_categories`.
- Category reads join `course_version_categories` on `courses.live_version` for `course_count`.
- Summary reads load categories for a page of courses in one query, ordered by `position`.

## HTTP

Public (optional bearer, shared catalog rate limit of 120 requests per minute per client IP):

- `GET /v1/courses` accepts `q`, `category` (slug) and `tag` in addition to `level`, `price`,
  `limit` and `cursor`. Empty values are `400 invalid_input`, as for existing filters. A `q`
  over 200 characters is `400 invalid_input`.
- `GET /v1/categories` returns `{"categories": [{"id", "name", "slug", "course_count"}]}`
  ordered by name, unpaginated.
- `CourseSummary` and `Course` gain `categories: [{id, name, slug}]` and `tags: [string]`.

Root admin:

- `POST /v1/categories` `{"name"}` creates; `201` with the category.
- `PATCH /v1/categories/{categoryID}` `{"name"}` renames; `200` with the category.
- `DELETE /v1/categories/{categoryID}` deletes; `204`.
- Errors: `403 forbidden`, `404 not_found`, `409 category_exists`,
  `422 invalid_category_name`.

Instructor:

- `PATCH /v1/courses/{courseID}` accepts optional `category_ids` and `tags`. Errors:
  `422 unknown_category`, `422 too_many_categories`, `422 invalid_tag`, `422 too_many_tags`,
  plus the existing errors of that route.

`api/openapi.yaml` documents all of the above.

## Testing

- `platform/tags` and `platform/slug` unit tests; blog tests pass unchanged.
- Domain: category name and slug rules including the non-ASCII fallback; `SetClassification`
  limits, deduplication, status transitions; publish snapshot and `DiscardDraft` restore of
  categories and tags.
- App with fakes: category authorization; `UpdateDetails` with nil versus set classification;
  unknown category; tag errors; invalid cursor cases.
- PostgreSQL integration:
  - a title match ranks above a description-only match; prefix match on the last word;
  - `q`, `category`, `tag`, `level` and `price` combined;
  - keyset pages with and without `q` return every match once;
  - a draft's tag or category change is not visible publicly until republished;
  - deleting a category removes it from working copies and versions;
  - a concurrent category delete during `UpdateDetails` maps to `unknown_category`.
- End to end (`cmd/api`): admin creates a category, instructor classifies and publishes a course,
  an anonymous caller finds it by `q`, `category` and `tag`, and `GET /v1/categories` counts it.

## Frontend follow-up

Not part of this slice; recorded for `ioe-frontend`:

- Search calls `GET /v1/courses?q=...` instead of `/v1/catalog/courses/search`.
- Categories come from `GET /v1/categories`; a category page calls `GET /v1/courses?category=<slug>`.
- Items carry `categories: [{id, name, slug}]` instead of `category_ids` and `category_names`.
- Course editing sends `category_ids` and `tags` on `PATCH /v1/courses/{id}`.
- Reads the error code from `problem.type` (already decided).
