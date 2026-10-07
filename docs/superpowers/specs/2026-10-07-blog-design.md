# Blog Design

Date: 2026-10-07

## Status

Approved in conversation on 2026-10-07. Pending written-spec review.

## Context

The product scope (foundation spec) lists a blog context. A blog package was copied into
`internal/blog` from another project (`hitox-backend`). That code is multi-tenant (456 `TenantID`
references), imports packages that do not exist here (`internal/shared/kernel`,
`shared/publication`, `shared/contentblocks`, `identity/adapters/httpapi`), authorizes through a
permission system (`blog.review:manage`) instead of this project's fixed roles, and integrates with
contexts this project does not have (tenant policy, social, profile portfolio, feed, Next.js cache
webhooks). It ships no migration and its sqlc output was generated from a foreign schema.

Adapting it in place would mean rewriting every layer. Instead, the blog context is rebuilt in this
repository's architecture, following the `courseauthoring` patterns (relational published
snapshots, `platform/contentblocks`, `TxRunner`/`Repos`, outbox events), and the domain logic worth
keeping (slug rules, slug history, reading time, publish/discard-draft semantics) is ported with its
tests. The copied folder is then deleted.

## Goals

- Instructors and root admins write blog posts as ordered content blocks with incremental autosave.
- Authors publish directly; each publish stores an immutable version. Root admins can unpublish.
- Authors can view version history and discard a draft back to the live version.
- Old slugs keep resolving to the post after a slug change.
- Anonymous readers list live posts (optionally by tag), read a post by slug, list tags with
  counts, and page through a sitemap index.

## Non-goals

- Review or approval workflow.
- Video, quiz, flashcard, or media-asset-backed image blocks.
- Comments, reactions, RSS, search, scheduled publishing.
- Outbound webhooks, rate limiting specific to blog, multi-tenancy.
- Frontend changes.

## Decisions

| Topic | Decision |
|-------|----------|
| Authors | `instructor` and `root_admin` create posts. Students and anonymous users only read live posts. |
| Management | The author or a root admin edits, publishes, archives, and discards drafts. Only a root admin unpublishes. |
| Review | None. Publish is immediate. |
| Content | `platform/contentblocks`, kinds `text` and `image` only. Images use an https `url` and no `mediaAssetID`; rich-doc text must contain no image asset IDs. |
| Versions | Relational snapshot tables (`post_versions`, `post_version_blocks`), as in `courseauthoring`. |
| Slugs | Globally unique. History is append-only; a slug that ever belonged to a post can never be claimed by another post. |
| Old-slug reads | `GET /v1/blog/public/posts/{slug}` returns 200 with the live post, its canonical `slug`, and the `requestedSlug`. The client redirects its own page when they differ. |
| Tags | Up to 10 per post, normalized, stored as `text[]` on the draft and on each version. Public tag reads use live versions only. |
| Sitemap | Keyset pagination on `(published_at DESC, post_id DESC)` with an opaque cursor. |
| Events | `blog.post_published` and `blog.post_unpublished` written to the outbox in the state-change transaction. No consumers yet. |

## Architecture

### Repository layout additions

```text
internal/blog/
  domain/              Post aggregate, Slug, Tag, Status, events, errors
  app/                 PostService, ContentService, PublicService, ports
  adapters/postgres/   repositories, queries.sql, sqlcgen/
  adapters/httpapi/    authenticated and public handlers
migrations/00013_blog.sql
```

`cmd/api` wires the context in its own file, including a `UserDirectory` adapter backed by
identity's application service.

### Dependency rules

- `.golangci.yml` gains `blog-domain` and `blog-app` depguard rules matching the other contexts.
- `internal/blog` is added to `platform-independent-of-contexts` and to the deny rule of every other
  context; blog denies every other context.
- `blog/domain` imports only the standard library and `platform/{id,auth,contentblocks}`;
  `blog/app` imports only `blog/domain` and `platform/{auth,clock,contentblocks,id}`.

## Domain (`internal/blog/domain`)

### Post aggregate

Fields: `ID`, `AuthorID`, `Title` (`contentblocks.Title`), `Summary` (at most 300 characters),
`CoverURL` (empty or `https://`), `Tags`, `Slug`, `Status`, `LastVersion`,
`Live{Number, PublishedAt}`, `FirstPublishedAt`, `Version` (optimistic-concurrency token),
`CreatedAt`, `UpdatedAt`.

Blocks are not part of the aggregate; they are stored and patched through the content repository,
as lecture content is in `courseauthoring`.

### Status and transitions

`Status` is `draft`, `published`, or `archived`.

| Operation | From | To | Effect |
|-----------|------|----|--------|
| `Publish` | `draft`, `published` | `published` | `LastVersion++`; `Live` set to the new version; `FirstPublishedAt` set on first publish only. |
| `Unpublish` | `published` | `draft` | `Live` cleared; versions kept. Root admin only. |
| `Archive` | `draft`, `published` | `archived` | `Live` cleared. Terminal. |
| `DiscardDraft` | `published` | `published` | Details and blocks restored from the live version. |

Any other transition returns `ErrInvalidTransition`. Archived posts reject every mutation.

### Value objects

- `Slug`: lowercase ASCII `a-z0-9` and `-`, 1 to 96 characters, no leading, trailing, or repeated
  hyphen. `SlugFromTitle` folds the title to that alphabet and falls back to `post-<id>` when nothing
  remains. Ported from the copied `slug.go` and its tests.
- `Tag`: trimmed, lowercased, 1 to 32 characters of `a-z0-9-`. `NewTags` deduplicates while keeping
  order and rejects more than 10.

### Authorization helpers

- `CanAuthor(p)`: `p.Role` is `instructor` or `root_admin`.
- `(*Post).IsManagedBy(p)`: `p.Role == root_admin` or `p.UserID == AuthorID`.

### Block policy

`ValidateBlogBlocks` runs `contentblocks.ValidateBlocks` and then rejects any block whose kind is not
`text` or `image`, any image with a `mediaAssetID`, and any text block whose rich document has
`ImageAssetIDs()`. Violations return `ErrBlockKindNotAllowed` or a validation error.

### Errors

`ErrNotFound`, `ErrForbidden`, `ErrInvalidTransition`, `ErrSlugTaken`, `ErrNotPublishable`,
`ErrConcurrentModification`, `ErrConcurrentContentModification`, `ErrBlockKindNotAllowed`, plus
validation errors for title, summary, cover URL, slug, and tags.

## Application (`internal/blog/app`)

### Ports

- `PostRepository`: `FindByID`, `FindByIDForUpdate`, `ListByAuthor`, `Insert`, `Update` (checks
  `Version`), `InsertVersion` (copies current blocks), `FindVersion`, `ListVersions`,
  `RestoreFromVersion`.
- `PostContentRepository`: `FindHeader`, `FindHeaderForUpdate`, `ListBlocks`, `ApplyPatch`
  (base-revision checked), `Replace`.
- `SlugRepository`: `Reserve(slug, postID)` inserting into history and returning `ErrSlugTaken` when
  the slug belongs to another post; `Resolve(slug)` returning the owning post.
- `PublicReadRepository`: `ListLive`, `GetLiveByPostID`, `ListTags`, `ListIndexPage`.
- `EventPublisher`, `TxRunner{RunInTx(ctx, fn func(Repos) error)}`, `UserDirectory`
  (batch lookup of display names, same shape as `payment/app.UserDirectory`).

### PostService (authenticated)

- `Create(p, title, summary, tags, coverURL)`: requires `CanAuthor`. Derives the slug from the title;
  on collision tries `-2`, `-3`, and so on up to a bounded number of attempts.
- `Get`, `ListByAuthor` (own posts; root admin may list anyone's).
- `UpdateDetails`: title, summary, tags, cover URL, guarded by `Version`.
- `SetSlug`: validates, reserves in history, and updates the post. Previous slugs remain resolvable.
- `Publish`: rejects an empty draft (`ErrNotPublishable`). In one transaction it computes reading
  time (220 words per minute, minimum 1, ported from the copied `reading_time.go`), inserts the
  version with copied blocks, updates live and `first_published_at`, and writes the outbox event.
- `Unpublish` (root admin), `Archive`, `ListVersions`, `GetVersion(n)`, `DiscardDraft`
  (restores details and blocks from the live version and bumps `content_revision`).

### ContentService (authenticated)

`Get` returns draft blocks and `content_revision`. `Replace` (PUT) and `Patch` (PATCH with
`baseRevision`) mirror `courseauthoring/app/content_service.go`: validate, apply the block policy,
diff, and write under a row lock on the post header only. A stale `baseRevision` returns
`ErrConcurrentContentModification`. Content writes never touch `posts.version`.

### PublicService (anonymous)

- `List(tag, limit, offset)`: live post cards with author names.
- `GetBySlug(slug)`: resolves current or historical slug to a post; returns the live version with the
  canonical slug and the requested slug. Unknown, draft, and archived posts all return `ErrNotFound`
  and are indistinguishable.
- `Tags()`: tag counts over live versions.
- `Index(cursor, limit)`: slug, `first_published_at`, `published_at` per live post.

## Persistence (`internal/blog/adapters/postgres`)

Migration `00013_blog.sql` creates schema `blog`:

- `posts`: `id`, `author_id`, `title`, `summary`, `cover_url` (CHECK empty or `^https://`),
  `tags text[]`, `slug` UNIQUE, `status` CHECK, `content_revision`, `last_version`,
  `live_version`, `first_published_at`, `version`, `created_at`, `updated_at`. Index on `author_id`.
  `live_version` references `post_versions (post_id, number)`, with a CHECK that an archived post is
  not live.
- `post_blocks`: `id`, `post_id`, `kind` CHECK IN (`text`, `image`), `position`, `client_block_id`,
  `payload jsonb`; UNIQUE (`post_id`, `client_block_id`) and (`post_id`, `position`).
- `post_versions`: PK (`post_id`, `number`); `title`, `summary`, `cover_url`, `tags text[]`,
  `reading_time_minutes`, `published_by`, `published_at`. GIN index on `tags`; index on
  (`published_at DESC`, `post_id DESC`).
- `post_version_blocks`: copied blocks, FK to `post_versions`.
- `post_slugs`: `slug` PK, `post_id` FK, `created_at`.

Public reads join `posts` to `post_versions` on `live_version` and never read draft columns.
`sqlc.yaml` gains a `blog` entry generating `internal/blog/adapters/postgres/sqlcgen`.

## HTTP API (`internal/blog/adapters/httpapi`)

### Authenticated

```text
POST   /v1/blog/posts
GET    /v1/blog/posts/{postID}
PATCH  /v1/blog/posts/{postID}
PUT    /v1/blog/posts/{postID}/slug
POST   /v1/blog/posts/{postID}/publish
POST   /v1/blog/posts/{postID}/unpublish
POST   /v1/blog/posts/{postID}/archive
POST   /v1/blog/posts/{postID}/discard-draft
GET    /v1/blog/posts/{postID}/versions
GET    /v1/blog/posts/{postID}/versions/{versionNumber}
GET    /v1/blog/posts/{postID}/content
PUT    /v1/blog/posts/{postID}/content
PATCH  /v1/blog/posts/{postID}/content
GET    /v1/users/{authorID}/blog/posts
```

Content writes use the same body-size limit as lecture content. Authenticated responses send
`Cache-Control: no-store`.

### Public

```text
GET /v1/blog/public/posts?tag=&limit=&offset=
GET /v1/blog/public/posts/{slug}
GET /v1/blog/public/tags
GET /v1/blog/public/index?cursor=&limit=
```

`limit` defaults to 20 and is capped at 100. The index cursor is opaque base64 of
`published_at|post_id`; an invalid cursor returns 422. Public responses send
`Cache-Control: public, max-age=60`.

### Errors

Mapped through `platform/problem`: not found 404, forbidden 403, slug taken / invalid transition /
concurrent modification 409, validation and not publishable 422.

### OpenAPI

`api/openapi.yaml` gains a `blog` tag, every route above, and schemas `BlogPost`,
`BlogPostDetailsInput`, `BlogContent`, `BlogVersionSummary`, `PublicBlogPost`, `PublicBlogCard`,
`BlogTagCount`, and `BlogIndexPage`.

## Testing

- Domain: slug and tag rules, every valid and invalid transition, `CanAuthor` and `IsManagedBy` per
  role, block policy (kinds, image `mediaAssetID`, rich-doc image asset IDs).
- Application, with in-memory fakes: publish creates version n+1 with copied blocks and reading time
  and sets `first_published_at` once; republish; discard draft; root-admin-only unpublish; student
  cannot create; non-author cannot edit; slug suffixing on create; `SetSlug` to another post's
  historical slug fails; stale content revision conflicts; public old-slug read returns the
  canonical slug; draft, archived, and unknown slugs are all not found.
- HTTP: status and problem mapping, authenticated versus public routes, limit and cursor validation.
- PostgreSQL integration (testcontainers): migration up and down, publish transaction and version
  copy, live read, slug history uniqueness, index pagination stable and complete, tag counts from
  live versions only, concurrent content patch conflict.
- All gates in `AGENTS.md`. Integration tests are reported as passing only when they actually ran.

## Removal of the copied package

After porting the slug, reading-time, and transition logic and tests, every file of the copied
`internal/blog` tree is replaced. No tenant, webhook relay, rate limiter, social, profile, feed, or
JSONB-document code is kept.

## Risks

- Slug suffixing on create can race; the unique constraint on `post_slugs` is the source of truth and
  the service retries on `ErrSlugTaken`.
- Tag arrays are duplicated on draft and versions; public tag reads must use only live versions,
  which the integration test asserts.
- Reading time is computed at publish time from text blocks only; changing the formula does not
  update existing versions.
