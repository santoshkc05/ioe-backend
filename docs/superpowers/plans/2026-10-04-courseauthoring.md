# Course Authoring Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add the `courseauthoring` bounded context so instructors and root admins can author courses, sections, lectures and block content through the API `ioe-frontend/packages/course-core` already calls.

**Architecture:** Port Hitox's `shared/contentblocks` into `internal/platform/contentblocks`, and port a trimmed `courseauthoring` (domain, app, postgres, httpapi) following ioe-backend's hexagonal layout. The `Course` aggregate holds structure only; block content is read and written through a separate lecture-content repository with a per-lecture revision. Authorization lives in the app layer, from `auth.Principal`.

**Tech Stack:** Go 1.27, `net/http` ServeMux, pgx v5, sqlc, goose, `bluemonday`, `golang.org/x/net/html`, watermill outbox.

**Spec:** `docs/superpowers/specs/2026-10-04-courseauthoring-design.md`

**Prerequisite:** `docs/superpowers/plans/2026-10-04-platform-snowflake-nethttp.md` is fully implemented (`internal/platform/id`, `httpserver.Router`, `auth.Principal.UserID id.ID`, `ids *id.Generator` in `buildApp`).

**Port source:** `/Users/hitohospital/personal/dev/hitox/hitox-backend` (below: `$HITOX`). Read the named Hitox file before porting from it.

## Global Constraints

- Module path `github.com/santoshkc2200/ioe-backend`. Hitox module path `github.com/santoshkc2200/hitox-backend` must never appear in this repository.
- No tenant concept anywhere: no `tenant_id` columns, parameters, or context lookups.
- IDs: `id.ID` in Go, `bigint` in SQL, JSON strings on the wire; minted only by the injected `*id.Generator`.
- Prices: NPR only, `amount_minor` in paisa; free means `amount_minor = 0` and currency `""`.
- Status values exactly `draft`, `published`, `archived`.
- A context reads and writes only its own schema (`courseauthoring`). No import of `internal/identity` or `internal/notification` from `internal/courseauthoring`, and vice versa.
- Outbox events are written in the same transaction as the state change.
- Errors are problem+json; `type` carries the machine codes listed in the spec's Errors table, spelled exactly.
- Wire field names match `ioe-frontend/packages/course-core/src/api/courses.ts` exactly.
- Conventional Commits. Each task ends with `make test` and `make lint` passing; tasks with `-tags integration` tests also run `make test-integration` (Docker). Never report integration tests as passing unless they ran.

## Review Focus

- A non-manager reading a draft or archived course by ID must get `404 not_found`, never `403`, so draft existence does not leak. Test in Task 7.
- Two browser tabs autosaving the same lecture with the same `base_revision` must produce exactly one success and one `409 revision_conflict` carrying the winner's content. Test in Task 6 (database) and Task 9 (HTTP body shape).
- Text block HTML containing `<script>` or `onerror=` must be sanitized or rejected (`unsafe_content`), never stored verbatim. Test in Task 2.
- A malformed path ID (`/v1/courses/abc`, `/v1/courses/0`) must return `404 not_found`, not 400 or 500. Test in Task 9.
- A `PUT .../content` followed by a stale `PATCH` (old `base_revision`) from another tab must conflict, so whole-lecture replaces cannot be silently overwritten. Test in Task 8.

---

## File Structure

```text
internal/platform/problem/problem.go               + WriteWithExtensions
internal/platform/contentblocks/                   ported from $HITOX/internal/shared/contentblocks
internal/courseauthoring/domain/
  course.go          Course aggregate, Status, Section, Lecture
  price.go           Price
  content.go         LectureContent (validated block list)
  events.go          Event, CoursePublished, CourseArchived
  errors.go          domain sentinels
  *_test.go
internal/courseauthoring/app/
  ports.go           repositories, TxRunner, EnrollmentQuery, LectureHeader, BlockWritePlan
  errors.go          app sentinels, RevisionConflictError
  blocks.go          BlockInput, buildBlock, buildContent (ported)
  course_service.go  CourseService (structure, publish, archive, reads, authorization)
  content_service.go ContentService (get, replace, patch)
  fakes_test.go, course_service_test.go, content_service_test.go
internal/courseauthoring/adapters/postgres/
  queries.sql, sqlcgen/ (generated)
  postgres.go        TxRunner, events publisher
  courses.go         CourseRepository
  contents.go        LectureContentRepository, block payload codec glue
  *_integration_test.go
internal/courseauthoring/adapters/httpapi/
  httpapi.go         Handler, New, Register, error mapping
  courses.go         course/section/lecture handlers
  content.go         content handlers, rate limit
  wire.go            request/response DTOs and mappers
  httpapi_test.go
internal/courseauthoring/adapters/enrollment/deny.go   deny-all EnrollmentQuery
migrations/00003_courseauthoring.sql
cmd/api/app.go                                      registerCourseAuthoring
cmd/api/e2e_integration_test.go                     course flow
api/openapi.yaml, .golangci.yml, sqlc.yaml
```

---

### Task 1: `problem.WriteWithExtensions`

**Files:**
- Modify: `internal/platform/problem/problem.go`
- Test: `internal/platform/problem/problem_test.go` (create if absent)

**Interfaces:**
- Produces: `func WriteWithExtensions(w http.ResponseWriter, r *http.Request, status int, typ, title, detail string, ext map[string]any)`.

- [ ] **Step 1: Write the failing test**

```go
package problem_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/santoshkc2200/ioe-backend/internal/platform/problem"
)

func TestWriteWithExtensions(t *testing.T) {
	w := httptest.NewRecorder()
	problem.WriteWithExtensions(w, httptest.NewRequest(http.MethodGet, "/", nil), http.StatusConflict,
		"revision_conflict", "Conflict", "", map[string]any{"lecture_id": "7", "content_revision": 3, "type": "spoofed"})
	if w.Code != 409 || w.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("%d %q", w.Code, w.Header().Get("Content-Type"))
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["type"] != "revision_conflict" || body["status"] != float64(409) || body["lecture_id"] != "7" || body["content_revision"] != float64(3) {
		t.Fatalf("body = %v", body)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/platform/problem/`
Expected: FAIL, `WriteWithExtensions` undefined.

- [ ] **Step 3: Implement**

Append to `problem.go`:

```go
// WriteWithExtensions sends a problem response with RFC 9457 extension members.
// Keys that collide with standard members are ignored.
func WriteWithExtensions(w http.ResponseWriter, r *http.Request, status int, typ, title, detail string, ext map[string]any) {
	body := make(map[string]any, len(ext)+5)
	for k, v := range ext {
		body[k] = v
	}
	body["type"], body["title"], body["status"] = typ, title, status
	delete(body, "detail")
	delete(body, "instance")
	if detail != "" {
		body["detail"] = detail
	}
	if id := logging.RequestID(r.Context()); id != "" {
		body["instance"] = id
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
```

- [ ] **Step 4: Run tests**

Run: `go test -race ./internal/platform/problem/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/platform/problem
git commit -m "feat(problem): support RFC 9457 extension members"
```

---

### Task 2: Port `contentblocks` into platform

**Files:**
- Create: `internal/platform/contentblocks/*.go` (copied from `$HITOX/internal/shared/contentblocks/`)
- Modify: `go.mod`, `go.sum`
- Modify: `.golangci.yml`

**Interfaces:**
- Produces (unchanged from Hitox except the ID type): `Title`, `NewTitle`, `TextBody`, `NewSanitizedTextBody`, `VideoRef`, `NewVideoRef`, `NewMediaVideoRef(id.ID, time.Duration)`, `ImageRef`, `NewImageRef(url string, mediaAssetID id.ID, ...)`, `Flashcard`, `FlashcardDeck`, `NewFlashcardDeck`, `BlockType` and `BlockText/BlockVideo/BlockQuiz/BlockImage/BlockFlashcard`, `Block` with `ID() id.ID`, `ClientBlockID()`, `Type()`, `Position()`, `QuizID() id.ID`, `Text()`, `Video()`, `Image()`, `Deck()`, `WithIdentity(id.ID, string, int)`, constructors `NewTextBlock/NewVideoBlock/NewQuizBlock/NewImageBlock/NewFlashcardBlock`, `ValidateClientBlockID`, `ValidateBlocks`, `EncodePayload`, `DecodePayload`, `CanonicalizePayload`, `MaxClientBlockIDLen`, and every `Err*` in `errors.go`.

Check the exact block-type constant names in `$HITOX/internal/shared/contentblocks/block.go` and use those; later tasks refer to them as `contentblocks.BlockText` etc. If Hitox names differ, use Hitox's names everywhere in this plan.

- [ ] **Step 1: Copy the package**

```bash
mkdir -p internal/platform/contentblocks
cp $HITOX/internal/shared/contentblocks/*.go internal/platform/contentblocks/
```

- [ ] **Step 2: Rewrite imports and the ID type**

```bash
cd internal/platform/contentblocks
sed -i '' \
  -e 's#github.com/santoshkc2200/hitox-backend/internal/shared/kernel#github.com/santoshkc2200/ioe-backend/internal/platform/id#' \
  -e 's/kernel\.ParseID/id.Parse/g' \
  -e 's/kernel\.MustParseID/mustParseID/g' \
  -e 's/kernel\.ID/id.ID/g' \
  *.go
grep -n "kernel\|hitox" *.go
```

Expected: no output from `grep`. If any test used `kernel.MustParseID`, add to that test file:

```go
func mustParseID(s string) id.ID {
	v, err := id.Parse(s)
	if err != nil {
		panic(err)
	}
	return v
}
```

Fix local-variable shadowing where a function parameter or variable is named `id` and the package `id` is also used in that scope: rename the variable to `blockID` (or `assetID`) in that function only. `go vet` reports these as compile errors (`id.ID is not a type`).

`import_test.go` asserts the package's import set; update its allowed list to the ioe paths (`internal/platform/id`, `bluemonday`, `golang.org/x/net/html`, stdlib).

- [ ] **Step 3: Pin sanitization behavior with an extra test**

Add to `sanitize_test.go` (adjust the constructor name to whatever Hitox exposes for sanitized text, `NewSanitizedTextBody` in Hitox today):

```go
func TestSanitizedTextBodyStripsScriptAndHandlers(t *testing.T) {
	for _, raw := range []string{
		`<p>hi</p><script>alert(1)</script>`,
		`<p><img src=x onerror="alert(1)">hi</p>`,
		`<p><a href="javascript:alert(1)">x</a></p>`,
	} {
		body, err := contentblocks.NewSanitizedTextBody(raw)
		if err != nil {
			if !errors.Is(err, contentblocks.ErrUnsafeContent) {
				t.Fatalf("%q: err = %v", raw, err)
			}
			continue
		}
		s := strings.ToLower(body.String())
		if strings.Contains(s, "<script") || strings.Contains(s, "onerror") || strings.Contains(s, "javascript:") {
			t.Fatalf("%q survived as %q", raw, body.String())
		}
	}
}
```

- [ ] **Step 4: Add dependencies and run the ported tests**

Run: `go mod tidy && go test -race ./internal/platform/contentblocks/`
Expected: PASS (bluemonday and x/net added to `go.mod`).

- [ ] **Step 5: depguard**

Add to `.golangci.yml` under `depguard.rules`:

```yaml
        platform-contentblocks:
          list-mode: strict
          files:
            - "**/internal/platform/contentblocks/**"
            - "!$test"
          allow:
            - $gostd
            - github.com/microcosm-cc/bluemonday
            - golang.org/x/net/html
            - github.com/santoshkc2200/ioe-backend/internal/platform/id
```

Run: `make lint`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/platform/contentblocks go.mod go.sum .golangci.yml
git commit -m "feat(platform): port content block value objects"
```

---

### Task 3: Domain

**Files:**
- Create: `internal/courseauthoring/domain/{course,price,content,events,errors}.go`
- Test: `internal/courseauthoring/domain/{course,price,content}_test.go`
- Modify: `.golangci.yml`

**Interfaces:**
- Consumes: `contentblocks.Title`, `contentblocks.Block`, `contentblocks.ValidateBlocks`, `id.ID`, `auth.Principal`.
- Produces: everything in the code blocks below, used verbatim by Tasks 5-9.

- [ ] **Step 1: Write the failing tests**

`price_test.go`:

```go
package domain_test

import (
	"errors"
	"testing"

	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/domain"
)

func TestNewPrice(t *testing.T) {
	cases := []struct {
		amount   int64
		currency string
		want     domain.Price
		err      error
	}{
		{0, "", domain.Price{}, nil},
		{0, "NPR", domain.Price{}, nil},
		{0, "USD", domain.Price{}, nil},
		{150000, "NPR", domain.Price{AmountMinor: 150000, Currency: "NPR"}, nil},
		{150000, "npr", domain.Price{}, domain.ErrUnsupportedCurrency},
		{150000, "USD", domain.Price{}, domain.ErrUnsupportedCurrency},
		{150000, "", domain.Price{}, domain.ErrUnsupportedCurrency},
		{-1, "NPR", domain.Price{}, domain.ErrInvalidPrice},
	}
	for _, c := range cases {
		got, err := domain.NewPrice(c.amount, c.currency)
		if !errors.Is(err, c.err) || got != c.want {
			t.Fatalf("NewPrice(%d, %q) = %+v, %v; want %+v, %v", c.amount, c.currency, got, err, c.want, c.err)
		}
	}
	if !(domain.Price{}).IsFree() || (domain.Price{AmountMinor: 1, Currency: "NPR"}).IsFree() {
		t.Fatal("IsFree wrong")
	}
}
```

`course_test.go`:

```go
package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/contentblocks"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

var t0 = time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)

func title(t *testing.T, s string) contentblocks.Title {
	t.Helper()
	v, err := contentblocks.NewTitle(s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func newCourse(t *testing.T) domain.Course {
	return domain.NewCourse(1, 100, title(t, "Go"), "desc", t0)
}

func TestNewCourseDefaults(t *testing.T) {
	c := newCourse(t)
	if c.Status != domain.StatusDraft || !c.Price.IsFree() || c.Version != 0 || !c.CreatedAt.Equal(t0) {
		t.Fatalf("%+v", c)
	}
}

func TestUpdateDetailsValidation(t *testing.T) {
	c := newCourse(t)
	if err := c.UpdateDetails(title(t, "New"), "d", "expert", "", t0); !errors.Is(err, domain.ErrInvalidLevel) {
		t.Fatalf("level err = %v", err)
	}
	if err := c.UpdateDetails(title(t, "New"), "d", "", "ftp://x/y.png", t0); !errors.Is(err, domain.ErrInvalidThumbnailURL) {
		t.Fatalf("thumbnail err = %v", err)
	}
	if err := c.UpdateDetails(title(t, "New"), "d", "beginner", "https://cdn.example.com/a.png", t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if c.Title.String() != "New" || c.Level != "beginner" || !c.UpdatedAt.Equal(t0.Add(time.Hour)) {
		t.Fatalf("%+v", c)
	}
}

func TestSectionsAndLectures(t *testing.T) {
	c := newCourse(t)
	if err := c.AddSection(10, title(t, "Intro"), t0); err != nil {
		t.Fatal(err)
	}
	if err := c.AddSection(11, title(t, "Intro"), t0); !errors.Is(err, domain.ErrDuplicateSectionTitle) {
		t.Fatalf("dup err = %v", err)
	}
	for i, lid := range []id.ID{20, 21, 22} {
		if err := c.AddLecture(lid, title(t, "L"), i == 0, false, t0); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.MoveLectureToSection(21, 10, t0); err != nil {
		t.Fatal(err)
	}
	if err := c.MoveLectureToSection(21, 999, t0); !errors.Is(err, domain.ErrSectionNotFound) {
		t.Fatalf("move err = %v", err)
	}
	if err := c.ReorderLectures([]id.ID{22, 20, 21}, t0); err != nil {
		t.Fatal(err)
	}
	if c.Lectures[0].ID != 22 || c.Lectures[1].ID != 20 || c.Lectures[2].ID != 21 {
		t.Fatalf("order = %+v", c.Lectures)
	}
	for i, l := range c.Lectures {
		if l.Order != i {
			t.Fatalf("lecture %d order %d", l.ID, l.Order)
		}
	}
	for _, bad := range [][]id.ID{{22, 20}, {22, 20, 20}, {22, 20, 99}} {
		if err := c.ReorderLectures(bad, t0); !errors.Is(err, domain.ErrInvalidLectureOrder) {
			t.Fatalf("reorder %v err = %v", bad, err)
		}
	}
	if err := c.RemoveSection(10, t0); err != nil {
		t.Fatal(err)
	}
	if l, _ := c.Lecture(21); !l.SectionID.IsZero() {
		t.Fatal("lecture not moved to unsectioned")
	}
	if err := c.RemoveLecture(20, t0); err != nil {
		t.Fatal(err)
	}
	if len(c.Lectures) != 2 || c.Lectures[0].Order != 0 || c.Lectures[1].Order != 1 {
		t.Fatalf("orders not dense: %+v", c.Lectures)
	}
	if err := c.RenameLecture(404, title(t, "x"), t0); !errors.Is(err, domain.ErrLectureNotFound) {
		t.Fatalf("rename missing err = %v", err)
	}
}

func TestLifecycle(t *testing.T) {
	c := newCourse(t)
	if err := c.Publish(t0); !errors.Is(err, domain.ErrCourseHasNoLectures) {
		t.Fatalf("publish empty err = %v", err)
	}
	_ = c.AddLecture(20, title(t, "L"), true, false, t0)
	if err := c.Publish(t0); err != nil || c.Status != domain.StatusPublished {
		t.Fatalf("publish = %v, %s", err, c.Status)
	}
	if err := c.Publish(t0); !errors.Is(err, domain.ErrInvalidStatusTransition) {
		t.Fatalf("republish err = %v", err)
	}
	if err := c.RenameLecture(20, title(t, "Edited live"), t0); err != nil {
		t.Fatalf("published course must stay editable: %v", err)
	}
	if err := c.Archive(t0); err != nil || c.Status != domain.StatusArchived {
		t.Fatalf("archive = %v", err)
	}
	if err := c.Archive(t0); !errors.Is(err, domain.ErrInvalidStatusTransition) {
		t.Fatalf("re-archive err = %v", err)
	}
	if err := c.RenameLecture(20, title(t, "x"), t0); !errors.Is(err, domain.ErrCourseNotEditable) {
		t.Fatalf("archived edit err = %v", err)
	}
	if err := c.SetPrice(domain.Price{}, t0); !errors.Is(err, domain.ErrCourseNotEditable) {
		t.Fatalf("archived price err = %v", err)
	}
}

func TestIsManagedBy(t *testing.T) {
	c := newCourse(t)
	cases := []struct {
		p    auth.Principal
		want bool
	}{
		{auth.Principal{UserID: 100, Role: auth.RoleInstructor}, true},
		{auth.Principal{UserID: 101, Role: auth.RoleInstructor}, false},
		{auth.Principal{UserID: 101, Role: auth.RoleRootAdmin}, true},
		{auth.Principal{UserID: 100, Role: auth.RoleStudent}, true},
	}
	for _, tc := range cases {
		if got := c.IsManagedBy(tc.p); got != tc.want {
			t.Fatalf("%+v: %v", tc.p, got)
		}
	}
}
```

The last `IsManagedBy` case is intentional: ownership, not current role, decides management of an existing course. Creating a course is what requires the instructor or root admin role (Task 7).

`content_test.go`:

```go
package domain_test

import (
	"testing"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/contentblocks"
)

func TestLectureContentFlags(t *testing.T) {
	body, err := contentblocks.NewSanitizedTextBody("<p>hi</p>")
	if err != nil {
		t.Fatal(err)
	}
	video, err := contentblocks.NewVideoRef("https://v.example.com/a.mp4", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	c, err := domain.NewLectureContent([]contentblocks.Block{
		contentblocks.NewTextBlock(1, "a", 0, body),
		contentblocks.NewVideoBlock(2, "b", 1, video),
	})
	if err != nil || !c.HasText() || !c.HasVideo() || len(c.Blocks()) != 2 {
		t.Fatalf("%v %v", c, err)
	}
	if _, err := domain.NewLectureContent([]contentblocks.Block{
		contentblocks.NewTextBlock(1, "a", 0, body),
		contentblocks.NewTextBlock(2, "a", 1, body),
	}); err == nil {
		t.Fatal("duplicate client_block_id accepted")
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/courseauthoring/domain/`
Expected: FAIL, package has no non-test Go files.

- [ ] **Step 3: Implement `errors.go`, `price.go`, `events.go`, `content.go`**

```go
// Package domain holds the course authoring model.
package domain

import "errors"

var (
	ErrInvalidPrice            = errors.New("price must not be negative")
	ErrUnsupportedCurrency     = errors.New("only NPR prices are supported")
	ErrInvalidLevel            = errors.New("level must be empty, beginner, intermediate or advanced")
	ErrInvalidThumbnailURL     = errors.New("thumbnail_url must be empty or an absolute http(s) URL")
	ErrDuplicateSectionTitle   = errors.New("section title already used in this course")
	ErrSectionNotFound         = errors.New("section not found")
	ErrLectureNotFound         = errors.New("lecture not found")
	ErrInvalidLectureOrder     = errors.New("lecture order must list every lecture exactly once")
	ErrCourseHasNoLectures     = errors.New("course has no lectures")
	ErrCourseNotEditable       = errors.New("course is archived")
	ErrInvalidStatusTransition = errors.New("invalid status transition")
)
```

```go
package domain

// CurrencyNPR is the only supported currency.
const CurrencyNPR = "NPR"

// Price is an amount in paisa. A free price has AmountMinor 0 and Currency "".
type Price struct {
	AmountMinor int64
	Currency    string
}

// NewPrice validates a price. Any currency is normalized away for a zero amount.
func NewPrice(amountMinor int64, currency string) (Price, error) {
	switch {
	case amountMinor < 0:
		return Price{}, ErrInvalidPrice
	case amountMinor == 0:
		return Price{}, nil
	case currency != CurrencyNPR:
		return Price{}, ErrUnsupportedCurrency
	default:
		return Price{AmountMinor: amountMinor, Currency: CurrencyNPR}, nil
	}
}

// IsFree reports whether the course costs nothing.
func (p Price) IsFree() bool { return p.AmountMinor == 0 }
```

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

type CoursePublished struct {
	CourseID         id.ID     `json:"course_id"`
	OwnerID          id.ID     `json:"owner_id"`
	PriceAmountMinor int64     `json:"price_amount_minor"`
	PriceCurrency    string    `json:"price_currency"`
	OccurredAt       time.Time `json:"occurred_at"`
}

func (CoursePublished) EventName() string { return "courseauthoring.course.published" }

type CourseArchived struct {
	CourseID   id.ID     `json:"course_id"`
	OwnerID    id.ID     `json:"owner_id"`
	OccurredAt time.Time `json:"occurred_at"`
}

func (CourseArchived) EventName() string { return "courseauthoring.course.archived" }
```

```go
package domain

import "github.com/santoshkc2200/ioe-backend/internal/platform/contentblocks"

// LectureContent is a validated, ordered block list.
type LectureContent struct{ blocks []contentblocks.Block }

// NewLectureContent validates blocks (identity, per-kind rules) and renumbers positions by slice order.
func NewLectureContent(blocks []contentblocks.Block) (LectureContent, error) {
	valid, err := contentblocks.ValidateBlocks(blocks)
	if err != nil {
		return LectureContent{}, err
	}
	return LectureContent{blocks: valid}, nil
}

func (c LectureContent) Blocks() []contentblocks.Block { return append([]contentblocks.Block(nil), c.blocks...) }

func (c LectureContent) HasText() bool  { return c.has(contentblocks.BlockText) }
func (c LectureContent) HasVideo() bool { return c.has(contentblocks.BlockVideo) }

func (c LectureContent) has(kind contentblocks.BlockType) bool {
	for _, b := range c.blocks {
		if b.Type() == kind {
			return true
		}
	}
	return false
}
```

Check `contentblocks.ValidateBlocks`'s exact contract in the ported code (it returns `([]Block, error)`); if it does not renumber positions, renumber here with `b.WithIdentity(b.ID(), b.ClientBlockID(), i)`.

- [ ] **Step 4: Implement `course.go`**

```go
package domain

import (
	"net/url"
	"slices"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/contentblocks"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

type Status string

const (
	StatusDraft     Status = "draft"
	StatusPublished Status = "published"
	StatusArchived  Status = "archived"
)

type Section struct {
	ID    id.ID
	Title contentblocks.Title
	Order int
}

// Lecture is lecture metadata. HasText/HasVideo are hydrated from stored blocks.
type Lecture struct {
	ID          id.ID
	SectionID   id.ID // zero when unsectioned
	Title       contentblocks.Title
	FreePreview bool
	Order       int
	HasText     bool
	HasVideo    bool
}

// Course is the authoring aggregate. It holds structure only; block content is
// stored and versioned per lecture outside the aggregate.
type Course struct {
	ID           id.ID
	OwnerID      id.ID
	Title        contentblocks.Title
	Description  string
	Level        string
	ThumbnailURL string
	Price        Price
	Status       Status
	Sections     []Section // ordered by Order
	Lectures     []Lecture // ordered by Order, dense from 0
	Version      int64     // optimistic-concurrency token; 0 until first insert
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

func NewCourse(courseID, ownerID id.ID, title contentblocks.Title, description string, now time.Time) Course {
	return Course{ID: courseID, OwnerID: ownerID, Title: title, Description: description,
		Status: StatusDraft, CreatedAt: now, UpdatedAt: now}
}

// IsManagedBy reports whether p may edit, publish or archive the course.
func (c *Course) IsManagedBy(p auth.Principal) bool {
	return p.Role == auth.RoleRootAdmin || p.UserID == c.OwnerID
}

func (c *Course) editable() error {
	if c.Status == StatusArchived {
		return ErrCourseNotEditable
	}
	return nil
}

func (c *Course) UpdateDetails(title contentblocks.Title, description, level, thumbnailURL string, now time.Time) error {
	if err := c.editable(); err != nil {
		return err
	}
	switch level {
	case "", "beginner", "intermediate", "advanced":
	default:
		return ErrInvalidLevel
	}
	if thumbnailURL != "" {
		u, err := url.Parse(thumbnailURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return ErrInvalidThumbnailURL
		}
	}
	c.Title, c.Description, c.Level, c.ThumbnailURL, c.UpdatedAt = title, description, level, thumbnailURL, now
	return nil
}

func (c *Course) SetPrice(p Price, now time.Time) error {
	if err := c.editable(); err != nil {
		return err
	}
	c.Price, c.UpdatedAt = p, now
	return nil
}

func (c *Course) Section(sectionID id.ID) (Section, bool) {
	i := slices.IndexFunc(c.Sections, func(s Section) bool { return s.ID == sectionID })
	if i < 0 {
		return Section{}, false
	}
	return c.Sections[i], true
}

func (c *Course) Lecture(lectureID id.ID) (Lecture, bool) {
	i := c.lectureIndex(lectureID)
	if i < 0 {
		return Lecture{}, false
	}
	return c.Lectures[i], true
}

func (c *Course) lectureIndex(lectureID id.ID) int {
	return slices.IndexFunc(c.Lectures, func(l Lecture) bool { return l.ID == lectureID })
}

func (c *Course) AddSection(sectionID id.ID, title contentblocks.Title, now time.Time) error {
	if err := c.editable(); err != nil {
		return err
	}
	if c.hasSectionTitle(title, 0) {
		return ErrDuplicateSectionTitle
	}
	c.Sections = append(c.Sections, Section{ID: sectionID, Title: title, Order: len(c.Sections)})
	c.UpdatedAt = now
	return nil
}

func (c *Course) RenameSection(sectionID id.ID, title contentblocks.Title, now time.Time) error {
	if err := c.editable(); err != nil {
		return err
	}
	i := slices.IndexFunc(c.Sections, func(s Section) bool { return s.ID == sectionID })
	if i < 0 {
		return ErrSectionNotFound
	}
	if c.hasSectionTitle(title, sectionID) {
		return ErrDuplicateSectionTitle
	}
	c.Sections[i].Title, c.UpdatedAt = title, now
	return nil
}

// RemoveSection deletes the section and moves its lectures to unsectioned.
func (c *Course) RemoveSection(sectionID id.ID, now time.Time) error {
	if err := c.editable(); err != nil {
		return err
	}
	i := slices.IndexFunc(c.Sections, func(s Section) bool { return s.ID == sectionID })
	if i < 0 {
		return ErrSectionNotFound
	}
	c.Sections = slices.Delete(c.Sections, i, i+1)
	for j := range c.Sections {
		c.Sections[j].Order = j
	}
	for j := range c.Lectures {
		if c.Lectures[j].SectionID == sectionID {
			c.Lectures[j].SectionID = 0
		}
	}
	c.UpdatedAt = now
	return nil
}

func (c *Course) hasSectionTitle(title contentblocks.Title, except id.ID) bool {
	return slices.ContainsFunc(c.Sections, func(s Section) bool { return s.ID != except && s.Title == title })
}

// AddLecture appends an unsectioned lecture. hasText/hasVideo describe its initial content.
func (c *Course) AddLecture(lectureID id.ID, title contentblocks.Title, hasText, hasVideo bool, now time.Time) error {
	if err := c.editable(); err != nil {
		return err
	}
	c.Lectures = append(c.Lectures, Lecture{ID: lectureID, Title: title, Order: len(c.Lectures), HasText: hasText, HasVideo: hasVideo})
	c.UpdatedAt = now
	return nil
}

func (c *Course) RenameLecture(lectureID id.ID, title contentblocks.Title, now time.Time) error {
	return c.updateLecture(lectureID, now, func(l *Lecture) error { l.Title = title; return nil })
}

func (c *Course) SetLectureFreePreview(lectureID id.ID, freePreview bool, now time.Time) error {
	return c.updateLecture(lectureID, now, func(l *Lecture) error { l.FreePreview = freePreview; return nil })
}

// MoveLectureToSection moves a lecture into sectionID, or to unsectioned when sectionID is zero.
func (c *Course) MoveLectureToSection(lectureID, sectionID id.ID, now time.Time) error {
	if !sectionID.IsZero() {
		if _, ok := c.Section(sectionID); !ok {
			return ErrSectionNotFound
		}
	}
	return c.updateLecture(lectureID, now, func(l *Lecture) error { l.SectionID = sectionID; return nil })
}

func (c *Course) updateLecture(lectureID id.ID, now time.Time, fn func(*Lecture) error) error {
	if err := c.editable(); err != nil {
		return err
	}
	i := c.lectureIndex(lectureID)
	if i < 0 {
		return ErrLectureNotFound
	}
	if err := fn(&c.Lectures[i]); err != nil {
		return err
	}
	c.UpdatedAt = now
	return nil
}

func (c *Course) RemoveLecture(lectureID id.ID, now time.Time) error {
	if err := c.editable(); err != nil {
		return err
	}
	i := c.lectureIndex(lectureID)
	if i < 0 {
		return ErrLectureNotFound
	}
	c.Lectures = slices.Delete(c.Lectures, i, i+1)
	c.renumberLectures()
	c.UpdatedAt = now
	return nil
}

// ReorderLectures sets the course-wide lecture order. ordered must name every lecture once.
func (c *Course) ReorderLectures(ordered []id.ID, now time.Time) error {
	if err := c.editable(); err != nil {
		return err
	}
	if len(ordered) != len(c.Lectures) {
		return ErrInvalidLectureOrder
	}
	next := make([]Lecture, 0, len(ordered))
	seen := make(map[id.ID]struct{}, len(ordered))
	for _, lid := range ordered {
		i := c.lectureIndex(lid)
		if _, dup := seen[lid]; dup || i < 0 {
			return ErrInvalidLectureOrder
		}
		seen[lid] = struct{}{}
		next = append(next, c.Lectures[i])
	}
	c.Lectures = next
	c.renumberLectures()
	c.UpdatedAt = now
	return nil
}

func (c *Course) renumberLectures() {
	for i := range c.Lectures {
		c.Lectures[i].Order = i
	}
}

func (c *Course) Publish(now time.Time) error {
	if c.Status != StatusDraft {
		return ErrInvalidStatusTransition
	}
	if len(c.Lectures) == 0 {
		return ErrCourseHasNoLectures
	}
	c.Status, c.UpdatedAt = StatusPublished, now
	return nil
}

// Archive is terminal: an archived course rejects every further change.
func (c *Course) Archive(now time.Time) error {
	if c.Status == StatusArchived {
		return ErrInvalidStatusTransition
	}
	c.Status, c.UpdatedAt = StatusArchived, now
	return nil
}
```

`contentblocks.Title` must be comparable with `==` (it is a struct with one string field in Hitox). If it is not, compare `.String()`.

- [ ] **Step 5: Run tests**

Run: `go test -race ./internal/courseauthoring/domain/`
Expected: PASS.

- [ ] **Step 6: depguard rules**

Add to `.golangci.yml`:

```yaml
        courseauthoring-domain:
          list-mode: strict
          files:
            - "**/internal/courseauthoring/domain/**"
            - "!$test"
          allow:
            - $gostd
            - github.com/santoshkc2200/ioe-backend/internal/platform/auth
            - github.com/santoshkc2200/ioe-backend/internal/platform/contentblocks
            - github.com/santoshkc2200/ioe-backend/internal/platform/id
        courseauthoring-app:
          list-mode: strict
          files:
            - "**/internal/courseauthoring/app/**"
            - "!$test"
          allow:
            - $gostd
            - github.com/santoshkc2200/ioe-backend/internal/courseauthoring/domain
            - github.com/santoshkc2200/ioe-backend/internal/platform/auth
            - github.com/santoshkc2200/ioe-backend/internal/platform/clock
            - github.com/santoshkc2200/ioe-backend/internal/platform/contentblocks
            - github.com/santoshkc2200/ioe-backend/internal/platform/id
        courseauthoring-independent:
          list-mode: lax
          files:
            - "**/internal/courseauthoring/**"
          deny:
            - pkg: github.com/santoshkc2200/ioe-backend/internal/identity
              desc: bounded contexts must not import each other
            - pkg: github.com/santoshkc2200/ioe-backend/internal/notification
              desc: bounded contexts must not import each other
```

Add `- pkg: github.com/santoshkc2200/ioe-backend/internal/courseauthoring` with `desc: platform must not depend on bounded contexts` to `platform-independent-of-contexts`, and the same pkg with `desc: bounded contexts must not import each other` to `identity-independent-of-notification` and `notification-independent-of-identity` (rename those two rules to `identity-independent` and `notification-independent` while there).

Run: `make lint`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/courseauthoring/domain .golangci.yml
git commit -m "feat(courseauthoring): add course aggregate"
```

---

### Task 4: Schema and queries

**Files:**
- Create: `migrations/00003_courseauthoring.sql`
- Create: `internal/courseauthoring/adapters/postgres/queries.sql`
- Generate: `internal/courseauthoring/adapters/postgres/sqlcgen/`
- Modify: `sqlc.yaml`

**Interfaces:**
- Produces: sqlc `Queries` methods named exactly as the `-- name:` lines below; Tasks 5-6 call them.

- [ ] **Step 1: Write the migration**

Copy the `CREATE SCHEMA` through `-- +goose Down` block from the spec's Persistence section verbatim into `migrations/00003_courseauthoring.sql`, with `-- +goose Up` as the first line. Add one index the content reads need:

```sql
CREATE INDEX lecture_blocks_lecture_kind_idx ON courseauthoring.lecture_blocks (lecture_id, kind);
```

- [ ] **Step 2: Write the queries**

`internal/courseauthoring/adapters/postgres/queries.sql`:

```sql
-- name: InsertCourse :exec
INSERT INTO courseauthoring.courses
  (id, owner_id, title, description, level, thumbnail_url, status, price_amount_minor, price_currency, version, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 1, $10, $11);

-- name: UpdateCourse :execrows
UPDATE courseauthoring.courses
SET title = $3, description = $4, level = $5, thumbnail_url = $6, status = $7,
    price_amount_minor = $8, price_currency = $9, updated_at = $10, version = version + 1
WHERE id = $1 AND version = $2;

-- name: ListCoursesByIDs :many
SELECT * FROM courseauthoring.courses WHERE id = ANY(sqlc.arg(ids)::bigint[]);

-- name: ListCourseIDsByOwner :many
SELECT id FROM courseauthoring.courses WHERE owner_id = $1 ORDER BY created_at DESC, id DESC;

-- name: ListSectionsByCourseIDs :many
SELECT * FROM courseauthoring.sections
WHERE course_id = ANY(sqlc.arg(course_ids)::bigint[])
ORDER BY course_id, sort_order;

-- name: ListLecturesByCourseIDs :many
SELECT l.id, l.course_id, l.section_id, l.title, l.free_preview, l.sort_order,
       EXISTS (SELECT 1 FROM courseauthoring.lecture_blocks b WHERE b.lecture_id = l.id AND b.kind = 'text')::bool AS has_text,
       EXISTS (SELECT 1 FROM courseauthoring.lecture_blocks b WHERE b.lecture_id = l.id AND b.kind = 'video')::bool AS has_video
FROM courseauthoring.lectures l
WHERE l.course_id = ANY(sqlc.arg(course_ids)::bigint[])
ORDER BY l.course_id, l.sort_order;

-- name: UpsertSection :exec
INSERT INTO courseauthoring.sections (id, course_id, title, sort_order)
VALUES ($1, $2, $3, $4)
ON CONFLICT (id) DO UPDATE SET title = EXCLUDED.title, sort_order = EXCLUDED.sort_order;

-- name: DeleteSectionsNotIn :exec
DELETE FROM courseauthoring.sections
WHERE course_id = $1 AND NOT (id = ANY(sqlc.arg(keep)::bigint[]));

-- name: UpsertLecture :exec
INSERT INTO courseauthoring.lectures (id, course_id, section_id, title, free_preview, sort_order, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $7)
ON CONFLICT (id) DO UPDATE SET section_id = EXCLUDED.section_id, title = EXCLUDED.title,
  free_preview = EXCLUDED.free_preview, sort_order = EXCLUDED.sort_order, updated_at = EXCLUDED.updated_at;

-- name: DeleteLecturesNotIn :exec
DELETE FROM courseauthoring.lectures
WHERE course_id = $1 AND NOT (id = ANY(sqlc.arg(keep)::bigint[]));

-- name: GetLectureHeader :one
SELECT id, course_id, title, free_preview, content_revision
FROM courseauthoring.lectures WHERE course_id = $1 AND id = $2;

-- name: GetLectureHeaderForUpdate :one
SELECT id, course_id, title, free_preview, content_revision
FROM courseauthoring.lectures WHERE course_id = $1 AND id = $2
FOR UPDATE;

-- name: ListLectureBlocks :many
SELECT id, client_block_id, kind, position, payload
FROM courseauthoring.lecture_blocks WHERE lecture_id = $1 ORDER BY position;

-- name: BumpLectureContentRevision :execrows
UPDATE courseauthoring.lectures
SET content_revision = content_revision + 1, updated_at = $3
WHERE id = $1 AND content_revision = $2;

-- name: ForceBumpLectureContentRevision :one
UPDATE courseauthoring.lectures
SET content_revision = content_revision + 1, updated_at = $2
WHERE id = $1
RETURNING content_revision;

-- name: DeleteLectureBlocks :exec
DELETE FROM courseauthoring.lecture_blocks WHERE lecture_id = $1;

-- name: DeleteLectureBlocksByClientIDs :exec
DELETE FROM courseauthoring.lecture_blocks
WHERE lecture_id = $1 AND client_block_id = ANY(sqlc.arg(client_block_ids)::text[]);

-- name: UpsertLectureBlocks :exec
-- One jsonb array of {id, client_block_id, kind, position, payload}: sqlc cannot
-- analyze a multi-array unnest(), and one bind value is one round trip either way.
INSERT INTO courseauthoring.lecture_blocks
  (id, course_id, lecture_id, client_block_id, kind, position, payload, created_at, updated_at)
SELECT (elem->>'id')::bigint, sqlc.arg(course_id)::bigint, sqlc.arg(lecture_id)::bigint,
       elem->>'client_block_id', elem->>'kind', (elem->>'position')::int, (elem->'payload')::jsonb,
       sqlc.arg(now)::timestamptz, sqlc.arg(now)::timestamptz
FROM jsonb_array_elements(sqlc.arg(blocks)::jsonb) AS elem
ON CONFLICT (lecture_id, client_block_id) DO UPDATE SET
  kind = EXCLUDED.kind, position = EXCLUDED.position, payload = EXCLUDED.payload, updated_at = EXCLUDED.updated_at;

-- name: ApplyLectureBlockOrder :exec
-- The position unique constraint is deferred, so permuting positions in one
-- statement is allowed. Rows already in place are skipped.
UPDATE courseauthoring.lecture_blocks b
SET position = o.position - 1, updated_at = sqlc.arg(now)::timestamptz
FROM unnest(sqlc.arg(client_block_ids)::text[]) WITH ORDINALITY AS o(client_block_id, position)
WHERE b.lecture_id = sqlc.arg(lecture_id)::bigint
  AND b.client_block_id = o.client_block_id
  AND b.position IS DISTINCT FROM (o.position - 1);
```

- [ ] **Step 3: Configure sqlc**

Append a second entry under `sql:` in `sqlc.yaml`:

```yaml
  - engine: postgresql
    schema: migrations
    queries: internal/courseauthoring/adapters/postgres/queries.sql
    gen:
      go:
        package: sqlcgen
        out: internal/courseauthoring/adapters/postgres/sqlcgen
        sql_package: pgx/v5
        overrides:
          - db_type: timestamptz
            go_type: time.Time
          - db_type: jsonb
            go_type: encoding/json.RawMessage
          - column: courseauthoring.lectures.section_id
            go_type:
              type: int64
              pointer: true
```

- [ ] **Step 4: Generate and verify**

Run: `sqlc generate && make sqlc-check && go build ./internal/courseauthoring/...`
Expected: success. `ListLecturesByCourseIDsRow` has `HasText bool`, `HasVideo bool`, `SectionID *int64` (from the column override).

- [ ] **Step 5: Commit**

```bash
git add migrations/00003_courseauthoring.sql internal/courseauthoring/adapters/postgres sqlc.yaml
git commit -m "feat(courseauthoring): add schema and queries"
```

---

### Task 5: App ports, errors and block building

**Files:**
- Create: `internal/courseauthoring/app/{ports,errors,blocks}.go`
- Test: `internal/courseauthoring/app/blocks_test.go`

**Interfaces:**
- Consumes: Task 3 domain, `contentblocks`.
- Produces: the declarations below, verbatim; `BlockInput`, `FlashcardCardInput`, `buildBlock(blockID id.ID, clientBlockID string, position int, in BlockInput) (contentblocks.Block, error)`, `buildContent(ids *id.Generator, in []BlockInput, legacy LegacyContent) (domain.LectureContent, error)`.

- [ ] **Step 1: Write `ports.go` and `errors.go`**

```go
// Package app contains the course authoring use cases and the ports they depend on.
package app

import (
	"context"

	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/contentblocks"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

// CourseRepository stores the Course aggregate. FindByID returns ErrNotFound;
// Update returns ErrConcurrentModification when c.Version is stale and increments
// c.Version on success. Insert sets c.Version to 1.
type CourseRepository interface {
	FindByID(ctx context.Context, courseID id.ID) (domain.Course, error)
	ListByOwner(ctx context.Context, ownerID id.ID) ([]domain.Course, error)
	Insert(ctx context.Context, c *domain.Course) error
	Update(ctx context.Context, c *domain.Course) error
}

// LectureHeader is a lecture's identity and content revision without its blocks.
type LectureHeader struct {
	LectureID       id.ID
	CourseID        id.ID
	Title           string
	FreePreview     bool
	ContentRevision int64
}

// BlockWritePlan is a validated content diff. Order is the full desired order of client block IDs.
type BlockWritePlan struct {
	Upserts []contentblocks.Block
	Deletes []string
	Order   []string
}

// LectureContentRepository reads and writes one lecture's blocks. Find* return ErrNotFound
// when the lecture does not exist in courseID. ApplyPatch returns ErrConcurrentModification
// when baseRevision is stale.
type LectureContentRepository interface {
	FindLecture(ctx context.Context, courseID, lectureID id.ID) (LectureHeader, error)
	FindLectureForUpdate(ctx context.Context, courseID, lectureID id.ID) (LectureHeader, error)
	ListBlocks(ctx context.Context, lectureID id.ID) ([]contentblocks.Block, error)
	ApplyPatch(ctx context.Context, courseID, lectureID id.ID, baseRevision int64, plan BlockWritePlan) (int64, error)
	ReplaceBlocks(ctx context.Context, courseID, lectureID id.ID, blocks []contentblocks.Block) (int64, error)
}

// EventPublisher records events in the current transaction.
type EventPublisher interface {
	Publish(ctx context.Context, events ...domain.Event) error
}

// Repos are bound to one transaction.
type Repos struct {
	Courses  CourseRepository
	Contents LectureContentRepository
	Events   EventPublisher
}

// TxRunner commits when fn returns nil and rolls back otherwise.
type TxRunner interface {
	RunInTx(ctx context.Context, fn func(Repos) error) error
}

// EnrollmentQuery reports whether a user may read a course's non-preview lectures.
type EnrollmentQuery interface {
	IsActivelyEnrolled(ctx context.Context, courseID, userID id.ID) (bool, error)
}
```

```go
package app

import "errors"

var (
	ErrNotFound               = errors.New("not found")
	ErrForbidden              = errors.New("forbidden")
	ErrEnrollmentRequired     = errors.New("enrollment required")
	ErrConcurrentModification = errors.New("concurrent modification")
	ErrInvalidInput           = errors.New("invalid input")
	ErrRevisionRequired       = errors.New("base_revision is required")
	ErrPatchTooLarge          = errors.New("patch exceeds a size limit")
	ErrBlockSetMismatch       = errors.New("order, upserts and deletes do not match the lecture's blocks")
	ErrOrderDeleteOverlap     = errors.New("a block cannot appear in both order and deletes")
	ErrDuplicateClientBlockID = errors.New("duplicate client block id")
)

// RevisionConflictError is returned when base_revision is stale. Current is the
// lecture's content at the time of the conflict.
type RevisionConflictError struct{ Current LectureContentView }

func (e *RevisionConflictError) Error() string { return "lecture content revision conflict" }
```

`LectureContentView` is declared in Task 8 (`content_service.go`); until then, add this to `ports.go` so the package compiles:

```go
// LectureContentView is a lecture's full content.
type LectureContentView struct {
	LectureID       id.ID
	CourseID        id.ID
	Title           string
	FreePreview     bool
	ContentRevision int64
	Blocks          []contentblocks.Block
}
```

- [ ] **Step 2: Port block building into `blocks.go`**

Copy from `$HITOX/internal/courseauthoring/application/dto.go` the types `ContentBlockInput` and `FlashcardCardInput` (rename the first to `BlockInput`), and from `$HITOX/internal/courseauthoring/application/course_service.go` the functions `newServerMintedClientBlockID`, `buildBlockFromInput` (rename to `buildBlock`), `buildLectureContent` (rename to `buildContent`), and `trimmedOrEmpty`. Apply these edits:

- `kernel.ID` → `id.ID`, `kernel.ParseID` → `id.Parse`; parse failures wrap `ErrInvalidInput`: `fmt.Errorf("%w: media_asset_id: %w", ErrInvalidInput, err)`.
- `domain.New*`/`domain.Block`/`domain.LectureContent` constructors that Hitox re-exported from `contentblocks` → call `contentblocks.*` directly; the content constructor is `domain.NewLectureContent`.
- `buildContent` takes `ids *id.Generator` and calls `ids.New()` where Hitox called `repos.LectureContents.NextBlockID()` or `kernel.NewID()`.
- Its legacy parameters become one struct:

```go
// LegacyContent is the pre-block request shape: optional text body and video URL.
type LegacyContent struct {
	TextBody        string
	VideoURL        string
	VideoDurationMs int64
}
```

- Delete every media/quiz validation call (`validateMediaAssetRef`, `validateMediaReferences`, `MediaAssetQuery`); references are stored unchecked.

Rule kept from Hitox: when `in` (the block list) is non-nil, it is exclusive and `legacy` is ignored; otherwise a text block is created from `legacy.TextBody` when non-empty and a video block from `legacy.VideoURL` when non-empty, each with a server-minted client block ID.

- [ ] **Step 3: Write `blocks_test.go`**

```go
package app

import (
	"errors"
	"testing"

	"github.com/santoshkc2200/ioe-backend/internal/platform/contentblocks"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

func testGen(t *testing.T) *id.Generator {
	t.Helper()
	g, err := id.NewGenerator(0)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func TestBuildContentLegacy(t *testing.T) {
	c, err := buildContent(testGen(t), nil, LegacyContent{TextBody: "<p>hi</p>", VideoURL: "https://v.example.com/a.mp4", VideoDurationMs: 5000})
	if err != nil || !c.HasText() || !c.HasVideo() || len(c.Blocks()) != 2 {
		t.Fatalf("%v %v", c, err)
	}
	empty, err := buildContent(testGen(t), nil, LegacyContent{})
	if err != nil || len(empty.Blocks()) != 0 {
		t.Fatalf("empty: %v %v", empty, err)
	}
}

func TestBuildContentBlocksAreExclusive(t *testing.T) {
	c, err := buildContent(testGen(t), []BlockInput{{ClientBlockID: "a", Type: "text", Body: "<p>x</p>"}}, LegacyContent{VideoURL: "https://v.example.com/a.mp4"})
	if err != nil || c.HasVideo() || len(c.Blocks()) != 1 {
		t.Fatalf("%v %v", c, err)
	}
}

func TestBuildBlockRejectsBadReferences(t *testing.T) {
	for _, in := range []BlockInput{
		{ClientBlockID: "q", Type: "quiz", QuizID: "abc"},
		{ClientBlockID: "v", Type: "video", MediaAssetID: "-5"},
		{ClientBlockID: "x", Type: "nope"},
	} {
		if _, err := buildBlock(1, in.ClientBlockID, 0, in); err == nil {
			t.Fatalf("%+v accepted", in)
		}
	}
	b, err := buildBlock(1, "q", 0, BlockInput{ClientBlockID: "q", Type: "quiz", QuizID: "1840396745219883008"})
	if err != nil || b.Type() != contentblocks.BlockQuiz || b.QuizID() != 1840396745219883008 {
		t.Fatalf("%v %v", b, err)
	}
	if _, err := buildBlock(1, "q", 0, BlockInput{Type: "quiz", QuizID: "x"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput", err)
	}
}
```

Field names on `BlockInput` (`ClientBlockID`, `Type`, `Body`, `QuizID`, `MediaAssetID`) are Hitox's `ContentBlockInput` names; keep them.

- [ ] **Step 4: Run tests**

Run: `go test -race ./internal/courseauthoring/app/ && make lint`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/courseauthoring/app
git commit -m "feat(courseauthoring): add app ports and block building"
```

---

### Task 6: Postgres adapter

**Files:**
- Create: `internal/courseauthoring/adapters/postgres/{postgres,courses,contents}.go`
- Test: `internal/courseauthoring/adapters/postgres/postgres_integration_test.go`

**Interfaces:**
- Consumes: Task 4 `sqlcgen`, Task 5 ports.
- Produces: `func NewTxRunner(pool *pgxpool.Pool, c clock.Clock) *TxRunner` implementing `app.TxRunner`. Block and entity IDs are minted in the app layer, so the adapter needs no generator.

- [ ] **Step 1: Write the failing integration tests**

```go
//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/adapters/postgres"
	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/app"
	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/clock"
	"github.com/santoshkc2200/ioe-backend/internal/platform/contentblocks"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
	"github.com/santoshkc2200/ioe-backend/internal/platform/postgres/pgtest"
)

type fixture struct {
	tx  *postgres.TxRunner
	ids *id.Generator
	now time.Time
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	ids, err := id.NewGenerator(0)
	if err != nil {
		t.Fatal(err)
	}
	return fixture{tx: postgres.NewTxRunner(pgtest.New(t), clock.System{}), ids: ids, now: time.Now().UTC().Truncate(time.Microsecond)}
}

func (f fixture) seedCourse(t *testing.T) domain.Course {
	t.Helper()
	ttl, _ := contentblocks.NewTitle("Go")
	c := domain.NewCourse(f.ids.New(), 100, ttl, "d", f.now)
	sec, _ := contentblocks.NewTitle("Intro")
	if err := c.AddSection(f.ids.New(), sec, f.now); err != nil {
		t.Fatal(err)
	}
	lt, _ := contentblocks.NewTitle("L1")
	if err := c.AddLecture(f.ids.New(), lt, false, false, f.now); err != nil {
		t.Fatal(err)
	}
	if err := c.MoveLectureToSection(c.Lectures[0].ID, c.Sections[0].ID, f.now); err != nil {
		t.Fatal(err)
	}
	if err := f.tx.RunInTx(context.Background(), func(r app.Repos) error { return r.Courses.Insert(context.Background(), &c) }); err != nil {
		t.Fatal(err)
	}
	return c
}

func textBlock(t *testing.T, blockID id.ID, client string, pos int, html string) contentblocks.Block {
	t.Helper()
	body, err := contentblocks.NewSanitizedTextBody(html)
	if err != nil {
		t.Fatal(err)
	}
	return contentblocks.NewTextBlock(blockID, client, pos, body)
}

func TestCourseRoundTripAndVersionConflict(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	c := f.seedCourse(t)

	var loaded domain.Course
	if err := f.tx.RunInTx(ctx, func(r app.Repos) error {
		var err error
		loaded, err = r.Courses.FindByID(ctx, c.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if loaded.Version != 1 || len(loaded.Sections) != 1 || len(loaded.Lectures) != 1 || loaded.Lectures[0].SectionID != c.Sections[0].ID {
		t.Fatalf("loaded = %+v", loaded)
	}

	stale := loaded
	if err := loaded.SetPrice(domain.Price{AmountMinor: 50000, Currency: "NPR"}, f.now); err != nil {
		t.Fatal(err)
	}
	if err := f.tx.RunInTx(ctx, func(r app.Repos) error { return r.Courses.Update(ctx, &loaded) }); err != nil || loaded.Version != 2 {
		t.Fatalf("update = %v, version %d", err, loaded.Version)
	}
	if err := f.tx.RunInTx(ctx, func(r app.Repos) error { return r.Courses.Update(ctx, &stale) }); !errors.Is(err, app.ErrConcurrentModification) {
		t.Fatalf("stale update err = %v", err)
	}

	if err := f.tx.RunInTx(ctx, func(r app.Repos) error {
		_, err := r.Courses.FindByID(ctx, 42)
		return err
	}); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("missing err = %v", err)
	}
}

func TestRemoveSectionAndLectureAreDeleted(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	c := f.seedCourse(t)
	if err := c.RemoveSection(c.Sections[0].ID, f.now); err != nil {
		t.Fatal(err)
	}
	if err := f.tx.RunInTx(ctx, func(r app.Repos) error { return r.Courses.Update(ctx, &c) }); err != nil {
		t.Fatal(err)
	}
	var got domain.Course
	_ = f.tx.RunInTx(ctx, func(r app.Repos) error { var err error; got, err = r.Courses.FindByID(ctx, c.ID); return err })
	if len(got.Sections) != 0 || got.Lectures[0].SectionID != 0 {
		t.Fatalf("got = %+v", got)
	}
	if err := c.RemoveLecture(c.Lectures[0].ID, f.now); err != nil {
		t.Fatal(err)
	}
	if err := f.tx.RunInTx(ctx, func(r app.Repos) error { return r.Courses.Update(ctx, &c) }); err != nil {
		t.Fatal(err)
	}
	_ = f.tx.RunInTx(ctx, func(r app.Repos) error { var err error; got, err = r.Courses.FindByID(ctx, c.ID); return err })
	if len(got.Lectures) != 0 {
		t.Fatalf("lecture not deleted: %+v", got.Lectures)
	}
}

func TestPatchRevisionAndReorder(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	c := f.seedCourse(t)
	lid := c.Lectures[0].ID

	var rev int64
	err := f.tx.RunInTx(ctx, func(r app.Repos) error {
		var err error
		rev, err = r.Contents.ReplaceBlocks(ctx, c.ID, lid, []contentblocks.Block{
			textBlock(t, f.ids.New(), "a", 0, "<p>a</p>"),
			textBlock(t, f.ids.New(), "b", 1, "<p>b</p>"),
		})
		return err
	})
	if err != nil || rev != 1 {
		t.Fatalf("replace = %d, %v", rev, err)
	}

	plan := app.BlockWritePlan{Upserts: []contentblocks.Block{textBlock(t, f.ids.New(), "c", 0, "<p>c</p>")}, Order: []string{"c", "b", "a"}}
	if err := f.tx.RunInTx(ctx, func(r app.Repos) error {
		var err error
		rev, err = r.Contents.ApplyPatch(ctx, c.ID, lid, 1, plan)
		return err
	}); err != nil || rev != 2 {
		t.Fatalf("patch = %d, %v", rev, err)
	}
	if err := f.tx.RunInTx(ctx, func(r app.Repos) error {
		_, err := r.Contents.ApplyPatch(ctx, c.ID, lid, 1, app.BlockWritePlan{Order: []string{"a", "b", "c"}})
		return err
	}); !errors.Is(err, app.ErrConcurrentModification) {
		t.Fatalf("stale patch err = %v", err)
	}

	var blocks []contentblocks.Block
	_ = f.tx.RunInTx(ctx, func(r app.Repos) error { var err error; blocks, err = r.Contents.ListBlocks(ctx, lid); return err })
	if len(blocks) != 3 || blocks[0].ClientBlockID() != "c" || blocks[1].ClientBlockID() != "b" || blocks[2].ClientBlockID() != "a" {
		t.Fatalf("blocks = %+v", blocks)
	}

	var course domain.Course
	_ = f.tx.RunInTx(ctx, func(r app.Repos) error { var err error; course, err = r.Courses.FindByID(ctx, c.ID); return err })
	if !course.Lectures[0].HasText || course.Lectures[0].HasVideo {
		t.Fatalf("flags = %+v", course.Lectures[0])
	}
}

func TestConcurrentPatchesExactlyOneWins(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	c := f.seedCourse(t)
	lid := c.Lectures[0].ID

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = f.tx.RunInTx(ctx, func(r app.Repos) error {
				if _, err := r.Contents.FindLectureForUpdate(ctx, c.ID, lid); err != nil {
					return err
				}
				_, err := r.Contents.ApplyPatch(ctx, c.ID, lid, 0, app.BlockWritePlan{
					Upserts: []contentblocks.Block{textBlock(t, f.ids.New(), "x", 0, "<p>x</p>")}, Order: []string{"x"}})
				return err
			})
		}()
	}
	wg.Wait()
	wins := 0
	for _, err := range errs {
		switch {
		case err == nil:
			wins++
		case !errors.Is(err, app.ErrConcurrentModification):
			t.Fatalf("unexpected err %v", err)
		}
	}
	if wins != 1 {
		t.Fatalf("wins = %d, errs = %v", wins, errs)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test -tags integration ./internal/courseauthoring/adapters/postgres/`
Expected: FAIL to compile, `postgres.NewTxRunner` undefined.

- [ ] **Step 3: Implement `postgres.go`**

```go
// Package postgres implements course authoring persistence on the courseauthoring schema.
package postgres

import (
	"context"
	"encoding/json"

	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/adapters/postgres/sqlcgen"
	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/app"
	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/clock"
	"github.com/santoshkc2200/ioe-backend/internal/platform/outbox"
)

// TxRunner runs course authoring use cases in one PostgreSQL transaction.
type TxRunner struct {
	pool  *pgxpool.Pool
	clock clock.Clock
}

func NewTxRunner(pool *pgxpool.Pool, c clock.Clock) *TxRunner {
	return &TxRunner{pool: pool, clock: c}
}

func (r *TxRunner) RunInTx(ctx context.Context, fn func(app.Repos) error) error {
	return pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		q := sqlcgen.New(tx)
		return fn(app.Repos{
			Courses:  courses{q: q},
			Contents: contents{q: q, clock: r.clock},
			Events:   events{tx: tx},
		})
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

- [ ] **Step 4: Implement `courses.go`**

```go
package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/adapters/postgres/sqlcgen"
	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/app"
	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/contentblocks"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

type courses struct{ q *sqlcgen.Queries }

func (r courses) FindByID(ctx context.Context, courseID id.ID) (domain.Course, error) {
	cs, err := r.load(ctx, []int64{int64(courseID)})
	if err != nil {
		return domain.Course{}, err
	}
	if len(cs) == 0 {
		return domain.Course{}, app.ErrNotFound
	}
	return cs[0], nil
}

func (r courses) ListByOwner(ctx context.Context, ownerID id.ID) ([]domain.Course, error) {
	ids, err := r.q.ListCourseIDsByOwner(ctx, int64(ownerID))
	if err != nil {
		return nil, err
	}
	cs, err := r.load(ctx, ids)
	if err != nil {
		return nil, err
	}
	// load returns database order; restore newest-first.
	byID := make(map[int64]domain.Course, len(cs))
	for _, c := range cs {
		byID[int64(c.ID)] = c
	}
	out := make([]domain.Course, 0, len(ids))
	for _, cid := range ids {
		out = append(out, byID[cid])
	}
	return out, nil
}

// load hydrates courses with their sections and lectures in three queries.
func (r courses) load(ctx context.Context, courseIDs []int64) ([]domain.Course, error) {
	if len(courseIDs) == 0 {
		return nil, nil
	}
	rows, err := r.q.ListCoursesByIDs(ctx, courseIDs)
	if err != nil {
		return nil, err
	}
	sections, err := r.q.ListSectionsByCourseIDs(ctx, courseIDs)
	if err != nil {
		return nil, err
	}
	lectures, err := r.q.ListLecturesByCourseIDs(ctx, courseIDs)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Course, 0, len(rows))
	index := make(map[int64]int, len(rows))
	for _, row := range rows {
		ttl, err := contentblocks.NewTitle(row.Title)
		if err != nil {
			return nil, fmt.Errorf("course %d title: %w", row.ID, err)
		}
		index[row.ID] = len(out)
		out = append(out, domain.Course{
			ID: id.ID(row.ID), OwnerID: id.ID(row.OwnerID), Title: ttl, Description: row.Description,
			Level: row.Level, ThumbnailURL: row.ThumbnailUrl, Status: domain.Status(row.Status),
			Price:   domain.Price{AmountMinor: row.PriceAmountMinor, Currency: row.PriceCurrency},
			Version: row.Version, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
		})
	}
	for _, s := range sections {
		ttl, err := contentblocks.NewTitle(s.Title)
		if err != nil {
			return nil, fmt.Errorf("section %d title: %w", s.ID, err)
		}
		c := &out[index[s.CourseID]]
		c.Sections = append(c.Sections, domain.Section{ID: id.ID(s.ID), Title: ttl, Order: int(s.SortOrder)})
	}
	for _, l := range lectures {
		ttl, err := contentblocks.NewTitle(l.Title)
		if err != nil {
			return nil, fmt.Errorf("lecture %d title: %w", l.ID, err)
		}
		var sectionID id.ID
		if l.SectionID != nil {
			sectionID = id.ID(*l.SectionID)
		}
		c := &out[index[l.CourseID]]
		c.Lectures = append(c.Lectures, domain.Lecture{ID: id.ID(l.ID), SectionID: sectionID, Title: ttl,
			FreePreview: l.FreePreview, Order: int(l.SortOrder), HasText: l.HasText, HasVideo: l.HasVideo})
	}
	return out, nil
}

func (r courses) Insert(ctx context.Context, c *domain.Course) error {
	if err := r.q.InsertCourse(ctx, sqlcgen.InsertCourseParams{
		ID: int64(c.ID), OwnerID: int64(c.OwnerID), Title: c.Title.String(), Description: c.Description,
		Level: c.Level, ThumbnailUrl: c.ThumbnailURL, Status: string(c.Status),
		PriceAmountMinor: c.Price.AmountMinor, PriceCurrency: c.Price.Currency,
		CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
	}); err != nil {
		return err
	}
	c.Version = 1
	return r.saveChildren(ctx, c)
}

func (r courses) Update(ctx context.Context, c *domain.Course) error {
	n, err := r.q.UpdateCourse(ctx, sqlcgen.UpdateCourseParams{
		ID: int64(c.ID), Version: c.Version, Title: c.Title.String(), Description: c.Description,
		Level: c.Level, ThumbnailUrl: c.ThumbnailURL, Status: string(c.Status),
		PriceAmountMinor: c.Price.AmountMinor, PriceCurrency: c.Price.Currency, UpdatedAt: c.UpdatedAt,
	})
	if err != nil {
		return err
	}
	if n == 0 {
		return app.ErrConcurrentModification
	}
	c.Version++
	return r.saveChildren(ctx, c)
}

// saveChildren upserts sections then lectures, then deletes rows no longer in the
// aggregate. It never touches lecture content or content_revision.
func (r courses) saveChildren(ctx context.Context, c *domain.Course) error {
	sectionIDs := make([]int64, 0, len(c.Sections))
	for _, s := range c.Sections {
		if err := r.q.UpsertSection(ctx, sqlcgen.UpsertSectionParams{
			ID: int64(s.ID), CourseID: int64(c.ID), Title: s.Title.String(), SortOrder: int32(s.Order),
		}); err != nil {
			return err
		}
		sectionIDs = append(sectionIDs, int64(s.ID))
	}
	lectureIDs := make([]int64, 0, len(c.Lectures))
	for _, l := range c.Lectures {
		var sectionID *int64
		if !l.SectionID.IsZero() {
			v := int64(l.SectionID)
			sectionID = &v
		}
		if err := r.q.UpsertLecture(ctx, sqlcgen.UpsertLectureParams{
			ID: int64(l.ID), CourseID: int64(c.ID), SectionID: sectionID, Title: l.Title.String(),
			FreePreview: l.FreePreview, SortOrder: int32(l.Order), CreatedAt: c.UpdatedAt,
		}); err != nil {
			return err
		}
		lectureIDs = append(lectureIDs, int64(l.ID))
	}
	if err := r.q.DeleteLecturesNotIn(ctx, sqlcgen.DeleteLecturesNotInParams{CourseID: int64(c.ID), Keep: lectureIDs}); err != nil {
		return err
	}
	return r.q.DeleteSectionsNotIn(ctx, sqlcgen.DeleteSectionsNotInParams{CourseID: int64(c.ID), Keep: sectionIDs})
}

func notFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return app.ErrNotFound
	}
	return err
}
```

Generated parameter and field names (`ThumbnailUrl`, `CreatedAt` in `UpsertLectureParams` for `$7`) may differ; use what `sqlcgen` produced. If sqlc names `$7` in `UpsertLecture` as `CreatedAt`, the value is `c.UpdatedAt` on purpose: it is the write time for new rows and `updated_at` for existing ones. `int32(...)` conversions on small orders are safe; add `//nolint:gosec // order is bounded by lecture count` if `gosec` flags G115.

- [ ] **Step 5: Implement `contents.go`**

Port `$HITOX/internal/courseauthoring/adapters/postgres/lecture_content_repository.go` (`ApplyPatch`, `ListBlocks`, `FindLecture*`, `upsertBlockElem`) and the helpers `lectureBlockPayload` / `lectureBlockFromRow` from `course_repository.go`, with: no tenant arguments; `kernel.ID` → `id.ID`; no dirty tracking, no `BumpCourseContentRevision` call; new `ReplaceBlocks`. The resulting file:

```go
package postgres

import (
	"context"
	"encoding/json"

	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/adapters/postgres/sqlcgen"
	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/app"
	"github.com/santoshkc2200/ioe-backend/internal/platform/clock"
	"github.com/santoshkc2200/ioe-backend/internal/platform/contentblocks"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

type contents struct {
	q     *sqlcgen.Queries
	clock clock.Clock
}

func (r contents) FindLecture(ctx context.Context, courseID, lectureID id.ID) (app.LectureHeader, error) {
	row, err := r.q.GetLectureHeader(ctx, sqlcgen.GetLectureHeaderParams{CourseID: int64(courseID), ID: int64(lectureID)})
	if err != nil {
		return app.LectureHeader{}, notFound(err)
	}
	return app.LectureHeader{LectureID: id.ID(row.ID), CourseID: id.ID(row.CourseID), Title: row.Title,
		FreePreview: row.FreePreview, ContentRevision: row.ContentRevision}, nil
}

func (r contents) FindLectureForUpdate(ctx context.Context, courseID, lectureID id.ID) (app.LectureHeader, error) {
	row, err := r.q.GetLectureHeaderForUpdate(ctx, sqlcgen.GetLectureHeaderForUpdateParams{CourseID: int64(courseID), ID: int64(lectureID)})
	if err != nil {
		return app.LectureHeader{}, notFound(err)
	}
	return app.LectureHeader{LectureID: id.ID(row.ID), CourseID: id.ID(row.CourseID), Title: row.Title,
		FreePreview: row.FreePreview, ContentRevision: row.ContentRevision}, nil
}

func (r contents) ListBlocks(ctx context.Context, lectureID id.ID) ([]contentblocks.Block, error) {
	rows, err := r.q.ListLectureBlocks(ctx, int64(lectureID))
	if err != nil {
		return nil, err
	}
	out := make([]contentblocks.Block, 0, len(rows))
	for _, row := range rows {
		b, err := contentblocks.DecodePayload(contentblocks.BlockType(row.Kind), row.Payload)
		if err != nil {
			return nil, err
		}
		out = append(out, b.WithIdentity(id.ID(row.ID), row.ClientBlockID, int(row.Position)))
	}
	return out, nil
}

type upsertBlockElem struct {
	ID            int64           `json:"id"`
	ClientBlockID string          `json:"client_block_id"`
	Kind          string          `json:"kind"`
	Position      int             `json:"position"`
	Payload       json.RawMessage `json:"payload"`
}

func (r contents) upsert(ctx context.Context, courseID, lectureID id.ID, blocks []contentblocks.Block) error {
	if len(blocks) == 0 {
		return nil
	}
	elems := make([]upsertBlockElem, len(blocks))
	for i, b := range blocks {
		payload, err := contentblocks.EncodePayload(b)
		if err != nil {
			return err
		}
		elems[i] = upsertBlockElem{ID: int64(b.ID()), ClientBlockID: b.ClientBlockID(), Kind: string(b.Type()), Position: b.Position(), Payload: payload}
	}
	raw, err := json.Marshal(elems)
	if err != nil {
		return err
	}
	return r.q.UpsertLectureBlocks(ctx, sqlcgen.UpsertLectureBlocksParams{
		CourseID: int64(courseID), LectureID: int64(lectureID), Now: r.clock.Now(), Blocks: raw,
	})
}

// ApplyPatch bumps the revision with a compare-and-swap first, so a stale writer
// changes nothing; then deletes, upserts and reorders.
func (r contents) ApplyPatch(ctx context.Context, courseID, lectureID id.ID, baseRevision int64, plan app.BlockWritePlan) (int64, error) {
	now := r.clock.Now()
	n, err := r.q.BumpLectureContentRevision(ctx, sqlcgen.BumpLectureContentRevisionParams{ID: int64(lectureID), ContentRevision: baseRevision, UpdatedAt: now})
	if err != nil {
		return 0, err
	}
	if n == 0 {
		return 0, app.ErrConcurrentModification
	}
	if len(plan.Deletes) > 0 {
		if err := r.q.DeleteLectureBlocksByClientIDs(ctx, sqlcgen.DeleteLectureBlocksByClientIDsParams{LectureID: int64(lectureID), ClientBlockIds: plan.Deletes}); err != nil {
			return 0, err
		}
	}
	if err := r.upsert(ctx, courseID, lectureID, plan.Upserts); err != nil {
		return 0, err
	}
	if len(plan.Order) > 0 {
		if err := r.q.ApplyLectureBlockOrder(ctx, sqlcgen.ApplyLectureBlockOrderParams{LectureID: int64(lectureID), Now: now, ClientBlockIds: plan.Order}); err != nil {
			return 0, err
		}
	}
	return baseRevision + 1, nil
}

// ReplaceBlocks swaps the whole block list and bumps the revision unconditionally,
// so any client holding the old revision conflicts on its next patch.
func (r contents) ReplaceBlocks(ctx context.Context, courseID, lectureID id.ID, blocks []contentblocks.Block) (int64, error) {
	if err := r.q.DeleteLectureBlocks(ctx, int64(lectureID)); err != nil {
		return 0, err
	}
	if err := r.upsert(ctx, courseID, lectureID, blocks); err != nil {
		return 0, err
	}
	return r.q.ForceBumpLectureContentRevision(ctx, sqlcgen.ForceBumpLectureContentRevisionParams{ID: int64(lectureID), UpdatedAt: r.clock.Now()})
}
```

Check the ported `contentblocks.DecodePayload`/`EncodePayload` signatures; if `DecodePayload` already takes identity arguments, drop the `WithIdentity` call.

- [ ] **Step 6: Run integration tests**

Run: `go test -race -tags integration ./internal/courseauthoring/adapters/postgres/`
Expected: PASS (Docker required; report if it did not run).

- [ ] **Step 7: Commit**

```bash
git add internal/courseauthoring/adapters/postgres
git commit -m "feat(courseauthoring): add postgres repositories"
```

---

### Task 7: `CourseService` with authorization

**Files:**
- Create: `internal/courseauthoring/app/course_service.go`
- Test: `internal/courseauthoring/app/fakes_test.go`, `course_service_test.go`

**Interfaces:**
- Consumes: Task 5 ports, Task 3 domain.
- Produces:

```go
func NewCourseService(tx TxRunner, ids *id.Generator, c clock.Clock) *CourseService

type CreateCourseInput struct{ OwnerID id.ID; Title, Description string } // OwnerID zero = caller
type DetailsInput struct{ Title, Description, Level, ThumbnailURL string }
type AddLectureInput struct{ Title string; Blocks []BlockInput; Legacy LegacyContent }

func (s *CourseService) Create(ctx context.Context, p auth.Principal, in CreateCourseInput) (domain.Course, error)
func (s *CourseService) Get(ctx context.Context, p auth.Principal, courseID id.ID) (domain.Course, error)
func (s *CourseService) ListByOwner(ctx context.Context, p auth.Principal, ownerID id.ID) ([]domain.Course, error)
func (s *CourseService) UpdateDetails(ctx context.Context, p auth.Principal, courseID id.ID, in DetailsInput) error
func (s *CourseService) SetPrice(ctx context.Context, p auth.Principal, courseID id.ID, amountMinor int64, currency string) (domain.Course, error)
func (s *CourseService) Publish(ctx context.Context, p auth.Principal, courseID id.ID) error
func (s *CourseService) Archive(ctx context.Context, p auth.Principal, courseID id.ID) error
func (s *CourseService) AddSection(ctx context.Context, p auth.Principal, courseID id.ID, title string) (domain.Course, error)
func (s *CourseService) RenameSection(ctx context.Context, p auth.Principal, courseID, sectionID id.ID, title string) error
func (s *CourseService) RemoveSection(ctx context.Context, p auth.Principal, courseID, sectionID id.ID) error
func (s *CourseService) AddLecture(ctx context.Context, p auth.Principal, courseID id.ID, in AddLectureInput) (domain.Course, error)
func (s *CourseService) RenameLecture(ctx context.Context, p auth.Principal, courseID, lectureID id.ID, title string) error
func (s *CourseService) RemoveLecture(ctx context.Context, p auth.Principal, courseID, lectureID id.ID) error
func (s *CourseService) ReorderLectures(ctx context.Context, p auth.Principal, courseID id.ID, lectureIDs []id.ID) error
func (s *CourseService) MoveLectureToSection(ctx context.Context, p auth.Principal, courseID, lectureID, sectionID id.ID) error
func (s *CourseService) SetLectureFreePreview(ctx context.Context, p auth.Principal, courseID, lectureID id.ID, freePreview bool) error
```

Domain errors are returned unwrapped (the HTTP layer maps them); title errors from `contentblocks.NewTitle` are wrapped with `ErrInvalidInput`.

- [ ] **Step 1: Write in-memory fakes**

`fakes_test.go`:

```go
package app_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/app"
	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/contentblocks"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time { return c.now }

// memStore is a single-goroutine fake; RunInTx copies state and commits only on success.
type memStore struct {
	mu        sync.Mutex
	courses   map[id.ID]domain.Course
	headers   map[id.ID]app.LectureHeader
	blocks    map[id.ID][]contentblocks.Block
	published []domain.Event
}

func newMemStore() *memStore {
	return &memStore{courses: map[id.ID]domain.Course{}, headers: map[id.ID]app.LectureHeader{}, blocks: map[id.ID][]contentblocks.Block{}}
}

func (m *memStore) RunInTx(_ context.Context, fn func(app.Repos) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	tx := &memTx{store: m, courses: clone(m.courses), headers: clone(m.headers), blocks: clone(m.blocks)}
	if err := fn(app.Repos{Courses: tx, Contents: tx, Events: tx}); err != nil {
		return err
	}
	m.courses, m.headers, m.blocks = tx.courses, tx.headers, tx.blocks
	m.published = append(m.published, tx.events...)
	return nil
}

func clone[K comparable, V any](in map[K]V) map[K]V {
	out := make(map[K]V, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

type memTx struct {
	store   *memStore
	courses map[id.ID]domain.Course
	headers map[id.ID]app.LectureHeader
	blocks  map[id.ID][]contentblocks.Block
	events  []domain.Event
}

func (t *memTx) FindByID(_ context.Context, cid id.ID) (domain.Course, error) {
	c, ok := t.courses[cid]
	if !ok {
		return domain.Course{}, app.ErrNotFound
	}
	c.Sections = append([]domain.Section(nil), c.Sections...)
	c.Lectures = append([]domain.Lecture(nil), c.Lectures...)
	return c, nil
}

func (t *memTx) ListByOwner(_ context.Context, owner id.ID) ([]domain.Course, error) {
	var out []domain.Course
	for _, c := range t.courses {
		if c.OwnerID == owner {
			out = append(out, c)
		}
	}
	return out, nil
}

func (t *memTx) Insert(_ context.Context, c *domain.Course) error {
	c.Version = 1
	t.courses[c.ID] = *c
	t.syncHeaders(*c)
	return nil
}

func (t *memTx) Update(_ context.Context, c *domain.Course) error {
	if t.courses[c.ID].Version != c.Version {
		return app.ErrConcurrentModification
	}
	c.Version++
	t.courses[c.ID] = *c
	t.syncHeaders(*c)
	return nil
}

func (t *memTx) syncHeaders(c domain.Course) {
	for lid, h := range t.headers {
		if h.CourseID == c.ID {
			if _, ok := c.Lecture(lid); !ok {
				delete(t.headers, lid)
				delete(t.blocks, lid)
			}
		}
	}
	for _, l := range c.Lectures {
		h := t.headers[l.ID]
		h.LectureID, h.CourseID, h.Title, h.FreePreview = l.ID, c.ID, l.Title.String(), l.FreePreview
		t.headers[l.ID] = h
	}
}

func (t *memTx) FindLecture(_ context.Context, cid, lid id.ID) (app.LectureHeader, error) {
	h, ok := t.headers[lid]
	if !ok || h.CourseID != cid {
		return app.LectureHeader{}, app.ErrNotFound
	}
	return h, nil
}

func (t *memTx) FindLectureForUpdate(ctx context.Context, cid, lid id.ID) (app.LectureHeader, error) {
	return t.FindLecture(ctx, cid, lid)
}

func (t *memTx) ListBlocks(_ context.Context, lid id.ID) ([]contentblocks.Block, error) {
	return append([]contentblocks.Block(nil), t.blocks[lid]...), nil
}

func (t *memTx) ApplyPatch(_ context.Context, _, lid id.ID, base int64, plan app.BlockWritePlan) (int64, error) {
	h := t.headers[lid]
	if h.ContentRevision != base {
		return 0, app.ErrConcurrentModification
	}
	byClient := map[string]contentblocks.Block{}
	for _, b := range t.blocks[lid] {
		byClient[b.ClientBlockID()] = b
	}
	for _, d := range plan.Deletes {
		delete(byClient, d)
	}
	for _, u := range plan.Upserts {
		byClient[u.ClientBlockID()] = u
	}
	next := make([]contentblocks.Block, 0, len(plan.Order))
	for i, cb := range plan.Order {
		b := byClient[cb]
		next = append(next, b.WithIdentity(b.ID(), cb, i))
	}
	t.blocks[lid] = next
	h.ContentRevision++
	t.headers[lid] = h
	return h.ContentRevision, nil
}

func (t *memTx) ReplaceBlocks(_ context.Context, _, lid id.ID, blocks []contentblocks.Block) (int64, error) {
	t.blocks[lid] = append([]contentblocks.Block(nil), blocks...)
	h := t.headers[lid]
	h.ContentRevision++
	t.headers[lid] = h
	return h.ContentRevision, nil
}

func (t *memTx) Publish(_ context.Context, evs ...domain.Event) error {
	t.events = append(t.events, evs...)
	return nil
}

type enrolled map[[2]id.ID]bool

func (e enrolled) IsActivelyEnrolled(_ context.Context, courseID, userID id.ID) (bool, error) {
	return e[[2]id.ID{courseID, userID}], nil
}

func testIDs(t *testing.T) *id.Generator {
	t.Helper()
	g, err := id.NewGenerator(0)
	if err != nil {
		t.Fatal(err)
	}
	return g
}
```

`Insert` must create headers for lectures added on insert (none in practice). `AddLecture` on an existing course goes through `Update`, which calls `syncHeaders`.

- [ ] **Step 2: Write the failing tests**

`course_service_test.go`:

```go
package app_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/app"
	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
)

var (
	owner      = auth.Principal{UserID: 100, Role: auth.RoleInstructor}
	otherInstr = auth.Principal{UserID: 101, Role: auth.RoleInstructor}
	admin      = auth.Principal{UserID: 1, Role: auth.RoleRootAdmin}
	student    = auth.Principal{UserID: 200, Role: auth.RoleStudent}
	ctx        = context.Background()
)

func newCourseService(t *testing.T) (*app.CourseService, *memStore) {
	t.Helper()
	store := newMemStore()
	return app.NewCourseService(store, testIDs(t), fixedClock{time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)}), store
}

func TestCreateAuthorization(t *testing.T) {
	svc, _ := newCourseService(t)
	if _, err := svc.Create(ctx, student, app.CreateCourseInput{Title: "Go"}); !errors.Is(err, app.ErrForbidden) {
		t.Fatalf("student err = %v", err)
	}
	if _, err := svc.Create(ctx, owner, app.CreateCourseInput{OwnerID: 999, Title: "Go"}); !errors.Is(err, app.ErrForbidden) {
		t.Fatalf("foreign owner err = %v", err)
	}
	if _, err := svc.Create(ctx, owner, app.CreateCourseInput{Title: "  "}); !errors.Is(err, app.ErrInvalidInput) {
		t.Fatalf("blank title err = %v", err)
	}
	c, err := svc.Create(ctx, owner, app.CreateCourseInput{OwnerID: owner.UserID, Title: "Go"})
	if err != nil || c.OwnerID != owner.UserID || c.Status != domain.StatusDraft || c.Version != 1 {
		t.Fatalf("create = %+v, %v", c, err)
	}
	c, err = svc.Create(ctx, admin, app.CreateCourseInput{Title: "Admin course"})
	if err != nil || c.OwnerID != admin.UserID {
		t.Fatalf("admin create = %+v, %v", c, err)
	}
}

func TestGetVisibility(t *testing.T) {
	svc, _ := newCourseService(t)
	c, _ := svc.Create(ctx, owner, app.CreateCourseInput{Title: "Go"})

	for _, p := range []auth.Principal{otherInstr, student} {
		if _, err := svc.Get(ctx, p, c.ID); !errors.Is(err, app.ErrNotFound) {
			t.Fatalf("%v draft err = %v, want ErrNotFound", p, err)
		}
	}
	for _, p := range []auth.Principal{owner, admin} {
		if _, err := svc.Get(ctx, p, c.ID); err != nil {
			t.Fatalf("%v draft err = %v", p, err)
		}
	}

	if _, err := svc.AddLecture(ctx, owner, c.ID, app.AddLectureInput{Title: "L1"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Publish(ctx, owner, c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Get(ctx, student, c.ID); err != nil {
		t.Fatalf("published read err = %v", err)
	}
	if err := svc.Archive(ctx, admin, c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Get(ctx, student, c.ID); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("archived read err = %v", err)
	}
}

func TestWritesRequireManager(t *testing.T) {
	svc, _ := newCourseService(t)
	c, _ := svc.Create(ctx, owner, app.CreateCourseInput{Title: "Go"})
	_, _ = svc.AddLecture(ctx, owner, c.ID, app.AddLectureInput{Title: "L1"})
	_ = svc.Publish(ctx, owner, c.ID)

	// A published course is visible, so a non-manager gets Forbidden.
	if err := svc.UpdateDetails(ctx, otherInstr, c.ID, app.DetailsInput{Title: "x"}); !errors.Is(err, app.ErrForbidden) {
		t.Fatalf("published write err = %v", err)
	}
	c2, _ := svc.Create(ctx, owner, app.CreateCourseInput{Title: "Draft"})
	// A draft is invisible to non-managers, so they get NotFound.
	if err := svc.UpdateDetails(ctx, otherInstr, c2.ID, app.DetailsInput{Title: "x"}); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("draft write err = %v", err)
	}
	if err := svc.UpdateDetails(ctx, admin, c2.ID, app.DetailsInput{Title: "Renamed", Level: "beginner"}); err != nil {
		t.Fatalf("admin write err = %v", err)
	}
}

func TestListByOwnerAuthorization(t *testing.T) {
	svc, _ := newCourseService(t)
	_, _ = svc.Create(ctx, owner, app.CreateCourseInput{Title: "Go"})
	if _, err := svc.ListByOwner(ctx, otherInstr, owner.UserID); !errors.Is(err, app.ErrForbidden) {
		t.Fatalf("err = %v", err)
	}
	for _, p := range []auth.Principal{owner, admin} {
		cs, err := svc.ListByOwner(ctx, p, owner.UserID)
		if err != nil || len(cs) != 1 {
			t.Fatalf("%v: %d %v", p, len(cs), err)
		}
	}
}

func TestPublishEmitsEventAndPrice(t *testing.T) {
	svc, store := newCourseService(t)
	c, _ := svc.Create(ctx, owner, app.CreateCourseInput{Title: "Go"})
	if _, err := svc.SetPrice(ctx, owner, c.ID, 150000, "USD"); !errors.Is(err, domain.ErrUnsupportedCurrency) {
		t.Fatalf("price err = %v", err)
	}
	if _, err := svc.SetPrice(ctx, owner, c.ID, 150000, "NPR"); err != nil {
		t.Fatal(err)
	}
	if err := svc.Publish(ctx, owner, c.ID); !errors.Is(err, domain.ErrCourseHasNoLectures) {
		t.Fatalf("empty publish err = %v", err)
	}
	if _, err := svc.AddLecture(ctx, owner, c.ID, app.AddLectureInput{Title: "L1", Legacy: app.LegacyContent{TextBody: "<p>x</p>"}}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Publish(ctx, owner, c.ID); err != nil {
		t.Fatal(err)
	}
	if len(store.published) != 1 {
		t.Fatalf("events = %+v", store.published)
	}
	ev, ok := store.published[0].(domain.CoursePublished)
	if !ok || ev.CourseID != c.ID || ev.PriceAmountMinor != 150000 || ev.PriceCurrency != "NPR" {
		t.Fatalf("event = %+v", store.published[0])
	}
	got, _ := svc.Get(ctx, owner, c.ID)
	if !got.Lectures[0].HasText {
		t.Fatal("initial content flags not set")
	}
}

func TestAddLectureWritesInitialBlocks(t *testing.T) {
	svc, store := newCourseService(t)
	c, _ := svc.Create(ctx, owner, app.CreateCourseInput{Title: "Go"})
	c, err := svc.AddLecture(ctx, owner, c.ID, app.AddLectureInput{Title: "L1", Legacy: app.LegacyContent{TextBody: "<p>x</p>"}})
	if err != nil {
		t.Fatal(err)
	}
	lid := c.Lectures[0].ID
	if len(store.blocks[lid]) != 1 || store.headers[lid].ContentRevision != 1 {
		t.Fatalf("blocks %v revision %d", store.blocks[lid], store.headers[lid].ContentRevision)
	}
}
```

- [ ] **Step 3: Run to verify failure**

Run: `go test ./internal/courseauthoring/app/`
Expected: FAIL, `app.NewCourseService` undefined.

- [ ] **Step 4: Implement `course_service.go`**

```go
package app

import (
	"context"
	"fmt"

	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/clock"
	"github.com/santoshkc2200/ioe-backend/internal/platform/contentblocks"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

// CourseService implements course structure, lifecycle and read use cases.
type CourseService struct {
	tx    TxRunner
	ids   *id.Generator
	clock clock.Clock
}

func NewCourseService(tx TxRunner, ids *id.Generator, c clock.Clock) *CourseService {
	return &CourseService{tx: tx, ids: ids, clock: c}
}

type CreateCourseInput struct {
	OwnerID     id.ID // zero means the caller
	Title       string
	Description string
}

type DetailsInput struct {
	Title, Description, Level, ThumbnailURL string
}

type AddLectureInput struct {
	Title  string
	Blocks []BlockInput
	Legacy LegacyContent
}

func newTitle(raw string) (contentblocks.Title, error) {
	t, err := contentblocks.NewTitle(raw)
	if err != nil {
		return contentblocks.Title{}, fmt.Errorf("%w: %w", ErrInvalidInput, err)
	}
	return t, nil
}

// visible reports whether p may read c at all.
func visible(p auth.Principal, c *domain.Course) bool {
	return c.IsManagedBy(p) || c.Status == domain.StatusPublished
}

// loadManaged returns the course for a write: ErrNotFound when the caller cannot see it,
// ErrForbidden when they can see it but do not manage it.
func loadManaged(ctx context.Context, r Repos, p auth.Principal, courseID id.ID) (domain.Course, error) {
	c, err := r.Courses.FindByID(ctx, courseID)
	if err != nil {
		return domain.Course{}, err
	}
	if !visible(p, &c) {
		return domain.Course{}, ErrNotFound
	}
	if !c.IsManagedBy(p) {
		return domain.Course{}, ErrForbidden
	}
	return c, nil
}

// mutate loads a managed course, applies fn, and saves it in one transaction.
func (s *CourseService) mutate(ctx context.Context, p auth.Principal, courseID id.ID, fn func(r Repos, c *domain.Course) error) (domain.Course, error) {
	var out domain.Course
	err := s.tx.RunInTx(ctx, func(r Repos) error {
		c, err := loadManaged(ctx, r, p, courseID)
		if err != nil {
			return err
		}
		if err := fn(r, &c); err != nil {
			return err
		}
		if err := r.Courses.Update(ctx, &c); err != nil {
			return err
		}
		out = c
		return nil
	})
	return out, err
}

func (s *CourseService) Create(ctx context.Context, p auth.Principal, in CreateCourseInput) (domain.Course, error) {
	if p.Role != auth.RoleInstructor && p.Role != auth.RoleRootAdmin {
		return domain.Course{}, ErrForbidden
	}
	if !in.OwnerID.IsZero() && in.OwnerID != p.UserID {
		return domain.Course{}, ErrForbidden
	}
	title, err := newTitle(in.Title)
	if err != nil {
		return domain.Course{}, err
	}
	c := domain.NewCourse(s.ids.New(), p.UserID, title, in.Description, s.clock.Now())
	err = s.tx.RunInTx(ctx, func(r Repos) error { return r.Courses.Insert(ctx, &c) })
	return c, err
}

func (s *CourseService) Get(ctx context.Context, p auth.Principal, courseID id.ID) (domain.Course, error) {
	var c domain.Course
	err := s.tx.RunInTx(ctx, func(r Repos) error {
		var err error
		c, err = r.Courses.FindByID(ctx, courseID)
		if err != nil {
			return err
		}
		if !visible(p, &c) {
			return ErrNotFound
		}
		return nil
	})
	return c, err
}

func (s *CourseService) ListByOwner(ctx context.Context, p auth.Principal, ownerID id.ID) ([]domain.Course, error) {
	if p.UserID != ownerID && p.Role != auth.RoleRootAdmin {
		return nil, ErrForbidden
	}
	var cs []domain.Course
	err := s.tx.RunInTx(ctx, func(r Repos) error {
		var err error
		cs, err = r.Courses.ListByOwner(ctx, ownerID)
		return err
	})
	return cs, err
}

func (s *CourseService) UpdateDetails(ctx context.Context, p auth.Principal, courseID id.ID, in DetailsInput) error {
	title, err := newTitle(in.Title)
	if err != nil {
		return err
	}
	_, err = s.mutate(ctx, p, courseID, func(_ Repos, c *domain.Course) error {
		return c.UpdateDetails(title, in.Description, in.Level, in.ThumbnailURL, s.clock.Now())
	})
	return err
}

func (s *CourseService) SetPrice(ctx context.Context, p auth.Principal, courseID id.ID, amountMinor int64, currency string) (domain.Course, error) {
	price, err := domain.NewPrice(amountMinor, currency)
	if err != nil {
		return domain.Course{}, err
	}
	return s.mutate(ctx, p, courseID, func(_ Repos, c *domain.Course) error { return c.SetPrice(price, s.clock.Now()) })
}

func (s *CourseService) Publish(ctx context.Context, p auth.Principal, courseID id.ID) error {
	_, err := s.mutate(ctx, p, courseID, func(r Repos, c *domain.Course) error {
		now := s.clock.Now()
		if err := c.Publish(now); err != nil {
			return err
		}
		return r.Events.Publish(ctx, domain.CoursePublished{CourseID: c.ID, OwnerID: c.OwnerID,
			PriceAmountMinor: c.Price.AmountMinor, PriceCurrency: c.Price.Currency, OccurredAt: now})
	})
	return err
}

func (s *CourseService) Archive(ctx context.Context, p auth.Principal, courseID id.ID) error {
	_, err := s.mutate(ctx, p, courseID, func(r Repos, c *domain.Course) error {
		now := s.clock.Now()
		if err := c.Archive(now); err != nil {
			return err
		}
		return r.Events.Publish(ctx, domain.CourseArchived{CourseID: c.ID, OwnerID: c.OwnerID, OccurredAt: now})
	})
	return err
}

func (s *CourseService) AddSection(ctx context.Context, p auth.Principal, courseID id.ID, rawTitle string) (domain.Course, error) {
	title, err := newTitle(rawTitle)
	if err != nil {
		return domain.Course{}, err
	}
	return s.mutate(ctx, p, courseID, func(_ Repos, c *domain.Course) error { return c.AddSection(s.ids.New(), title, s.clock.Now()) })
}

func (s *CourseService) RenameSection(ctx context.Context, p auth.Principal, courseID, sectionID id.ID, rawTitle string) error {
	title, err := newTitle(rawTitle)
	if err != nil {
		return err
	}
	_, err = s.mutate(ctx, p, courseID, func(_ Repos, c *domain.Course) error { return c.RenameSection(sectionID, title, s.clock.Now()) })
	return err
}

func (s *CourseService) RemoveSection(ctx context.Context, p auth.Principal, courseID, sectionID id.ID) error {
	_, err := s.mutate(ctx, p, courseID, func(_ Repos, c *domain.Course) error { return c.RemoveSection(sectionID, s.clock.Now()) })
	return err
}

// AddLecture adds the lecture, saves the course, then writes the initial blocks in the same transaction.
func (s *CourseService) AddLecture(ctx context.Context, p auth.Principal, courseID id.ID, in AddLectureInput) (domain.Course, error) {
	title, err := newTitle(in.Title)
	if err != nil {
		return domain.Course{}, err
	}
	content, err := buildContent(s.ids, in.Blocks, in.Legacy)
	if err != nil {
		return domain.Course{}, err
	}
	var out domain.Course
	err = s.tx.RunInTx(ctx, func(r Repos) error {
		c, err := loadManaged(ctx, r, p, courseID)
		if err != nil {
			return err
		}
		lectureID := s.ids.New()
		if err := c.AddLecture(lectureID, title, content.HasText(), content.HasVideo(), s.clock.Now()); err != nil {
			return err
		}
		if err := r.Courses.Update(ctx, &c); err != nil {
			return err
		}
		if blocks := content.Blocks(); len(blocks) > 0 {
			if _, err := r.Contents.ReplaceBlocks(ctx, c.ID, lectureID, blocks); err != nil {
				return err
			}
		}
		out = c
		return nil
	})
	return out, err
}

func (s *CourseService) RenameLecture(ctx context.Context, p auth.Principal, courseID, lectureID id.ID, rawTitle string) error {
	title, err := newTitle(rawTitle)
	if err != nil {
		return err
	}
	_, err = s.mutate(ctx, p, courseID, func(_ Repos, c *domain.Course) error { return c.RenameLecture(lectureID, title, s.clock.Now()) })
	return err
}

func (s *CourseService) RemoveLecture(ctx context.Context, p auth.Principal, courseID, lectureID id.ID) error {
	_, err := s.mutate(ctx, p, courseID, func(_ Repos, c *domain.Course) error { return c.RemoveLecture(lectureID, s.clock.Now()) })
	return err
}

func (s *CourseService) ReorderLectures(ctx context.Context, p auth.Principal, courseID id.ID, lectureIDs []id.ID) error {
	_, err := s.mutate(ctx, p, courseID, func(_ Repos, c *domain.Course) error { return c.ReorderLectures(lectureIDs, s.clock.Now()) })
	return err
}

func (s *CourseService) MoveLectureToSection(ctx context.Context, p auth.Principal, courseID, lectureID, sectionID id.ID) error {
	_, err := s.mutate(ctx, p, courseID, func(_ Repos, c *domain.Course) error {
		return c.MoveLectureToSection(lectureID, sectionID, s.clock.Now())
	})
	return err
}

func (s *CourseService) SetLectureFreePreview(ctx context.Context, p auth.Principal, courseID, lectureID id.ID, freePreview bool) error {
	_, err := s.mutate(ctx, p, courseID, func(_ Repos, c *domain.Course) error {
		return c.SetLectureFreePreview(lectureID, freePreview, s.clock.Now())
	})
	return err
}
```

- [ ] **Step 5: Run tests**

Run: `go test -race ./internal/courseauthoring/app/ && make lint`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/courseauthoring/app
git commit -m "feat(courseauthoring): add course service with authorization"
```

---

### Task 8: `ContentService` (get, replace, patch)

**Files:**
- Create: `internal/courseauthoring/app/content_service.go`
- Modify: `internal/courseauthoring/app/ports.go` (move `LectureContentView` here from Task 5's temporary spot)
- Test: `internal/courseauthoring/app/content_service_test.go`

**Interfaces:**
- Consumes: Task 5 ports and block building; `loadManaged`, `visible` from Task 7.
- Produces:

```go
func NewContentService(tx TxRunner, ids *id.Generator, enrollments EnrollmentQuery) *ContentService

type PatchInput struct {
	BaseRevision *int64
	Order        []string
	Upserts      []BlockInput
	Deletes      []string
}

func (s *ContentService) Get(ctx context.Context, p auth.Principal, courseID, lectureID id.ID) (LectureContentView, error)
func (s *ContentService) Replace(ctx context.Context, p auth.Principal, courseID, lectureID id.ID, blocks []BlockInput, legacy LegacyContent) error
func (s *ContentService) Patch(ctx context.Context, p auth.Principal, courseID, lectureID id.ID, in PatchInput) (int64, error)
```

- [ ] **Step 1: Write the failing tests**

```go
package app_test

import (
	"errors"
	"testing"

	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/app"
	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

func ptr[T any](v T) *T { return &v }

type contentFixture struct {
	courses  *app.CourseService
	contents *app.ContentService
	store    *memStore
	course   domain.Course
	free     id.ID
	locked   id.ID
}

func newContentFixture(t *testing.T, enroll enrolled) contentFixture {
	t.Helper()
	courses, store := newCourseService(t)
	contents := app.NewContentService(store, testIDs(t), enroll)
	c, _ := courses.Create(ctx, owner, app.CreateCourseInput{Title: "Go"})
	c, _ = courses.AddLecture(ctx, owner, c.ID, app.AddLectureInput{Title: "Free", Legacy: app.LegacyContent{TextBody: "<p>free</p>"}})
	c, _ = courses.AddLecture(ctx, owner, c.ID, app.AddLectureInput{Title: "Locked", Legacy: app.LegacyContent{TextBody: "<p>locked</p>"}})
	free, locked := c.Lectures[0].ID, c.Lectures[1].ID
	if err := courses.SetLectureFreePreview(ctx, owner, c.ID, free, true); err != nil {
		t.Fatal(err)
	}
	return contentFixture{courses: courses, contents: contents, store: store, course: c, free: free, locked: locked}
}

func TestGetContentAccess(t *testing.T) {
	f := newContentFixture(t, enrolled{})
	if _, err := f.contents.Get(ctx, student, f.course.ID, f.free); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("draft read err = %v", err)
	}
	if err := f.courses.Publish(ctx, owner, f.course.ID); err != nil {
		t.Fatal(err)
	}
	if v, err := f.contents.Get(ctx, student, f.course.ID, f.free); err != nil || len(v.Blocks) != 1 {
		t.Fatalf("free preview = %+v, %v", v, err)
	}
	if _, err := f.contents.Get(ctx, student, f.course.ID, f.locked); !errors.Is(err, app.ErrEnrollmentRequired) {
		t.Fatalf("locked err = %v", err)
	}
	if _, err := f.contents.Get(ctx, owner, f.course.ID, f.locked); err != nil {
		t.Fatalf("owner err = %v", err)
	}
	if _, err := f.contents.Get(ctx, student, f.course.ID, 424242); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("missing lecture err = %v", err)
	}

	g := newContentFixture(t, enrolled{})
	_ = g.courses.Publish(ctx, owner, g.course.ID)
	g.contents = app.NewContentService(g.store, testIDs(t), enrolled{{g.course.ID, student.UserID}: true})
	if _, err := g.contents.Get(ctx, student, g.course.ID, g.locked); err != nil {
		t.Fatalf("enrolled err = %v", err)
	}
}

func TestPatchFlow(t *testing.T) {
	f := newContentFixture(t, enrolled{})
	v, _ := f.contents.Get(ctx, owner, f.course.ID, f.locked)
	first := v.Blocks[0].ClientBlockID()

	if _, err := f.contents.Patch(ctx, owner, f.course.ID, f.locked, app.PatchInput{Order: []string{first}}); !errors.Is(err, app.ErrRevisionRequired) {
		t.Fatalf("no base err = %v", err)
	}
	rev, err := f.contents.Patch(ctx, owner, f.course.ID, f.locked, app.PatchInput{
		BaseRevision: ptr(v.ContentRevision),
		Order:        []string{"new", first},
		Upserts:      []app.BlockInput{{ClientBlockID: "new", Type: "text", Body: "<p>new</p>"}},
	})
	if err != nil || rev != v.ContentRevision+1 {
		t.Fatalf("patch = %d, %v", rev, err)
	}

	_, err = f.contents.Patch(ctx, owner, f.course.ID, f.locked, app.PatchInput{BaseRevision: ptr(v.ContentRevision), Order: []string{first}, Deletes: []string{"new"}})
	var conflict *app.RevisionConflictError
	if !errors.As(err, &conflict) || conflict.Current.ContentRevision != rev || len(conflict.Current.Blocks) != 2 {
		t.Fatalf("stale err = %v", err)
	}

	cases := []struct {
		in   app.PatchInput
		want error
	}{
		{app.PatchInput{BaseRevision: ptr(rev), Order: []string{first}}, app.ErrBlockSetMismatch},
		{app.PatchInput{BaseRevision: ptr(rev), Order: []string{first, first, "new"}}, app.ErrDuplicateClientBlockID},
		{app.PatchInput{BaseRevision: ptr(rev), Order: []string{first, "new"}, Deletes: []string{"new"}}, app.ErrOrderDeleteOverlap},
		{app.PatchInput{BaseRevision: ptr(rev), Order: make([]string, 501)}, app.ErrPatchTooLarge},
	}
	for _, c := range cases {
		if _, err := f.contents.Patch(ctx, owner, f.course.ID, f.locked, c.in); !errors.Is(err, c.want) {
			t.Fatalf("%+v: err = %v, want %v", c.in, err, c.want)
		}
	}

	if _, err := f.contents.Patch(ctx, otherInstr, f.course.ID, f.locked, app.PatchInput{BaseRevision: ptr(rev), Order: []string{first, "new"}}); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("non-manager on draft err = %v", err)
	}
}

func TestReplaceInvalidatesStalePatch(t *testing.T) {
	f := newContentFixture(t, enrolled{})
	v, _ := f.contents.Get(ctx, owner, f.course.ID, f.locked)
	if err := f.contents.Replace(ctx, owner, f.course.ID, f.locked, []app.BlockInput{{ClientBlockID: "r", Type: "text", Body: "<p>r</p>"}}, app.LegacyContent{}); err != nil {
		t.Fatal(err)
	}
	_, err := f.contents.Patch(ctx, owner, f.course.ID, f.locked, app.PatchInput{BaseRevision: ptr(v.ContentRevision), Order: []string{"r"}})
	var conflict *app.RevisionConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("stale patch after PUT err = %v", err)
	}
}

func TestContentWritesRejectArchived(t *testing.T) {
	f := newContentFixture(t, enrolled{})
	_ = f.courses.Archive(ctx, owner, f.course.ID)
	if err := f.contents.Replace(ctx, owner, f.course.ID, f.locked, nil, app.LegacyContent{TextBody: "<p>x</p>"}); !errors.Is(err, domain.ErrCourseNotEditable) {
		t.Fatalf("err = %v", err)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/courseauthoring/app/ -run 'Content|Patch|Replace'`
Expected: FAIL, `app.NewContentService` undefined.

- [ ] **Step 3: Implement `content_service.go`**

Port the validation half of `$HITOX/internal/courseauthoring/application/lecture_content_service.go` (`Patch` steps for size limits, duplicate detection, order/delete overlap, `validateBlockSetIdentity`, `setEquals`, `symmetricDifference`, and the block-building loop) into this shape. Media validation, tenant lookups, and the post-transaction re-read are removed: the lecture row is locked first, so the conflict view is read in the same transaction.

```go
package app

import (
	"context"
	"fmt"
	"sort"

	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/contentblocks"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

const (
	maxPatchOrderLen         = 500
	maxPatchUpsertsLen       = 200
	maxPatchDeletesLen       = 500
	maxSetMismatchIDsInError = 10
)

// ContentService reads and writes lecture block content.
type ContentService struct {
	tx          TxRunner
	ids         *id.Generator
	enrollments EnrollmentQuery
}

func NewContentService(tx TxRunner, ids *id.Generator, enrollments EnrollmentQuery) *ContentService {
	return &ContentService{tx: tx, ids: ids, enrollments: enrollments}
}

type PatchInput struct {
	BaseRevision *int64
	Order        []string
	Upserts      []BlockInput
	Deletes      []string
}

func readView(ctx context.Context, r Repos, h LectureHeader) (LectureContentView, error) {
	blocks, err := r.Contents.ListBlocks(ctx, h.LectureID)
	if err != nil {
		return LectureContentView{}, err
	}
	return LectureContentView{LectureID: h.LectureID, CourseID: h.CourseID, Title: h.Title,
		FreePreview: h.FreePreview, ContentRevision: h.ContentRevision, Blocks: blocks}, nil
}

func (s *ContentService) Get(ctx context.Context, p auth.Principal, courseID, lectureID id.ID) (LectureContentView, error) {
	var v LectureContentView
	err := s.tx.RunInTx(ctx, func(r Repos) error {
		c, err := r.Courses.FindByID(ctx, courseID)
		if err != nil {
			return err
		}
		if !visible(p, &c) {
			return ErrNotFound
		}
		h, err := r.Contents.FindLecture(ctx, courseID, lectureID)
		if err != nil {
			return err
		}
		if !c.IsManagedBy(p) && !h.FreePreview {
			ok, err := s.enrollments.IsActivelyEnrolled(ctx, courseID, p.UserID)
			if err != nil {
				return err
			}
			if !ok {
				return ErrEnrollmentRequired
			}
		}
		v, err = readView(ctx, r, h)
		return err
	})
	return v, err
}

// lockEditable checks the caller manages an editable course and locks the lecture row.
func lockEditable(ctx context.Context, r Repos, p auth.Principal, courseID, lectureID id.ID) (LectureHeader, error) {
	c, err := loadManaged(ctx, r, p, courseID)
	if err != nil {
		return LectureHeader{}, err
	}
	if c.Status == domain.StatusArchived {
		return LectureHeader{}, domain.ErrCourseNotEditable
	}
	return r.Contents.FindLectureForUpdate(ctx, courseID, lectureID)
}

func (s *ContentService) Replace(ctx context.Context, p auth.Principal, courseID, lectureID id.ID, blocks []BlockInput, legacy LegacyContent) error {
	content, err := buildContent(s.ids, blocks, legacy)
	if err != nil {
		return err
	}
	return s.tx.RunInTx(ctx, func(r Repos) error {
		if _, err := lockEditable(ctx, r, p, courseID, lectureID); err != nil {
			return err
		}
		_, err := r.Contents.ReplaceBlocks(ctx, courseID, lectureID, content.Blocks())
		return err
	})
}

func (s *ContentService) Patch(ctx context.Context, p auth.Principal, courseID, lectureID id.ID, in PatchInput) (int64, error) {
	if in.BaseRevision == nil {
		return 0, ErrRevisionRequired
	}
	if len(in.Order) > maxPatchOrderLen || len(in.Upserts) > maxPatchUpsertsLen || len(in.Deletes) > maxPatchDeletesLen {
		return 0, ErrPatchTooLarge
	}
	upserts, orderSet, deleteSet, err := indexPatch(in)
	if err != nil {
		return 0, err
	}
	var rev int64
	err = s.tx.RunInTx(ctx, func(r Repos) error {
		h, err := lockEditable(ctx, r, p, courseID, lectureID)
		if err != nil {
			return err
		}
		if h.ContentRevision != *in.BaseRevision {
			current, err := readView(ctx, r, h)
			if err != nil {
				return err
			}
			return &RevisionConflictError{Current: current}
		}
		existing, err := r.Contents.ListBlocks(ctx, lectureID)
		if err != nil {
			return err
		}
		byClient := make(map[string]contentblocks.Block, len(existing))
		existingIDs := make(map[string]struct{}, len(existing))
		for _, b := range existing {
			byClient[b.ClientBlockID()] = b
			existingIDs[b.ClientBlockID()] = struct{}{}
		}
		if err := validateBlockSetIdentity(existingIDs, upserts, deleteSet, orderSet, in.Order); err != nil {
			return err
		}
		resulting := make([]contentblocks.Block, 0, len(in.Order))
		var changed []contentblocks.Block
		for i, cid := range in.Order {
			input, isUpsert := upserts[cid]
			if !isUpsert {
				b := byClient[cid]
				resulting = append(resulting, b.WithIdentity(b.ID(), cid, i))
				continue
			}
			blockID := s.ids.New()
			if prev, ok := byClient[cid]; ok {
				blockID = prev.ID() // edits keep the server ID for life
			}
			b, err := buildBlock(blockID, cid, i, input)
			if err != nil {
				return fmt.Errorf("%w: %w", ErrInvalidInput, err)
			}
			resulting = append(resulting, b)
			changed = append(changed, b)
		}
		if _, err := domain.NewLectureContent(resulting); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidInput, err)
		}
		rev, err = r.Contents.ApplyPatch(ctx, courseID, lectureID, *in.BaseRevision,
			BlockWritePlan{Upserts: changed, Deletes: in.Deletes, Order: in.Order})
		return err
	})
	return rev, err
}

// indexPatch rejects duplicates within each list and overlap between order and deletes.
func indexPatch(in PatchInput) (map[string]BlockInput, map[string]struct{}, map[string]struct{}, error) {
	upserts := make(map[string]BlockInput, len(in.Upserts))
	for _, u := range in.Upserts {
		if err := contentblocks.ValidateClientBlockID(u.ClientBlockID); err != nil {
			return nil, nil, nil, err
		}
		if _, dup := upserts[u.ClientBlockID]; dup {
			return nil, nil, nil, ErrDuplicateClientBlockID
		}
		upserts[u.ClientBlockID] = u
	}
	orderSet := make(map[string]struct{}, len(in.Order))
	for _, o := range in.Order {
		if _, dup := orderSet[o]; dup {
			return nil, nil, nil, ErrDuplicateClientBlockID
		}
		orderSet[o] = struct{}{}
	}
	deleteSet := make(map[string]struct{}, len(in.Deletes))
	for _, d := range in.Deletes {
		if _, dup := deleteSet[d]; dup {
			return nil, nil, nil, ErrDuplicateClientBlockID
		}
		if _, both := orderSet[d]; both {
			return nil, nil, nil, ErrOrderDeleteOverlap
		}
		deleteSet[d] = struct{}{}
	}
	for cid := range upserts {
		if _, ok := orderSet[cid]; !ok {
			return nil, nil, nil, fmt.Errorf("%w: upsert %q missing from order", ErrBlockSetMismatch, cid)
		}
	}
	return upserts, orderSet, deleteSet, nil
}

// validateBlockSetIdentity asserts set(order) == (existing ∪ upserts) − deletes.
func validateBlockSetIdentity(existing map[string]struct{}, upserts map[string]BlockInput, deletes, order map[string]struct{}, _ []string) error {
	expected := make(map[string]struct{}, len(existing)+len(upserts))
	for k := range existing {
		expected[k] = struct{}{}
	}
	for k := range upserts {
		expected[k] = struct{}{}
	}
	for k := range deletes {
		if _, ok := existing[k]; !ok {
			return fmt.Errorf("%w: delete %q does not exist", ErrBlockSetMismatch, k)
		}
		delete(expected, k)
	}
	if setEquals(expected, order) {
		return nil
	}
	return fmt.Errorf("%w: %d in order, %d expected, differing ids %v",
		ErrBlockSetMismatch, len(order), len(expected), symmetricDifference(expected, order))
}

func setEquals(a, b map[string]struct{}) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if _, ok := b[k]; !ok {
			return false
		}
	}
	return true
}

func symmetricDifference(a, b map[string]struct{}) []string {
	var out []string
	for k := range a {
		if _, ok := b[k]; !ok {
			out = append(out, k)
		}
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	if len(out) > maxSetMismatchIDsInError {
		out = out[:maxSetMismatchIDsInError]
	}
	return out
}
```

Compare with Hitox's `validateBlockSetIdentity` before finalizing; if Hitox treats a delete of a non-existent ID as a no-op rather than a mismatch, keep Hitox's behavior (the frontend's retry path may rely on it) and remove the "does not exist" branch. Drop the unused fifth parameter.

The `make([]string, 501)` case in the test hits `ErrPatchTooLarge` before any other check, as intended.

- [ ] **Step 4: Run tests**

Run: `go test -race ./internal/courseauthoring/app/ && make lint`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/courseauthoring/app
git commit -m "feat(courseauthoring): add lecture content service"
```

---

### Task 9: HTTP adapter

**Files:**
- Create: `internal/courseauthoring/adapters/httpapi/{httpapi,courses,content,wire}.go`
- Test: `internal/courseauthoring/adapters/httpapi/httpapi_test.go`

**Interfaces:**
- Consumes: `*app.CourseService` and `*app.ContentService` through the interfaces below; `httpserver.Router`, `DecodeJSON`, `WriteJSON`, `RateLimiter`; `problem.Write`, `problem.WriteWithExtensions`.
- Produces:

```go
type Config struct {
	RequireAuth    httpserver.Middleware
	ContentLimiter *httpserver.RateLimiter // 60 per minute
	Logger         *slog.Logger
}
func New(courses CourseService, contents ContentService, cfg Config) *Handler
func (h *Handler) Register(r *httpserver.Router)
```

where `CourseService` and `ContentService` are interfaces in this package listing exactly the methods from Tasks 7-8 (so tests can fake them).

- [ ] **Step 1: Write the wire types (`wire.go`)**

Requests and responses mirror `ioe-frontend/packages/course-core/src/api/courses.ts`:

```go
package httpapi

import (
	"encoding/json"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/app"
	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/contentblocks"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

type priceWire struct {
	AmountMinor int64  `json:"amount_minor"`
	Currency    string `json:"currency"`
}

type sectionWire struct {
	ID    id.ID  `json:"id"`
	Title string `json:"title"`
	Order int    `json:"order"`
}

type lectureWire struct {
	ID          id.ID  `json:"id"`
	SectionID   *id.ID `json:"section_id,omitempty"`
	Title       string `json:"title"`
	HasText     bool   `json:"has_text"`
	HasVideo    bool   `json:"has_video"`
	FreePreview bool   `json:"free_preview"`
	Order       int    `json:"order"`
}

type courseWire struct {
	ID           id.ID         `json:"id"`
	OwnerID      id.ID         `json:"owner_id"`
	Title        string        `json:"title"`
	Description  string        `json:"description"`
	Status       string        `json:"status"`
	Price        priceWire     `json:"price"`
	IsFree       bool          `json:"is_free"`
	Sections     []sectionWire `json:"sections"`
	Lectures     []lectureWire `json:"lectures"`
	ThumbnailURL string        `json:"thumbnail_url"`
	Level        string        `json:"level"`
	CreatedAt    time.Time     `json:"created_at"`
	UpdatedAt    time.Time     `json:"updated_at"`
}

type coursePageWire struct {
	Courses []courseWire `json:"courses"`
	Total   int          `json:"total"`
}

func toCourseWire(c domain.Course) courseWire {
	w := courseWire{ID: c.ID, OwnerID: c.OwnerID, Title: c.Title.String(), Description: c.Description,
		Status: string(c.Status), Price: priceWire{c.Price.AmountMinor, c.Price.Currency}, IsFree: c.Price.IsFree(),
		Sections: make([]sectionWire, 0, len(c.Sections)), Lectures: make([]lectureWire, 0, len(c.Lectures)),
		ThumbnailURL: c.ThumbnailURL, Level: c.Level, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt}
	for _, s := range c.Sections {
		w.Sections = append(w.Sections, sectionWire{ID: s.ID, Title: s.Title.String(), Order: s.Order})
	}
	for _, l := range c.Lectures {
		lw := lectureWire{ID: l.ID, Title: l.Title.String(), HasText: l.HasText, HasVideo: l.HasVideo, FreePreview: l.FreePreview, Order: l.Order}
		if !l.SectionID.IsZero() {
			sid := l.SectionID
			lw.SectionID = &sid
		}
		w.Lectures = append(w.Lectures, lw)
	}
	return w
}

type createCourseRequest struct {
	OwnerID     *id.ID `json:"owner_id"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

type updateDetailsRequest struct {
	Title        string `json:"title"`
	Description  string `json:"description"`
	ThumbnailURL string `json:"thumbnail_url"`
	Level        string `json:"level"`
}

type setPriceRequest struct {
	AmountMinor int64  `json:"amount_minor"`
	Currency    string `json:"currency"`
}

type titleRequest struct {
	Title string `json:"title"`
}

type addLectureRequest struct {
	Title           string `json:"title"`
	TextBody        string `json:"text_body"`
	VideoURL        string `json:"video_url"`
	VideoDurationMs int64  `json:"video_duration_ms"`
}

type reorderLecturesRequest struct {
	LectureIDs []id.ID `json:"lecture_ids"`
}

type moveLectureRequest struct {
	SectionID string `json:"section_id"` // "" means unsectioned
}

type freePreviewRequest struct {
	FreePreview bool `json:"free_preview"`
}

type replaceContentRequest struct {
	Blocks          []contentBlockRequest `json:"blocks"`
	TextBody        string                `json:"text_body"`
	VideoURL        string                `json:"video_url"`
	VideoDurationMs int64                 `json:"video_duration_ms"`
}

type patchContentRequest struct {
	BaseRevision *int64                `json:"base_revision"`
	Order        []string              `json:"order"`
	Upserts      []contentBlockRequest `json:"upserts"`
	Deletes      []string              `json:"deletes"`
}
```

Then port, from `$HITOX/internal/courseauthoring/adapters/httpapi/dto.go`, the types `contentBlockRequest` and `flashcardCardInput` and the function `toContentBlockInputs` (returning `[]app.BlockInput`), and from `$HITOX/internal/courseauthoring/application/dto.go` the types `ContentBlockView` and `FlashcardCardView` (lowercase them: `contentBlockWire`, `flashcardCardWire`) and the block-to-view loop inside `toLectureContentView`, turned into:

```go
type lectureContentWire struct {
	LectureID       id.ID              `json:"lecture_id"`
	CourseID        id.ID              `json:"course_id"`
	Title           string             `json:"title"`
	TextBody        string             `json:"text_body"`
	VideoURL        string             `json:"video_url"`
	VideoDurationMs int64              `json:"video_duration_ms"`
	FreePreview     bool               `json:"free_preview"`
	ContentRevision int64              `json:"content_revision"`
	Blocks          []contentBlockWire `json:"blocks"`
}

func toContentBlockWire(b contentblocks.Block) contentBlockWire // ported loop body
func toLectureContentWire(v app.LectureContentView) lectureContentWire
```

`toLectureContentWire` fills `TextBody` from the first text block's body and `VideoURL`/`VideoDurationMs` from the first video block, then maps every block. Remove the `quiz` inline-document field (`quiz`, `InlineQuizView`) from the ported block view; there is no versioning. IDs in block views (`id`, `media_asset_id`, `quiz_id`) are strings: use `id.ID.String()` and omit when zero. Keep Hitox's `json` tags exactly; the frontend reads them.

`contentBlockRequest` must not have a `position` field (the frontend comment in `courses.ts` relies on unknown-field rejection).

Also add `lectureContentConflictExt(v app.LectureContentView) map[string]any` that marshals `toLectureContentWire(v)` to JSON and back into a `map[string]any`, for `problem.WriteWithExtensions`.

- [ ] **Step 2: Write handler tests**

`httpapi_test.go` builds a real `httpserver.Router` with the real services over the Task 7 in-memory store pattern. Because `fakes_test.go` lives in package `app_test` and is not importable, copy `memStore`/`memTx`/`enrolled` into this test file (package `httpapi_test`) unchanged. Auth is faked by a middleware that reads `X-Test-User` and `X-Test-Role`:

```go
func fakeAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uid, err := id.Parse(r.Header.Get("X-Test-User"))
		if err != nil {
			problem.Write(w, r, http.StatusUnauthorized, "invalid_token", "Invalid Token", "")
			return
		}
		next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{UserID: uid, Role: auth.Role(r.Header.Get("X-Test-Role"))})))
	})
}

func newServer(t *testing.T, perMinute int) http.Handler {
	t.Helper()
	store := newMemStore()
	ids, _ := id.NewGenerator(0)
	clk := fixedClock{time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)}
	r, h := httpserver.NewRouter(httpserver.Options{Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)), AllowedOrigins: []string{"https://app.test"}, ServiceName: "test"})
	httpapi.New(app.NewCourseService(store, ids, clk), app.NewContentService(store, ids, enrolled{}), httpapi.Config{
		RequireAuth: fakeAuth, ContentLimiter: httpserver.NewRateLimiter(perMinute), Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)),
	}).Register(r)
	return h
}

func call(h http.Handler, method, path, user, role, body string) (*http.Response, map[string]any) {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	if user != "" {
		r.Header.Set("X-Test-User", user)
		r.Header.Set("X-Test-Role", role)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	resp := w.Result()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp, out
}
```

Tests to write (each a separate `Test` function, each asserting status and the listed body fields):

1. `TestCreateAndGetCourseWireShape`: `POST /v1/courses` as `100/instructor` with `{"title":"Go","description":"d"}` returns `201`; body has string `id`, `owner_id == "100"`, `status == "draft"`, `price == {"amount_minor":0,"currency":""}`, `is_free == true`, `sections == []`, `lectures == []`, `thumbnail_url == ""`, `level == ""`, `created_at`. `GET /v1/courses/{id}?view=draft` returns the same `id`.
2. `TestUnknownFieldRejected`: `POST /v1/courses` with `{"title":"Go","description":"","position":1}` returns `400` and `type == "invalid_request"`.
3. `TestPathIDParseFailureIs404`: `GET /v1/courses/abc` and `GET /v1/courses/0` return `404`, `type == "not_found"`.
4. `TestDraftHiddenFromOthers`: create as `100`; `GET` as `200/student` returns `404`.
5. `TestLectureFlowAndContent`: add section (`201`, returns course with one section), add lecture with `text_body` (`201`, lecture `has_text == true`), move lecture into section (`204`), `PUT .../free-preview` is `POST` with `{"free_preview":true}` (`204`), `GET .../content` as owner returns `200` with `content_revision == 1`, `blocks[0].type == "text"`, `text_body` non-empty.
6. `TestPatchConflictBody`: after step 5, `PATCH .../content` with `base_revision: 0` returns `409`, `Content-Type: application/problem+json`, `type == "revision_conflict"`, and body also has `lecture_id`, `content_revision == 1`, `blocks` (length 1).
7. `TestPatchSuccess`: `PATCH` with the right `base_revision`, `order` including the existing `client_block_id` and a new one, and one upsert, returns `200 {"content_revision":2}`.
8. `TestContentRateLimit`: server with `perMinute = 2`; three `PATCH` calls as the same user on the same lecture; the third is `429`, `type == "rate_limited"`, `Retry-After == "60"`.
9. `TestErrorCodes`: table of request → `(status, type)`: publish empty course → `(400, "empty_course")`; duplicate section title → `(400, "duplicate_title")`; price `{"amount_minor":100,"currency":"USD"}` → `(400, "unsupported_currency")`; `PATCH` content without `base_revision` → `(400, "lecture_content_revision_required")`; any write after archive → `(409, "course_not_editable")`; archive twice → `(409, "invalid_transition")`; create as student → `(403, "forbidden")`; non-preview lecture content as student on a published course → `(403, "enrollment_required")`; reorder with a missing ID → `(400, "invalid_input")`; text block with `<script>` where sanitization rejects → `(400, "unsafe_content")` if the ported sanitizer rejects, otherwise assert the stored body has no `<script>`.
10. `TestListByOwner`: `GET /v1/users/100/courses` as `100` returns `200 {"courses":[...],"total":1}`; as `101/instructor` returns `403`.

- [ ] **Step 3: Run to verify failure**

Run: `go test ./internal/courseauthoring/adapters/httpapi/`
Expected: FAIL to compile.

- [ ] **Step 4: Implement `httpapi.go`**

```go
// Package httpapi exposes course authoring over HTTP.
package httpapi

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/app"
	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/contentblocks"
	"github.com/santoshkc2200/ioe-backend/internal/platform/httpserver"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
	"github.com/santoshkc2200/ioe-backend/internal/platform/problem"
)

type Config struct {
	RequireAuth    httpserver.Middleware
	ContentLimiter *httpserver.RateLimiter
	Logger         *slog.Logger
}

type Handler struct {
	courses  CourseService
	contents ContentService
	cfg      Config
}

func New(courses CourseService, contents ContentService, cfg Config) *Handler {
	return &Handler{courses: courses, contents: contents, cfg: cfg}
}

// Register mounts the course authoring routes. Every route requires authentication.
func (h *Handler) Register(r *httpserver.Router) {
	a := func(f http.HandlerFunc) http.Handler { return h.cfg.RequireAuth(f) }
	r.Handle("POST /v1/courses", a(h.createCourse))
	r.Handle("GET /v1/users/{ownerID}/courses", a(h.listByOwner))
	r.Handle("GET /v1/courses/{courseID}", a(h.getCourse))
	r.Handle("PATCH /v1/courses/{courseID}", a(h.updateDetails))
	r.Handle("POST /v1/courses/{courseID}/price", a(h.setPrice))
	r.Handle("POST /v1/courses/{courseID}/publish", a(h.publish))
	r.Handle("POST /v1/courses/{courseID}/archive", a(h.archive))
	r.Handle("POST /v1/courses/{courseID}/sections", a(h.addSection))
	r.Handle("PATCH /v1/courses/{courseID}/sections/{sectionID}", a(h.renameSection))
	r.Handle("DELETE /v1/courses/{courseID}/sections/{sectionID}", a(h.removeSection))
	r.Handle("POST /v1/courses/{courseID}/lectures", a(h.addLecture))
	r.Handle("PATCH /v1/courses/{courseID}/lectures/{lectureID}", a(h.renameLecture))
	r.Handle("DELETE /v1/courses/{courseID}/lectures/{lectureID}", a(h.removeLecture))
	r.Handle("PUT /v1/courses/{courseID}/lectures-order", a(h.reorderLectures))
	r.Handle("POST /v1/courses/{courseID}/lectures/{lectureID}/section", a(h.moveLecture))
	r.Handle("POST /v1/courses/{courseID}/lectures/{lectureID}/free-preview", a(h.setFreePreview))
	r.Handle("GET /v1/courses/{courseID}/lectures/{lectureID}/content", a(h.getContent))
	r.Handle("PUT /v1/courses/{courseID}/lectures/{lectureID}/content", a(h.limitContent(h.replaceContent)))
	r.Handle("PATCH /v1/courses/{courseID}/lectures/{lectureID}/content", a(h.limitContent(h.patchContent)))
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

// errorMappings is checked in order; the first errors.Is match wins.
var errorMappings = []errorMapping{
	{app.ErrNotFound, 404, "not_found", "Not Found"},
	{domain.ErrSectionNotFound, 404, "not_found", "Not Found"},
	{domain.ErrLectureNotFound, 404, "not_found", "Not Found"},
	{app.ErrForbidden, 403, "forbidden", "Forbidden"},
	{app.ErrEnrollmentRequired, 403, "enrollment_required", "Enrollment Required"},
	{app.ErrConcurrentModification, 409, "concurrent_modification", "Concurrent Modification"},
	{domain.ErrCourseNotEditable, 409, "course_not_editable", "Course Not Editable"},
	{domain.ErrInvalidStatusTransition, 409, "invalid_transition", "Invalid Transition"},
	{app.ErrRevisionRequired, 400, "lecture_content_revision_required", "Revision Required"},
	{app.ErrPatchTooLarge, 400, "patch_too_large", "Patch Too Large"},
	{app.ErrBlockSetMismatch, 400, "block_set_mismatch", "Block Set Mismatch"},
	{app.ErrOrderDeleteOverlap, 400, "order_delete_overlap", "Order Delete Overlap"},
	{app.ErrDuplicateClientBlockID, 400, "duplicate_client_block_id", "Duplicate Client Block ID"},
	{contentblocks.ErrDuplicateClientBlockID, 400, "duplicate_client_block_id", "Duplicate Client Block ID"},
	{contentblocks.ErrInvalidClientBlockID, 400, "invalid_client_block_id", "Invalid Client Block ID"},
	{contentblocks.ErrUnsafeContent, 400, "unsafe_content", "Unsafe Content"},
	{domain.ErrDuplicateSectionTitle, 400, "duplicate_title", "Duplicate Title"},
	{domain.ErrCourseHasNoLectures, 400, "empty_course", "Empty Course"},
	{domain.ErrUnsupportedCurrency, 400, "unsupported_currency", "Unsupported Currency"},
	{app.ErrInvalidInput, 400, "invalid_input", "Invalid Input"},
	{domain.ErrInvalidPrice, 400, "invalid_input", "Invalid Input"},
	{domain.ErrInvalidLevel, 400, "invalid_input", "Invalid Input"},
	{domain.ErrInvalidThumbnailURL, 400, "invalid_input", "Invalid Input"},
	{domain.ErrInvalidLectureOrder, 400, "invalid_input", "Invalid Input"},
}

func (h *Handler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	var conflict *app.RevisionConflictError
	if errors.As(err, &conflict) {
		problem.WriteWithExtensions(w, r, http.StatusConflict, "revision_conflict", "Revision Conflict", "",
			lectureContentConflictExt(conflict.Current))
		return
	}
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
	h.cfg.Logger.ErrorContext(r.Context(), "courseauthoring request failed", "error", err)
	problem.Write(w, r, http.StatusInternalServerError, problem.TypeInternal, "Internal Server Error", "")
}
```

Specific sentinels come before `app.ErrInvalidInput` because block-building wraps both (`fmt.Errorf("%w: %w", ErrInvalidInput, err)`) and the specific code must win. 400 details carry `err.Error()`, which contains only IDs and rule text, never block bodies.

- [ ] **Step 5: Implement `courses.go` and `content.go`**

Each handler: `pathIDs`, decode body with `httpserver.DecodeJSON` when there is one, call the service with `principal(r)`, then `writeError` or the success response from the spec's route table. Examples to follow for every route:

```go
func (h *Handler) createCourse(w http.ResponseWriter, r *http.Request) {
	var req createCourseRequest
	if !httpserver.DecodeJSON(w, r, &req) {
		return
	}
	in := app.CreateCourseInput{Title: req.Title, Description: req.Description}
	if req.OwnerID != nil {
		in.OwnerID = *req.OwnerID
	}
	c, err := h.courses.Create(r.Context(), principal(r), in)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, toCourseWire(c))
}

func (h *Handler) publish(w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "courseID")
	if !ok {
		return
	}
	if err := h.courses.Publish(r.Context(), principal(r), ids[0]); err != nil {
		h.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) moveLecture(w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "courseID", "lectureID")
	if !ok {
		return
	}
	var req moveLectureRequest
	if !httpserver.DecodeJSON(w, r, &req) {
		return
	}
	var sectionID id.ID
	if req.SectionID != "" {
		v, err := id.Parse(req.SectionID)
		if err != nil {
			h.writeError(w, r, domain.ErrSectionNotFound)
			return
		}
		sectionID = v
	}
	if err := h.courses.MoveLectureToSection(r.Context(), principal(r), ids[0], ids[1], sectionID); err != nil {
		h.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
```

`listByOwner` returns `coursePageWire{Courses: ..., Total: len(courses)}`. `addSection`, `addLecture` return `201` with the course; `setPrice` returns `200` with the course. A `null` `lecture_ids` in `reorderLecturesRequest` is passed as an empty slice and fails with `invalid_input` from the domain.

`content.go`:

```go
// limitContent rate-limits content writes per (user, lecture).
func (h *Handler) limitContent(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := principal(r).UserID.String() + ":" + r.PathValue("lectureID")
		if !h.cfg.ContentLimiter.Allow(key) {
			w.Header().Set("Retry-After", "60")
			problem.Write(w, r, http.StatusTooManyRequests, problem.TypeRateLimited, "Too Many Requests", "")
			return
		}
		next(w, r)
	}
}

func (h *Handler) getContent(w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "courseID", "lectureID")
	if !ok {
		return
	}
	v, err := h.contents.Get(r.Context(), principal(r), ids[0], ids[1])
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toLectureContentWire(v))
}

func (h *Handler) replaceContent(w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "courseID", "lectureID")
	if !ok {
		return
	}
	var req replaceContentRequest
	if !httpserver.DecodeJSON(w, r, &req) {
		return
	}
	var blocks []app.BlockInput
	if req.Blocks != nil {
		blocks = toContentBlockInputs(req.Blocks)
	}
	err := h.contents.Replace(r.Context(), principal(r), ids[0], ids[1], blocks,
		app.LegacyContent{TextBody: req.TextBody, VideoURL: req.VideoURL, VideoDurationMs: req.VideoDurationMs})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) patchContent(w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "courseID", "lectureID")
	if !ok {
		return
	}
	var req patchContentRequest
	if !httpserver.DecodeJSON(w, r, &req) {
		return
	}
	rev, err := h.contents.Patch(r.Context(), principal(r), ids[0], ids[1], app.PatchInput{
		BaseRevision: req.BaseRevision, Order: req.Order, Upserts: toContentBlockInputs(req.Upserts), Deletes: req.Deletes,
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]int64{"content_revision": rev})
}
```

`toContentBlockInputs` must return a non-nil empty slice for `[]` and nil for an absent field; in `replaceContent`, an explicit `"blocks": []` clears the lecture, while an absent `blocks` uses the legacy fields.

The `?view=` query parameter is ignored by every handler; nothing reads it.

- [ ] **Step 6: Run tests**

Run: `go test -race ./internal/courseauthoring/adapters/httpapi/ && make lint`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/courseauthoring/adapters/httpapi
git commit -m "feat(courseauthoring): expose course authoring over HTTP"
```

---

### Task 10: Composition and end-to-end test

**Files:**
- Create: `internal/courseauthoring/adapters/enrollment/deny.go`
- Modify: `cmd/api/app.go`
- Modify: `cmd/api/e2e_integration_test.go`

**Interfaces:**
- Consumes: everything above; identity `*httpapi.Handler.RequireAuth`.
- Produces: `registerCourseAuthoring(r *httpserver.Router, pool *pgxpool.Pool, ids *id.Generator, clk clock.Clock, requireAuth httpserver.Middleware, logger *slog.Logger)`.

- [ ] **Step 1: Deny-all enrollment adapter**

```go
// Package enrollment adapts enrollment facts for course authoring. Until the
// enrollment context exists, nobody is enrolled.
package enrollment

import (
	"context"

	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

// Deny reports every user as not enrolled.
type Deny struct{}

func (Deny) IsActivelyEnrolled(context.Context, id.ID, id.ID) (bool, error) { return false, nil }
```

- [ ] **Step 2: Extend the e2e test (failing)**

In `TestEndToEnd`, set `cfg.BootstrapRootAdminEmails = []string{"admin@example.com"}` and, after the existing identity assertions, append:

```go
	adminToken := google.Sign(t, googletest.Claims("sub-admin", "admin@example.com", "web-client", time.Now()))
	resp, body = c.do(http.MethodPost, "/v1/auth/google", `{"id_token":"`+adminToken+`"}`, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("admin sign in %d %v", resp.StatusCode, body)
	}
	adminAuth := map[string]string{"Authorization": "Bearer " + body["access_token"].(string)}
	studentAuth := map[string]string{"Authorization": "Bearer " + access}

	resp, body = c.do(http.MethodPost, "/v1/courses", `{"title":"Go","description":"d"}`, adminAuth)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create course %d %v", resp.StatusCode, body)
	}
	courseID := body["id"].(string)
	resp, body = c.do(http.MethodPost, "/v1/courses/"+courseID+"/lectures", `{"title":"Free","text_body":"<p>free</p>"}`, adminAuth)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("add lecture %d %v", resp.StatusCode, body)
	}
	resp, body = c.do(http.MethodPost, "/v1/courses/"+courseID+"/lectures", `{"title":"Paid","text_body":"<p>paid</p>"}`, adminAuth)
	lectures := body["lectures"].([]any)
	freeID := lectures[0].(map[string]any)["id"].(string)
	paidID := lectures[1].(map[string]any)["id"].(string)
	resp, _ = c.do(http.MethodPost, "/v1/courses/"+courseID+"/lectures/"+freeID+"/free-preview", `{"free_preview":true}`, adminAuth)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("free preview %d", resp.StatusCode)
	}

	resp, body = c.do(http.MethodGet, "/v1/courses/"+courseID+"/lectures/"+paidID+"/content", "", adminAuth)
	rev := body["content_revision"].(float64)
	first := body["blocks"].([]any)[0].(map[string]any)["client_block_id"].(string)
	patch := fmt.Sprintf(`{"base_revision":%d,"order":["%s","n1"],"upserts":[{"client_block_id":"n1","type":"text","body":"<p>more</p>"}],"deletes":[]}`, int64(rev), first)
	resp, body = c.do(http.MethodPatch, "/v1/courses/"+courseID+"/lectures/"+paidID+"/content", patch, adminAuth)
	if resp.StatusCode != http.StatusOK || body["content_revision"].(float64) != rev+1 {
		t.Fatalf("patch %d %v", resp.StatusCode, body)
	}

	resp, _ = c.do(http.MethodGet, "/v1/courses/"+courseID, "", studentAuth)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("student sees draft: %d", resp.StatusCode)
	}
	resp, _ = c.do(http.MethodPost, "/v1/courses/"+courseID+"/publish", "", adminAuth)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("publish %d", resp.StatusCode)
	}
	resp, _ = c.do(http.MethodGet, "/v1/courses/"+courseID, "", studentAuth)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("student outline %d", resp.StatusCode)
	}
	resp, _ = c.do(http.MethodGet, "/v1/courses/"+courseID+"/lectures/"+freeID+"/content", "", studentAuth)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("free preview read %d", resp.StatusCode)
	}
	resp, body = c.do(http.MethodGet, "/v1/courses/"+courseID+"/lectures/"+paidID+"/content", "", studentAuth)
	if resp.StatusCode != http.StatusForbidden || body["type"] != "enrollment_required" {
		t.Fatalf("paid read %d %v", resp.StatusCode, body)
	}
```

Add `"fmt"` to the test file's imports. The existing assertion `events != 1` counts outbox messages; it now runs before the course flow, so leave it where it is. After the course flow add:

```go
	var published int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM platform.outbox_messages WHERE metadata->>'event_name' = 'courseauthoring.course.published'").Scan(&published); err != nil {
		t.Fatal(err)
	}
	if published != 1 {
		t.Fatalf("published events = %d, want 1", published)
	}
```

If the outbox table stores the event name in a different column than `metadata`, check `platform.outbox_messages`' columns in `migrations/00001_platform_outbox.sql` and adjust the `WHERE`.

The admin user signs up as a second registration, so the welcome-email assertions now see two emails; change the "unexpected second email" check to drain one more email for `admin@example.com` before asserting no third.

- [ ] **Step 3: Wire composition**

In `cmd/api/app.go`:

```go
	identityHandler, err := registerIdentity(router, identity, tokens, cfg, logger)
	if err != nil {
		return nil, err
	}
	registerCourseAuthoring(router, pool, ids, clk, identityHandler.RequireAuth, logger)
```

Change `registerIdentity` to return `(*httpapi.Handler, error)`. Add:

```go
func registerCourseAuthoring(r *httpserver.Router, pool *pgxpool.Pool, ids *id.Generator, clk clock.Clock, requireAuth httpserver.Middleware, logger *slog.Logger) {
	tx := courseauthoringpg.NewTxRunner(pool, clk)
	courseauthoringhttp.New(
		courseauthoringapp.NewCourseService(tx, ids, clk),
		courseauthoringapp.NewContentService(tx, ids, courseauthoringenrollment.Deny{}),
		courseauthoringhttp.Config{RequireAuth: requireAuth, ContentLimiter: httpserver.NewRateLimiter(60), Logger: logger},
	).Register(r)
}
```

Import aliases: `courseauthoringpg`, `courseauthoringapp`, `courseauthoringhttp`, `courseauthoringenrollment`.

- [ ] **Step 4: Run**

Run: `make test && make lint && make test-integration`
Expected: PASS, including `TestEndToEnd`.

- [ ] **Step 5: Commit**

```bash
git add internal/courseauthoring/adapters/enrollment cmd/api
git commit -m "feat(api): wire course authoring"
```

---

### Task 11: OpenAPI and gates

**Files:**
- Modify: `api/openapi.yaml`
- Modify: `AGENTS.md` (list `courseauthoring` among contexts if the file enumerates them; it currently does not, so only verify)

- [ ] **Step 1: Document the API**

Add every route in the spec's HTTP API table to `api/openapi.yaml` with `security: [{bearer: []}]`, request schemas from Task 9's request types, and responses: success code with `Course`, `CoursePage`, `LectureContent`, `ContentRevision` schemas; `400`, `401`, `403`, `404`, `409`, `413`, `415`, `429` as `#/components/responses/Problem`; `405` as `MethodNotAllowed`; `500` as `InternalError`. Add schemas `Price`, `Section`, `Lecture`, `Course`, `CoursePage`, `ContentBlock` (all Hitox block fields, `type` enum `text|video|quiz|image|flashcard`), `FlashcardCard`, `LectureContent`, and `LectureContentConflict` (allOf `Problem` + `LectureContent`) used for the `PATCH .../content` 409. All ID fields are `type: string, pattern: '^[1-9][0-9]{0,18}$'`.

Validate: `npx --yes @redocly/cli@latest lint api/openapi.yaml` if Node is available; otherwise confirm the file parses with `go run github.com/getkin/kin-openapi/cmd/validate@latest api/openapi.yaml` or note that no validator was run.

- [ ] **Step 2: Run every gate**

Run, in order, and report each result: `make check`, `make test-integration`, `docker compose config`, `make docker-build`, `git diff --check`.
Expected: all pass. Integration tests must actually run.

- [ ] **Step 3: Commit**

```bash
git add api/openapi.yaml
git commit -m "docs(api): document course authoring endpoints"
```

- [ ] **Step 4: Report the frontend follow-up**

Tell the user: `ioe-frontend/packages/course-core/src/api/http.ts` must read `problem.type ?? problem.code` (spec, "Frontend follow-up"). Do not edit the frontend repository unless asked.
