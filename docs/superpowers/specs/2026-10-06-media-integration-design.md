# Media Integration Design

Date: 2026-10-06

## Status

Approved in conversation on 2026-10-06. Pending written-spec review.

## Context

Lecture content blocks reference uploaded media by `media_asset_id` in three places: video blocks,
image blocks, and flashcard cards. (`contentblocks.RichDoc` can also hold image asset IDs, but no
courseauthoring block type uses it; text blocks hold sanitized HTML.) The courseauthoring
spec stores these IDs unchecked and defers validation "until the media context exists". Nothing
uploads media and nothing resolves an ID to a playable URL, so a course cannot serve video.

The standalone media service (`../hitox-media-service`) is application-neutral. Behind a service
API key it:

- creates an asset and returns presigned direct-upload URLs (`POST /v1/assets`, single `PUT` for
  images up to 64 MiB, multipart for larger images and all videos), refreshes part URLs
  (`POST /v1/assets/{id}/parts`), and verifies the upload and enqueues processing
  (`POST /v1/assets/{id}/complete`);
- transcodes video into adaptive HLS plus a JPEG poster, and re-encodes images into named variants;
- reports state through `GET /v1/assets/{id}` (`uploading | processing | ready | failed | canceled |
  deleting`) and deletes idempotently (`DELETE /v1/assets/{id}`);
- issues delivery grants (`POST /v1/assets/{id}/delivery`). For private assets the grant holds an
  expiry-bound signed HLS manifest URL (child playlists signed, segments presigned) or short-lived
  image URLs. Browsers fetch these directly from the media service and object storage;
- isolates callers by `X-Namespace-ID` and requires an `Idempotency-Key` on create.

Asset IDs are decimal snowflakes, so `id.Parse` accepts them and `contentblocks` already stores them
as `id.ID`.

`ioe-frontend/packages/course-media` calls an older Hitox contract under `/v1/media/*` (asset-scoped
`/view`, a backend-proxied `playlist.m3u8`). As with the catalog slice, the backend contract in this
spec leads and the frontend migrates as a follow-up.

## Goals

- A course owner or root admin uploads a video or an image for a course, completes the upload, and
  polls processing status.
- Anyone who may read a lecture gets short-lived playback (video) or display (image) URLs for assets
  that lecture references. The rule is exactly the lecture content gate.
- Content writes reject asset references that do not exist, belong to another course, or have the
  wrong kind.
- Media service credentials never reach the browser.

## Non-goals

- Captions and chapters.
- Media service webhooks. Status is read on demand.
- Filling a video block's `duration_ms` from the asset; it stays client-supplied.
- Deleting assets when blocks, lectures, or courses are removed.
- Public visibility and stable public URLs.
- A backend manifest or segment proxy.
- Profile avatars, portfolio, blog, or any non-course media.
- Frontend changes (follow-up).

## Decisions

| Topic | Decision |
|---|---|
| Ownership record | New context `internal/media` with schema `media`; table `media.assets` maps asset to course and kind |
| Processing state | Never mirrored; read from the media service on demand |
| Student access | Lecture-scoped: `GET /v1/courses/{c}/lectures/{l}/media/{a}` reuses the content gate and requires the lecture to reference the asset |
| Playback delivery | Signed media-service URLs returned to the browser; no proxy |
| Visibility | Always `private` |
| Namespace | Fixed `X-Namespace-ID: ioe` |
| Image variant | One fixed variant `display`: width 1600, `webp`, quality 82 |
| Content validation | Courseauthoring port `AssetCatalog`, checked before the content transaction opens |
| Configuration | `MEDIA_SERVICE_BASE_URL`, `MEDIA_SERVICE_PUBLIC_URL`, and `MEDIA_SERVICE_API_KEY`: all or none |
| Relative delivery URLs | The private manifest URL comes back as a path (`/v1/delivery/...`); the backend prefixes `MEDIA_SERVICE_PUBLIC_URL`. Absolute URLs (presigned object-storage URLs) pass through unchanged |
| Media service schema | In the shared `ioe` database the service runs with `DATABASE_SCHEMA=media_service` under its own role |

## Architecture

### Repository layout additions

```text
internal/media/
  domain/        Asset, Kind
  app/           AssetService and the ports it owns
  adapters/
    postgres/    media.assets repository (sqlc)
    mediasvc/    HTTP client for the media service
    httpapi/     routes
migrations/      media schema and media.assets
```

### Dependency rules

- `internal/media/domain` imports the standard library and `platform/id` only.
- `internal/media/app` imports its domain and `platform/{auth,clock,id}` only.
- `.golangci.yml` gains `media-domain` and `media-app` depguard rules, adds `internal/media` to
  `platform-independent-of-contexts`, and adds deny rules so `media` and every other context cannot
  import each other.
- `cmd/api` is the only package that wires `media` to `courseauthoring`.
- `internal/platform/contentblocks` is unchanged.

## Domain (`internal/media/domain`)

```go
type Kind string

const (
    KindVideo Kind = "video"
    KindImage Kind = "image"
)

type Asset struct {
    ID        id.ID // the media service's asset ID
    CourseID  id.ID
    Kind      Kind
    CreatedBy id.ID
    CreatedAt time.Time
}
```

`ParseKind` rejects anything other than `video` and `image`. `ValidContentType(kind, contentType)`
accepts `video/*` for videos and `image/jpeg`, `image/png`, `image/webp` for images; the media
service validates the bytes again.

## Application (`internal/media/app`)

### Ports

```go
// Remote is the media service.
type Remote interface {
    Create(ctx context.Context, in RemoteCreate) (Upload, error)
    PresignParts(ctx context.Context, assetID id.ID, partNumbers []int) ([]UploadPart, error)
    Complete(ctx context.Context, assetID id.ID, parts []CompletedPart) (RemoteAsset, error)
    Get(ctx context.Context, assetID id.ID) (RemoteAsset, error)
    Delete(ctx context.Context, assetID id.ID) error
    Delivery(ctx context.Context, assetID id.ID) (Delivery, error)
}

// CourseAccess is backed by courseauthoring.
type CourseAccess interface {
    // CanManage returns nil when p owns the course or is a root admin and the course is
    // not archived; ErrNotFound when the course is not visible to p; ErrForbidden or
    // ErrCourseNotEditable otherwise.
    CanManage(ctx context.Context, p auth.Principal, courseID id.ID) error
    // CanReadLectureAsset applies the lecture content gate and returns ErrNotFound when
    // the lecture's blocks do not reference assetID.
    CanReadLectureAsset(ctx context.Context, p auth.Principal, courseID, lectureID, assetID id.ID) error
}

type AssetRepository interface {
    Insert(ctx context.Context, a domain.Asset) error
    Find(ctx context.Context, assetID id.ID) (domain.Asset, error)
    Delete(ctx context.Context, assetID id.ID) error
}
```

Remote errors are classified by sentinel: `ErrRemoteInvalid` (400), `ErrRemoteTooLarge` (413),
`ErrRemoteNotFound` (404), `ErrRemoteConflict` (409), and `ErrRemoteUnavailable` (network error,
timeout, 429, 5xx, or any other unexpected status).

### Use cases

- `CreateUpload(p, courseID, {Kind, ContentType, Filename, SizeBytes})`: validates kind, content
  type, a non-empty filename, and a positive size (`ErrInvalidInput`); calls
  `CourseAccess.CanManage`; calls `Remote.Create` with `visibility: private`,
  `owner_id: <p.UserID>`, `external_ref: course:<courseID>`, the `display` variant for images, and
  `Idempotency-Key: ioe:upload:<fresh snowflake>`; then inserts the row. If the insert fails the
  remote asset is orphaned; the media service fails abandoned uploads on its own.
- `PresignParts`, `Complete`, `Status`, `Delete(p, assetID, ...)`: load the row (`ErrNotFound` if
  missing), call `CanManage` for its course, mapping `ErrForbidden` to `ErrNotFound` so asset IDs
  cannot be probed, then call the remote. `Delete` calls `Remote.Delete` (treating
  `ErrRemoteNotFound` as success) and then deletes the row.
- `Resolve(p, courseID, lectureID, assetID)`: loads the row and requires `row.CourseID == courseID`
  (`ErrNotFound`); calls `CanReadLectureAsset`; calls `Remote.Get`. When status is not `ready`,
  returns `{ID, Kind, Status}` only. When ready, calls `Remote.Delivery` and returns, for video, the
  signed manifest URL, the poster rendition URL when present, duration, dimensions, and expiry; for
  an image, the `display` rendition URL, its dimensions, and expiry. A ready asset whose grant lacks
  the expected rendition is `ErrRemoteUnavailable` and is logged.

### Media-reference query for courseauthoring

`AssetKinds(ctx, courseID id.ID, ids []id.ID) (map[id.ID]domain.Kind, error)` returns the kinds of
the given IDs that belong to `courseID`; IDs that do not exist or belong to another course are
absent. It is a read-only query on `AssetRepository` (one `SELECT ... WHERE course_id = $1 AND id =
ANY($2)`) and needs no remote client, so `cmd/api` constructs it independently of `AssetService`
and of the media service configuration.

## Courseauthoring changes

### Content validation

- New port in `courseauthoring/app`:

  ```go
  type AssetKind string // "video" or "image"

  // AssetCatalog reports the kinds of the given asset IDs that belong to courseID.
  type AssetCatalog interface {
      Kinds(ctx context.Context, courseID id.ID, ids []id.ID) (map[id.ID]AssetKind, error)
  }
  ```

- `Replace`, `Patch`, and `AddLecture` with initial content collect asset references from the
  blocks being written: a video block's `media_asset_id` must be a video; an image block's
  `media_asset_id` and each flashcard card's `media_asset_id` must be images. `Patch` checks only
  upserted blocks.
- Every referenced ID must appear in the result with the expected kind; otherwise the write fails
  with `ErrInvalidMediaReference` naming the block's `client_block_id`. Asset status is not checked,
  so a lecture can be saved while its video is still processing.
- `Kinds` is called before the content transaction opens, so a write never holds two pool
  connections (the reason `Get` asks about enrollment first).
- URL-based video and image blocks are unchanged. Existing rows are not re-validated.
- HTTP maps `ErrInvalidMediaReference` to `400 invalid_media_reference` with the error text as
  detail, matching the other content validation errors.

### Access checks for media

- `CourseService.CheckManage(ctx, p, courseID) error`: loads the course; `ErrNotFound` when not
  visible to `p`, `ErrForbidden` when `p` does not manage it, `domain.ErrCourseNotEditable` when
  archived.
- `ContentService.CheckAssetRead(ctx, p, courseID, lectureID, assetID) error`: runs `Get` (which
  applies the visibility, free-preview, and enrollment gate) and returns `ErrNotFound` unless one of
  the returned blocks references `assetID` in any of the three places above.
- One helper in `courseauthoring/app` extracts `(assetID, expected kind, client block ID)` from a
  block list; both validation and `CheckAssetRead` use it.

## Persistence (`internal/media/adapters/postgres`)

Migration:

```sql
CREATE SCHEMA media;

CREATE TABLE media.assets (
    id          bigint      PRIMARY KEY,
    course_id   bigint      NOT NULL,
    kind        text        NOT NULL CHECK (kind IN ('video', 'image')),
    created_by  bigint      NOT NULL,
    created_at  timestamptz NOT NULL
);

CREATE INDEX assets_course_id_idx ON media.assets (course_id);
```

No foreign key to `courseauthoring`: a context reads and writes only its own schema. Queries are
generated with sqlc and run outside explicit transactions; each use case writes at most one row.

## Media service client (`internal/media/adapters/mediasvc`)

- Every request sends `Authorization: Bearer <MEDIA_SERVICE_API_KEY>` and `X-Namespace-ID: ioe`.
- `http.Client` with a 10s timeout and `otelhttp.NewTransport`.
- Response mapping:

| Response | Result |
|---|---|
| `200` or `201` (`204` for delete) | success |
| `400` | `ErrRemoteInvalid`, carrying the problem `detail` |
| `404` | `ErrRemoteNotFound` |
| `409` | `ErrRemoteConflict` |
| `413` | `ErrRemoteTooLarge` |
| network error, timeout, `429`, `5xx`, anything else | `ErrRemoteUnavailable` |

- Logs and errors never include the API key, presigned upload URLs, or signed delivery URLs.
  Problem responses are reduced to status, `code`, and `request_id`.

## HTTP API (`internal/media/adapters/httpapi`)

All routes require bearer authentication and respond with `application/problem+json` on error.

### Authoring

| Route | Request | Success |
|---|---|---|
| `POST /v1/courses/{courseID}/media/uploads` | `{kind, content_type, filename, size_bytes}` | `201` Upload |
| `POST /v1/media/uploads/{assetID}/parts` | `{part_numbers}` (1-1000 integers in 1-10000) | `200 {part_urls}` |
| `POST /v1/media/uploads/{assetID}/complete` | `{parts?: [{part_number, etag}]}` | `200` Asset |
| `GET /v1/media/assets/{assetID}` | | `200` Asset |
| `DELETE /v1/media/assets/{assetID}` | | `204` |

- Upload: `{asset_id, upload_id?, upload_url?, part_size?, part_urls?, expires_at}`.
- Asset: `{id, course_id, kind, status, progress_percent, duration_ms?, width?, height?,
  error_message?, updated_at}`.
- The create route is rate-limited per user with the existing in-memory `httpserver.RateLimiter` at
  30 requests per minute.

### Playback

`GET /v1/courses/{courseID}/lectures/{lectureID}/media/{assetID}` returns `200` with
`Cache-Control: no-store`:

- not ready: `{id, kind, status}`;
- ready video: `{id, kind, status, playback_url, poster_url?, duration_ms, width, height,
  expires_at}`;
- ready image: `{id, kind, status, url, width, height, expires_at}`.

### Errors

| Condition | Response |
|---|---|
| course, lecture, or asset not found; asset in another course; asset not referenced by the lecture; caller cannot manage the asset's course on an asset-ID route | `404 not_found` |
| caller cannot manage the course on the create route | `403 forbidden` |
| lecture not free-preview and caller not actively enrolled | `403 enrollment_required` |
| course archived (authoring) | `409 course_not_editable` |
| invalid input, or `ErrRemoteInvalid` | `400 invalid_input` |
| `ErrRemoteTooLarge` | `413 payload_too_large` |
| `ErrRemoteNotFound` for an asset that has a row | `404 not_found` |
| `ErrRemoteConflict` | `409 asset_state_conflict` |
| `ErrRemoteUnavailable` | `502 media_unavailable` |

When the media service is not configured these routes are not registered and answer `404`.

`api/openapi.yaml` documents all six routes and the `invalid_media_reference` response on the
content write routes.

## Composition (`cmd/api`)

- `registerMedia` constructs the repository, the `mediasvc` client, `AssetService`, and the HTTP
  handler when all three variables are set. Otherwise it logs a warning that media is disabled and
  registers nothing.
- A `courseAccess` adapter implements `media/app.CourseAccess` with `CourseService.CheckManage` and
  `ContentService.CheckAssetRead`, translating courseauthoring sentinels to media sentinels.
- An `assetCatalog` adapter implements `courseauthoring/app.AssetCatalog` with the media
  `AssetKinds` query. It is always wired, so content validation does not depend on media service
  configuration.

## Configuration

| Variable | Required | Purpose |
|---|---|---|
| `MEDIA_SERVICE_BASE_URL` | with the others | absolute `http` or `https` URL the backend calls |
| `MEDIA_SERVICE_PUBLIC_URL` | with the others | absolute `http` or `https` URL browsers use for the media service's `/v1/delivery` paths |
| `MEDIA_SERVICE_API_KEY` | with the others | service API key, at least 32 characters |

Setting some but not all stops the process with a message naming each missing variable. The key is
never logged.

## Database

In local development and current deployments the media service shares the `ioe` database. It runs
with `DATABASE_SCHEMA=media_service`, because `ioe-backend` owns schema `media`, and under its own
login role holding `CONNECT` and `CREATE` on the database. `ioe-backend` migrations never create,
alter, or read `media_service`, and the application role gets no privileges on it. In production a
database administrator creates the role and grants these privileges before the media service first
starts; `README.md` documents this, as for the notification service.

## Local Development

`docker-compose.yml` gains, under the `app` profile:

- `media-db-setup`: one-shot `psql` that creates role `media_service` with a local-only password if
  missing and grants `CONNECT, CREATE ON DATABASE ioe`. Idempotent.
- `minio` with ports bound to `127.0.0.1` and `MINIO_API_CORS_ALLOW_ORIGIN` set to the local frontend
  origins (as the media service's own compose does), and `minio-init`, which creates the bucket.
- `media`: built from `../hitox-media-service`, with `DATABASE_SCHEMA=media_service`,
  `DEFAULT_NAMESPACE_ID=ioe`, `AUTO_MIGRATE=true`, local-only API and delivery keys,
  `OBJECT_STORAGE_PUBLIC_URL` pointing at the host-reachable MinIO, and its port bound to
  `127.0.0.1`.
- `api` gains `MEDIA_SERVICE_BASE_URL=http://media:8080`, `MEDIA_SERVICE_PUBLIC_URL=http://localhost:8082`
  (the media port published on the host), and the local key, and depends on `media`
  being healthy.

`.env.example` documents all three variables. Local keys are marked local-only and allowlisted in
`.gitleaks.toml` if gitleaks flags them.

## Testing

Unit tests:

- Domain: kind parsing and content-type rules.
- `AssetService` with fakes: authorization on every use case (including `ErrForbidden` becoming
  `ErrNotFound` on asset-ID routes); create inserts only after remote success; delete tolerates
  remote `404`; resolve for not-ready, ready video, ready image, wrong course, and a grant missing
  its rendition.
- `mediasvc` against `httptest.Server`: method, path, headers, and body for each call; mapping of
  `200`, `201`, `204`, `400`, `404`, `409`, `413`, `429`, `500`, and a closed connection; the API key
  never appears in returned errors.
- HTTP handlers: status codes and bodies from the error table.
- Courseauthoring: reference extraction across video, image, and flashcard blocks;
  `Replace`, `Patch`, and `AddLecture` reject unknown, other-course, and wrong-kind IDs and accept
  valid ones; `Patch` ignores untouched blocks; `CheckAssetRead` with a referenced and an
  unreferenced asset; `CheckManage` for owner, root admin, other instructor, and archived course.
- Configuration: both set, neither set, and each single-variable case.

Integration tests (`make test-integration`, real PostgreSQL, media service faked with
`httptest.Server`):

- Repository insert, find, delete, and `AssetKinds` filtering by course.
- End to end: an instructor creates a video upload, completes it, and polls; a lecture saves with
  the asset; a signed-in student who is not enrolled gets `403 enrollment_required`; after enrolling
  the student gets a playback URL; a free-preview lecture's asset resolves for any signed-in user; a
  block referencing another course's asset is rejected with `400 invalid_media_reference`; an asset
  not referenced by the lecture is `404`; after delete, playback is `404`.

Not covered by `make check` or `make test-integration`: the real media service, MinIO uploads,
FFmpeg processing, and HLS playback. These are verified manually with
`docker compose --profile app up --build`: upload a short video through the API, poll until ready,
and play the returned `playback_url`.

## Risks

- Deleting an asset leaves dangling block references; playback returns `404` and the reader must
  tolerate it.
- A failed row insert after a successful remote create orphans a remote asset until the media
  service's abandoned-upload sweep fails it.
- Without webhooks the authoring UI polls; each poll is one media service call.
- Signed URLs expire after the media service's `MEDIA_DELIVERY_TTL` (15 minutes by default); the
  player re-resolves through the playback route.
- Browsers must reach the media service's `/v1/delivery` paths and object storage directly.
- A delete racing a content write can store a reference to a just-deleted asset.
- Compose couples the repositories by relative path (`../hitox-media-service`). Production deploys
  the service independently.
