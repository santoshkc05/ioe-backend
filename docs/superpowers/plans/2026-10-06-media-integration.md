# Media Integration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let course managers upload lecture videos and images through `hitox-media-service`, let anyone who may read a lecture get short-lived playback URLs for the assets it references, and reject content writes that reference foreign, unknown, or wrong-kind assets.

**Architecture:** A new `internal/media` bounded context owns schema `media` (one table mapping a media-service asset to its course and kind), a `mediasvc` HTTP client, an `AssetService`, and HTTP routes. It reaches courseauthoring only through its `CourseAccess` port; courseauthoring reaches media only through its new `AssetCatalog` port. `cmd/api` wires both adapters. Processing state is never mirrored; it is read from the media service on demand.

**Tech Stack:** Go 1.27, `net/http` ServeMux routing via `internal/platform/httpserver`, pgx v5 + sqlc, goose migrations, `otelhttp`, testcontainers (integration), golangci-lint depguard.

**Spec:** `docs/superpowers/specs/2026-10-06-media-integration-design.md`

## Global Constraints

- Module path: `github.com/santoshkc2200/ioe-backend`.
- A context never imports another context; only `cmd/api` wires `media` to `courseauthoring`.
- `internal/media/domain` imports the standard library and `platform/id` only. `internal/media/app` imports its domain and `platform/{auth,clock,id}` only.
- `ioe-backend` reads and writes only schema `media`; it never touches the media service's `media_service` schema.
- Every media service request sends `Authorization: Bearer <MEDIA_SERVICE_API_KEY>` and `X-Namespace-ID: ioe`.
- Assets are always created with `visibility: private`, `owner_id: <uploader user ID>`, `external_ref: course:<courseID>`; images request exactly one variant `{"name":"display","width":1600,"format":"webp","quality":82}`.
- Create uses `Idempotency-Key: ioe:upload:<fresh snowflake>`.
- Outbound client: 10s timeout, `otelhttp.NewTransport(http.DefaultTransport)`.
- Logs and errors never include the API key, presigned upload URLs, or signed delivery URLs.
- Relative delivery URLs (starting with `/`) are prefixed with `MEDIA_SERVICE_PUBLIC_URL`; absolute URLs pass through unchanged.
- Configuration: `MEDIA_SERVICE_BASE_URL`, `MEDIA_SERVICE_PUBLIC_URL`, `MEDIA_SERVICE_API_KEY` — all or none; the key is at least 32 characters.
- Validation errors are `400`, as everywhere else in this codebase.
- Integration tests must not be reported as passing unless they actually ran (`make test-integration` needs Docker).
- Run `make fmt` before `make lint`; test tables in this plan are not hand-aligned.
- Conventional Commits.

## Review Focus

1. **Authorization ordering on content writes.** A caller who cannot manage a course and sends an unknown or foreign `media_asset_id` must get the same `404`/`403` as without it, never `400 invalid_media_reference`, or asset IDs leak across courses. Pinned in Task 6 (`TestMediaReferenceDoesNotPreemptAuthorization`).
2. **Asset-ID routes for non-managers.** A student or another instructor calling `GET /v1/media/assets/{id}` for an asset that exists must get `404`, indistinguishable from a missing asset. Pinned in Task 4 (`TestManagedRoutesHideAssetsFromNonManagers`).
3. **Playback for an asset of another course through a readable lecture.** `GET /v1/courses/A/lectures/L/media/{asset of B}` must be `404` even when L is readable and B's asset exists. Pinned in Task 4 (`TestResolveRejectsAssetOfAnotherCourse`).
4. **A ready asset whose delivery grant lacks the expected rendition** (no manifest URL for a video, no `display` rendition for an image) must be `502 media_unavailable`, not a `200` with an empty URL. Pinned in Task 4 (`TestResolveMissingRendition`).
5. **Media service error bodies leaking secrets.** A non-2xx response must never produce an error string containing the API key. Pinned in Task 3 (`TestErrorsNeverContainAPIKey`).

---

## File Structure

```text
internal/platform/config/config.go             + MEDIA_SERVICE_* fields, MediaEnabled, validateMedia
internal/platform/config/config_test.go        + media config tests
migrations/00006_media.sql                     schema media, table media.assets
sqlc.yaml                                      + media package entry
.golangci.yml                                  + media depguard rules; media added to every deny list
internal/media/domain/asset.go                 Kind, ParseKind, ValidContentType, Asset
internal/media/domain/asset_test.go
internal/media/app/errors.go                   sentinels
internal/media/app/ports.go                    AssetRepository, CourseAccess
internal/media/app/remote.go                   Remote port and its value types, DisplayVariant
internal/media/app/service.go                  AssetService
internal/media/app/query.go                    AssetQuery (Kinds for courseauthoring)
internal/media/app/fakes_test.go
internal/media/app/service_test.go
internal/media/adapters/postgres/queries.sql
internal/media/adapters/postgres/sqlcgen/      generated
internal/media/adapters/postgres/assets.go     AssetRepository on a pool
internal/media/adapters/postgres/assets_integration_test.go
internal/media/adapters/mediasvc/client.go     Remote over HTTP
internal/media/adapters/mediasvc/client_test.go
internal/media/adapters/httpapi/httpapi.go     routes, error mapping
internal/media/adapters/httpapi/wire.go        request/response shapes
internal/media/adapters/httpapi/httpapi_test.go
internal/courseauthoring/app/assets.go         AssetKind, AssetCatalog, reference extraction and checks
internal/courseauthoring/app/errors.go         + ErrInvalidMediaReference
internal/courseauthoring/app/course_service.go + catalog dependency, AddLecture check, CheckManage
internal/courseauthoring/app/content_service.go + catalog dependency, Replace/Patch checks, CheckAssetRead
internal/courseauthoring/app/assets_test.go
internal/courseauthoring/app/{fakes,course_service,content_service}_test.go  constructor updates
internal/courseauthoring/adapters/httpapi/httpapi.go   + invalid_media_reference mapping
internal/courseauthoring/adapters/httpapi/{fakes,httpapi}_test.go  constructor updates + mapping test
cmd/api/media.go                               adapters + registerMedia
cmd/api/app.go                                 wiring changes
cmd/api/e2e_integration_test.go                + TestMediaEndToEnd
api/openapi.yaml                               + six media routes, invalid_media_reference note
README.md                                      + Media section
docker-compose.yml, compose/media-db-setup.sql + local media stack
.env.example, .gitleaks.toml                   + media variables, allowlist if needed
```

---

### Task 1: Media service configuration

**Files:**
- Modify: `internal/platform/config/config.go`
- Test: `internal/platform/config/config_test.go`

**Interfaces:**
- Produces: `config.Config.MediaServiceBaseURL`, `.MediaServicePublicURL`, `.MediaServiceAPIKey` (all `string`); `func (c Config) MediaEnabled() bool`.

- [ ] **Step 1: Write the failing tests** — append to `internal/platform/config/config_test.go`:

```go
const localMediaKey = "local-media-api-key-change-me-32-bytes"

func mediaEnv() map[string]string {
	env := validEnv()
	env["MEDIA_SERVICE_BASE_URL"] = "http://media:8080"
	env["MEDIA_SERVICE_PUBLIC_URL"] = "http://localhost:8082"
	env["MEDIA_SERVICE_API_KEY"] = localMediaKey
	return env
}

func TestMediaDisabledByDefault(t *testing.T) {
	cfg, err := config.LoadFrom(validEnv())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MediaEnabled() {
		t.Fatal("media enabled without configuration")
	}
}

func TestMediaEnabled(t *testing.T) {
	cfg, err := config.LoadFrom(mediaEnv())
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.MediaEnabled() || cfg.MediaServiceBaseURL != "http://media:8080" ||
		cfg.MediaServicePublicURL != "http://localhost:8082" || cfg.MediaServiceAPIKey != localMediaKey {
		t.Fatalf("%+v", cfg)
	}
}

func TestMediaConfigRejectsInvalidValues(t *testing.T) {
	cases := map[string]struct{ key, value, wantVar string }{
		"missing base url":      {"MEDIA_SERVICE_BASE_URL", "", "MEDIA_SERVICE_BASE_URL"},
		"missing public url":    {"MEDIA_SERVICE_PUBLIC_URL", "", "MEDIA_SERVICE_PUBLIC_URL"},
		"missing key":           {"MEDIA_SERVICE_API_KEY", "", "MEDIA_SERVICE_API_KEY"},
		"base url no scheme":    {"MEDIA_SERVICE_BASE_URL", "media:8080", "MEDIA_SERVICE_BASE_URL"},
		"public url with query": {"MEDIA_SERVICE_PUBLIC_URL", "http://localhost:8082?x=1", "MEDIA_SERVICE_PUBLIC_URL"},
		"short key":             {"MEDIA_SERVICE_API_KEY", "short", "MEDIA_SERVICE_API_KEY"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			env := mediaEnv()
			env[tc.key] = tc.value
			_, err := config.LoadFrom(env)
			if err == nil || !strings.Contains(err.Error(), tc.wantVar) {
				t.Fatalf("err = %v, want mention of %s", err, tc.wantVar)
			}
			if err != nil && strings.Contains(err.Error(), localMediaKey) {
				t.Fatalf("error leaks the key: %v", err)
			}
		})
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/platform/config/ -run Media`
Expected: FAIL — `cfg.MediaEnabled undefined`.

- [ ] **Step 3: Implement** — in `internal/platform/config/config.go` add three fields at the end of `Config`:

```go
	MediaServiceBaseURL           string   `env:"MEDIA_SERVICE_BASE_URL"`
	MediaServicePublicURL         string   `env:"MEDIA_SERVICE_PUBLIC_URL"`
	MediaServiceAPIKey            string   `env:"MEDIA_SERVICE_API_KEY"`
```

Add after `NotificationsEnabled`:

```go
// MediaEnabled reports whether the media service is configured.
// LoadFrom guarantees that all three media variables are set or none is.
func (c Config) MediaEnabled() bool { return c.MediaServiceBaseURL != "" }
```

In `validate`, after `errs = append(errs, c.validateNotifications()...)` add:

```go
	errs = append(errs, c.validateMedia()...)
```

Add below `validateNotifications`:

```go
const minMediaAPIKeyLen = 32

func (c *Config) validateMedia() []error {
	vars := []struct{ name, value string }{
		{"MEDIA_SERVICE_BASE_URL", c.MediaServiceBaseURL},
		{"MEDIA_SERVICE_PUBLIC_URL", c.MediaServicePublicURL},
		{"MEDIA_SERVICE_API_KEY", c.MediaServiceAPIKey},
	}
	var set, missing []string
	for _, v := range vars {
		if v.value == "" {
			missing = append(missing, v.name)
		} else {
			set = append(set, v.name)
		}
	}
	if len(set) == 0 {
		return nil
	}
	var errs []error
	for _, name := range missing {
		errs = append(errs, fmt.Errorf("%s: required when %s is set", name, strings.Join(set, " and ")))
	}
	if len(errs) > 0 {
		return errs
	}
	if !isBaseURL(c.MediaServiceBaseURL) {
		errs = append(errs, fmt.Errorf("MEDIA_SERVICE_BASE_URL: %q is not an absolute http(s) URL without query or fragment", c.MediaServiceBaseURL))
	}
	if !isBaseURL(c.MediaServicePublicURL) {
		errs = append(errs, fmt.Errorf("MEDIA_SERVICE_PUBLIC_URL: %q is not an absolute http(s) URL without query or fragment", c.MediaServicePublicURL))
	}
	if len(c.MediaServiceAPIKey) < minMediaAPIKeyLen {
		errs = append(errs, fmt.Errorf("MEDIA_SERVICE_API_KEY: must be at least %d characters", minMediaAPIKeyLen))
	}
	return errs
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/platform/config/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/platform/config/
git commit -m "feat(config): add media service settings"
```

---

### Task 2: Media domain, schema, and persistence

**Files:**
- Create: `internal/media/domain/asset.go`, `internal/media/domain/asset_test.go`
- Create: `internal/media/app/errors.go`, `internal/media/app/ports.go`
- Create: `migrations/00006_media.sql`
- Create: `internal/media/adapters/postgres/queries.sql`, `internal/media/adapters/postgres/assets.go`, `internal/media/adapters/postgres/assets_integration_test.go`
- Generate: `internal/media/adapters/postgres/sqlcgen/`
- Modify: `sqlc.yaml`, `.golangci.yml`

**Interfaces:**
- Produces (domain): `type Kind string`; `KindVideo`, `KindImage`; `ErrInvalidKind`; `func ParseKind(s string) (Kind, error)`; `func ValidContentType(k Kind, contentType string) bool`; `type Asset struct { ID, CourseID id.ID; Kind Kind; CreatedBy id.ID; CreatedAt time.Time }`.
- Produces (app): sentinels `ErrNotFound`, `ErrForbidden`, `ErrInvalidInput`, `ErrCourseNotEditable`, `ErrEnrollmentRequired`, `ErrRemoteInvalid`, `ErrRemoteTooLarge`, `ErrRemoteNotFound`, `ErrRemoteConflict`, `ErrRemoteUnavailable`; interfaces `AssetRepository`, `CourseAccess` (below).
- Produces (postgres): `func New(pool *pgxpool.Pool) *Assets` implementing `app.AssetRepository`.

- [ ] **Step 1: Write the failing domain test** — `internal/media/domain/asset_test.go`:

```go
package domain_test

import (
	"errors"
	"testing"

	"github.com/santoshkc2200/ioe-backend/internal/media/domain"
)

func TestParseKind(t *testing.T) {
	for _, s := range []string{"video", "image"} {
		if k, err := domain.ParseKind(s); err != nil || string(k) != s {
			t.Fatalf("ParseKind(%q) = %q, %v", s, k, err)
		}
	}
	for _, s := range []string{"", "VIDEO", "thumbnail", "audio"} {
		if _, err := domain.ParseKind(s); !errors.Is(err, domain.ErrInvalidKind) {
			t.Fatalf("ParseKind(%q) err = %v", s, err)
		}
	}
}

func TestValidContentType(t *testing.T) {
	cases := []struct {
		kind domain.Kind
		ct   string
		want bool
	}{
		{domain.KindVideo, "video/mp4", true},
		{domain.KindVideo, "video/quicktime", true},
		{domain.KindVideo, "Video/MP4; codecs=avc1", true},
		{domain.KindVideo, "image/png", false},
		{domain.KindVideo, "video/", false},
		{domain.KindImage, "image/jpeg", true},
		{domain.KindImage, "image/png", true},
		{domain.KindImage, "image/webp", true},
		{domain.KindImage, "image/gif", false},
		{domain.KindImage, "video/mp4", false},
		{domain.KindImage, "", false},
		{domain.KindImage, "not a type", false},
	}
	for _, c := range cases {
		if got := domain.ValidContentType(c.kind, c.ct); got != c.want {
			t.Fatalf("ValidContentType(%s, %q) = %v", c.kind, c.ct, got)
		}
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/media/domain/`
Expected: FAIL — package does not exist.

- [ ] **Step 3: Implement the domain** — `internal/media/domain/asset.go`:

```go
// Package domain holds the media asset ownership model.
package domain

import (
	"errors"
	"mime"
	"strings"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

// Kind is what an asset holds.
type Kind string

const (
	KindVideo Kind = "video"
	KindImage Kind = "image"
)

var ErrInvalidKind = errors.New("kind must be video or image")

func ParseKind(s string) (Kind, error) {
	switch Kind(s) {
	case KindVideo, KindImage:
		return Kind(s), nil
	}
	return "", ErrInvalidKind
}

// ValidContentType reports whether contentType is acceptable for an upload of kind k.
// The media service re-validates the bytes.
func ValidContentType(k Kind, contentType string) bool {
	mt, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return false
	}
	switch k {
	case KindVideo:
		return strings.HasPrefix(mt, "video/") && len(mt) > len("video/")
	case KindImage:
		return mt == "image/jpeg" || mt == "image/png" || mt == "image/webp"
	}
	return false
}

// Asset records which course owns a media-service asset. ID is the media service's asset ID.
type Asset struct {
	ID        id.ID
	CourseID  id.ID
	Kind      Kind
	CreatedBy id.ID
	CreatedAt time.Time
}
```

- [ ] **Step 4: Run domain tests**

Run: `go test ./internal/media/domain/`
Expected: PASS.

- [ ] **Step 5: Add app errors and repository port** — `internal/media/app/errors.go`:

```go
package app

import "errors"

var (
	ErrNotFound           = errors.New("not found")
	ErrForbidden          = errors.New("forbidden")
	ErrInvalidInput       = errors.New("invalid input")
	ErrCourseNotEditable  = errors.New("course not editable")
	ErrEnrollmentRequired = errors.New("enrollment required")

	ErrRemoteInvalid     = errors.New("media service rejected the request")
	ErrRemoteTooLarge    = errors.New("media service rejected the upload size")
	ErrRemoteNotFound    = errors.New("media service has no such asset")
	ErrRemoteConflict    = errors.New("media service asset is in the wrong state")
	ErrRemoteUnavailable = errors.New("media service unavailable")
)
```

`internal/media/app/ports.go`:

```go
// Package app contains the media use cases and the ports they depend on.
package app

import (
	"context"

	"github.com/santoshkc2200/ioe-backend/internal/media/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

// AssetRepository stores asset ownership. Each call is its own statement.
type AssetRepository interface {
	Insert(ctx context.Context, a domain.Asset) error
	// Find returns ErrNotFound when no row exists.
	Find(ctx context.Context, assetID id.ID) (domain.Asset, error)
	// Delete is a no-op when no row exists.
	Delete(ctx context.Context, assetID id.ID) error
	// KindsInCourse returns the kinds of the given IDs that belong to courseID.
	KindsInCourse(ctx context.Context, courseID id.ID, ids []id.ID) (map[id.ID]domain.Kind, error)
}

// CourseAccess is backed by courseauthoring.
type CourseAccess interface {
	// CanManage returns nil when p owns the course or is a root admin and the course is not
	// archived; ErrNotFound when the course is not visible to p; ErrForbidden or
	// ErrCourseNotEditable otherwise.
	CanManage(ctx context.Context, p auth.Principal, courseID id.ID) error
	// CanReadLectureAsset applies the lecture content gate (ErrNotFound,
	// ErrEnrollmentRequired) and returns ErrNotFound when the lecture's blocks do not
	// reference assetID.
	CanReadLectureAsset(ctx context.Context, p auth.Principal, courseID, lectureID, assetID id.ID) error
}
```

- [ ] **Step 6: Add the migration** — `migrations/00006_media.sql`:

```sql
-- +goose Up
CREATE SCHEMA media;

CREATE TABLE media.assets (
  id         bigint PRIMARY KEY,
  course_id  bigint NOT NULL,
  kind       text NOT NULL CHECK (kind IN ('video', 'image')),
  created_by bigint NOT NULL,
  created_at timestamptz NOT NULL
);

CREATE INDEX assets_course_id_idx ON media.assets (course_id);

-- +goose Down
DROP SCHEMA media CASCADE;
```

- [ ] **Step 7: Add queries and sqlc entry** — `internal/media/adapters/postgres/queries.sql`:

```sql
-- name: InsertAsset :exec
INSERT INTO media.assets (id, course_id, kind, created_by, created_at)
VALUES ($1, $2, $3, $4, $5);

-- name: GetAsset :one
SELECT * FROM media.assets WHERE id = $1;

-- name: DeleteAsset :exec
DELETE FROM media.assets WHERE id = $1;

-- name: ListAssetKindsInCourse :many
SELECT id, kind FROM media.assets
WHERE course_id = sqlc.arg(course_id) AND id = ANY(sqlc.arg(ids)::bigint[]);
```

Append to `sqlc.yaml` under `sql:`:

```yaml
  - engine: postgresql
    schema: migrations
    queries: internal/media/adapters/postgres/queries.sql
    gen:
      go:
        package: sqlcgen
        out: internal/media/adapters/postgres/sqlcgen
        sql_package: pgx/v5
        overrides:
          - db_type: timestamptz
            go_type: time.Time
```

Run: `make sqlc`
Expected: `internal/media/adapters/postgres/sqlcgen/{db.go,models.go,queries.sql.go}` generated with `MediaAsset`, `InsertAssetParams`, `ListAssetKindsInCourseParams{CourseID int64; Ids []int64}`, `ListAssetKindsInCourseRow{ID int64; Kind string}`. If sqlc names the slice field differently, use the generated name in Step 9.

- [ ] **Step 8: Write the failing repository integration test** — `internal/media/adapters/postgres/assets_integration_test.go`:

```go
//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/media/adapters/postgres"
	"github.com/santoshkc2200/ioe-backend/internal/media/app"
	"github.com/santoshkc2200/ioe-backend/internal/media/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
	"github.com/santoshkc2200/ioe-backend/internal/platform/postgres/pgtest"
)

var ctx = context.Background()

func TestAssets(t *testing.T) {
	repo := postgres.New(pgtest.New(t))
	at := time.Date(2026, 10, 6, 9, 0, 0, 0, time.UTC)
	video := domain.Asset{ID: 900, CourseID: 10, Kind: domain.KindVideo, CreatedBy: 7, CreatedAt: at}
	image := domain.Asset{ID: 901, CourseID: 10, Kind: domain.KindImage, CreatedBy: 7, CreatedAt: at}
	foreign := domain.Asset{ID: 902, CourseID: 11, Kind: domain.KindVideo, CreatedBy: 7, CreatedAt: at}
	for _, a := range []domain.Asset{video, image, foreign} {
		if err := repo.Insert(ctx, a); err != nil {
			t.Fatal(err)
		}
	}

	got, err := repo.Find(ctx, video.ID)
	if err != nil || got != video {
		t.Fatalf("Find = %+v, %v", got, err)
	}
	if _, err := repo.Find(ctx, 999); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("Find missing err = %v", err)
	}

	kinds, err := repo.KindsInCourse(ctx, 10, []id.ID{900, 901, 902, 999})
	if err != nil || len(kinds) != 2 || kinds[900] != domain.KindVideo || kinds[901] != domain.KindImage {
		t.Fatalf("KindsInCourse = %v, %v", kinds, err)
	}
	if kinds, err := repo.KindsInCourse(ctx, 10, nil); err != nil || len(kinds) != 0 {
		t.Fatalf("KindsInCourse(nil) = %v, %v", kinds, err)
	}

	if err := repo.Delete(ctx, video.ID); err != nil {
		t.Fatal(err)
	}
	if err := repo.Delete(ctx, video.ID); err != nil {
		t.Fatalf("second delete err = %v", err)
	}
	if _, err := repo.Find(ctx, video.ID); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("Find after delete err = %v", err)
	}
}
```

- [ ] **Step 9: Implement the repository** — `internal/media/adapters/postgres/assets.go`:

```go
// Package postgres implements media asset persistence on the media schema.
package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/santoshkc2200/ioe-backend/internal/media/adapters/postgres/sqlcgen"
	"github.com/santoshkc2200/ioe-backend/internal/media/app"
	"github.com/santoshkc2200/ioe-backend/internal/media/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

// Assets implements app.AssetRepository. Each method is one statement on the pool.
type Assets struct{ q *sqlcgen.Queries }

var _ app.AssetRepository = (*Assets)(nil)

func New(pool *pgxpool.Pool) *Assets { return &Assets{q: sqlcgen.New(pool)} }

func (r *Assets) Insert(ctx context.Context, a domain.Asset) error {
	return r.q.InsertAsset(ctx, sqlcgen.InsertAssetParams{
		ID: int64(a.ID), CourseID: int64(a.CourseID), Kind: string(a.Kind),
		CreatedBy: int64(a.CreatedBy), CreatedAt: a.CreatedAt,
	})
}

func (r *Assets) Find(ctx context.Context, assetID id.ID) (domain.Asset, error) {
	row, err := r.q.GetAsset(ctx, int64(assetID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Asset{}, app.ErrNotFound
	}
	if err != nil {
		return domain.Asset{}, err
	}
	return domain.Asset{
		ID: id.ID(row.ID), CourseID: id.ID(row.CourseID), Kind: domain.Kind(row.Kind),
		CreatedBy: id.ID(row.CreatedBy), CreatedAt: row.CreatedAt,
	}, nil
}

func (r *Assets) Delete(ctx context.Context, assetID id.ID) error {
	return r.q.DeleteAsset(ctx, int64(assetID))
}

func (r *Assets) KindsInCourse(ctx context.Context, courseID id.ID, ids []id.ID) (map[id.ID]domain.Kind, error) {
	out := make(map[id.ID]domain.Kind, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	raw := make([]int64, len(ids))
	for i, v := range ids {
		raw[i] = int64(v)
	}
	rows, err := r.q.ListAssetKindsInCourse(ctx, sqlcgen.ListAssetKindsInCourseParams{CourseID: int64(courseID), Ids: raw})
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		out[id.ID(row.ID)] = domain.Kind(row.Kind)
	}
	return out, nil
}
```

- [ ] **Step 10: Add depguard rules** — in `.golangci.yml`:

1. Under `platform-independent-of-contexts.deny` append:

```yaml
            - pkg: github.com/santoshkc2200/ioe-backend/internal/media
              desc: platform must not depend on bounded contexts
```

2. Append the same deny entry (with `desc: bounded contexts must not import each other`) to `identity-independent`, `notification-independent`, `courseauthoring-independent`, `enrollment-independent`, and `progress-independent`:

```yaml
            - pkg: github.com/santoshkc2200/ioe-backend/internal/media
              desc: bounded contexts must not import each other
```

3. After the `progress-independent` rule add:

```yaml
        media-domain:
          list-mode: strict
          files:
            - "**/internal/media/domain/**"
            - "!$test"
          allow:
            - $gostd
            - github.com/santoshkc2200/ioe-backend/internal/platform/id
        media-app:
          list-mode: strict
          files:
            - "**/internal/media/app/**"
            - "!$test"
          allow:
            - $gostd
            - github.com/santoshkc2200/ioe-backend/internal/media/domain
            - github.com/santoshkc2200/ioe-backend/internal/platform/auth
            - github.com/santoshkc2200/ioe-backend/internal/platform/clock
            - github.com/santoshkc2200/ioe-backend/internal/platform/id
        media-independent:
          list-mode: lax
          files:
            - "**/internal/media/**"
          deny:
            - pkg: github.com/santoshkc2200/ioe-backend/internal/identity
              desc: bounded contexts must not import each other
            - pkg: github.com/santoshkc2200/ioe-backend/internal/notification
              desc: bounded contexts must not import each other
            - pkg: github.com/santoshkc2200/ioe-backend/internal/courseauthoring
              desc: bounded contexts must not import each other
            - pkg: github.com/santoshkc2200/ioe-backend/internal/enrollment
              desc: bounded contexts must not import each other
            - pkg: github.com/santoshkc2200/ioe-backend/internal/progress
              desc: bounded contexts must not import each other
```

- [ ] **Step 11: Verify**

Run: `go build ./... && go test ./internal/media/... && make sqlc-check && make lint`
Expected: build OK, unit tests PASS, `sqlc diff` clean, lint clean.

Run: `go test -race -tags integration ./internal/media/adapters/postgres/`
Expected: PASS (requires Docker; if Docker is unavailable, record that the test did not run).

- [ ] **Step 12: Commit**

```bash
git add internal/media migrations/00006_media.sql sqlc.yaml .golangci.yml
git commit -m "feat(media): add asset ownership domain and persistence"
```

---

### Task 3: Media service client

**Files:**
- Create: `internal/media/app/remote.go`
- Create: `internal/media/adapters/mediasvc/client.go`, `internal/media/adapters/mediasvc/client_test.go`

**Interfaces:**
- Consumes: `app.Err*` sentinels and `domain.Kind` from Task 2.
- Produces (app): `DisplayVariant = "display"`, `PosterRendition = "poster"`; types `RemoteCreate`, `UploadPart`, `Upload`, `CompletedPart`, `RemoteAsset`, `Rendition`, `Delivery`; interface `Remote` — exact definitions in Step 1.
- Produces (mediasvc): `func New(baseURL, publicURL, apiKey string) *Client` implementing `app.Remote`.

- [ ] **Step 1: Add the Remote port** — `internal/media/app/remote.go`:

```go
package app

import (
	"context"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/media/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

const (
	// DisplayVariant is the only image rendition requested and served.
	DisplayVariant = "display"
	// PosterRendition is the media service's JPEG poster for a video.
	PosterRendition = "poster"
	// StatusReady is the media service status of a playable asset.
	StatusReady = "ready"
)

type RemoteCreate struct {
	Kind           domain.Kind
	ContentType    string
	Filename       string
	SizeBytes      int64
	OwnerID        id.ID
	CourseID       id.ID
	IdempotencyKey string
}

type UploadPart struct {
	PartNumber int
	URL        string
}

// Upload carries either UploadURL (a single PUT) or PartSize and PartURLs (multipart).
type Upload struct {
	AssetID   id.ID
	UploadID  string
	UploadURL string
	PartSize  int64
	PartURLs  []UploadPart
	ExpiresAt time.Time
}

type CompletedPart struct {
	PartNumber int
	ETag       string
}

// RemoteAsset is the media service's view of an asset.
type RemoteAsset struct {
	ID              id.ID
	Status          string
	ProgressPercent int
	DurationMs      int64
	Width           int
	Height          int
	ErrorMessage    string
	UpdatedAt       time.Time
}

type Rendition struct {
	Name   string
	URL    string
	Width  int
	Height int
}

// Delivery is a grant of short-lived URLs. URL is the signed HLS manifest for a video.
// Every URL is absolute.
type Delivery struct {
	URL        string
	ExpiresAt  time.Time
	Renditions []Rendition
}

// Remote is the media service. Errors wrap one of the ErrRemote* sentinels.
type Remote interface {
	Create(ctx context.Context, in RemoteCreate) (Upload, error)
	PresignParts(ctx context.Context, assetID id.ID, partNumbers []int) ([]UploadPart, error)
	Complete(ctx context.Context, assetID id.ID, parts []CompletedPart) (RemoteAsset, error)
	Get(ctx context.Context, assetID id.ID) (RemoteAsset, error)
	Delete(ctx context.Context, assetID id.ID) error
	Delivery(ctx context.Context, assetID id.ID) (Delivery, error)
}
```

- [ ] **Step 2: Write the failing client tests** — `internal/media/adapters/mediasvc/client_test.go`:

```go
package mediasvc_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/santoshkc2200/ioe-backend/internal/media/adapters/mediasvc"
	"github.com/santoshkc2200/ioe-backend/internal/media/app"
	"github.com/santoshkc2200/ioe-backend/internal/media/domain"
)

const apiKey = "test-media-api-key-0123456789abcdef"

var ctx = context.Background()

type captured struct {
	method, path, auth, namespace, idem, contentType string
	body                                             map[string]any
}

func server(t *testing.T, status int, response string, got *captured) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*got = captured{method: r.Method, path: r.URL.Path, auth: r.Header.Get("Authorization"),
			namespace: r.Header.Get("X-Namespace-ID"), idem: r.Header.Get("Idempotency-Key"),
			contentType: r.Header.Get("Content-Type")}
		b, _ := io.ReadAll(r.Body)
		if len(b) > 0 {
			_ = json.Unmarshal(b, &got.body)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, response)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestCreateImage(t *testing.T) {
	var got captured
	srv := server(t, http.StatusCreated, `{"asset_id":"900","namespace_id":"ioe","upload_id":"u1",
		"upload_url":"https://objects.test/put","expires_at":"2026-10-06T10:00:00Z"}`, &got)
	c := mediasvc.New(srv.URL, "https://media.test", apiKey)
	up, err := c.Create(ctx, app.RemoteCreate{Kind: domain.KindImage, ContentType: "image/png", Filename: "a.png",
		SizeBytes: 10, OwnerID: 7, CourseID: 10, IdempotencyKey: "ioe:upload:1"})
	if err != nil {
		t.Fatal(err)
	}
	if up.AssetID != 900 || up.UploadURL != "https://objects.test/put" || up.UploadID != "u1" || up.ExpiresAt.IsZero() {
		t.Fatalf("upload = %+v", up)
	}
	if got.method != http.MethodPost || got.path != "/v1/assets" || got.auth != "Bearer "+apiKey ||
		got.namespace != "ioe" || got.idem != "ioe:upload:1" || got.contentType != "application/json" {
		t.Fatalf("request = %+v", got)
	}
	b := got.body
	if b["kind"] != "image" || b["visibility"] != "private" || b["owner_id"] != "7" || b["external_ref"] != "course:10" ||
		b["content_type"] != "image/png" || b["filename"] != "a.png" || b["size_bytes"] != float64(10) {
		t.Fatalf("body = %v", b)
	}
	variants, _ := b["image_variants"].([]any)
	if len(variants) != 1 {
		t.Fatalf("variants = %v", b["image_variants"])
	}
	v := variants[0].(map[string]any)
	if v["name"] != "display" || v["width"] != float64(1600) || v["format"] != "webp" || v["quality"] != float64(82) {
		t.Fatalf("variant = %v", v)
	}
}

func TestCreateVideoMultipart(t *testing.T) {
	var got captured
	srv := server(t, http.StatusCreated, `{"asset_id":"901","namespace_id":"ioe","upload_id":"u2","part_size":5242880,
		"part_urls":[{"part_number":1,"url":"https://objects.test/1"}],"expires_at":"2026-10-06T10:00:00Z"}`, &got)
	up, err := mediasvc.New(srv.URL, "https://media.test", apiKey).Create(ctx, app.RemoteCreate{
		Kind: domain.KindVideo, ContentType: "video/mp4", Filename: "a.mp4", SizeBytes: 9, OwnerID: 7, CourseID: 10, IdempotencyKey: "k"})
	if err != nil || up.PartSize != 5242880 || len(up.PartURLs) != 1 || up.PartURLs[0].PartNumber != 1 {
		t.Fatalf("upload = %+v, %v", up, err)
	}
	if _, ok := got.body["image_variants"]; ok {
		t.Fatalf("video request carries image variants: %v", got.body)
	}
}

func TestPartsCompleteGetDelete(t *testing.T) {
	var got captured
	srv := server(t, http.StatusOK, `{"part_urls":[{"part_number":2,"url":"https://objects.test/2"}]}`, &got)
	c := mediasvc.New(srv.URL+"/", "https://media.test", apiKey) // trailing slash is trimmed
	parts, err := c.PresignParts(ctx, 900, []int{2})
	if err != nil || len(parts) != 1 || parts[0].URL != "https://objects.test/2" {
		t.Fatalf("parts = %+v, %v", parts, err)
	}
	if got.path != "/v1/assets/900/parts" || got.body["part_numbers"].([]any)[0] != float64(2) {
		t.Fatalf("request = %+v", got)
	}

	asset := `{"id":"900","namespace_id":"ioe","kind":"video","status":"processing","progress_percent":40,
		"duration_ms":60000,"width":1280,"height":720,"version":2,"updated_at":"2026-10-06T10:00:00Z"}`
	srv = server(t, http.StatusOK, asset, &got)
	c = mediasvc.New(srv.URL, "https://media.test", apiKey)
	a, err := c.Complete(ctx, 900, []app.CompletedPart{{PartNumber: 1, ETag: `"e1"`}})
	if err != nil || a.ID != 900 || a.Status != "processing" || a.ProgressPercent != 40 || a.Width != 1280 {
		t.Fatalf("complete = %+v, %v", a, err)
	}
	p := got.body["parts"].([]any)[0].(map[string]any)
	if got.path != "/v1/assets/900/complete" || p["part_number"] != float64(1) || p["etag"] != `"e1"` {
		t.Fatalf("request = %+v", got)
	}
	if a, err = c.Get(ctx, 900); err != nil || got.method != http.MethodGet || got.path != "/v1/assets/900" || a.DurationMs != 60000 {
		t.Fatalf("get = %+v, %v, %+v", a, err, got)
	}

	srv = server(t, http.StatusNoContent, "", &got)
	if err := mediasvc.New(srv.URL, "https://media.test", apiKey).Delete(ctx, 900); err != nil ||
		got.method != http.MethodDelete || got.path != "/v1/assets/900" {
		t.Fatalf("delete err = %v, %+v", err, got)
	}
}

func TestDeliveryAbsolutizesRelativeURLs(t *testing.T) {
	var got captured
	srv := server(t, http.StatusOK, `{"visibility":"private","url":"/v1/delivery/900/master.m3u8?token=t",
		"expires_at":"2026-10-06T10:15:00Z","renditions":[
		{"name":"poster","content_type":"image/jpeg","url":"https://objects.test/poster.jpg","width":1280,"height":720},
		{"name":"display","content_type":"image/webp","url":"/v1/delivery/900/display","width":1600,"height":900}]}`, &got)
	d, err := mediasvc.New(srv.URL, "https://media.test/", apiKey).Delivery(ctx, 900)
	if err != nil {
		t.Fatal(err)
	}
	if got.method != http.MethodPost || got.path != "/v1/assets/900/delivery" {
		t.Fatalf("request = %+v", got)
	}
	if d.URL != "https://media.test/v1/delivery/900/master.m3u8?token=t" || d.ExpiresAt.IsZero() {
		t.Fatalf("delivery = %+v", d)
	}
	if d.Renditions[0].URL != "https://objects.test/poster.jpg" || d.Renditions[1].URL != "https://media.test/v1/delivery/900/display" ||
		d.Renditions[1].Width != 1600 {
		t.Fatalf("renditions = %+v", d.Renditions)
	}
}

func TestErrorMapping(t *testing.T) {
	cases := []struct {
		status int
		want   error
	}{
		{http.StatusBadRequest, app.ErrRemoteInvalid},
		{http.StatusNotFound, app.ErrRemoteNotFound},
		{http.StatusConflict, app.ErrRemoteConflict},
		{http.StatusRequestEntityTooLarge, app.ErrRemoteTooLarge},
		{http.StatusTooManyRequests, app.ErrRemoteUnavailable},
		{http.StatusInternalServerError, app.ErrRemoteUnavailable},
		{http.StatusUnauthorized, app.ErrRemoteUnavailable},
	}
	for _, tc := range cases {
		var got captured
		srv := server(t, tc.status, `{"code":"x","detail":"filename is required","status":400,"request_id":"r1"}`, &got)
		_, err := mediasvc.New(srv.URL, "https://media.test", apiKey).Get(ctx, 900)
		if !errors.Is(err, tc.want) {
			t.Fatalf("%d: err = %v, want %v", tc.status, err, tc.want)
		}
		if tc.status == http.StatusBadRequest && !strings.Contains(err.Error(), "filename is required") {
			t.Fatalf("400 detail not carried: %v", err)
		}
	}
}

func TestClosedConnectionIsUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		conn, _, _ := w.(http.Hijacker).Hijack()
		_ = conn.Close()
	}))
	defer srv.Close()
	if _, err := mediasvc.New(srv.URL, "https://media.test", apiKey).Get(ctx, 900); !errors.Is(err, app.ErrRemoteUnavailable) {
		t.Fatalf("err = %v", err)
	}
}

func TestMalformedSuccessIsUnavailable(t *testing.T) {
	var got captured
	srv := server(t, http.StatusCreated, `{"asset_id":"not-a-snowflake","expires_at":"2026-10-06T10:00:00Z"}`, &got)
	_, err := mediasvc.New(srv.URL, "https://media.test", apiKey).Create(ctx, app.RemoteCreate{Kind: domain.KindVideo})
	if !errors.Is(err, app.ErrRemoteUnavailable) {
		t.Fatalf("err = %v", err)
	}
}

func TestErrorsNeverContainAPIKey(t *testing.T) {
	for _, status := range []int{400, 401, 404, 409, 413, 500} {
		var got captured
		srv := server(t, status, `{"code":"x","detail":"Bearer `+apiKey+`","request_id":"r"}`, &got)
		_, err := mediasvc.New(srv.URL, "https://media.test", apiKey).Get(ctx, 900)
		if err == nil || strings.Contains(err.Error(), apiKey) {
			t.Fatalf("%d: err = %v", status, err)
		}
	}
}
```

- [ ] **Step 3: Run to verify they fail**

Run: `go test ./internal/media/adapters/mediasvc/`
Expected: FAIL — package `mediasvc` does not exist.

- [ ] **Step 4: Implement the client** — `internal/media/adapters/mediasvc/client.go`:

```go
// Package mediasvc calls the standalone media service.
package mediasvc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/santoshkc2200/ioe-backend/internal/media/app"
	"github.com/santoshkc2200/ioe-backend/internal/media/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

const (
	requestTimeout = 10 * time.Second
	maxBodyBytes   = 1 << 20
	namespace      = "ioe"
)

// Client implements app.Remote. Errors never include the API key or returned URLs.
type Client struct {
	base   string
	public string
	apiKey string
	http   *http.Client
}

var _ app.Remote = (*Client)(nil)

// New returns a client that calls baseURL and prefixes relative delivery URLs with publicURL.
func New(baseURL, publicURL, apiKey string) *Client {
	return &Client{
		base:   strings.TrimRight(baseURL, "/"),
		public: strings.TrimRight(publicURL, "/"),
		apiKey: apiKey,
		http:   &http.Client{Timeout: requestTimeout, Transport: otelhttp.NewTransport(http.DefaultTransport)},
	}
}

type imageVariant struct {
	Name    string `json:"name"`
	Width   int    `json:"width"`
	Format  string `json:"format"`
	Quality int    `json:"quality"`
}

type createRequest struct {
	OwnerID       string         `json:"owner_id"`
	ExternalRef   string         `json:"external_ref"`
	Kind          string         `json:"kind"`
	Visibility    string         `json:"visibility"`
	ContentType   string         `json:"content_type"`
	Filename      string         `json:"filename"`
	SizeBytes     int64          `json:"size_bytes"`
	ImageVariants []imageVariant `json:"image_variants,omitempty"`
}

type partWire struct {
	PartNumber int    `json:"part_number"`
	URL        string `json:"url"`
}

type uploadWire struct {
	AssetID   string     `json:"asset_id"`
	UploadID  string     `json:"upload_id"`
	UploadURL string     `json:"upload_url"`
	PartSize  int64      `json:"part_size"`
	PartURLs  []partWire `json:"part_urls"`
	ExpiresAt time.Time  `json:"expires_at"`
}

type completedPartWire struct {
	PartNumber int    `json:"part_number"`
	ETag       string `json:"etag"`
}

type assetWire struct {
	ID              string    `json:"id"`
	Status          string    `json:"status"`
	ProgressPercent int       `json:"progress_percent"`
	DurationMs      int64     `json:"duration_ms"`
	Width           int       `json:"width"`
	Height          int       `json:"height"`
	ErrorMessage    string    `json:"error_message"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type renditionWire struct {
	Name   string `json:"name"`
	URL    string `json:"url"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

type deliveryWire struct {
	URL        string          `json:"url"`
	ExpiresAt  time.Time       `json:"expires_at"`
	Renditions []renditionWire `json:"renditions"`
}

func (c *Client) Create(ctx context.Context, in app.RemoteCreate) (app.Upload, error) {
	req := createRequest{
		OwnerID: in.OwnerID.String(), ExternalRef: "course:" + in.CourseID.String(),
		Kind: string(in.Kind), Visibility: "private",
		ContentType: in.ContentType, Filename: in.Filename, SizeBytes: in.SizeBytes,
	}
	if in.Kind == domain.KindImage {
		req.ImageVariants = []imageVariant{{Name: app.DisplayVariant, Width: 1600, Format: "webp", Quality: 82}}
	}
	var out uploadWire
	if err := c.do(ctx, http.MethodPost, "/v1/assets", in.IdempotencyKey, req, &out); err != nil {
		return app.Upload{}, err
	}
	assetID, err := id.Parse(out.AssetID)
	if err != nil {
		return app.Upload{}, fmt.Errorf("%w: create returned an invalid asset id", app.ErrRemoteUnavailable)
	}
	return app.Upload{AssetID: assetID, UploadID: out.UploadID, UploadURL: out.UploadURL,
		PartSize: out.PartSize, PartURLs: toParts(out.PartURLs), ExpiresAt: out.ExpiresAt}, nil
}

func (c *Client) PresignParts(ctx context.Context, assetID id.ID, partNumbers []int) ([]app.UploadPart, error) {
	var out struct {
		PartURLs []partWire `json:"part_urls"`
	}
	in := struct {
		PartNumbers []int `json:"part_numbers"`
	}{partNumbers}
	if err := c.do(ctx, http.MethodPost, assetPath(assetID, "/parts"), "", in, &out); err != nil {
		return nil, err
	}
	return toParts(out.PartURLs), nil
}

func (c *Client) Complete(ctx context.Context, assetID id.ID, parts []app.CompletedPart) (app.RemoteAsset, error) {
	in := struct {
		Parts []completedPartWire `json:"parts,omitempty"`
	}{}
	for _, p := range parts {
		in.Parts = append(in.Parts, completedPartWire{PartNumber: p.PartNumber, ETag: p.ETag})
	}
	var out assetWire
	if err := c.do(ctx, http.MethodPost, assetPath(assetID, "/complete"), "", in, &out); err != nil {
		return app.RemoteAsset{}, err
	}
	return toAsset(out)
}

func (c *Client) Get(ctx context.Context, assetID id.ID) (app.RemoteAsset, error) {
	var out assetWire
	if err := c.do(ctx, http.MethodGet, assetPath(assetID, ""), "", nil, &out); err != nil {
		return app.RemoteAsset{}, err
	}
	return toAsset(out)
}

func (c *Client) Delete(ctx context.Context, assetID id.ID) error {
	return c.do(ctx, http.MethodDelete, assetPath(assetID, ""), "", nil, nil)
}

func (c *Client) Delivery(ctx context.Context, assetID id.ID) (app.Delivery, error) {
	var out deliveryWire
	if err := c.do(ctx, http.MethodPost, assetPath(assetID, "/delivery"), "", nil, &out); err != nil {
		return app.Delivery{}, err
	}
	d := app.Delivery{URL: c.absolute(out.URL), ExpiresAt: out.ExpiresAt, Renditions: make([]app.Rendition, len(out.Renditions))}
	for i, r := range out.Renditions {
		d.Renditions[i] = app.Rendition{Name: r.Name, URL: c.absolute(r.URL), Width: r.Width, Height: r.Height}
	}
	return d, nil
}

// absolute prefixes the public base to a path-only URL; absolute URLs pass through.
func (c *Client) absolute(u string) string {
	if strings.HasPrefix(u, "/") {
		return c.public + u
	}
	return u
}

func (c *Client) do(ctx context.Context, method, path, idempotencyKey string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("encode media request: %w", err)
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return fmt.Errorf("build media request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("X-Namespace-ID", namespace)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %s %s: %w", app.ErrRemoteUnavailable, method, path, err)
	}
	defer resp.Body.Close()
	limited := io.LimitReader(resp.Body, maxBodyBytes)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return remoteError(resp.StatusCode, limited)
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, limited)
		return nil
	}
	if err := json.NewDecoder(limited).Decode(out); err != nil {
		return fmt.Errorf("%w: decode %s %s response: %w", app.ErrRemoteUnavailable, method, path, err)
	}
	return nil
}

// remoteError maps a non-2xx response. Only a 400's detail is kept, for the caller.
func remoteError(status int, body io.Reader) error {
	var p struct {
		Code      string `json:"code"`
		Detail    string `json:"detail"`
		RequestID string `json:"request_id"`
	}
	_ = json.NewDecoder(body).Decode(&p)
	failure := fmt.Errorf("media service responded %d (code %q, request %q)", status, p.Code, p.RequestID)
	switch status {
	case http.StatusBadRequest:
		return fmt.Errorf("%w: %s", app.ErrRemoteInvalid, sanitizeDetail(p.Detail))
	case http.StatusNotFound:
		return fmt.Errorf("%w: %w", app.ErrRemoteNotFound, failure)
	case http.StatusConflict:
		return fmt.Errorf("%w: %w", app.ErrRemoteConflict, failure)
	case http.StatusRequestEntityTooLarge:
		return fmt.Errorf("%w: %w", app.ErrRemoteTooLarge, failure)
	default:
		return fmt.Errorf("%w: %w", app.ErrRemoteUnavailable, failure)
	}
}

// sanitizeDetail keeps a validation message short and drops anything that looks like a
// credential, since it is shown to the caller.
func sanitizeDetail(s string) string {
	if strings.Contains(strings.ToLower(s), "bearer") {
		return "rejected by the media service"
	}
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

func assetPath(assetID id.ID, suffix string) string {
	return "/v1/assets/" + assetID.String() + suffix
}

func toParts(in []partWire) []app.UploadPart {
	out := make([]app.UploadPart, len(in))
	for i, p := range in {
		out[i] = app.UploadPart{PartNumber: p.PartNumber, URL: p.URL}
	}
	return out
}

func toAsset(w assetWire) (app.RemoteAsset, error) {
	assetID, err := id.Parse(w.ID)
	if err != nil {
		return app.RemoteAsset{}, fmt.Errorf("%w: invalid asset id in response", app.ErrRemoteUnavailable)
	}
	return app.RemoteAsset{ID: assetID, Status: w.Status, ProgressPercent: w.ProgressPercent,
		DurationMs: w.DurationMs, Width: w.Width, Height: w.Height, ErrorMessage: w.ErrorMessage, UpdatedAt: w.UpdatedAt}, nil
}
```

- [ ] **Step 5: Run tests**

Run: `go test -race ./internal/media/... && make lint`
Expected: PASS; lint clean.

- [ ] **Step 6: Commit**

```bash
git add internal/media/app/remote.go internal/media/adapters/mediasvc
git commit -m "feat(media): add media service client"
```

---

### Task 4: Media application service

**Files:**
- Create: `internal/media/app/service.go`, `internal/media/app/query.go`
- Create: `internal/media/app/fakes_test.go`, `internal/media/app/service_test.go`

**Interfaces:**
- Consumes: Task 2 ports and sentinels; Task 3 `Remote` and value types.
- Produces:

```go
func NewAssetService(assets AssetRepository, remote Remote, courses CourseAccess, ids *id.Generator, clk clock.Clock) *AssetService
type UploadInput struct { Kind, ContentType, Filename string; SizeBytes int64 }
type AssetView struct { Asset domain.Asset; Remote RemoteAsset }
type Playback struct { ID id.ID; Kind domain.Kind; Status, URL, PosterURL string; DurationMs int64; Width, Height int; ExpiresAt time.Time }
func (s *AssetService) CreateUpload(ctx, p auth.Principal, courseID id.ID, in UploadInput) (Upload, error)
func (s *AssetService) PresignParts(ctx, p auth.Principal, assetID id.ID, partNumbers []int) ([]UploadPart, error)
func (s *AssetService) Complete(ctx, p auth.Principal, assetID id.ID, parts []CompletedPart) (AssetView, error)
func (s *AssetService) Status(ctx, p auth.Principal, assetID id.ID) (AssetView, error)
func (s *AssetService) Delete(ctx, p auth.Principal, assetID id.ID) error
func (s *AssetService) Resolve(ctx, p auth.Principal, courseID, lectureID, assetID id.ID) (Playback, error)
func NewAssetQuery(assets AssetRepository) *AssetQuery
func (q *AssetQuery) Kinds(ctx, courseID id.ID, ids []id.ID) (map[id.ID]domain.Kind, error)
```

- [ ] **Step 1: Write fakes** — `internal/media/app/fakes_test.go`:

```go
package app_test

import (
	"context"
	"testing"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/media/app"
	"github.com/santoshkc2200/ioe-backend/internal/media/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

var (
	ctx      = context.Background()
	t0       = time.Date(2026, 10, 6, 9, 0, 0, 0, time.UTC)
	owner    = auth.Principal{UserID: 100, Role: auth.RoleInstructor}
	stranger = auth.Principal{UserID: 101, Role: auth.RoleInstructor}
	student  = auth.Principal{UserID: 200, Role: auth.RoleStudent}
)

type authPrincipal = auth.Principal

const (
	courseA  id.ID = 10
	courseB  id.ID = 11
	lecture1 id.ID = 20
)

type fixedClock struct{}

func (fixedClock) Now() time.Time { return t0 }

type memRepo struct {
	rows      map[id.ID]domain.Asset
	insertErr error
}

func newRepo(rows ...domain.Asset) *memRepo {
	r := &memRepo{rows: map[id.ID]domain.Asset{}}
	for _, a := range rows {
		r.rows[a.ID] = a
	}
	return r
}

func (r *memRepo) Insert(_ context.Context, a domain.Asset) error {
	if r.insertErr != nil {
		return r.insertErr
	}
	r.rows[a.ID] = a
	return nil
}

func (r *memRepo) Find(_ context.Context, assetID id.ID) (domain.Asset, error) {
	a, ok := r.rows[assetID]
	if !ok {
		return domain.Asset{}, app.ErrNotFound
	}
	return a, nil
}

func (r *memRepo) Delete(_ context.Context, assetID id.ID) error {
	delete(r.rows, assetID)
	return nil
}

func (r *memRepo) KindsInCourse(_ context.Context, courseID id.ID, ids []id.ID) (map[id.ID]domain.Kind, error) {
	out := map[id.ID]domain.Kind{}
	for _, v := range ids {
		if a, ok := r.rows[v]; ok && a.CourseID == courseID {
			out[v] = a.Kind
		}
	}
	return out, nil
}

// fakeRemote records calls and returns canned values.
type fakeRemote struct {
	created   []app.RemoteCreate
	upload    app.Upload
	asset     app.RemoteAsset
	delivery  app.Delivery
	err       error
	deleteErr error
	calls     []string
}

func (f *fakeRemote) Create(_ context.Context, in app.RemoteCreate) (app.Upload, error) {
	f.calls = append(f.calls, "create")
	f.created = append(f.created, in)
	return f.upload, f.err
}

func (f *fakeRemote) PresignParts(_ context.Context, _ id.ID, n []int) ([]app.UploadPart, error) {
	f.calls = append(f.calls, "parts")
	return []app.UploadPart{{PartNumber: n[0], URL: "https://objects.test/p"}}, f.err
}

func (f *fakeRemote) Complete(context.Context, id.ID, []app.CompletedPart) (app.RemoteAsset, error) {
	f.calls = append(f.calls, "complete")
	return f.asset, f.err
}

func (f *fakeRemote) Get(context.Context, id.ID) (app.RemoteAsset, error) {
	f.calls = append(f.calls, "get")
	return f.asset, f.err
}

func (f *fakeRemote) Delete(context.Context, id.ID) error {
	f.calls = append(f.calls, "delete")
	return f.deleteErr
}

func (f *fakeRemote) Delivery(context.Context, id.ID) (app.Delivery, error) {
	f.calls = append(f.calls, "delivery")
	return f.delivery, f.err
}

// fakeAccess: owner manages courseA and courseB; readable maps (lecture, asset) to the gate result.
type fakeAccess struct {
	archived bool
	read     map[[2]id.ID]error
}

func (f fakeAccess) CanManage(_ context.Context, p auth.Principal, courseID id.ID) error {
	if courseID != courseA && courseID != courseB {
		return app.ErrNotFound
	}
	if p.UserID != owner.UserID {
		return app.ErrForbidden
	}
	if f.archived {
		return app.ErrCourseNotEditable
	}
	return nil
}

func (f fakeAccess) CanReadLectureAsset(_ context.Context, _ auth.Principal, _, lectureID, assetID id.ID) error {
	if err, ok := f.read[[2]id.ID{lectureID, assetID}]; ok {
		return err
	}
	return app.ErrNotFound
}

func testIDs(t *testing.T) *id.Generator {
	t.Helper()
	g, err := id.NewGenerator(1)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func newService(t *testing.T, repo *memRepo, remote *fakeRemote, access fakeAccess) *app.AssetService {
	t.Helper()
	return app.NewAssetService(repo, remote, access, testIDs(t), fixedClock{})
}
```

- [ ] **Step 2: Write the failing service tests** — `internal/media/app/service_test.go`:

```go
package app_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/santoshkc2200/ioe-backend/internal/media/app"
	"github.com/santoshkc2200/ioe-backend/internal/media/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

var (
	videoA = domain.Asset{ID: 900, CourseID: courseA, Kind: domain.KindVideo, CreatedBy: owner.UserID, CreatedAt: t0}
	imageA = domain.Asset{ID: 901, CourseID: courseA, Kind: domain.KindImage, CreatedBy: owner.UserID, CreatedAt: t0}
	videoB = domain.Asset{ID: 902, CourseID: courseB, Kind: domain.KindVideo, CreatedBy: owner.UserID, CreatedAt: t0}
)

func TestCreateUpload(t *testing.T) {
	repo, remote := newRepo(), &fakeRemote{upload: app.Upload{AssetID: 777, UploadID: "u"}}
	svc := newService(t, repo, remote, fakeAccess{})
	up, err := svc.CreateUpload(ctx, owner, courseA, app.UploadInput{Kind: "video", ContentType: "video/mp4", Filename: "a.mp4", SizeBytes: 9})
	if err != nil || up.AssetID != 777 {
		t.Fatalf("upload = %+v, %v", up, err)
	}
	c := remote.created[0]
	if c.Kind != domain.KindVideo || c.OwnerID != owner.UserID || c.CourseID != courseA ||
		!strings.HasPrefix(c.IdempotencyKey, "ioe:upload:") || len(c.IdempotencyKey) <= len("ioe:upload:") {
		t.Fatalf("remote create = %+v", c)
	}
	want := domain.Asset{ID: 777, CourseID: courseA, Kind: domain.KindVideo, CreatedBy: owner.UserID, CreatedAt: t0}
	if repo.rows[777] != want {
		t.Fatalf("row = %+v", repo.rows[777])
	}
	if _, err := svc.CreateUpload(ctx, owner, courseA, app.UploadInput{Kind: "video", ContentType: "video/mp4", Filename: "a.mp4", SizeBytes: 9}); err != nil {
		t.Fatal(err)
	}
	if remote.created[0].IdempotencyKey == remote.created[1].IdempotencyKey {
		t.Fatal("idempotency key reused across uploads")
	}
}

func TestCreateUploadRejects(t *testing.T) {
	valid := app.UploadInput{Kind: "image", ContentType: "image/png", Filename: "a.png", SizeBytes: 1}
	tests := []struct {
		name   string
		in     app.UploadInput
		who    authPrincipal
		course id.ID
		access fakeAccess
		want   error
	}{
		{"bad kind", app.UploadInput{Kind: "audio", ContentType: "audio/mp3", Filename: "a", SizeBytes: 1}, owner, courseA, fakeAccess{}, app.ErrInvalidInput},
		{"kind/type mismatch", app.UploadInput{Kind: "image", ContentType: "video/mp4", Filename: "a", SizeBytes: 1}, owner, courseA, fakeAccess{}, app.ErrInvalidInput},
		{"blank filename", app.UploadInput{Kind: "image", ContentType: "image/png", Filename: " ", SizeBytes: 1}, owner, courseA, fakeAccess{}, app.ErrInvalidInput},
		{"zero size", app.UploadInput{Kind: "image", ContentType: "image/png", Filename: "a", SizeBytes: 0}, owner, courseA, fakeAccess{}, app.ErrInvalidInput},
		{"not manager", valid, stranger, courseA, fakeAccess{}, app.ErrForbidden},
		{"unknown course", valid, owner, 999, fakeAccess{}, app.ErrNotFound},
		{"archived", valid, owner, courseA, fakeAccess{archived: true}, app.ErrCourseNotEditable},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo, remote := newRepo(), &fakeRemote{}
			_, err := newService(t, repo, remote, tc.access).CreateUpload(ctx, tc.who, tc.course, tc.in)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if len(remote.calls) != 0 || len(repo.rows) != 0 {
				t.Fatalf("side effects: calls=%v rows=%v", remote.calls, repo.rows)
			}
		})
	}
}

func TestCreateUploadRemoteFailureInsertsNothing(t *testing.T) {
	repo, remote := newRepo(), &fakeRemote{err: app.ErrRemoteTooLarge}
	_, err := newService(t, repo, remote, fakeAccess{}).CreateUpload(ctx, owner, courseA,
		app.UploadInput{Kind: "video", ContentType: "video/mp4", Filename: "a.mp4", SizeBytes: 9})
	if !errors.Is(err, app.ErrRemoteTooLarge) || len(repo.rows) != 0 {
		t.Fatalf("err = %v, rows = %v", err, repo.rows)
	}
}

func TestManagedRoutes(t *testing.T) {
	repo := newRepo(videoA)
	remote := &fakeRemote{asset: app.RemoteAsset{ID: videoA.ID, Status: "processing", ProgressPercent: 30}}
	svc := newService(t, repo, remote, fakeAccess{})
	if parts, err := svc.PresignParts(ctx, owner, videoA.ID, []int{3}); err != nil || parts[0].PartNumber != 3 {
		t.Fatalf("parts = %+v, %v", parts, err)
	}
	v, err := svc.Complete(ctx, owner, videoA.ID, []app.CompletedPart{{PartNumber: 1, ETag: "e"}})
	if err != nil || v.Asset != videoA || v.Remote.Status != "processing" {
		t.Fatalf("complete = %+v, %v", v, err)
	}
	if v, err = svc.Status(ctx, owner, videoA.ID); err != nil || v.Remote.ProgressPercent != 30 {
		t.Fatalf("status = %+v, %v", v, err)
	}
	if err := svc.Delete(ctx, owner, videoA.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := repo.rows[videoA.ID]; ok {
		t.Fatal("row survived delete")
	}
	if !slices.Equal(remote.calls, []string{"parts", "complete", "get", "delete"}) {
		t.Fatalf("calls = %v", remote.calls)
	}
}

func TestPresignPartsValidatesNumbers(t *testing.T) {
	svc := newService(t, newRepo(videoA), &fakeRemote{}, fakeAccess{})
	for _, n := range [][]int{nil, {0}, {10001}, make([]int, 1001)} {
		if _, err := svc.PresignParts(ctx, owner, videoA.ID, n); !errors.Is(err, app.ErrInvalidInput) {
			t.Fatalf("%v: err = %v", n, err)
		}
	}
}

func TestManagedRoutesHideAssetsFromNonManagers(t *testing.T) {
	for _, who := range []authPrincipal{stranger, student} {
		remote := &fakeRemote{}
		svc := newService(t, newRepo(videoA), remote, fakeAccess{})
		if _, err := svc.Status(ctx, who, videoA.ID); !errors.Is(err, app.ErrNotFound) {
			t.Fatalf("status err = %v", err)
		}
		if err := svc.Delete(ctx, who, videoA.ID); !errors.Is(err, app.ErrNotFound) {
			t.Fatalf("delete err = %v", err)
		}
		if _, err := svc.Complete(ctx, who, videoA.ID, nil); !errors.Is(err, app.ErrNotFound) {
			t.Fatalf("complete err = %v", err)
		}
		if len(remote.calls) != 0 {
			t.Fatalf("remote called: %v", remote.calls)
		}
	}
	if _, err := newService(t, newRepo(), &fakeRemote{}, fakeAccess{}).Status(ctx, owner, 404); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("missing err = %v", err)
	}
}

func TestDeleteToleratesRemoteNotFound(t *testing.T) {
	repo := newRepo(videoA)
	err := newService(t, repo, &fakeRemote{deleteErr: app.ErrRemoteNotFound}, fakeAccess{}).Delete(ctx, owner, videoA.ID)
	if err != nil || len(repo.rows) != 0 {
		t.Fatalf("err = %v, rows = %v", err, repo.rows)
	}
	repo = newRepo(videoA)
	err = newService(t, repo, &fakeRemote{deleteErr: app.ErrRemoteUnavailable}, fakeAccess{}).Delete(ctx, owner, videoA.ID)
	if !errors.Is(err, app.ErrRemoteUnavailable) || len(repo.rows) != 1 {
		t.Fatalf("unavailable: err = %v, rows = %v", err, repo.rows)
	}
}

func readable(pairs ...[2]id.ID) fakeAccess {
	m := map[[2]id.ID]error{}
	for _, p := range pairs {
		m[p] = nil
	}
	return fakeAccess{read: m}
}

func TestResolveNotReady(t *testing.T) {
	remote := &fakeRemote{asset: app.RemoteAsset{ID: videoA.ID, Status: "processing"}}
	pb, err := newService(t, newRepo(videoA), remote, readable([2]id.ID{lecture1, videoA.ID})).Resolve(ctx, student, courseA, lecture1, videoA.ID)
	if err != nil || pb.Status != "processing" || pb.URL != "" || pb.Kind != domain.KindVideo {
		t.Fatalf("playback = %+v, %v", pb, err)
	}
	if slices.Contains(remote.calls, "delivery") {
		t.Fatal("delivery requested for an asset that is not ready")
	}
}

func TestResolveReadyVideo(t *testing.T) {
	remote := &fakeRemote{
		asset: app.RemoteAsset{ID: videoA.ID, Status: app.StatusReady, DurationMs: 60000, Width: 1280, Height: 720},
		delivery: app.Delivery{URL: "https://media.test/v1/delivery/900/master.m3u8?token=t", ExpiresAt: t0,
			Renditions: []app.Rendition{{Name: "poster", URL: "https://objects.test/poster.jpg"}}},
	}
	pb, err := newService(t, newRepo(videoA), remote, readable([2]id.ID{lecture1, videoA.ID})).Resolve(ctx, student, courseA, lecture1, videoA.ID)
	if err != nil {
		t.Fatal(err)
	}
	if pb.URL != remote.delivery.URL || pb.PosterURL != "https://objects.test/poster.jpg" || pb.DurationMs != 60000 ||
		pb.Width != 1280 || pb.Height != 720 || !pb.ExpiresAt.Equal(t0) || pb.Status != app.StatusReady {
		t.Fatalf("playback = %+v", pb)
	}
}

func TestResolveReadyImage(t *testing.T) {
	remote := &fakeRemote{
		asset: app.RemoteAsset{ID: imageA.ID, Status: app.StatusReady, Width: 4000, Height: 3000},
		delivery: app.Delivery{ExpiresAt: t0, Renditions: []app.Rendition{
			{Name: "original-ish", URL: "https://objects.test/x"},
			{Name: app.DisplayVariant, URL: "https://objects.test/display.webp", Width: 1600, Height: 1200}}},
	}
	pb, err := newService(t, newRepo(imageA), remote, readable([2]id.ID{lecture1, imageA.ID})).Resolve(ctx, student, courseA, lecture1, imageA.ID)
	if err != nil || pb.URL != "https://objects.test/display.webp" || pb.Width != 1600 || pb.Height != 1200 || pb.PosterURL != "" {
		t.Fatalf("playback = %+v, %v", pb, err)
	}
}

func TestResolveMissingRendition(t *testing.T) {
	video := &fakeRemote{asset: app.RemoteAsset{Status: app.StatusReady}, delivery: app.Delivery{}}
	if _, err := newService(t, newRepo(videoA), video, readable([2]id.ID{lecture1, videoA.ID})).Resolve(ctx, student, courseA, lecture1, videoA.ID); !errors.Is(err, app.ErrRemoteUnavailable) {
		t.Fatalf("video err = %v", err)
	}
	image := &fakeRemote{asset: app.RemoteAsset{Status: app.StatusReady}, delivery: app.Delivery{Renditions: []app.Rendition{{Name: "poster", URL: "u"}}}}
	if _, err := newService(t, newRepo(imageA), image, readable([2]id.ID{lecture1, imageA.ID})).Resolve(ctx, student, courseA, lecture1, imageA.ID); !errors.Is(err, app.ErrRemoteUnavailable) {
		t.Fatalf("image err = %v", err)
	}
}

func TestResolveRejectsAssetOfAnotherCourse(t *testing.T) {
	remote := &fakeRemote{asset: app.RemoteAsset{Status: app.StatusReady}}
	// Even if the gate would allow it, an asset of course B is never served through course A.
	svc := newService(t, newRepo(videoB), remote, readable([2]id.ID{lecture1, videoB.ID}))
	if _, err := svc.Resolve(ctx, student, courseA, lecture1, videoB.ID); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
	if len(remote.calls) != 0 {
		t.Fatalf("remote called: %v", remote.calls)
	}
}

func TestResolveGate(t *testing.T) {
	gate := fakeAccess{read: map[[2]id.ID]error{{lecture1, videoA.ID}: app.ErrEnrollmentRequired}}
	if _, err := newService(t, newRepo(videoA), &fakeRemote{}, gate).Resolve(ctx, student, courseA, lecture1, videoA.ID); !errors.Is(err, app.ErrEnrollmentRequired) {
		t.Fatalf("gate err = %v", err)
	}
	if _, err := newService(t, newRepo(videoA), &fakeRemote{}, fakeAccess{}).Resolve(ctx, student, courseA, lecture1, videoA.ID); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("unreferenced err = %v", err)
	}
	if _, err := newService(t, newRepo(), &fakeRemote{}, fakeAccess{}).Resolve(ctx, student, courseA, lecture1, 404); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("missing err = %v", err)
	}
}

func TestAssetQueryKinds(t *testing.T) {
	kinds, err := app.NewAssetQuery(newRepo(videoA, imageA, videoB)).Kinds(ctx, courseA, []id.ID{videoA.ID, imageA.ID, videoB.ID})
	if err != nil || len(kinds) != 2 || kinds[videoA.ID] != domain.KindVideo || kinds[imageA.ID] != domain.KindImage {
		t.Fatalf("kinds = %v, %v", kinds, err)
	}
}
```

- [ ] **Step 3: Run to verify they fail**

Run: `go test ./internal/media/app/`
Expected: FAIL — `undefined: app.NewAssetService`.

- [ ] **Step 4: Implement** — `internal/media/app/service.go`:

```go
package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/media/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/clock"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

const (
	maxPartsPerRequest = 1000
	maxPartNumber      = 10000
)

// AssetService implements upload, status and playback use cases.
type AssetService struct {
	assets  AssetRepository
	remote  Remote
	courses CourseAccess
	ids     *id.Generator
	clock   clock.Clock
}

func NewAssetService(assets AssetRepository, remote Remote, courses CourseAccess, ids *id.Generator, clk clock.Clock) *AssetService {
	return &AssetService{assets: assets, remote: remote, courses: courses, ids: ids, clock: clk}
}

type UploadInput struct {
	Kind        string
	ContentType string
	Filename    string
	SizeBytes   int64
}

// AssetView is an asset's ownership row with the media service's current state.
type AssetView struct {
	Asset  domain.Asset
	Remote RemoteAsset
}

// Playback carries URLs only when Status is ready. URL is the signed HLS manifest for a
// video and the display rendition for an image.
type Playback struct {
	ID         id.ID
	Kind       domain.Kind
	Status     string
	URL        string
	PosterURL  string
	DurationMs int64
	Width      int
	Height     int
	ExpiresAt  time.Time
}

func (s *AssetService) CreateUpload(ctx context.Context, p auth.Principal, courseID id.ID, in UploadInput) (Upload, error) {
	kind, err := domain.ParseKind(in.Kind)
	if err != nil {
		return Upload{}, fmt.Errorf("%w: %w", ErrInvalidInput, err)
	}
	if !domain.ValidContentType(kind, in.ContentType) {
		return Upload{}, fmt.Errorf("%w: content_type %q is not accepted for %s", ErrInvalidInput, in.ContentType, kind)
	}
	if strings.TrimSpace(in.Filename) == "" {
		return Upload{}, fmt.Errorf("%w: filename is required", ErrInvalidInput)
	}
	if in.SizeBytes <= 0 {
		return Upload{}, fmt.Errorf("%w: size_bytes must be positive", ErrInvalidInput)
	}
	if err := s.courses.CanManage(ctx, p, courseID); err != nil {
		return Upload{}, err
	}
	up, err := s.remote.Create(ctx, RemoteCreate{
		Kind: kind, ContentType: in.ContentType, Filename: in.Filename, SizeBytes: in.SizeBytes,
		OwnerID: p.UserID, CourseID: courseID, IdempotencyKey: "ioe:upload:" + s.ids.New().String(),
	})
	if err != nil {
		return Upload{}, err
	}
	// A failed insert orphans the remote asset; the media service fails abandoned uploads itself.
	err = s.assets.Insert(ctx, domain.Asset{ID: up.AssetID, CourseID: courseID, Kind: kind, CreatedBy: p.UserID, CreatedAt: s.clock.Now()})
	if err != nil {
		return Upload{}, err
	}
	return up, nil
}

func (s *AssetService) PresignParts(ctx context.Context, p auth.Principal, assetID id.ID, partNumbers []int) ([]UploadPart, error) {
	if len(partNumbers) == 0 || len(partNumbers) > maxPartsPerRequest {
		return nil, fmt.Errorf("%w: part_numbers must hold 1 to %d entries", ErrInvalidInput, maxPartsPerRequest)
	}
	for _, n := range partNumbers {
		if n < 1 || n > maxPartNumber {
			return nil, fmt.Errorf("%w: part numbers must be between 1 and %d", ErrInvalidInput, maxPartNumber)
		}
	}
	if _, err := s.managed(ctx, p, assetID); err != nil {
		return nil, err
	}
	return s.remote.PresignParts(ctx, assetID, partNumbers)
}

func (s *AssetService) Complete(ctx context.Context, p auth.Principal, assetID id.ID, parts []CompletedPart) (AssetView, error) {
	a, err := s.managed(ctx, p, assetID)
	if err != nil {
		return AssetView{}, err
	}
	r, err := s.remote.Complete(ctx, assetID, parts)
	if err != nil {
		return AssetView{}, err
	}
	return AssetView{Asset: a, Remote: r}, nil
}

func (s *AssetService) Status(ctx context.Context, p auth.Principal, assetID id.ID) (AssetView, error) {
	a, err := s.managed(ctx, p, assetID)
	if err != nil {
		return AssetView{}, err
	}
	r, err := s.remote.Get(ctx, assetID)
	if err != nil {
		return AssetView{}, err
	}
	return AssetView{Asset: a, Remote: r}, nil
}

// Delete removes the remote asset, then the row. Existing block references then resolve to 404.
func (s *AssetService) Delete(ctx context.Context, p auth.Principal, assetID id.ID) error {
	if _, err := s.managed(ctx, p, assetID); err != nil {
		return err
	}
	if err := s.remote.Delete(ctx, assetID); err != nil && !errors.Is(err, ErrRemoteNotFound) {
		return err
	}
	return s.assets.Delete(ctx, assetID)
}

func (s *AssetService) Resolve(ctx context.Context, p auth.Principal, courseID, lectureID, assetID id.ID) (Playback, error) {
	a, err := s.assets.Find(ctx, assetID)
	if err != nil {
		return Playback{}, err
	}
	if a.CourseID != courseID {
		return Playback{}, ErrNotFound
	}
	if err := s.courses.CanReadLectureAsset(ctx, p, courseID, lectureID, assetID); err != nil {
		return Playback{}, err
	}
	r, err := s.remote.Get(ctx, assetID)
	if err != nil {
		return Playback{}, err
	}
	pb := Playback{ID: a.ID, Kind: a.Kind, Status: r.Status}
	if r.Status != StatusReady {
		return pb, nil
	}
	d, err := s.remote.Delivery(ctx, assetID)
	if err != nil {
		return Playback{}, err
	}
	pb.ExpiresAt = d.ExpiresAt
	switch a.Kind {
	case domain.KindVideo:
		if d.URL == "" {
			return Playback{}, fmt.Errorf("%w: ready video %s has no manifest URL", ErrRemoteUnavailable, assetID)
		}
		pb.URL, pb.DurationMs, pb.Width, pb.Height = d.URL, r.DurationMs, r.Width, r.Height
		if poster, ok := rendition(d, PosterRendition); ok {
			pb.PosterURL = poster.URL
		}
	case domain.KindImage:
		display, ok := rendition(d, DisplayVariant)
		if !ok || display.URL == "" {
			return Playback{}, fmt.Errorf("%w: ready image %s has no %s rendition", ErrRemoteUnavailable, assetID, DisplayVariant)
		}
		pb.URL, pb.Width, pb.Height = display.URL, display.Width, display.Height
	}
	return pb, nil
}

// managed loads the row and checks the caller manages its course. A caller who cannot
// manage it gets ErrNotFound so asset IDs cannot be probed.
func (s *AssetService) managed(ctx context.Context, p auth.Principal, assetID id.ID) (domain.Asset, error) {
	a, err := s.assets.Find(ctx, assetID)
	if err != nil {
		return domain.Asset{}, err
	}
	if err := s.courses.CanManage(ctx, p, a.CourseID); err != nil {
		if errors.Is(err, ErrForbidden) {
			return domain.Asset{}, ErrNotFound
		}
		return domain.Asset{}, err
	}
	return a, nil
}

func rendition(d Delivery, name string) (Rendition, bool) {
	for _, r := range d.Renditions {
		if r.Name == name {
			return r, true
		}
	}
	return Rendition{}, false
}
```

`internal/media/app/query.go`:

```go
package app

import (
	"context"

	"github.com/santoshkc2200/ioe-backend/internal/media/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

// AssetQuery answers which assets belong to a course. It needs no media service, so
// content validation works when media uploads are disabled.
type AssetQuery struct{ assets AssetRepository }

func NewAssetQuery(assets AssetRepository) *AssetQuery { return &AssetQuery{assets: assets} }

// Kinds returns the kinds of the given IDs that belong to courseID; others are absent.
func (q *AssetQuery) Kinds(ctx context.Context, courseID id.ID, ids []id.ID) (map[id.ID]domain.Kind, error) {
	return q.assets.KindsInCourse(ctx, courseID, ids)
}
```

- [ ] **Step 5: Run tests**

Run: `go test -race ./internal/media/... && make lint`
Expected: PASS; lint clean (depguard confirms `media/app` imports only allowed packages).

- [ ] **Step 6: Commit**

```bash
git add internal/media/app
git commit -m "feat(media): add upload and playback use cases"
```

---

### Task 5: Media HTTP adapter

**Files:**
- Create: `internal/media/adapters/httpapi/httpapi.go`, `internal/media/adapters/httpapi/wire.go`, `internal/media/adapters/httpapi/httpapi_test.go`

**Interfaces:**
- Consumes: Task 4 `AssetService` method set (via the `Service` interface below), app types and sentinels.
- Produces: `type Config struct { RequireAuth httpserver.Middleware; CreateLimiter *httpserver.RateLimiter; Logger *slog.Logger }`; `func New(svc Service, cfg Config) *Handler`; `func (h *Handler) Register(r *httpserver.Router)`.

- [ ] **Step 1: Write the failing tests** — `internal/media/adapters/httpapi/httpapi_test.go`:

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

	"github.com/santoshkc2200/ioe-backend/internal/media/adapters/httpapi"
	"github.com/santoshkc2200/ioe-backend/internal/media/app"
	"github.com/santoshkc2200/ioe-backend/internal/media/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/httpserver"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

var t0 = time.Date(2026, 10, 6, 9, 0, 0, 0, time.UTC)

type stub struct {
	err      error
	upload   app.Upload
	view     app.AssetView
	playback app.Playback

	courseID, lectureID, assetID id.ID
	input                        app.UploadInput
	partNumbers                  []int
	parts                        []app.CompletedPart
	principal                    auth.Principal
}

func (s *stub) CreateUpload(_ context.Context, p auth.Principal, courseID id.ID, in app.UploadInput) (app.Upload, error) {
	s.principal, s.courseID, s.input = p, courseID, in
	return s.upload, s.err
}

func (s *stub) PresignParts(_ context.Context, _ auth.Principal, assetID id.ID, n []int) ([]app.UploadPart, error) {
	s.assetID, s.partNumbers = assetID, n
	return []app.UploadPart{{PartNumber: 2, URL: "https://objects.test/2"}}, s.err
}

func (s *stub) Complete(_ context.Context, _ auth.Principal, assetID id.ID, parts []app.CompletedPart) (app.AssetView, error) {
	s.assetID, s.parts = assetID, parts
	return s.view, s.err
}

func (s *stub) Status(_ context.Context, _ auth.Principal, assetID id.ID) (app.AssetView, error) {
	s.assetID = assetID
	return s.view, s.err
}

func (s *stub) Delete(_ context.Context, _ auth.Principal, assetID id.ID) error {
	s.assetID = assetID
	return s.err
}

func (s *stub) Resolve(_ context.Context, _ auth.Principal, courseID, lectureID, assetID id.ID) (app.Playback, error) {
	s.courseID, s.lectureID, s.assetID = courseID, lectureID, assetID
	return s.playback, s.err
}

func fakeAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{UserID: 100, Role: auth.RoleInstructor})))
	})
}

func newServer(s *stub, perMinute int) http.Handler {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	r, h := httpserver.NewRouter(httpserver.Options{Logger: logger, AllowedOrigins: []string{"https://app.test"}, ServiceName: "test"})
	httpapi.New(s, httpapi.Config{RequireAuth: fakeAuth, CreateLimiter: httpserver.NewRateLimiter(perMinute), Logger: logger}).Register(r)
	return h
}

func call(h http.Handler, method, path, body string) (*httptest.ResponseRecorder, map[string]any) {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w, out
}

var readyView = app.AssetView{
	Asset:  domain.Asset{ID: 900, CourseID: 10, Kind: domain.KindVideo},
	Remote: app.RemoteAsset{ID: 900, Status: "ready", ProgressPercent: 100, DurationMs: 60000, Width: 1280, Height: 720, UpdatedAt: t0},
}

func TestCreateUpload(t *testing.T) {
	s := &stub{upload: app.Upload{AssetID: 900, UploadID: "u", PartSize: 5, PartURLs: []app.UploadPart{{PartNumber: 1, URL: "https://objects.test/1"}}, ExpiresAt: t0}}
	w, body := call(newServer(s, 30), http.MethodPost, "/v1/courses/10/media/uploads",
		`{"kind":"video","content_type":"video/mp4","filename":"a.mp4","size_bytes":9}`)
	if w.Code != http.StatusCreated || body["asset_id"] != "900" || body["upload_id"] != "u" || body["part_size"] != float64(5) {
		t.Fatalf("%d %v", w.Code, body)
	}
	if _, ok := body["upload_url"]; ok {
		t.Fatalf("empty upload_url serialized: %v", body)
	}
	if s.courseID != 10 || s.input != (app.UploadInput{Kind: "video", ContentType: "video/mp4", Filename: "a.mp4", SizeBytes: 9}) || s.principal.UserID != 100 {
		t.Fatalf("stub = %+v", s)
	}
}

func TestCreateUploadRateLimited(t *testing.T) {
	h := newServer(&stub{}, 1)
	body := `{"kind":"video","content_type":"video/mp4","filename":"a.mp4","size_bytes":9}`
	if w, _ := call(h, http.MethodPost, "/v1/courses/10/media/uploads", body); w.Code != http.StatusCreated {
		t.Fatalf("first = %d", w.Code)
	}
	if w, b := call(h, http.MethodPost, "/v1/courses/10/media/uploads", body); w.Code != http.StatusTooManyRequests || b["type"] != "rate_limited" {
		t.Fatalf("second = %d %v", w.Code, b)
	}
}

func TestPartsCompleteStatusDelete(t *testing.T) {
	s := &stub{view: readyView}
	h := newServer(s, 30)
	w, body := call(h, http.MethodPost, "/v1/media/uploads/900/parts", `{"part_numbers":[2]}`)
	if w.Code != http.StatusOK || len(body["part_urls"].([]any)) != 1 || s.assetID != 900 || s.partNumbers[0] != 2 {
		t.Fatalf("parts %d %v", w.Code, body)
	}
	w, body = call(h, http.MethodPost, "/v1/media/uploads/900/complete", `{"parts":[{"part_number":1,"etag":"e"}]}`)
	if w.Code != http.StatusOK || body["id"] != "900" || body["course_id"] != "10" || body["status"] != "ready" ||
		body["kind"] != "video" || body["progress_percent"] != float64(100) || s.parts[0] != (app.CompletedPart{PartNumber: 1, ETag: "e"}) {
		t.Fatalf("complete %d %v", w.Code, body)
	}
	if w, body = call(h, http.MethodPost, "/v1/media/uploads/900/complete", `{}`); w.Code != http.StatusOK || len(s.parts) != 0 {
		t.Fatalf("complete without parts %d %v", w.Code, body)
	}
	if w, body = call(h, http.MethodGet, "/v1/media/assets/900", ""); w.Code != http.StatusOK || body["duration_ms"] != float64(60000) {
		t.Fatalf("status %d %v", w.Code, body)
	}
	if w, _ = call(h, http.MethodDelete, "/v1/media/assets/900", ""); w.Code != http.StatusNoContent {
		t.Fatalf("delete %d", w.Code)
	}
}

func TestPlayback(t *testing.T) {
	s := &stub{playback: app.Playback{ID: 900, Kind: domain.KindVideo, Status: "ready", URL: "https://media.test/m3u8",
		PosterURL: "https://objects.test/p.jpg", DurationMs: 60000, Width: 1280, Height: 720, ExpiresAt: t0}}
	h := newServer(s, 30)
	w, body := call(h, http.MethodGet, "/v1/courses/10/lectures/20/media/900", "")
	if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-store" || body["playback_url"] != "https://media.test/m3u8" ||
		body["poster_url"] != "https://objects.test/p.jpg" || body["expires_at"] == nil || body["url"] != nil {
		t.Fatalf("video %d %v", w.Code, body)
	}
	if s.courseID != 10 || s.lectureID != 20 || s.assetID != 900 {
		t.Fatalf("ids = %+v", s)
	}
	s.playback = app.Playback{ID: 901, Kind: domain.KindImage, Status: "ready", URL: "https://objects.test/d.webp", Width: 1600, Height: 900, ExpiresAt: t0}
	if w, body = call(h, http.MethodGet, "/v1/courses/10/lectures/20/media/901", ""); w.Code != http.StatusOK ||
		body["url"] != "https://objects.test/d.webp" || body["playback_url"] != nil || body["width"] != float64(1600) {
		t.Fatalf("image %d %v", w.Code, body)
	}
	s.playback = app.Playback{ID: 900, Kind: domain.KindVideo, Status: "processing"}
	if w, body = call(h, http.MethodGet, "/v1/courses/10/lectures/20/media/900", ""); w.Code != http.StatusOK ||
		len(body) != 3 || body["status"] != "processing" {
		t.Fatalf("processing %d %v", w.Code, body)
	}
}

func TestErrorMapping(t *testing.T) {
	cases := []struct {
		err    error
		status int
		typ    string
	}{
		{app.ErrNotFound, 404, "not_found"},
		{app.ErrRemoteNotFound, 404, "not_found"},
		{app.ErrForbidden, 403, "forbidden"},
		{app.ErrEnrollmentRequired, 403, "enrollment_required"},
		{app.ErrCourseNotEditable, 409, "course_not_editable"},
		{app.ErrRemoteConflict, 409, "asset_state_conflict"},
		{app.ErrInvalidInput, 400, "invalid_input"},
		{app.ErrRemoteInvalid, 400, "invalid_input"},
		{app.ErrRemoteTooLarge, 413, "payload_too_large"},
		{app.ErrRemoteUnavailable, 502, "media_unavailable"},
		{io.ErrUnexpectedEOF, 500, "internal"},
	}
	for _, c := range cases {
		w, body := call(newServer(&stub{err: c.err}, 30), http.MethodGet, "/v1/courses/10/lectures/20/media/900", "")
		if w.Code != c.status || body["type"] != c.typ {
			t.Fatalf("%v: %d %v", c.err, w.Code, body)
		}
	}
}

func TestMalformedIDsAreNotFound(t *testing.T) {
	h := newServer(&stub{}, 30)
	for _, path := range []string{"/v1/media/assets/abc", "/v1/courses/x/lectures/20/media/900"} {
		if w, _ := call(h, http.MethodGet, path, ""); w.Code != http.StatusNotFound {
			t.Fatalf("%s: %d", path, w.Code)
		}
	}
}
```

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/media/adapters/httpapi/`
Expected: FAIL — package does not exist.

- [ ] **Step 3: Implement the wire shapes** — `internal/media/adapters/httpapi/wire.go`:

```go
package httpapi

import (
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/media/app"
	"github.com/santoshkc2200/ioe-backend/internal/media/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

type createUploadRequest struct {
	Kind        string `json:"kind"`
	ContentType string `json:"content_type"`
	Filename    string `json:"filename"`
	SizeBytes   int64  `json:"size_bytes"`
}

type partsRequest struct {
	PartNumbers []int `json:"part_numbers"`
}

type completedPartWire struct {
	PartNumber int    `json:"part_number"`
	ETag       string `json:"etag"`
}

type completeRequest struct {
	Parts []completedPartWire `json:"parts"`
}

type partWire struct {
	PartNumber int    `json:"part_number"`
	URL        string `json:"url"`
}

type uploadWire struct {
	AssetID   id.ID      `json:"asset_id"`
	UploadID  string     `json:"upload_id,omitempty"`
	UploadURL string     `json:"upload_url,omitempty"`
	PartSize  int64      `json:"part_size,omitempty"`
	PartURLs  []partWire `json:"part_urls,omitempty"`
	ExpiresAt time.Time  `json:"expires_at"`
}

type partsWire struct {
	PartURLs []partWire `json:"part_urls"`
}

type assetWire struct {
	ID              id.ID     `json:"id"`
	CourseID        id.ID     `json:"course_id"`
	Kind            string    `json:"kind"`
	Status          string    `json:"status"`
	ProgressPercent int       `json:"progress_percent"`
	DurationMs      int64     `json:"duration_ms,omitempty"`
	Width           int       `json:"width,omitempty"`
	Height          int       `json:"height,omitempty"`
	ErrorMessage    string    `json:"error_message,omitempty"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type playbackWire struct {
	ID          id.ID      `json:"id"`
	Kind        string     `json:"kind"`
	Status      string     `json:"status"`
	PlaybackURL string     `json:"playback_url,omitempty"`
	URL         string     `json:"url,omitempty"`
	PosterURL   string     `json:"poster_url,omitempty"`
	DurationMs  int64      `json:"duration_ms,omitempty"`
	Width       int        `json:"width,omitempty"`
	Height      int        `json:"height,omitempty"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
}

func toParts(in []app.UploadPart) []partWire {
	out := make([]partWire, len(in))
	for i, p := range in {
		out[i] = partWire{PartNumber: p.PartNumber, URL: p.URL}
	}
	return out
}

func toUploadWire(u app.Upload) uploadWire {
	return uploadWire{AssetID: u.AssetID, UploadID: u.UploadID, UploadURL: u.UploadURL,
		PartSize: u.PartSize, PartURLs: toParts(u.PartURLs), ExpiresAt: u.ExpiresAt}
}

func toAssetWire(v app.AssetView) assetWire {
	return assetWire{ID: v.Asset.ID, CourseID: v.Asset.CourseID, Kind: string(v.Asset.Kind),
		Status: v.Remote.Status, ProgressPercent: v.Remote.ProgressPercent, DurationMs: v.Remote.DurationMs,
		Width: v.Remote.Width, Height: v.Remote.Height, ErrorMessage: v.Remote.ErrorMessage, UpdatedAt: v.Remote.UpdatedAt}
}

func toPlaybackWire(p app.Playback) playbackWire {
	w := playbackWire{ID: p.ID, Kind: string(p.Kind), Status: p.Status}
	if p.URL == "" {
		return w
	}
	if p.Kind == domain.KindVideo {
		w.PlaybackURL, w.PosterURL, w.DurationMs = p.URL, p.PosterURL, p.DurationMs
	} else {
		w.URL = p.URL
	}
	w.Width, w.Height = p.Width, p.Height
	at := p.ExpiresAt
	w.ExpiresAt = &at
	return w
}
```

- [ ] **Step 4: Implement the handler** — `internal/media/adapters/httpapi/httpapi.go`:

```go
// Package httpapi exposes media uploads and playback over HTTP.
package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/santoshkc2200/ioe-backend/internal/media/app"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/httpserver"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
	"github.com/santoshkc2200/ioe-backend/internal/platform/problem"
)

// Service is the media use-case surface the handlers call.
type Service interface {
	CreateUpload(ctx context.Context, p auth.Principal, courseID id.ID, in app.UploadInput) (app.Upload, error)
	PresignParts(ctx context.Context, p auth.Principal, assetID id.ID, partNumbers []int) ([]app.UploadPart, error)
	Complete(ctx context.Context, p auth.Principal, assetID id.ID, parts []app.CompletedPart) (app.AssetView, error)
	Status(ctx context.Context, p auth.Principal, assetID id.ID) (app.AssetView, error)
	Delete(ctx context.Context, p auth.Principal, assetID id.ID) error
	Resolve(ctx context.Context, p auth.Principal, courseID, lectureID, assetID id.ID) (app.Playback, error)
}

type Config struct {
	RequireAuth   httpserver.Middleware
	CreateLimiter *httpserver.RateLimiter
	Logger        *slog.Logger
}

type Handler struct {
	svc Service
	cfg Config
}

func New(svc Service, cfg Config) *Handler { return &Handler{svc: svc, cfg: cfg} }

// Register mounts the media routes. Every route requires authentication and is not cacheable.
func (h *Handler) Register(r *httpserver.Router) {
	a := func(f http.HandlerFunc) http.Handler { return httpserver.NoStore(h.cfg.RequireAuth(f)) }
	r.Handle("POST /v1/courses/{courseID}/media/uploads", a(h.limitCreate(h.createUpload)))
	r.Handle("POST /v1/media/uploads/{assetID}/parts", a(h.presignParts))
	r.Handle("POST /v1/media/uploads/{assetID}/complete", a(h.complete))
	r.Handle("GET /v1/media/assets/{assetID}", a(h.status))
	r.Handle("DELETE /v1/media/assets/{assetID}", a(h.delete))
	r.Handle("GET /v1/courses/{courseID}/lectures/{lectureID}/media/{assetID}", a(h.playback))
}

// limitCreate rate-limits upload creation per user.
func (h *Handler) limitCreate(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !h.cfg.CreateLimiter.Allow(principal(r).UserID.String()) {
			w.Header().Set("Retry-After", "60")
			problem.Write(w, r, http.StatusTooManyRequests, problem.TypeRateLimited, "Too Many Requests", "")
			return
		}
		next(w, r)
	}
}

func (h *Handler) createUpload(w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "courseID")
	if !ok {
		return
	}
	var req createUploadRequest
	if !httpserver.DecodeJSON(w, r, &req) {
		return
	}
	up, err := h.svc.CreateUpload(r.Context(), principal(r), ids[0], app.UploadInput{
		Kind: req.Kind, ContentType: req.ContentType, Filename: req.Filename, SizeBytes: req.SizeBytes})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, toUploadWire(up))
}

func (h *Handler) presignParts(w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "assetID")
	if !ok {
		return
	}
	var req partsRequest
	if !httpserver.DecodeJSON(w, r, &req) {
		return
	}
	parts, err := h.svc.PresignParts(r.Context(), principal(r), ids[0], req.PartNumbers)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, partsWire{PartURLs: toParts(parts)})
}

func (h *Handler) complete(w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "assetID")
	if !ok {
		return
	}
	var req completeRequest
	if !httpserver.DecodeJSON(w, r, &req) {
		return
	}
	parts := make([]app.CompletedPart, len(req.Parts))
	for i, p := range req.Parts {
		parts[i] = app.CompletedPart{PartNumber: p.PartNumber, ETag: p.ETag}
	}
	v, err := h.svc.Complete(r.Context(), principal(r), ids[0], parts)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toAssetWire(v))
}

func (h *Handler) status(w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "assetID")
	if !ok {
		return
	}
	v, err := h.svc.Status(r.Context(), principal(r), ids[0])
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toAssetWire(v))
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "assetID")
	if !ok {
		return
	}
	if err := h.svc.Delete(r.Context(), principal(r), ids[0]); err != nil {
		h.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) playback(w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "courseID", "lectureID", "assetID")
	if !ok {
		return
	}
	pb, err := h.svc.Resolve(r.Context(), principal(r), ids[0], ids[1], ids[2])
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toPlaybackWire(pb))
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
	{app.ErrRemoteNotFound, http.StatusNotFound, "not_found", "Not Found"},
	{app.ErrForbidden, http.StatusForbidden, "forbidden", "Forbidden"},
	{app.ErrEnrollmentRequired, http.StatusForbidden, "enrollment_required", "Enrollment Required"},
	{app.ErrCourseNotEditable, http.StatusConflict, "course_not_editable", "Course Not Editable"},
	{app.ErrRemoteConflict, http.StatusConflict, "asset_state_conflict", "Asset State Conflict"},
	{app.ErrInvalidInput, http.StatusBadRequest, "invalid_input", "Invalid Input"},
	{app.ErrRemoteInvalid, http.StatusBadRequest, "invalid_input", "Invalid Input"},
	{app.ErrRemoteTooLarge, http.StatusRequestEntityTooLarge, problem.TypePayloadTooLarge, "Payload Too Large"},
	{app.ErrRemoteUnavailable, http.StatusBadGateway, "media_unavailable", "Media Unavailable"},
}

func (h *Handler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	for _, m := range errorMappings {
		if errors.Is(err, m.err) {
			detail := ""
			if m.status == http.StatusBadRequest {
				detail = err.Error()
			}
			if m.status == http.StatusBadGateway {
				h.cfg.Logger.WarnContext(r.Context(), "media service unavailable", "error", err)
			}
			problem.Write(w, r, m.status, m.typ, m.title, detail)
			return
		}
	}
	h.cfg.Logger.ErrorContext(r.Context(), "media request failed", "error", err)
	problem.Write(w, r, http.StatusInternalServerError, problem.TypeInternal, "Internal Server Error", "")
}
```

- [ ] **Step 5: Run tests**

Run: `go test -race ./internal/media/... && make lint`
Expected: PASS; lint clean.

- [ ] **Step 6: Commit**

```bash
git add internal/media/adapters/httpapi
git commit -m "feat(media): expose uploads and playback over HTTP"
```

---

### Task 6: Courseauthoring media references and access checks

**Files:**
- Create: `internal/courseauthoring/app/assets.go`, `internal/courseauthoring/app/assets_test.go`
- Modify: `internal/courseauthoring/app/errors.go`, `course_service.go`, `content_service.go`
- Modify tests: `internal/courseauthoring/app/fakes_test.go`, `course_service_test.go`, `content_service_test.go`
- Modify: `internal/courseauthoring/adapters/httpapi/httpapi.go`, `fakes_test.go`, `httpapi_test.go`
- Modify: `cmd/api/app.go` (constructor call sites only, so the tree compiles)

**Interfaces:**
- Produces:

```go
type AssetKind string
const ( AssetVideo AssetKind = "video"; AssetImage AssetKind = "image" )
type AssetCatalog interface {
	Kinds(ctx context.Context, courseID id.ID, ids []id.ID) (map[id.ID]AssetKind, error)
}
var ErrInvalidMediaReference = errors.New("invalid media reference")
func NewCourseService(tx TxRunner, ids *id.Generator, c clock.Clock, assets AssetCatalog) *CourseService
func NewContentService(tx TxRunner, ids *id.Generator, enrollments EnrollmentQuery, assets AssetCatalog) *ContentService
func (s *CourseService) CheckManage(ctx context.Context, p auth.Principal, courseID id.ID) error
func (s *ContentService) CheckAssetRead(ctx context.Context, p auth.Principal, courseID, lectureID, assetID id.ID) error
```

- [ ] **Step 1: Update test fakes and constructors first** — in `internal/courseauthoring/app/fakes_test.go`, below the `enrolled` type, add:

```go
// assetCatalog maps {courseID, assetID} to a kind.
type assetCatalog map[[2]id.ID]app.AssetKind

var _ app.AssetCatalog = assetCatalog(nil)

func (c assetCatalog) Kinds(_ context.Context, courseID id.ID, ids []id.ID) (map[id.ID]app.AssetKind, error) {
	out := map[id.ID]app.AssetKind{}
	for _, v := range ids {
		if k, ok := c[[2]id.ID{courseID, v}]; ok {
			out[v] = k
		}
	}
	return out, nil
}
```

Update every constructor call in the app tests:
- `course_service_test.go:27`: `app.NewCourseService(store, testIDs(t), fixedClock{time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)}, assetCatalog{})`
- `content_service_test.go:27`: `app.NewContentService(store, testIDs(t), enroll, assetCatalog{})`
- `content_service_test.go:61`: `app.NewContentService(g.store, testIDs(t), enrolled{{g.course.ID, student.UserID}: true}, assetCatalog{})`
- `content_service_test.go:85`: `app.NewContentService(f.store, testIDs(t), txProbe{t: t, store: f.store}, assetCatalog{})`

In `internal/courseauthoring/adapters/httpapi/fakes_test.go`, add the same `assetCatalog` type and method (with the package's `app` import), and in `httpapi_test.go:55` change to:

```go
	httpapi.New(app.NewCourseService(store, ids, clk, assetCatalog{}), app.NewContentService(store, ids, enrolled{}, assetCatalog{}), httpapi.Config{
```

In `cmd/api/app.go` `registerCourseAuthoring`, temporarily pass `nil` catalogs is not allowed (nil interface would panic on use). Instead change the function signature now to accept the catalog and pass it through:

```go
func registerCourseAuthoring(r *httpserver.Router, pool *pgxpool.Pool, ids *id.Generator, clk clock.Clock, enrollments courseauthoringapp.EnrollmentQuery, assets courseauthoringapp.AssetCatalog, authn *httpapi.Handler, ips httpserver.IPResolver, logger *slog.Logger) (*courseauthoringapp.CourseService, *courseauthoringapp.ContentService) {
	tx := courseauthoringpg.NewTxRunner(pool, clk)
	courses := courseauthoringapp.NewCourseService(tx, ids, clk, assets)
	contents := courseauthoringapp.NewContentService(tx, ids, enrollments, assets)
	courseauthoringhttp.New(
		courses,
		contents,
		courseauthoringhttp.Config{
			RequireAuth: authn.RequireAuth, OptionalAuth: authn.OptionalAuth, IPs: ips,
			ContentLimiter: httpserver.NewRateLimiter(60), CatalogLimiter: httpserver.NewRateLimiter(120),
			Logger: logger,
		},
	).Register(r)
	return courses, contents
}
```

Update its doc comment to "returns its course and content services for contexts that read course facts or check lecture access." In `buildApp`, replace the `courses := registerCourseAuthoring(...)` line with:

```go
	courses, _ := registerCourseAuthoring(router, pool, ids, clk, enrollmentAccess, noAssets{}, identityHandler, ips, logger)
```

and add at the bottom of `cmd/api/app.go` (Task 7 replaces it with the media-backed catalog):

```go
// noAssets is a placeholder catalog that knows no assets; Task 7 replaces it.
type noAssets struct{}

func (noAssets) Kinds(context.Context, id.ID, []id.ID) (map[id.ID]courseauthoringapp.AssetKind, error) {
	return map[id.ID]courseauthoringapp.AssetKind{}, nil
}
```

- [ ] **Step 2: Write the failing tests** — `internal/courseauthoring/app/assets_test.go`:

```go
package app_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/app"
	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

const (
	videoAsset   id.ID = 9001
	imageAsset   id.ID = 9002
	foreignAsset id.ID = 9003
)

type assetFixture struct {
	contentFixture
	cat assetCatalog
}

func newAssetFixture(t *testing.T) assetFixture {
	t.Helper()
	f := newContentFixture(t, enrolled{})
	cat := assetCatalog{
		{f.course.ID, videoAsset}: app.AssetVideo,
		{f.course.ID, imageAsset}: app.AssetImage,
		{777, foreignAsset}:       app.AssetVideo,
	}
	f.contents = app.NewContentService(f.store, testIDs(t), enrolled{}, cat)
	f.courses = app.NewCourseService(f.store, testIDs(t), fixedClock{time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)}, cat)
	return assetFixture{contentFixture: f, cat: cat}
}

func videoBlock(cid string, asset id.ID) app.BlockInput {
	return app.BlockInput{ClientBlockID: cid, Type: "video", MediaAssetID: asset.String(), DurationMs: 60000}
}

func imageBlock(cid string, asset id.ID) app.BlockInput {
	return app.BlockInput{ClientBlockID: cid, Type: "image", MediaAssetID: asset.String()}
}

func deckBlock(cid string, asset id.ID) app.BlockInput {
	return app.BlockInput{ClientBlockID: cid, Type: "flashcard", Cards: []app.FlashcardCardInput{
		{Front: "f", Back: "b"}, {Front: "f2", Back: "b2", MediaAssetID: asset.String()}}}
}

func TestReplaceValidatesMediaReferences(t *testing.T) {
	f := newAssetFixture(t)
	ok := []app.BlockInput{videoBlock("v", videoAsset), imageBlock("i", imageAsset), deckBlock("d", imageAsset)}
	if err := f.contents.Replace(ctx, owner, f.course.ID, f.locked, ok, app.LegacyContent{}); err != nil {
		t.Fatalf("valid refs err = %v", err)
	}
	bad := map[string]app.BlockInput{
		"video holds image":     videoBlock("v", imageAsset),
		"image holds video":     imageBlock("i", videoAsset),
		"card holds video":      deckBlock("d", videoAsset),
		"other course's asset":  videoBlock("v", foreignAsset),
		"unknown asset":         imageBlock("i", 424242),
	}
	for name, block := range bad {
		err := f.contents.Replace(ctx, owner, f.course.ID, f.locked, []app.BlockInput{block}, app.LegacyContent{})
		if !errors.Is(err, app.ErrInvalidMediaReference) || !strings.Contains(err.Error(), block.ClientBlockID) {
			t.Fatalf("%s: err = %v", name, err)
		}
		if errors.Is(err, app.ErrInvalidInput) {
			t.Fatalf("%s: must not also be invalid_input", name)
		}
	}
	urlVideo := app.BlockInput{ClientBlockID: "u", Type: "video", URL: "https://videos.test/a.mp4"}
	if err := f.contents.Replace(ctx, owner, f.course.ID, f.locked, []app.BlockInput{urlVideo}, app.LegacyContent{}); err != nil {
		t.Fatalf("url video err = %v", err)
	}
}

func TestPatchValidatesOnlyUpserts(t *testing.T) {
	f := newAssetFixture(t)
	if err := f.contents.Replace(ctx, owner, f.course.ID, f.locked, []app.BlockInput{videoBlock("v", videoAsset)}, app.LegacyContent{}); err != nil {
		t.Fatal(err)
	}
	v, _ := f.contents.Get(ctx, owner, f.course.ID, f.locked)
	// The catalog forgets the video: untouched blocks are not re-checked.
	f.contents = app.NewContentService(f.store, testIDs(t), enrolled{}, assetCatalog{})
	rev, err := f.contents.Patch(ctx, owner, f.course.ID, f.locked, app.PatchInput{
		BaseRevision: ptr(v.ContentRevision), Order: []string{"v", "t"},
		Upserts: []app.BlockInput{{ClientBlockID: "t", Type: "text", Body: "<p>t</p>"}},
	})
	if err != nil {
		t.Fatalf("patch without media upserts err = %v", err)
	}
	_, err = f.contents.Patch(ctx, owner, f.course.ID, f.locked, app.PatchInput{
		BaseRevision: ptr(rev), Order: []string{"v", "t", "i"},
		Upserts: []app.BlockInput{imageBlock("i", imageAsset)},
	})
	if !errors.Is(err, app.ErrInvalidMediaReference) {
		t.Fatalf("patch with unknown upsert err = %v", err)
	}
}

func TestAddLectureValidatesMediaReferences(t *testing.T) {
	f := newAssetFixture(t)
	if _, err := f.courses.AddLecture(ctx, owner, f.course.ID, app.AddLectureInput{Title: "V", Blocks: []app.BlockInput{videoBlock("v", videoAsset)}}); err != nil {
		t.Fatalf("valid err = %v", err)
	}
	if _, err := f.courses.AddLecture(ctx, owner, f.course.ID, app.AddLectureInput{Title: "X", Blocks: []app.BlockInput{videoBlock("v", foreignAsset)}}); !errors.Is(err, app.ErrInvalidMediaReference) {
		t.Fatalf("foreign err = %v", err)
	}
}

func TestMediaReferenceDoesNotPreemptAuthorization(t *testing.T) {
	f := newAssetFixture(t)
	foreign := []app.BlockInput{videoBlock("v", foreignAsset)}
	// Draft course: a non-manager cannot see it at all.
	if err := f.contents.Replace(ctx, otherInstr, f.course.ID, f.locked, foreign, app.LegacyContent{}); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("draft replace err = %v", err)
	}
	if _, err := f.courses.AddLecture(ctx, otherInstr, f.course.ID, app.AddLectureInput{Title: "X", Blocks: foreign}); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("draft add err = %v", err)
	}
	if err := f.courses.Publish(ctx, owner, f.course.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.contents.Replace(ctx, student, f.course.ID, f.locked, foreign, app.LegacyContent{}); !errors.Is(err, app.ErrForbidden) {
		t.Fatalf("published replace err = %v", err)
	}
	v, _ := f.contents.Get(ctx, owner, f.course.ID, f.locked)
	first := v.Blocks[0].ClientBlockID()
	_, err := f.contents.Patch(ctx, student, f.course.ID, f.locked, app.PatchInput{
		BaseRevision: ptr(v.ContentRevision), Order: []string{first, "v"}, Upserts: foreign})
	if !errors.Is(err, app.ErrForbidden) {
		t.Fatalf("published patch err = %v", err)
	}
}

// catalogProbe fails the test if the catalog is consulted inside a store transaction.
type catalogProbe struct {
	t     *testing.T
	store *memStore
}

func (q catalogProbe) Kinds(context.Context, id.ID, []id.ID) (map[id.ID]app.AssetKind, error) {
	if !q.store.mu.TryLock() {
		q.t.Error("asset catalog consulted inside the content transaction")
		return nil, nil
	}
	q.store.mu.Unlock()
	return map[id.ID]app.AssetKind{}, nil
}

func TestCatalogIsConsultedOutsideTx(t *testing.T) {
	f := newContentFixture(t, enrolled{})
	probe := catalogProbe{t: t, store: f.store}
	contents := app.NewContentService(f.store, testIDs(t), enrolled{}, probe)
	courses := app.NewCourseService(f.store, testIDs(t), fixedClock{time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)}, probe)
	_ = contents.Replace(ctx, owner, f.course.ID, f.locked, []app.BlockInput{videoBlock("v", videoAsset)}, app.LegacyContent{})
	v, _ := contents.Get(ctx, owner, f.course.ID, f.locked)
	_, _ = contents.Patch(ctx, owner, f.course.ID, f.locked, app.PatchInput{BaseRevision: ptr(v.ContentRevision),
		Order: []string{v.Blocks[0].ClientBlockID(), "v"}, Upserts: []app.BlockInput{videoBlock("v", videoAsset)}})
	_, _ = courses.AddLecture(ctx, owner, f.course.ID, app.AddLectureInput{Title: "V", Blocks: []app.BlockInput{videoBlock("v", videoAsset)}})
}

func TestCheckManage(t *testing.T) {
	f := newAssetFixture(t)
	if err := f.courses.CheckManage(ctx, owner, f.course.ID); err != nil {
		t.Fatalf("owner err = %v", err)
	}
	if err := f.courses.CheckManage(ctx, admin, f.course.ID); err != nil {
		t.Fatalf("admin err = %v", err)
	}
	if err := f.courses.CheckManage(ctx, otherInstr, f.course.ID); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("other on draft err = %v", err)
	}
	_ = f.courses.Publish(ctx, owner, f.course.ID)
	if err := f.courses.CheckManage(ctx, otherInstr, f.course.ID); !errors.Is(err, app.ErrForbidden) {
		t.Fatalf("other on published err = %v", err)
	}
	_ = f.courses.Archive(ctx, owner, f.course.ID)
	if err := f.courses.CheckManage(ctx, owner, f.course.ID); !errors.Is(err, domain.ErrCourseNotEditable) {
		t.Fatalf("archived err = %v", err)
	}
	if err := f.courses.CheckManage(ctx, owner, 424242); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("missing err = %v", err)
	}
}

func TestCheckAssetRead(t *testing.T) {
	f := newAssetFixture(t)
	blocks := []app.BlockInput{videoBlock("v", videoAsset), deckBlock("d", imageAsset)}
	if err := f.contents.Replace(ctx, owner, f.course.ID, f.locked, blocks, app.LegacyContent{}); err != nil {
		t.Fatal(err)
	}
	_ = f.courses.Publish(ctx, owner, f.course.ID)
	if err := f.contents.CheckAssetRead(ctx, owner, f.course.ID, f.locked, videoAsset); err != nil {
		t.Fatalf("owner video err = %v", err)
	}
	if err := f.contents.CheckAssetRead(ctx, owner, f.course.ID, f.locked, imageAsset); err != nil {
		t.Fatalf("owner card image err = %v", err)
	}
	if err := f.contents.CheckAssetRead(ctx, owner, f.course.ID, f.free, videoAsset); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("unreferenced err = %v", err)
	}
	if err := f.contents.CheckAssetRead(ctx, student, f.course.ID, f.locked, videoAsset); !errors.Is(err, app.ErrEnrollmentRequired) {
		t.Fatalf("not enrolled err = %v", err)
	}
	enrolledContents := app.NewContentService(f.store, testIDs(t), enrolled{{f.course.ID, student.UserID}: true}, f.cat)
	if err := enrolledContents.CheckAssetRead(ctx, student, f.course.ID, f.locked, videoAsset); err != nil {
		t.Fatalf("enrolled err = %v", err)
	}
}
```

Also add the mapping test to `internal/courseauthoring/adapters/httpapi/httpapi_test.go` (it reuses that file's `newServer`, `lectureFlow`, `call`, `expectProblem`, `instr`, `instrRL`; the server's catalog is the empty `assetCatalog{}`, so every asset ID is unknown):

```go
func TestInvalidMediaReference(t *testing.T) {
	h := newServer(t, 100)
	cid, _, lid := lectureFlow(t, h)
	resp, body := call(h, "PUT", "/v1/courses/"+cid+"/lectures/"+lid+"/content", instr, instrRL,
		`{"blocks":[{"client_block_id":"v","type":"video","media_asset_id":"9001","duration_ms":60000}]}`)
	expectProblem(t, resp, body, 400, "invalid_media_reference")
}
```

- [ ] **Step 3: Run to verify they fail**

Run: `go test ./internal/courseauthoring/...`
Expected: FAIL — `undefined: app.AssetKind` / `app.ErrInvalidMediaReference`.

- [ ] **Step 4: Implement the port and helpers** — `internal/courseauthoring/app/assets.go`:

```go
package app

import (
	"context"
	"fmt"

	"github.com/santoshkc2200/ioe-backend/internal/platform/contentblocks"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

// AssetKind is the kind of an uploaded media asset.
type AssetKind string

const (
	AssetVideo AssetKind = "video"
	AssetImage AssetKind = "image"
)

// AssetCatalog reports the kinds of the given asset IDs that belong to courseID. IDs that do
// not exist or belong to another course are absent from the result.
type AssetCatalog interface {
	Kinds(ctx context.Context, courseID id.ID, ids []id.ID) (map[id.ID]AssetKind, error)
}

type assetRef struct {
	id            id.ID
	kind          AssetKind
	clientBlockID string
}

// inputAssetRefs lists the media assets the given inputs reference. IDs that do not parse are
// skipped; block building rejects them as invalid input.
func inputAssetRefs(in []BlockInput) []assetRef {
	var refs []assetRef
	add := func(raw string, kind AssetKind, clientBlockID string) {
		if raw == "" {
			return
		}
		if v, err := id.Parse(raw); err == nil {
			refs = append(refs, assetRef{id: v, kind: kind, clientBlockID: clientBlockID})
		}
	}
	for _, b := range in {
		switch contentblocks.BlockType(b.Type) {
		case contentblocks.BlockTypeVideo:
			add(b.MediaAssetID, AssetVideo, b.ClientBlockID)
		case contentblocks.BlockTypeImage:
			add(b.MediaAssetID, AssetImage, b.ClientBlockID)
		case contentblocks.BlockTypeFlashcard:
			for _, c := range b.Cards {
				add(c.MediaAssetID, AssetImage, b.ClientBlockID)
			}
		}
	}
	return refs
}

// checkAssetRefs returns refErr when a reference is unknown, foreign, or of the wrong kind,
// and err when the catalog itself failed. Callers consult it before opening a transaction
// and return refErr only after authorizing the caller, so a non-manager learns nothing
// about other courses' assets.
func checkAssetRefs(ctx context.Context, catalog AssetCatalog, courseID id.ID, refs []assetRef) (refErr, err error) {
	if len(refs) == 0 {
		return nil, nil
	}
	ids := make([]id.ID, len(refs))
	for i, r := range refs {
		ids[i] = r.id
	}
	kinds, err := catalog.Kinds(ctx, courseID, ids)
	if err != nil {
		return nil, err
	}
	for _, r := range refs {
		if kinds[r.id] != r.kind {
			return fmt.Errorf("%w: block %q references media asset %s, which is not a %s in this course",
				ErrInvalidMediaReference, r.clientBlockID, r.id, r.kind), nil
		}
	}
	return nil, nil
}

// blocksReference reports whether any block references assetID as a video, an image, or a
// flashcard card image.
func blocksReference(blocks []contentblocks.Block, assetID id.ID) bool {
	for _, b := range blocks {
		if v, ok := b.Video(); ok && v.MediaAssetID() == assetID {
			return true
		}
		if img, ok := b.Image(); ok && img.MediaAssetID() == assetID {
			return true
		}
		if deck, ok := b.Deck(); ok {
			for _, c := range deck.Cards() {
				if c.MediaAssetID == assetID {
					return true
				}
			}
		}
	}
	return false
}
```

In `internal/courseauthoring/app/errors.go` add to the `var` block:

```go
	ErrInvalidMediaReference  = errors.New("invalid media reference")
```

- [ ] **Step 5: Wire the catalog into the services**

`course_service.go` — add an `assets AssetCatalog` field and constructor parameter:

```go
type CourseService struct {
	tx     TxRunner
	ids    *id.Generator
	clock  clock.Clock
	assets AssetCatalog
}

func NewCourseService(tx TxRunner, ids *id.Generator, c clock.Clock, assets AssetCatalog) *CourseService {
	return &CourseService{tx: tx, ids: ids, clock: c, assets: assets}
}
```

In `AddLecture`, after `buildContent` succeeds and before `s.tx.RunInTx`, add:

```go
	// Ask before opening the transaction (see ContentService.Get); report only after authorizing.
	refErr, err := checkAssetRefs(ctx, s.assets, courseID, inputAssetRefs(in.Blocks))
	if err != nil {
		return domain.Course{}, err
	}
```

and inside the transaction, immediately after the `loadManaged` error check, add:

```go
		if refErr != nil {
			return refErr
		}
```

Add after `Facts`:

```go
// CheckManage returns nil when p manages the course and it is not archived: ErrNotFound when
// p cannot see it, ErrForbidden when p does not manage it, domain.ErrCourseNotEditable when
// archived. For internal callers.
func (s *CourseService) CheckManage(ctx context.Context, p auth.Principal, courseID id.ID) error {
	return s.tx.RunInTx(ctx, func(r Repos) error {
		c, err := loadManaged(ctx, r, p, courseID)
		if err != nil {
			return err
		}
		if c.Status == domain.StatusArchived {
			return domain.ErrCourseNotEditable
		}
		return nil
	})
}
```

`content_service.go` — add the field and parameter:

```go
type ContentService struct {
	tx          TxRunner
	ids         *id.Generator
	enrollments EnrollmentQuery
	assets      AssetCatalog
}

func NewContentService(tx TxRunner, ids *id.Generator, enrollments EnrollmentQuery, assets AssetCatalog) *ContentService {
	return &ContentService{tx: tx, ids: ids, enrollments: enrollments, assets: assets}
}
```

In `Replace`, after `buildContent` succeeds:

```go
	refErr, err := checkAssetRefs(ctx, s.assets, courseID, inputAssetRefs(blocks))
	if err != nil {
		return err
	}
```

and inside its transaction replace the `lockEditable` call block with:

```go
		if _, err := lockEditable(ctx, r, p, courseID, lectureID); err != nil {
			return err
		}
		if refErr != nil {
			return refErr
		}
```

In `Patch`, after `indexPatch` succeeds:

```go
	refErr, err := checkAssetRefs(ctx, s.assets, courseID, inputAssetRefs(in.Upserts))
	if err != nil {
		return 0, err
	}
```

and inside its transaction, right after the `lockEditable` error check (before the revision comparison):

```go
		if refErr != nil {
			return refErr
		}
```

Add after `Get`:

```go
// CheckAssetRead applies Get's access rules and returns ErrNotFound unless the lecture's blocks
// reference assetID. For internal callers.
func (s *ContentService) CheckAssetRead(ctx context.Context, p auth.Principal, courseID, lectureID, assetID id.ID) error {
	v, err := s.Get(ctx, p, courseID, lectureID)
	if err != nil {
		return err
	}
	if !blocksReference(v.Blocks, assetID) {
		return ErrNotFound
	}
	return nil
}
```

- [ ] **Step 6: Map the error in HTTP** — in `internal/courseauthoring/adapters/httpapi/httpapi.go` `errorMappings`, insert before the `app.ErrInvalidInput` entry:

```go
	{app.ErrInvalidMediaReference, http.StatusBadRequest, "invalid_media_reference", "Invalid Media Reference"},
```

- [ ] **Step 7: Run tests**

Run: `go build ./... && go test -race ./internal/courseauthoring/... ./cmd/... && make lint`
Expected: PASS; lint clean.

- [ ] **Step 8: Commit**

```bash
git add internal/courseauthoring cmd/api/app.go
git commit -m "feat(courseauthoring): validate media references and expose access checks"
```

---

### Task 7: Composition, end-to-end test, contract, docs, and local stack

**Files:**
- Create: `cmd/api/media.go`, `compose/media-db-setup.sql`
- Modify: `cmd/api/app.go`, `cmd/api/e2e_integration_test.go`, `api/openapi.yaml`, `README.md`, `docker-compose.yml`, `.env.example`, `.gitleaks.toml` (only if gitleaks flags the local key)

**Interfaces:**
- Consumes: everything above.
- Produces: `registerMedia(r *httpserver.Router, assets *mediapg.Assets, courses *courseauthoringapp.CourseService, contents *courseauthoringapp.ContentService, ids *id.Generator, clk clock.Clock, cfg config.Config, requireAuth httpserver.Middleware, logger *slog.Logger)`; `mediaAssetCatalog`; `mediaCourseAccess`.

- [ ] **Step 1: Write the failing end-to-end test** — append to `cmd/api/e2e_integration_test.go` (add `"strconv"` and `"sync"` to its imports):

```go
const mediaKey = "test-media-api-key-0123456789abcdef"

// fakeMediaService implements the subset of the media service API the backend calls.
type fakeMediaService struct {
	mu     sync.Mutex
	next   int64
	assets map[string]string // id -> kind
	auth   []string
}

func newFakeMediaService(t *testing.T) (*fakeMediaService, *httptest.Server) {
	t.Helper()
	f := &fakeMediaService{next: 900000000000000000, assets: map[string]string{}}
	asset := func(w http.ResponseWriter, assetID, kind string) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": assetID, "namespace_id": "ioe", "kind": kind, "status": "ready", "progress_percent": 100,
			"duration_ms": 60000, "width": 1280, "height": 720, "version": 1, "updated_at": time.Now().UTC(),
		})
	}
	find := func(w http.ResponseWriter, r *http.Request) (string, string, bool) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.auth = append(f.auth, r.Header.Get("Authorization")+"|"+r.Header.Get("X-Namespace-ID"))
		assetID := r.PathValue("id")
		kind, ok := f.assets[assetID]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"code":"not_found","detail":"asset not found","status":404}`)
		}
		return assetID, kind, ok
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/assets", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Kind string `json:"kind"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.next++
		assetID := strconv.FormatInt(f.next, 10)
		f.assets[assetID] = body.Kind
		f.auth = append(f.auth, r.Header.Get("Authorization")+"|"+r.Header.Get("X-Namespace-ID"))
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"asset_id": assetID, "namespace_id": "ioe", "upload_id": "up-" + assetID, "part_size": 5242880,
			"part_urls":  []map[string]any{{"part_number": 1, "url": "http://objects.test/" + assetID + "/1"}},
			"expires_at": time.Now().Add(time.Hour).UTC(),
		})
	})
	mux.HandleFunc("POST /v1/assets/{id}/complete", func(w http.ResponseWriter, r *http.Request) {
		if assetID, kind, ok := find(w, r); ok {
			asset(w, assetID, kind)
		}
	})
	mux.HandleFunc("GET /v1/assets/{id}", func(w http.ResponseWriter, r *http.Request) {
		if assetID, kind, ok := find(w, r); ok {
			asset(w, assetID, kind)
		}
	})
	mux.HandleFunc("POST /v1/assets/{id}/delivery", func(w http.ResponseWriter, r *http.Request) {
		assetID, _, ok := find(w, r)
		if !ok {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"visibility": "private", "url": "/v1/delivery/" + assetID + "/master.m3u8?token=t",
			"expires_at": time.Now().Add(15 * time.Minute).UTC(),
			"renditions": []map[string]any{{"name": "poster", "content_type": "image/jpeg", "url": "http://objects.test/" + assetID + "/poster.jpg"}},
		})
	})
	mux.HandleFunc("DELETE /v1/assets/{id}", func(w http.ResponseWriter, r *http.Request) {
		if assetID, _, ok := find(w, r); ok {
			f.mu.Lock()
			delete(f.assets, assetID)
			f.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return f, srv
}

func TestMediaEndToEnd(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pool := pgtest.New(t)
	google := googletest.NewIssuer(t)
	media, mediaSrv := newFakeMediaService(t)
	cfg := baseConfig(t, google)
	cfg.MediaServiceBaseURL = mediaSrv.URL
	cfg.MediaServicePublicURL = "https://media.test"
	cfg.MediaServiceAPIKey = mediaKey
	a, err := buildApp(ctx, cfg, slog.New(slog.NewJSONHandler(io.Discard, nil)), pool)
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
	strangerTok, _ := signIn("sub-stranger", "stranger@example.com")
	admin, student, stranger := bearer(adminTok), bearer(studentTok), bearer(strangerTok)

	newCourse := func(title string) string {
		resp, body := c.do(http.MethodPost, "/v1/courses", `{"title":"`+title+`","description":"d"}`, admin)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("create course: %d %v", resp.StatusCode, body)
		}
		return body["id"].(string)
	}
	addLecture := func(courseID, title string) string {
		resp, body := c.do(http.MethodPost, "/v1/courses/"+courseID+"/lectures", `{"title":"`+title+`","text_body":"<p>x</p>"}`, admin)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("lecture: %d %v", resp.StatusCode, body)
		}
		ls := body["lectures"].([]any)
		return ls[len(ls)-1].(map[string]any)["id"].(string)
	}
	upload := func(courseID string, who map[string]string) (int, string) {
		resp, body := c.do(http.MethodPost, "/v1/courses/"+courseID+"/media/uploads",
			`{"kind":"video","content_type":"video/mp4","filename":"a.mp4","size_bytes":1048576}`, who)
		id, _ := body["asset_id"].(string)
		return resp.StatusCode, id
	}
	putVideo := func(courseID, lectureID, assetID string) (int, any) {
		resp, body := c.do(http.MethodPut, "/v1/courses/"+courseID+"/lectures/"+lectureID+"/content",
			`{"blocks":[{"client_block_id":"v","type":"video","media_asset_id":"`+assetID+`","duration_ms":60000}]}`, admin)
		return resp.StatusCode, body["type"]
	}
	play := func(courseID, lectureID, assetID string, who map[string]string) (int, map[string]any) {
		resp, body := c.do(http.MethodGet, "/v1/courses/"+courseID+"/lectures/"+lectureID+"/media/"+assetID, "", who)
		return resp.StatusCode, body
	}

	courseA, courseB := newCourse("A"), newCourse("B")
	locked, preview := addLecture(courseA, "Locked"), addLecture(courseA, "Preview")

	if code, _ := upload(courseA, student); code != http.StatusNotFound {
		t.Fatalf("student upload to draft: %d", code)
	}
	code, video := upload(courseA, admin)
	if code != http.StatusCreated || video == "" {
		t.Fatalf("upload: %d %q", code, video)
	}
	resp, body := c.do(http.MethodPost, "/v1/media/uploads/"+video+"/complete", `{"parts":[{"part_number":1,"etag":"e1"}]}`, admin)
	if resp.StatusCode != http.StatusOK || body["status"] != "ready" || body["course_id"] != courseA {
		t.Fatalf("complete: %d %v", resp.StatusCode, body)
	}
	if resp, body = c.do(http.MethodGet, "/v1/media/assets/"+video, "", admin); resp.StatusCode != http.StatusOK || body["kind"] != "video" {
		t.Fatalf("status: %d %v", resp.StatusCode, body)
	}
	if resp, _ = c.do(http.MethodGet, "/v1/media/assets/"+video, "", stranger); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("stranger status: %d", resp.StatusCode)
	}
	_, foreign := upload(courseB, admin)

	if code, typ := putVideo(courseA, locked, foreign); code != http.StatusBadRequest || typ != "invalid_media_reference" {
		t.Fatalf("foreign asset: %d %v", code, typ)
	}
	if code, _ := putVideo(courseA, locked, video); code != http.StatusNoContent {
		t.Fatalf("put video: %d", code)
	}
	if resp, _ = c.do(http.MethodPost, "/v1/courses/"+courseA+"/lectures/"+preview+"/free-preview", `{"free_preview":true}`, admin); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("free preview: %d", resp.StatusCode)
	}
	if resp, _ = c.do(http.MethodPost, "/v1/courses/"+courseA+"/publish", "", admin); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("publish: %d", resp.StatusCode)
	}

	if code, body := play(courseA, locked, video, student); code != http.StatusForbidden || body["type"] != "enrollment_required" {
		t.Fatalf("before enroll: %d %v", code, body)
	}
	if code, _ := play(courseA, preview, video, stranger); code != http.StatusNotFound {
		t.Fatalf("unreferenced on preview lecture: %d", code)
	}
	if resp, body = c.do(http.MethodPost, "/v1/courses/"+courseA+"/enrollments/"+studentID, "", student); resp.StatusCode != http.StatusCreated {
		t.Fatalf("enroll: %d %v", resp.StatusCode, body)
	}
	code, body = play(courseA, locked, video, student)
	if code != http.StatusOK || body["status"] != "ready" ||
		body["playback_url"] != "https://media.test/v1/delivery/"+video+"/master.m3u8?token=t" ||
		body["poster_url"] != "http://objects.test/"+video+"/poster.jpg" {
		t.Fatalf("playback: %d %v", code, body)
	}

	if code, _ := putVideo(courseA, preview, video); code != http.StatusNoContent {
		t.Fatalf("put preview video: %d", code)
	}
	if code, body := play(courseA, preview, video, stranger); code != http.StatusOK || body["playback_url"] == nil {
		t.Fatalf("free preview playback: %d %v", code, body)
	}
	if code, _ := play(courseA, locked, foreign, student); code != http.StatusNotFound {
		t.Fatalf("foreign asset playback: %d", code)
	}

	if resp, _ = c.do(http.MethodDelete, "/v1/media/assets/"+video, "", admin); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: %d", resp.StatusCode)
	}
	if code, _ := play(courseA, locked, video, student); code != http.StatusNotFound {
		t.Fatalf("after delete: %d", code)
	}

	media.mu.Lock()
	defer media.mu.Unlock()
	for _, h := range media.auth {
		if h != "Bearer "+mediaKey+"|ioe" {
			t.Fatalf("media request headers = %q", h)
		}
	}
}

func TestMediaDisabledRoutesAreAbsent(t *testing.T) {
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
	resp, _ := client{t: t, base: srv.URL}.do(http.MethodGet, "/v1/media/assets/1", "", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}
```

Notes for the implementer: the student upload to a draft course returns `404` because `CheckManage` hides a draft course from non-managers. If the enrollment endpoint for a free course returns a different success code than `201`, copy it from `TestProgressEndToEnd`, which uses the same call.

- [ ] **Step 2: Run to verify it fails**

Run: `go test -race -tags integration ./cmd/api/ -run Media`
Expected: FAIL — upload route answers `404` for the admin (media not wired). Requires Docker.

- [ ] **Step 3: Implement composition** — `cmd/api/media.go`:

```go
package main

import (
	"context"
	"errors"
	"log/slog"

	courseauthoringapp "github.com/santoshkc2200/ioe-backend/internal/courseauthoring/app"
	courseauthoringdomain "github.com/santoshkc2200/ioe-backend/internal/courseauthoring/domain"
	mediahttp "github.com/santoshkc2200/ioe-backend/internal/media/adapters/httpapi"
	"github.com/santoshkc2200/ioe-backend/internal/media/adapters/mediasvc"
	mediapg "github.com/santoshkc2200/ioe-backend/internal/media/adapters/postgres"
	mediaapp "github.com/santoshkc2200/ioe-backend/internal/media/app"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/clock"
	"github.com/santoshkc2200/ioe-backend/internal/platform/config"
	"github.com/santoshkc2200/ioe-backend/internal/platform/httpserver"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

// mediaAssetCatalog lets course authoring validate asset references against media.
type mediaAssetCatalog struct{ query *mediaapp.AssetQuery }

func (c mediaAssetCatalog) Kinds(ctx context.Context, courseID id.ID, ids []id.ID) (map[id.ID]courseauthoringapp.AssetKind, error) {
	kinds, err := c.query.Kinds(ctx, courseID, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[id.ID]courseauthoringapp.AssetKind, len(kinds))
	for k, v := range kinds {
		out[k] = courseauthoringapp.AssetKind(v)
	}
	return out, nil
}

// mediaCourseAccess lets media ask course authoring who may manage a course or read a lecture.
type mediaCourseAccess struct {
	courses  *courseauthoringapp.CourseService
	contents *courseauthoringapp.ContentService
}

func (a mediaCourseAccess) CanManage(ctx context.Context, p auth.Principal, courseID id.ID) error {
	return toMediaError(a.courses.CheckManage(ctx, p, courseID))
}

func (a mediaCourseAccess) CanReadLectureAsset(ctx context.Context, p auth.Principal, courseID, lectureID, assetID id.ID) error {
	return toMediaError(a.contents.CheckAssetRead(ctx, p, courseID, lectureID, assetID))
}

func toMediaError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, courseauthoringapp.ErrNotFound):
		return mediaapp.ErrNotFound
	case errors.Is(err, courseauthoringapp.ErrForbidden):
		return mediaapp.ErrForbidden
	case errors.Is(err, courseauthoringapp.ErrEnrollmentRequired):
		return mediaapp.ErrEnrollmentRequired
	case errors.Is(err, courseauthoringdomain.ErrCourseNotEditable):
		return mediaapp.ErrCourseNotEditable
	}
	return err
}

// registerMedia mounts media routes when the media service is configured.
func registerMedia(r *httpserver.Router, assets *mediapg.Assets, courses *courseauthoringapp.CourseService, contents *courseauthoringapp.ContentService, ids *id.Generator, clk clock.Clock, cfg config.Config, requireAuth httpserver.Middleware, logger *slog.Logger) {
	if !cfg.MediaEnabled() {
		logger.Warn("media uploads disabled: MEDIA_SERVICE_BASE_URL, MEDIA_SERVICE_PUBLIC_URL and MEDIA_SERVICE_API_KEY are not set")
		return
	}
	remote := mediasvc.New(cfg.MediaServiceBaseURL, cfg.MediaServicePublicURL, cfg.MediaServiceAPIKey)
	svc := mediaapp.NewAssetService(assets, remote, mediaCourseAccess{courses: courses, contents: contents}, ids, clk)
	mediahttp.New(svc, mediahttp.Config{
		RequireAuth: requireAuth, CreateLimiter: httpserver.NewRateLimiter(30), Logger: logger,
	}).Register(r)
}
```

In `cmd/api/app.go`:
- Delete the `noAssets` placeholder type added in Task 6.
- Replace the `courses, _ := registerCourseAuthoring(...)` line with:

```go
	mediaAssets := mediapg.New(pool)
	courses, contents := registerCourseAuthoring(router, pool, ids, clk, enrollmentAccess,
		mediaAssetCatalog{query: mediaapp.NewAssetQuery(mediaAssets)}, identityHandler, ips, logger)
```

- After `registerProgress(...)` add:

```go
	registerMedia(router, mediaAssets, courses, contents, ids, clk, cfg, identityHandler.RequireAuth, logger)
```

- Add the imports `mediapg "github.com/santoshkc2200/ioe-backend/internal/media/adapters/postgres"` and `mediaapp "github.com/santoshkc2200/ioe-backend/internal/media/app"`.

- [ ] **Step 4: Run the end-to-end tests**

Run: `go build ./... && go test -race -tags integration ./cmd/api/`
Expected: PASS for all e2e tests, including `TestMediaEndToEnd` and `TestMediaDisabledRoutesAreAbsent`. Requires Docker; if Docker is unavailable, say so and do not report these as passing.

- [ ] **Step 5: Document the contract** — in `api/openapi.yaml`:

1. Under `components.parameters` add:

```yaml
    AssetID:
      name: assetID
      in: path
      required: true
      schema: { type: string, pattern: "^[0-9]+$" }
```

2. Under `components.schemas` add:

```yaml
    CreateMediaUploadRequest:
      type: object
      additionalProperties: false
      required: [kind, content_type, filename, size_bytes]
      properties:
        kind: { type: string, enum: [video, image] }
        content_type: { type: string, description: "video/* for videos; image/jpeg, image/png or image/webp for images" }
        filename: { type: string }
        size_bytes: { type: integer, format: int64, minimum: 1 }
    MediaUploadPart:
      type: object
      required: [part_number, url]
      properties:
        part_number: { type: integer }
        url: { type: string, format: uri }
    MediaUpload:
      type: object
      required: [asset_id, expires_at]
      description: Either upload_url (one PUT) or part_size and part_urls (multipart).
      properties:
        asset_id: { type: string }
        upload_id: { type: string }
        upload_url: { type: string, format: uri }
        part_size: { type: integer, format: int64 }
        part_urls: { type: array, items: { $ref: "#/components/schemas/MediaUploadPart" } }
        expires_at: { type: string, format: date-time }
    MediaAsset:
      type: object
      required: [id, course_id, kind, status, progress_percent, updated_at]
      properties:
        id: { type: string }
        course_id: { type: string }
        kind: { type: string, enum: [video, image] }
        status: { type: string, enum: [uploading, processing, ready, failed, canceled, deleting] }
        progress_percent: { type: integer, minimum: 0, maximum: 100 }
        duration_ms: { type: integer, format: int64 }
        width: { type: integer }
        height: { type: integer }
        error_message: { type: string }
        updated_at: { type: string, format: date-time }
    MediaPlayback:
      type: object
      required: [id, kind, status]
      description: >
        URLs are present only when status is ready. A video carries playback_url (a signed HLS
        manifest) and optionally poster_url; an image carries url. URLs expire at expires_at.
      properties:
        id: { type: string }
        kind: { type: string, enum: [video, image] }
        status: { type: string }
        playback_url: { type: string, format: uri }
        url: { type: string, format: uri }
        poster_url: { type: string, format: uri }
        duration_ms: { type: integer, format: int64 }
        width: { type: integer }
        height: { type: integer }
        expires_at: { type: string, format: date-time }
```

3. Under `paths`, after the progress paths, add:

```yaml
  /v1/courses/{courseID}/media/uploads:
    post:
      summary: Start a direct upload of a video or image for a course (owner or root admin)
      description: Rate-limited per user. Upload the bytes to the returned URLs, then complete.
      security:
        - bearer: []
      parameters:
        - $ref: "#/components/parameters/CourseID"
      requestBody:
        required: true
        content:
          application/json:
            schema: { $ref: "#/components/schemas/CreateMediaUploadRequest" }
      responses:
        "201":
          description: Created
          content:
            application/json:
              schema: { $ref: "#/components/schemas/MediaUpload" }
        "400": { $ref: "#/components/responses/Problem" }
        "401": { $ref: "#/components/responses/Problem" }
        "403": { $ref: "#/components/responses/Problem" }
        "404": { $ref: "#/components/responses/Problem" }
        "409": { $ref: "#/components/responses/Problem" }
        "413": { $ref: "#/components/responses/Problem" }
        "415": { $ref: "#/components/responses/Problem" }
        "429": { $ref: "#/components/responses/Problem" }
        "502": { $ref: "#/components/responses/Problem" }
        "405": { $ref: "#/components/responses/MethodNotAllowed" }
        "500": { $ref: "#/components/responses/InternalError" }
  /v1/media/uploads/{assetID}/parts:
    post:
      summary: Refresh presigned multipart URLs
      security:
        - bearer: []
      parameters:
        - $ref: "#/components/parameters/AssetID"
      requestBody:
        required: true
        content:
          application/json:
            schema:
              type: object
              additionalProperties: false
              required: [part_numbers]
              properties:
                part_numbers:
                  type: array
                  minItems: 1
                  maxItems: 1000
                  items: { type: integer, minimum: 1, maximum: 10000 }
      responses:
        "200":
          description: OK
          content:
            application/json:
              schema:
                type: object
                required: [part_urls]
                properties:
                  part_urls: { type: array, items: { $ref: "#/components/schemas/MediaUploadPart" } }
        "400": { $ref: "#/components/responses/Problem" }
        "401": { $ref: "#/components/responses/Problem" }
        "404": { $ref: "#/components/responses/Problem" }
        "409": { $ref: "#/components/responses/Problem" }
        "415": { $ref: "#/components/responses/Problem" }
        "502": { $ref: "#/components/responses/Problem" }
        "405": { $ref: "#/components/responses/MethodNotAllowed" }
        "500": { $ref: "#/components/responses/InternalError" }
  /v1/media/uploads/{assetID}/complete:
    post:
      summary: Finish an upload and start processing
      description: Send {} for a single-PUT image upload.
      security:
        - bearer: []
      parameters:
        - $ref: "#/components/parameters/AssetID"
      requestBody:
        required: true
        content:
          application/json:
            schema:
              type: object
              additionalProperties: false
              properties:
                parts:
                  type: array
                  items:
                    type: object
                    additionalProperties: false
                    required: [part_number, etag]
                    properties:
                      part_number: { type: integer, minimum: 1, maximum: 10000 }
                      etag: { type: string }
      responses:
        "200":
          description: OK
          content:
            application/json:
              schema: { $ref: "#/components/schemas/MediaAsset" }
        "400": { $ref: "#/components/responses/Problem" }
        "401": { $ref: "#/components/responses/Problem" }
        "404": { $ref: "#/components/responses/Problem" }
        "409": { $ref: "#/components/responses/Problem" }
        "415": { $ref: "#/components/responses/Problem" }
        "502": { $ref: "#/components/responses/Problem" }
        "405": { $ref: "#/components/responses/MethodNotAllowed" }
        "500": { $ref: "#/components/responses/InternalError" }
  /v1/media/assets/{assetID}:
    get:
      summary: Read an asset's processing state (owner or root admin)
      security:
        - bearer: []
      parameters:
        - $ref: "#/components/parameters/AssetID"
      responses:
        "200":
          description: OK
          content:
            application/json:
              schema: { $ref: "#/components/schemas/MediaAsset" }
        "401": { $ref: "#/components/responses/Problem" }
        "404": { $ref: "#/components/responses/Problem" }
        "409": { $ref: "#/components/responses/Problem" }
        "502": { $ref: "#/components/responses/Problem" }
        "405": { $ref: "#/components/responses/MethodNotAllowed" }
        "500": { $ref: "#/components/responses/InternalError" }
    delete:
      summary: Delete an asset; blocks that still reference it resolve to not_found
      security:
        - bearer: []
      parameters:
        - $ref: "#/components/parameters/AssetID"
      responses:
        "204": { description: No Content }
        "401": { $ref: "#/components/responses/Problem" }
        "404": { $ref: "#/components/responses/Problem" }
        "409": { $ref: "#/components/responses/Problem" }
        "502": { $ref: "#/components/responses/Problem" }
        "405": { $ref: "#/components/responses/MethodNotAllowed" }
        "500": { $ref: "#/components/responses/InternalError" }
  /v1/courses/{courseID}/lectures/{lectureID}/media/{assetID}:
    get:
      summary: Resolve playback or display URLs for an asset the lecture references
      description: >
        Same access rules as reading the lecture's content. Not cacheable.
      security:
        - bearer: []
      parameters:
        - $ref: "#/components/parameters/CourseID"
        - $ref: "#/components/parameters/LectureID"
        - $ref: "#/components/parameters/AssetID"
      responses:
        "200":
          description: OK
          content:
            application/json:
              schema: { $ref: "#/components/schemas/MediaPlayback" }
        "401": { $ref: "#/components/responses/Problem" }
        "403": { $ref: "#/components/responses/Problem" }
        "404": { $ref: "#/components/responses/Problem" }
        "502": { $ref: "#/components/responses/Problem" }
        "405": { $ref: "#/components/responses/MethodNotAllowed" }
        "500": { $ref: "#/components/responses/InternalError" }
```

4. On the content `put` and `patch` operations and on `POST /v1/courses/{courseID}/lectures`, append to (or add) the `description`: `A media_asset_id must name an uploaded asset of this course with the block's kind (video for video blocks, image for image blocks and flashcard cards); otherwise 400 invalid_media_reference.`

Run: `docker run --rm -v "$PWD/api:/api" redocly/cli:latest lint /api/openapi.yaml` if the repository already lints the contract that way (check `Makefile` and `.github/workflows`); otherwise validate YAML with `python3 -c "import yaml,sys; yaml.safe_load(open('api/openapi.yaml'))"`.
Expected: valid.

- [ ] **Step 6: Document operations** — in `README.md`, after the `## Progress` section add:

```markdown
## Media

Course owners and root admins upload lecture videos and images through the standalone media
service in `../hitox-media-service`. `POST /v1/courses/{courseID}/media/uploads` with
`{kind, content_type, filename, size_bytes}` returns presigned URLs; the browser uploads the
bytes directly to object storage, then calls `POST /v1/media/uploads/{assetID}/complete` and
polls `GET /v1/media/assets/{assetID}` until `status` is `ready`. Put the asset ID in a video
block's, image block's, or flashcard card's `media_asset_id`; content writes reject assets of
another course or the wrong kind with `400 invalid_media_reference`. Anyone who may read a
lecture gets short-lived URLs with `GET /v1/courses/{courseID}/lectures/{lectureID}/media/{assetID}`.

Configure `MEDIA_SERVICE_BASE_URL` (what the backend calls), `MEDIA_SERVICE_PUBLIC_URL` (what
browsers use for the service's `/v1/delivery` paths), and `MEDIA_SERVICE_API_KEY`, or none of
them to disable uploads. `docker compose --profile app up --build` runs the service with MinIO.

In production the media service shares the application database but owns only the
`media_service` schema (`ioe-backend` owns `media`). Before the media service first starts, a
database administrator creates a dedicated login role for it and runs
`GRANT CONNECT, CREATE ON DATABASE <database> TO <role>;`, and the service runs with
`DATABASE_SCHEMA=media_service`. Never grant the application role access to that schema.
```

In `.env.example` append:

```sh
# Media service. Leave all three empty to disable uploads (startup logs a warning).
# Local service: docker compose --profile app up -d --wait media
# then set MEDIA_SERVICE_BASE_URL=http://localhost:8082, MEDIA_SERVICE_PUBLIC_URL=http://localhost:8082
# and the local-only key MEDIA_SERVICE_API_KEY=local-media-api-key-change-me-32-bytes
MEDIA_SERVICE_BASE_URL=
MEDIA_SERVICE_PUBLIC_URL=
MEDIA_SERVICE_API_KEY=
```

- [ ] **Step 7: Add the local stack** — `compose/media-db-setup.sql`:

```sql
-- Local development only. Creates the media service's login role in the shared ioe database.
-- The service creates and owns the `media_service` schema when it migrates (AUTO_MIGRATE).
-- Safe to run on every `docker compose up`.
DO $$
BEGIN
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'media_service') THEN
        CREATE ROLE media_service LOGIN PASSWORD 'media-local-dev';
    END IF;
END
$$;

GRANT CONNECT, CREATE ON DATABASE ioe TO media_service;
```

In `docker-compose.yml`, before `migrate:` add:

```yaml
  media-db-setup:
    profiles: [app]
    image: postgres:17-alpine@sha256:b0f9560a2de083e2cc7382e75f808c7381a32852a7ec49117deedb300e552b24
    environment:
      PGPASSWORD: ioe
    command: ["psql", "-h", "postgres", "-U", "ioe", "-d", "ioe", "-v", "ON_ERROR_STOP=1", "-f", "/setup/media-db-setup.sql"]
    volumes:
      - ./compose/media-db-setup.sql:/setup/media-db-setup.sql:ro
    depends_on:
      postgres:
        condition: service_healthy

  minio:
    profiles: [app]
    image: minio/minio:RELEASE.2025-09-07T16-13-09Z
    command: server /data --console-address ":9001"
    environment:
      MINIO_ROOT_USER: mediaadmin
      MINIO_ROOT_PASSWORD: media-local-dev
      MINIO_API_CORS_ALLOW_ORIGIN: http://localhost:5173,http://localhost:5174,http://localhost:3000
    ports:
      - "127.0.0.1:9000:9000"
      - "127.0.0.1:9001:9001"
    volumes:
      - miniodata:/data
    healthcheck:
      test: ["CMD", "curl", "-f", "http://localhost:9000/minio/health/live"]
      interval: 2s
      timeout: 3s
      retries: 30

  minio-init:
    profiles: [app]
    image: minio/mc:RELEASE.2025-07-21T05-28-08Z
    entrypoint:
      - /bin/sh
      - -c
      - mc alias set local http://minio:9000 mediaadmin media-local-dev && mc mb --ignore-existing local/media
    depends_on:
      minio:
        condition: service_healthy

  media:
    profiles: [app]
    build: ../hitox-media-service
    environment:
      HTTP_ADDR: ":8080"
      DATABASE_URL: postgres://media_service:media-local-dev@postgres:5432/ioe?sslmode=disable
      DATABASE_SCHEMA: media_service
      AUTO_MIGRATE: "true"
      SNOWFLAKE_NODE_ID: "0"
      # Local-only keys.
      MEDIA_API_KEY: local-media-api-key-change-me-32-bytes
      MEDIA_DELIVERY_SECRET: local-media-delivery-secret-change-me-32
      DEFAULT_NAMESPACE_ID: ioe
      CORS_ALLOWED_ORIGINS: http://localhost:5173,http://localhost:5174,http://localhost:3000
      OBJECT_STORAGE_ENDPOINT: minio:9000
      OBJECT_STORAGE_PUBLIC_URL: http://localhost:9000/media
      OBJECT_STORAGE_ACCESS_KEY: mediaadmin
      OBJECT_STORAGE_SECRET_KEY: media-local-dev
      OBJECT_STORAGE_BUCKET: media
      OBJECT_STORAGE_REGION: us-east-1
      OBJECT_STORAGE_USE_SSL: "false"
      OBJECT_STORAGE_AUTO_CREATE_BUCKET: "false"
      MEDIA_WORKER_ENABLED: "true"
    ports:
      - "127.0.0.1:8082:8080"
    healthcheck:
      test: ["CMD", "wget", "-q", "-O", "/dev/null", "http://localhost:8080/readyz"]
      interval: 5s
      timeout: 5s
      retries: 30
    depends_on:
      media-db-setup:
        condition: service_completed_successfully
      minio-init:
        condition: service_completed_successfully
```

In the `api` service `environment` add:

```yaml
      MEDIA_SERVICE_BASE_URL: http://media:8080
      MEDIA_SERVICE_PUBLIC_URL: http://localhost:8082
      MEDIA_SERVICE_API_KEY: local-media-api-key-change-me-32-bytes
```

and under its `depends_on` add:

```yaml
      media:
        condition: service_healthy
```

Under top-level `volumes:` add `miniodata:`.

Before relying on these values, compare against `../hitox-media-service/internal/config/config.go` (`grep -n 'env("' ../hitox-media-service/internal/config/config.go`) and adjust any variable this compose sets that the service does not read, or any required variable it omits.

- [ ] **Step 8: Run all gates**

```sh
make check
make test-integration
docker compose config
make docker-build
git diff --check
```

Expected: all pass. If `make secrets` (inside `make check`) flags `local-media-api-key-change-me-32-bytes`, `media-local-dev`, or `local-media-delivery-secret-change-me-32`, add one allowlist entry to `.gitleaks.toml` in the existing style:

```toml
[[allowlists]]
description = "Local-only media service credentials used by docker compose and .env.example"
regexTarget = "secret"
regexes = ['''local-media-api-key-change-me-32-bytes''', '''local-media-delivery-secret-change-me-32''', '''media-local-dev''']
```

and re-run `make check`. If `make test-integration` cannot run because Docker is unavailable, report that explicitly.

- [ ] **Step 9: Manual check (not covered by gates)** — with Docker available:

```sh
docker compose --profile app up --build -d --wait
```

Sign in as a bootstrap root admin, create a course, `POST /v1/courses/{id}/media/uploads` for a small MP4, `PUT` each part URL, complete, poll status until `ready`, add a video block referencing the asset, and open the returned `playback_url` in a browser HLS player (for example `ffplay` or hls.js demo). Record the outcome in the final report; if it was not done, say so.

- [ ] **Step 10: Commit**

```bash
git add cmd/api api/openapi.yaml README.md docker-compose.yml compose/media-db-setup.sql .env.example .gitleaks.toml
git commit -m "feat(api): wire media uploads and playback"
```
