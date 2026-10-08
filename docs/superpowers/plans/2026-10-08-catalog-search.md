# Catalog Search, Categories and Tags Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let anyone search live courses by text and filter them by category and tag, let root admins manage a category list, and let instructors classify their courses through the existing review flow.

**Architecture:** Everything stays in `internal/courseauthoring`. Categories are a new entity with their own repository port; a course's categories and tags live on the working copy and are snapshotted into each published version, so they change publicly only through publication. Search is PostgreSQL full text over a stored generated `tsvector` on `course_versions`, built by one immutable SQL function so the language configuration lives in one place. The tag rule and slug derivation move out of blog into `internal/platform/{tags,slug}` so both contexts share them.

**Tech Stack:** Go 1.27, pgx/v5, sqlc, goose, `net/http` ServeMux via `platform/httpserver`, testcontainers (`platform/postgres/pgtest`).

**Spec:** `docs/superpowers/specs/2026-10-08-catalog-search-design.md`

## Global Constraints

- Module path `github.com/santoshkc2200/ioe-backend`. No context imports another context; only `cmd/api` wires.
- `courseauthoring/domain` imports only stdlib and `platform/{auth,contentblocks,id,slug,tags}`; `courseauthoring/app` imports only its domain and `platform/{auth,clock,contentblocks,id,tags}`. `blog/domain` gains `platform/{slug,tags}`.
- Categories: name trimmed, 1–60 runes; slug `slug.From(name, 64)`, falling back to `category-<id>` when shorter than 2 bytes; name unique ignoring case; slug unique. Writes are root-admin only.
- A course has at most 3 categories (duplicates dropped, order kept) and at most 10 tags.
- Tags: trimmed, lowercased, 1–32 bytes matching `^[a-z0-9]+(-[a-z0-9]+)*$`, duplicates dropped keeping first occurrence.
- Search: `simple` configuration; weights title `A`, tags `B`, description `C`; `q` trimmed, at most 200 runes; ordered `ts_rank DESC, id DESC`; without `q`, `id DESC` as today.
- Cursor without `q` stays the plain course ID. With `q` it is base64url (no padding) of `<id>:<rank>:<hash>`, where `hash` is the hex of the first 8 bytes of SHA-256 of the trimmed `q`. Bad cursors are `400 invalid_input`.
- Every validation error is `400` (the repository has no `422`). New problem types: `category_exists` (409), `unknown_category`, `too_many_categories`, `invalid_tag`, `too_many_tags`, `invalid_category_name` (400).
- Public routes (`GET /v1/courses`, `GET /v1/categories`) use optional auth and the catalog rate limiter (120/min per client IP).
- Conventional Commits. Gates from `AGENTS.md`: `make check`, `make test-integration` (needs Docker; never report it passing unless it ran), `docker compose config`, `make docker-build`, `git diff --check`.

## Review Focus

1. A visitor types `go!`, `"unterminated` or `c++` into search: the query must never reach `to_tsquery` with syntax characters, so it returns results or an empty page, never a 500. Pinned by `TestSearchTerms` (Task 5) and `TestCatalogSearchOddInput` (Task 5).
2. A client reuses a `next_cursor` from one search with a different `q`, or with no `q`: `400 invalid_input`, never a silently wrong page. Pinned by `TestCatalogSearchCursorBoundToQuery` (Task 6).
3. A root admin deletes a category a live course uses: the course stays listed, loses the category in list and detail, and the category's count is gone. Pinned by `TestDeleteCategoryCascadesIntoVersions` (Task 4).
4. An instructor's editor sends `"tags": null` or omits it versus `"tags": []`: null and absent keep tags, `[]` clears them. Pinned by `TestUpdateDetailsClassification` (Task 3) and `TestUpdateDetailsTagsNullVersusEmpty` (Task 6).
5. Many live courses with the same rank (identical titles): keyset paging by `(rank, id)` returns each exactly once. Pinned by `TestCatalogSearchKeysetWalk` (Task 5).

---

## File Structure

```text
internal/platform/tags/tags.go                         tag normalization (new)
internal/platform/tags/tags_test.go
internal/platform/slug/slug.go                         slug derivation (new)
internal/platform/slug/slug_test.go
internal/blog/domain/tag.go                            delegates to platform/tags (modify)
internal/blog/domain/slug.go                           SlugFromTitle uses platform/slug (modify)
.golangci.yml                                          depguard allow lists (modify)

internal/courseauthoring/domain/errors.go              new sentinels (modify)
internal/courseauthoring/domain/category.go            Category, CategoryRef (new)
internal/courseauthoring/domain/category_test.go
internal/courseauthoring/domain/course.go              Categories, Tags, SetClassification (modify)
internal/courseauthoring/domain/classification_test.go

internal/courseauthoring/app/errors.go                 ErrCategoryExists, ErrUnknownCategory (modify)
internal/courseauthoring/app/ports.go                  CategoryRepository, Repos.Categories (modify)
internal/courseauthoring/app/category_service.go       CategoryService (new)
internal/courseauthoring/app/course_service.go         DetailsInput, UpdateDetails (modify)
internal/courseauthoring/app/catalog.go                query fields, rank, normalization (modify)
internal/courseauthoring/app/fakes_test.go             categories fake, hydration, filters (modify)
internal/courseauthoring/app/category_service_test.go
internal/courseauthoring/app/classification_test.go

migrations/00014_courseauthoring_catalog_search.sql
internal/courseauthoring/adapters/postgres/queries.sql (modify)
internal/courseauthoring/adapters/postgres/sqlcgen/    generated
internal/courseauthoring/adapters/postgres/postgres.go Repos.Categories, pg error codes (modify)
internal/courseauthoring/adapters/postgres/categories.go (new)
internal/courseauthoring/adapters/postgres/courses.go  tags, categories, search (modify)
internal/courseauthoring/adapters/postgres/search.go   searchTerms (new)
internal/courseauthoring/adapters/postgres/search_test.go
internal/courseauthoring/adapters/postgres/classification_integration_test.go
internal/courseauthoring/adapters/postgres/search_integration_test.go

internal/courseauthoring/adapters/httpapi/httpapi.go   CategoryService, routes, errors (modify)
internal/courseauthoring/adapters/httpapi/categories.go (new)
internal/courseauthoring/adapters/httpapi/cursor.go    search cursor (new)
internal/courseauthoring/adapters/httpapi/catalog.go   q, category, tag, cursor (modify)
internal/courseauthoring/adapters/httpapi/courses.go   details request (modify)
internal/courseauthoring/adapters/httpapi/wire.go      categories and tags on wires (modify)
internal/courseauthoring/adapters/httpapi/fakes_test.go (modify)
internal/courseauthoring/adapters/httpapi/httpapi_test.go newServer (modify)
internal/courseauthoring/adapters/httpapi/categories_test.go

cmd/api/app.go                                          wire CategoryService (modify)
cmd/api/e2e_integration_test.go                         TestCatalogSearchEndToEnd (modify)
api/openapi.yaml                                        contract (modify)
```

---

### Task 1: Shared tag and slug packages

**Files:**
- Create: `internal/platform/tags/tags.go`, `internal/platform/tags/tags_test.go`
- Create: `internal/platform/slug/slug.go`, `internal/platform/slug/slug_test.go`
- Modify: `internal/blog/domain/tag.go`, `internal/blog/domain/slug.go`
- Modify: `.golangci.yml` (`blog-domain`, `courseauthoring-domain`, `courseauthoring-app`)

**Interfaces:**
- Produces: `tags.New(raw string) (string, error)`, `tags.NewList(raw []string) ([]string, error)`, `tags.Max = 10`, `tags.ErrInvalid`, `tags.ErrTooMany`; `slug.From(text string, maxLen int) string`.

- [ ] **Step 1: Write the failing tests**

`internal/platform/tags/tags_test.go`:

```go
package tags_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/santoshkc2200/ioe-backend/internal/platform/tags"
)

func TestNew(t *testing.T) {
	got, err := tags.New("  Web-Dev ")
	if err != nil || got != "web-dev" {
		t.Fatalf("New = %q, %v", got, err)
	}
	for _, bad := range []string{"", "  ", "c++", "a--b", "-a", "a-", "has space", "ünï", strings.Repeat("a", 33)} {
		if _, err := tags.New(bad); !errors.Is(err, tags.ErrInvalid) {
			t.Errorf("New(%q) = %v, want ErrInvalid", bad, err)
		}
	}
}

func TestNewList(t *testing.T) {
	got, err := tags.NewList([]string{"Go", "web", "go", " WEB "})
	if err != nil || !slices.Equal(got, []string{"go", "web"}) {
		t.Fatalf("NewList = %v, %v", got, err)
	}
	if got, err := tags.NewList(nil); err != nil || got == nil || len(got) != 0 {
		t.Fatalf("NewList(nil) = %#v, %v", got, err)
	}
	eleven := []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k"}
	if _, err := tags.NewList(eleven); !errors.Is(err, tags.ErrTooMany) {
		t.Fatalf("eleven = %v", err)
	}
	// Duplicates do not count toward the limit.
	if got, err := tags.NewList(append(eleven[:10:10], "a")); err != nil || len(got) != 10 {
		t.Fatalf("ten plus duplicate = %v, %v", got, err)
	}
	if _, err := tags.NewList([]string{"ok", "c++"}); !errors.Is(err, tags.ErrInvalid) {
		t.Fatalf("invalid = %v", err)
	}
}
```

`internal/platform/slug/slug_test.go`:

```go
package slug_test

import (
	"strings"
	"testing"

	"github.com/santoshkc2200/ioe-backend/internal/platform/slug"
)

func TestFrom(t *testing.T) {
	cases := map[string]string{
		"  Hello, World!  ": "hello-world",
		"a---b   c":         "a-b-c",
		"Go 1.27 released":  "go-127-released",
		"日本語のタイトル":          "",
		"C++":               "c",
		"Web Development":   "web-development",
	}
	for in, want := range cases {
		if got := slug.From(in, 96); got != want {
			t.Errorf("From(%q) = %q, want %q", in, got, want)
		}
	}
	if got := slug.From(strings.Repeat("ab ", 60), 96); got != strings.TrimSuffix(strings.Repeat("ab-", 32), "-") {
		t.Errorf("truncated = %q", got)
	}
	if got := slug.From("abc def", 4); got != "abc" {
		t.Errorf("no trailing hyphen = %q", got)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/platform/tags/ ./internal/platform/slug/`
Expected: FAIL — packages do not exist.

- [ ] **Step 3: Implement the packages**

`internal/platform/tags/tags.go`:

```go
// Package tags normalizes the free-form labels that contexts attach to content.
package tags

import (
	"errors"
	"regexp"
	"strings"
)

// Max is the most tags one item carries.
const Max = 10

const maxLen = 32

var (
	ErrInvalid = errors.New("tags must be 1-32 lowercase letters, digits and single hyphens")
	ErrTooMany = errors.New("at most 10 tags")
)

var kebab = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// New trims and lowercases raw and validates it.
func New(raw string) (string, error) {
	v := strings.ToLower(strings.TrimSpace(raw))
	if len(v) > maxLen || !kebab.MatchString(v) {
		return "", ErrInvalid
	}
	return v, nil
}

// NewList normalizes each tag and drops duplicates, keeping first-occurrence order. It never
// returns a nil slice on success.
func NewList(raw []string) ([]string, error) {
	out := make([]string, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, r := range raw {
		t, err := New(r)
		if err != nil {
			return nil, err
		}
		if _, dup := seen[t]; dup {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	if len(out) > Max {
		return nil, ErrTooMany
	}
	return out, nil
}
```

`internal/platform/slug/slug.go`:

```go
// Package slug derives URL path segments from human text.
package slug

import (
	"regexp"
	"strings"
)

var nonSlugRun = regexp.MustCompile(`[^a-z0-9]+`)

// From lowercases text, drops dots ("1.27" becomes "127" rather than "1-27"), joins the
// remaining runs of [a-z0-9] with single hyphens and truncates to maxLen bytes without a
// trailing hyphen. No transliteration is attempted, so text with few ASCII letters or digits
// yields an empty or short result; callers choose a fallback.
func From(text string, maxLen int) string {
	v := strings.ToLower(strings.ReplaceAll(text, ".", ""))
	v = strings.Trim(nonSlugRun.ReplaceAllString(v, "-"), "-")
	if len(v) > maxLen {
		v = strings.TrimRight(v[:maxLen], "-")
	}
	return v
}
```

- [ ] **Step 4: Make blog delegate**

Replace `internal/blog/domain/tag.go` with:

```go
package domain

import (
	"errors"

	"github.com/santoshkc2200/ioe-backend/internal/platform/tags"
)

// MaxTags is the most tags a post carries.
const MaxTags = tags.Max

// NewTag trims and lowercases raw and validates it.
func NewTag(raw string) (string, error) {
	t, err := tags.New(raw)
	if err != nil {
		return "", ErrInvalidTag
	}
	return t, nil
}

// NewTags normalizes each tag and drops duplicates, keeping first occurrence order.
func NewTags(raw []string) ([]string, error) {
	out, err := tags.NewList(raw)
	switch {
	case errors.Is(err, tags.ErrTooMany):
		return nil, ErrTooManyTags
	case err != nil:
		return nil, ErrInvalidTag
	}
	return out, nil
}
```

In `internal/blog/domain/slug.go`: delete `nonSlugRun` and `removeDots`, keep `kebab` (its comment becomes `// kebab is the slug shape.`), add the import `"github.com/santoshkc2200/ioe-backend/internal/platform/slug"`, and replace `SlugFromTitle`'s body with:

```go
func SlugFromTitle(title string, postID id.ID) Slug {
	v := slug.From(title, maxSlugLen)
	if len(v) < 3 {
		return Slug{value: "post-" + postID.String()}
	}
	return Slug{value: v}
}
```

- [ ] **Step 5: Allow the packages in depguard**

In `.golangci.yml`, add these lines to the `allow:` list of `blog-domain` and of `courseauthoring-domain` (after `.../platform/id`):

```yaml
            - github.com/santoshkc2200/ioe-backend/internal/platform/slug
            - github.com/santoshkc2200/ioe-backend/internal/platform/tags
```

and add this line to the `allow:` list of `courseauthoring-app` (after `.../platform/id`):

```yaml
            - github.com/santoshkc2200/ioe-backend/internal/platform/tags
```

- [ ] **Step 6: Run the tests**

Run: `go test ./internal/platform/tags/ ./internal/platform/slug/ ./internal/blog/... && golangci-lint run ./internal/platform/... ./internal/blog/...`
Expected: PASS, no lint findings. Blog's existing `TestSlugFromTitle` and `details_test.go` pass unchanged.

- [ ] **Step 7: Commit**

```bash
git add internal/platform/tags internal/platform/slug internal/blog/domain/tag.go internal/blog/domain/slug.go .golangci.yml
git commit -m "refactor(platform): share tag and slug rules between contexts"
```

---

### Task 2: Category entity and course classification

**Files:**
- Modify: `internal/courseauthoring/domain/errors.go`
- Create: `internal/courseauthoring/domain/category.go`, `internal/courseauthoring/domain/category_test.go`
- Modify: `internal/courseauthoring/domain/course.go`
- Create: `internal/courseauthoring/domain/classification_test.go`

**Interfaces:**
- Consumes: `tags.NewList`, `tags.ErrTooMany`, `slug.From` (Task 1).
- Produces:
  - `domain.Category{ID id.ID; Name, Slug string; CreatedAt time.Time}`, `domain.NewCategory(categoryID id.ID, name string, now time.Time) (Category, error)`, `(*Category).Rename(name string) error`, `domain.MaxCategoryNameRunes = 60`.
  - `domain.CategoryRef{ID id.ID; Name, Slug string}`.
  - `Course.Categories []CategoryRef`, `Course.Tags []string`, `(*Course).SetClassification(categoryIDs []id.ID, tagList []string, now time.Time) error`, `(*Course).CategoryIDs() []id.ID`, `domain.MaxCourseCategories = 3`.
  - Errors `ErrInvalidCategoryName`, `ErrTooManyCategories`, `ErrInvalidTag`, `ErrTooManyTags`.

- [ ] **Step 1: Write the failing tests**

`internal/courseauthoring/domain/category_test.go`:

```go
package domain_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/domain"
)

func TestNewCategory(t *testing.T) {
	now := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	c, err := domain.NewCategory(42, "  Web Development ", now)
	if err != nil || c.ID != 42 || c.Name != "Web Development" || c.Slug != "web-development" || !c.CreatedAt.Equal(now) {
		t.Fatalf("NewCategory = %+v, %v", c, err)
	}
	for name, want := range map[string]string{"C++": "category-42", "日本語": "category-42", "Go": "go"} {
		c, err := domain.NewCategory(42, name, now)
		if err != nil || c.Slug != want {
			t.Errorf("%q: slug = %q, %v, want %q", name, c.Slug, err, want)
		}
	}
	for _, bad := range []string{"", "   ", strings.Repeat("x", domain.MaxCategoryNameRunes+1)} {
		if _, err := domain.NewCategory(42, bad, now); !errors.Is(err, domain.ErrInvalidCategoryName) {
			t.Errorf("%q: err = %v", bad, err)
		}
	}
	if _, err := domain.NewCategory(42, strings.Repeat("é", domain.MaxCategoryNameRunes), now); err != nil {
		t.Errorf("60 runes: %v", err)
	}
}

func TestCategoryRename(t *testing.T) {
	c, _ := domain.NewCategory(7, "Web", time.Now())
	if err := c.Rename("Data Science"); err != nil || c.Name != "Data Science" || c.Slug != "data-science" {
		t.Fatalf("Rename = %+v, %v", c, err)
	}
	if err := c.Rename(" "); !errors.Is(err, domain.ErrInvalidCategoryName) || c.Name != "Data Science" {
		t.Fatalf("bad rename = %+v, %v", c, err)
	}
}
```

`internal/courseauthoring/domain/classification_test.go`:

```go
package domain_test

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/contentblocks"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

var classifyNow = time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)

func classifiedCourse(t *testing.T) domain.Course {
	t.Helper()
	ttl, _ := contentblocks.NewTitle("Go")
	c := domain.NewCourse(1, 100, ttl, "d", classifyNow)
	lt, _ := contentblocks.NewTitle("L1")
	if err := c.AddLecture(2, lt, false, false, classifyNow); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestSetClassification(t *testing.T) {
	c := classifiedCourse(t)
	if err := c.SetClassification([]id.ID{9, 8, 9}, []string{"Go", "web", "GO"}, classifyNow); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(c.CategoryIDs(), []id.ID{9, 8}) || !slices.Equal(c.Tags, []string{"go", "web"}) {
		t.Fatalf("categories = %v, tags = %v", c.CategoryIDs(), c.Tags)
	}
	if err := c.SetClassification([]id.ID{1, 2, 3, 4}, nil, classifyNow); !errors.Is(err, domain.ErrTooManyCategories) {
		t.Fatalf("four categories = %v", err)
	}
	if err := c.SetClassification(nil, []string{"c++"}, classifyNow); !errors.Is(err, domain.ErrInvalidTag) {
		t.Fatalf("bad tag = %v", err)
	}
	eleven := []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k"}
	if err := c.SetClassification(nil, eleven, classifyNow); !errors.Is(err, domain.ErrTooManyTags) {
		t.Fatalf("eleven tags = %v", err)
	}
	if err := c.SetClassification(nil, nil, classifyNow); err != nil || len(c.Categories) != 0 || c.Tags == nil || len(c.Tags) != 0 {
		t.Fatalf("clear = %v, %+v %#v", err, c.Categories, c.Tags)
	}
}

func TestSetClassificationFollowsEditRules(t *testing.T) {
	c := classifiedCourse(t)
	if err := c.Publish(true, classifyNow); err != nil {
		t.Fatal(err)
	}
	if err := c.SetClassification([]id.ID{9}, nil, classifyNow); err != nil || c.Status != domain.StatusDraft {
		t.Fatalf("published edit = %v, status %s", err, c.Status)
	}
	if err := c.Archive(classifyNow); err != nil {
		t.Fatal(err)
	}
	if err := c.SetClassification(nil, nil, classifyNow); !errors.Is(err, domain.ErrCourseNotEditable) {
		t.Fatalf("archived edit = %v", err)
	}
}

func TestDiscardDraftRestoresClassification(t *testing.T) {
	c := classifiedCourse(t)
	if err := c.SetClassification([]id.ID{9}, []string{"go"}, classifyNow); err != nil {
		t.Fatal(err)
	}
	if err := c.Publish(true, classifyNow); err != nil {
		t.Fatal(err)
	}
	live := c
	if err := c.SetClassification([]id.ID{8}, []string{"rust"}, classifyNow); err != nil {
		t.Fatal(err)
	}
	if err := c.DiscardDraft(live, classifyNow); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(c.CategoryIDs(), []id.ID{9}) || !slices.Equal(c.Tags, []string{"go"}) {
		t.Fatalf("restored categories = %v, tags = %v", c.CategoryIDs(), c.Tags)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/courseauthoring/domain/`
Expected: FAIL — `undefined: domain.NewCategory`, `c.SetClassification undefined`.

- [ ] **Step 3: Add the sentinels**

Append to the `var (...)` block in `internal/courseauthoring/domain/errors.go`:

```go
	ErrInvalidCategoryName     = errors.New("category name must be 1-60 characters")
	ErrTooManyCategories       = errors.New("a course has at most 3 categories")
	ErrInvalidTag              = errors.New("tags must be 1-32 lowercase letters, digits and single hyphens")
	ErrTooManyTags             = errors.New("a course has at most 10 tags")
```

- [ ] **Step 4: Add the category entity**

`internal/courseauthoring/domain/category.go`:

```go
package domain

import (
	"strings"
	"time"
	"unicode/utf8"

	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
	"github.com/santoshkc2200/ioe-backend/internal/platform/slug"
)

// MaxCategoryNameRunes is the longest category name.
const MaxCategoryNameRunes = 60

const maxCategorySlugLen = 64

// Category is an entry in the root-admin-managed list courses are filed under.
type Category struct {
	ID        id.ID
	Name      string
	Slug      string
	CreatedAt time.Time
}

// CategoryRef is a course's link to a category. Writes use only ID; reads hydrate Name and
// Slug from the category.
type CategoryRef struct {
	ID   id.ID
	Name string
	Slug string
}

func NewCategory(categoryID id.ID, name string, now time.Time) (Category, error) {
	c := Category{ID: categoryID, CreatedAt: now}
	if err := c.Rename(name); err != nil {
		return Category{}, err
	}
	return c, nil
}

// Rename sets the trimmed name and re-derives the slug. A name with fewer than 2 usable ASCII
// characters gets the slug "category-<id>".
func (c *Category) Rename(raw string) error {
	name := strings.TrimSpace(raw)
	if name == "" || utf8.RuneCountInString(name) > MaxCategoryNameRunes {
		return ErrInvalidCategoryName
	}
	s := slug.From(name, maxCategorySlugLen)
	if len(s) < 2 {
		s = "category-" + c.ID.String()
	}
	c.Name, c.Slug = name, s
	return nil
}
```

- [ ] **Step 5: Classify courses**

In `internal/courseauthoring/domain/course.go`:

1. Add imports `"errors"` and `"github.com/santoshkc2200/ioe-backend/internal/platform/tags"`.
2. Add to the `Course` struct, after `Price Price`:

```go
	Categories   []CategoryRef // ordered as the instructor chose; Name and Slug hydrated on read
	Tags         []string      // normalized, ordered as the instructor chose
```

3. Add after `SetPrice`:

```go
// MaxCourseCategories is the most categories a course is filed under.
const MaxCourseCategories = 3

// SetClassification replaces the course's categories and tags. Duplicate category IDs are
// dropped keeping order; whether each ID names a category is the caller's check.
func (c *Course) SetClassification(categoryIDs []id.ID, tagList []string, now time.Time) error {
	if err := c.BeginEdit(); err != nil {
		return err
	}
	refs := make([]CategoryRef, 0, len(categoryIDs))
	for _, cid := range categoryIDs {
		if !slices.ContainsFunc(refs, func(r CategoryRef) bool { return r.ID == cid }) {
			refs = append(refs, CategoryRef{ID: cid})
		}
	}
	if len(refs) > MaxCourseCategories {
		return ErrTooManyCategories
	}
	normalized, err := tags.NewList(tagList)
	switch {
	case errors.Is(err, tags.ErrTooMany):
		return ErrTooManyTags
	case err != nil:
		return ErrInvalidTag
	}
	c.Categories, c.Tags, c.UpdatedAt = refs, normalized, now
	return nil
}

// CategoryIDs returns the IDs of the course's categories in order.
func (c *Course) CategoryIDs() []id.ID {
	out := make([]id.ID, len(c.Categories))
	for i, r := range c.Categories {
		out[i] = r.ID
	}
	return out
}
```

4. In `DiscardDraft`, after the line restoring `c.Title, ... c.Price`, add:

```go
	c.Categories = append([]CategoryRef(nil), live.Categories...)
	c.Tags = append([]string(nil), live.Tags...)
```

- [ ] **Step 6: Run the tests**

Run: `go test ./internal/courseauthoring/domain/ && golangci-lint run ./internal/courseauthoring/domain/`
Expected: PASS, no lint findings.

- [ ] **Step 7: Commit**

```bash
git add internal/courseauthoring/domain
git commit -m "feat(courseauthoring): add categories and course classification"
```

---

### Task 3: Category use cases, classification on details, catalog query fields

**Files:**
- Modify: `internal/courseauthoring/app/errors.go`, `ports.go`, `course_service.go`, `catalog.go`, `fakes_test.go`
- Create: `internal/courseauthoring/app/category_service.go`, `category_service_test.go`, `classification_test.go`

**Interfaces:**
- Consumes: Task 2 domain API; `tags.New` (Task 1).
- Produces:
  - `app.ErrCategoryExists`, `app.ErrUnknownCategory`.
  - `app.CategoryRepository` with `Insert(ctx, domain.Category) error`, `Update(ctx, domain.Category) error`, `Delete(ctx, id.ID) error`, `Get(ctx, id.ID) (domain.Category, error)`, `List(ctx) ([]app.CategoryWithCount, error)`, `ExistAll(ctx, []id.ID) (bool, error)`; `app.CategoryWithCount{domain.Category; CourseCount int}`; `app.Repos.Categories`.
  - `app.NewCategoryService(tx TxRunner, ids *id.Generator, c clock.Clock) *CategoryService` with `Create(ctx, p, name) (domain.Category, error)`, `Rename(ctx, p, categoryID, name) (domain.Category, error)`, `Delete(ctx, p, categoryID) error`, `List(ctx) ([]CategoryWithCount, error)`.
  - `app.DetailsInput.CategoryIDs *[]id.ID`, `app.DetailsInput.Tags *[]string`.
  - `app.CatalogQuery` gains `Q`, `Category`, `Tag string`, `AfterRank float32`; `app.MaxSearchRunes = 200`; `app.CourseSummary` gains `Categories []domain.CategoryRef`, `Tags []string`, `Rank float32`; `app.CatalogPage.NextRank float32`.

- [ ] **Step 1: Extend the fake**

In `internal/courseauthoring/app/fakes_test.go`:

1. Add `"strings"` to the imports.
2. Add a field to `memStore`: `categories map[id.ID]domain.Category`; and to `memTx`: `categories map[id.ID]domain.Category`.
3. In `newMemStore`, add `categories: map[id.ID]domain.Category{},` to the literal.
4. In `RunInTx`: add `categories: clone(m.categories),` to the `&memTx{...}` literal; change the `fn` call to `fn(app.Repos{Courses: tx, Contents: tx, Events: tx, Categories: memCategories{tx}})`; after the commit line `m.submitted, m.versionPins = ...` add `m.categories = tx.categories`.
5. In `FindByID`, replace the final `return c, nil` with `return t.hydrate(c), nil`.
6. In `FindVersion`, replace the final `return v, nil` with `return t.hydrate(v), nil`.
7. Replace `ListPublished` with:

```go
func (t *memTx) ListPublished(_ context.Context, q app.CatalogQuery) ([]app.CourseSummary, error) {
	var out []app.CourseSummary
	for _, w := range t.courses {
		if !w.IsLive() {
			continue
		}
		c := t.hydrate(t.versions[versionKey{w.ID, w.Live.Number}].course)
		if (q.After != 0 && c.ID >= q.After) ||
			(q.Level != "" && c.Level != q.Level) ||
			(q.Price == app.PriceFree && !c.Price.IsFree()) || (q.Price == app.PricePaid && c.Price.IsFree()) ||
			(q.Category != "" && !slices.ContainsFunc(c.Categories, func(r domain.CategoryRef) bool { return r.Slug == q.Category })) ||
			(q.Tag != "" && !slices.Contains(c.Tags, q.Tag)) ||
			(q.Q != "" && !strings.Contains(strings.ToLower(c.Title.String()+" "+c.Description), strings.ToLower(q.Q))) {
			continue
		}
		var rank float32
		if q.Q != "" {
			rank = 1 // the fake ranks every match alike; ordering by rank is a PostgreSQL concern
		}
		out = append(out, app.CourseSummary{
			ID: c.ID, OwnerID: c.OwnerID, Title: c.Title.String(), Description: c.Description,
			Level: c.Level, ThumbnailURL: c.ThumbnailURL, Price: c.Price,
			Categories: c.Categories, Tags: c.Tags, Rank: rank,
			LectureCount: len(c.Lectures), SectionCount: len(c.Sections),
			CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	if len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return out, nil
}
```

8. Append:

```go
// hydrate fills category names and drops categories that no longer exist, as the database's
// join and cascade do.
func (t *memTx) hydrate(c domain.Course) domain.Course {
	refs := make([]domain.CategoryRef, 0, len(c.Categories))
	for _, r := range c.Categories {
		if k, ok := t.categories[r.ID]; ok {
			refs = append(refs, domain.CategoryRef{ID: k.ID, Name: k.Name, Slug: k.Slug})
		}
	}
	c.Categories, c.Tags = refs, append([]string{}, c.Tags...)
	return c
}

// memCategories is the category repository over a memTx.
type memCategories struct{ t *memTx }

func (m memCategories) conflicts(c domain.Category) bool {
	for _, k := range m.t.categories {
		if k.ID != c.ID && (strings.EqualFold(k.Name, c.Name) || k.Slug == c.Slug) {
			return true
		}
	}
	return false
}

func (m memCategories) Insert(_ context.Context, c domain.Category) error {
	if m.conflicts(c) {
		return app.ErrCategoryExists
	}
	m.t.categories[c.ID] = c
	return nil
}

func (m memCategories) Update(_ context.Context, c domain.Category) error {
	if _, ok := m.t.categories[c.ID]; !ok {
		return app.ErrNotFound
	}
	if m.conflicts(c) {
		return app.ErrCategoryExists
	}
	m.t.categories[c.ID] = c
	return nil
}

func (m memCategories) Delete(_ context.Context, categoryID id.ID) error {
	if _, ok := m.t.categories[categoryID]; !ok {
		return app.ErrNotFound
	}
	delete(m.t.categories, categoryID)
	return nil
}

func (m memCategories) Get(_ context.Context, categoryID id.ID) (domain.Category, error) {
	c, ok := m.t.categories[categoryID]
	if !ok {
		return domain.Category{}, app.ErrNotFound
	}
	return c, nil
}

func (m memCategories) List(_ context.Context) ([]app.CategoryWithCount, error) {
	out := make([]app.CategoryWithCount, 0, len(m.t.categories))
	for _, k := range m.t.categories {
		n := 0
		for _, w := range m.t.courses {
			if w.IsLive() && slices.ContainsFunc(m.t.versions[versionKey{w.ID, w.Live.Number}].course.Categories,
				func(r domain.CategoryRef) bool { return r.ID == k.ID }) {
				n++
			}
		}
		out = append(out, app.CategoryWithCount{Category: k, CourseCount: n})
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out, nil
}

func (m memCategories) ExistAll(_ context.Context, ids []id.ID) (bool, error) {
	for _, cid := range ids {
		if _, ok := m.t.categories[cid]; !ok {
			return false, nil
		}
	}
	return true, nil
}
```

- [ ] **Step 2: Write the failing tests**

`internal/courseauthoring/app/category_service_test.go`:

```go
package app_test

import (
	"errors"
	"testing"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/app"
	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/domain"
)

func newCategoryService(t *testing.T, store *memStore) *app.CategoryService {
	t.Helper()
	return app.NewCategoryService(store, testIDs(t), fixedClock{time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)})
}

func TestCategoryWritesRequireRootAdmin(t *testing.T) {
	store := newMemStore()
	svc := newCategoryService(t, store)
	if _, err := svc.Create(ctx, owner, "Web"); !errors.Is(err, app.ErrForbidden) {
		t.Fatalf("create = %v", err)
	}
	k, err := svc.Create(ctx, admin, "Web")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Rename(ctx, owner, k.ID, "Data"); !errors.Is(err, app.ErrForbidden) {
		t.Fatalf("rename = %v", err)
	}
	if err := svc.Delete(ctx, student, k.ID); !errors.Is(err, app.ErrForbidden) {
		t.Fatalf("delete = %v", err)
	}
}

func TestCategoryLifecycle(t *testing.T) {
	store := newMemStore()
	svc := newCategoryService(t, store)
	web, err := svc.Create(ctx, admin, "Web Development")
	if err != nil || web.Slug != "web-development" {
		t.Fatalf("create = %+v, %v", web, err)
	}
	if _, err := svc.Create(ctx, admin, "web development"); !errors.Is(err, app.ErrCategoryExists) {
		t.Fatalf("duplicate name = %v", err)
	}
	if _, err := svc.Create(ctx, admin, "Web-Development"); !errors.Is(err, app.ErrCategoryExists) {
		t.Fatalf("duplicate slug = %v", err)
	}
	if _, err := svc.Create(ctx, admin, " "); !errors.Is(err, domain.ErrInvalidCategoryName) {
		t.Fatalf("blank = %v", err)
	}
	data, _ := svc.Create(ctx, admin, "Data")
	if _, err := svc.Rename(ctx, admin, data.ID, "WEB DEVELOPMENT"); !errors.Is(err, app.ErrCategoryExists) {
		t.Fatalf("rename onto taken = %v", err)
	}
	renamed, err := svc.Rename(ctx, admin, data.ID, "Data Science")
	if err != nil || renamed.Slug != "data-science" {
		t.Fatalf("rename = %+v, %v", renamed, err)
	}
	list, err := svc.List(ctx)
	if err != nil || len(list) != 2 || list[0].Name != "Data Science" || list[1].Name != "Web Development" || list[0].CourseCount != 0 {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if err := svc.Delete(ctx, admin, web.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.Delete(ctx, admin, web.ID); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("delete twice = %v", err)
	}
	if _, err := svc.Rename(ctx, admin, web.ID, "X"); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("rename deleted = %v", err)
	}
}
```

`internal/courseauthoring/app/classification_test.go`:

```go
package app_test

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/app"
	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/contentblocks"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

func refIDs(refs []domain.CategoryRef) []id.ID {
	out := make([]id.ID, len(refs))
	for i, r := range refs {
		out[i] = r.ID
	}
	return out
}

func TestUpdateDetailsClassification(t *testing.T) {
	svc, store := newCourseService(t)
	cats := newCategoryService(t, store)
	web, _ := cats.Create(ctx, admin, "Web")
	data, _ := cats.Create(ctx, admin, "Data")
	c, err := svc.Create(ctx, owner, app.CreateCourseInput{Title: "Go"})
	if err != nil {
		t.Fatal(err)
	}
	details := func(categoryIDs *[]id.ID, tagList *[]string) error {
		return svc.UpdateDetails(ctx, owner, c.ID, app.DetailsInput{Title: "Go", CategoryIDs: categoryIDs, Tags: tagList})
	}
	if err := details(&[]id.ID{data.ID, web.ID}, &[]string{"Go", "web"}); err != nil {
		t.Fatal(err)
	}
	got, _ := svc.Get(ctx, owner, c.ID)
	if !slices.Equal(refIDs(got.Categories), []id.ID{data.ID, web.ID}) || got.Categories[0].Name != "Data" ||
		got.Categories[0].Slug != "data" || !slices.Equal(got.Tags, []string{"go", "web"}) {
		t.Fatalf("classified = %+v %v", got.Categories, got.Tags)
	}

	// nil leaves both unchanged.
	if err := details(nil, nil); err != nil {
		t.Fatal(err)
	}
	got, _ = svc.Get(ctx, owner, c.ID)
	if len(got.Categories) != 2 || len(got.Tags) != 2 {
		t.Fatalf("after nil = %+v %v", got.Categories, got.Tags)
	}

	// An empty slice clears; the other field is kept.
	if err := details(nil, &[]string{}); err != nil {
		t.Fatal(err)
	}
	got, _ = svc.Get(ctx, owner, c.ID)
	if len(got.Categories) != 2 || len(got.Tags) != 0 {
		t.Fatalf("after clear = %+v %v", got.Categories, got.Tags)
	}

	// Unknown category: error, nothing saved.
	if err := details(&[]id.ID{web.ID, 999}, nil); !errors.Is(err, app.ErrUnknownCategory) {
		t.Fatalf("unknown = %v", err)
	}
	if err := details(nil, &[]string{"c++"}); !errors.Is(err, domain.ErrInvalidTag) {
		t.Fatalf("bad tag = %v", err)
	}
	got, _ = svc.Get(ctx, owner, c.ID)
	if len(got.Categories) != 2 {
		t.Fatalf("after failures = %+v", got.Categories)
	}

	// A deleted category disappears from the course.
	if err := cats.Delete(ctx, admin, data.ID); err != nil {
		t.Fatal(err)
	}
	got, _ = svc.Get(ctx, owner, c.ID)
	if !slices.Equal(refIDs(got.Categories), []id.ID{web.ID}) {
		t.Fatalf("after delete = %+v", got.Categories)
	}
	if err := svc.UpdateDetails(ctx, otherInstr, c.ID, app.DetailsInput{Title: "Go", Tags: &[]string{"x"}}); !errors.Is(err, app.ErrForbidden) {
		t.Fatalf("other instructor = %v", err)
	}
}

// putClassified stores a published course whose live version carries the given details.
func putClassified(t *testing.T, store *memStore, cid id.ID, title, description string, cats []domain.Category, tagList []string) {
	t.Helper()
	ttl, _ := contentblocks.NewTitle(title)
	now := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	c := domain.NewCourse(cid, owner.UserID, ttl, description, now)
	lt, _ := contentblocks.NewTitle("L1")
	if err := c.AddLecture(cid+1000, lt, false, false, now); err != nil {
		t.Fatal(err)
	}
	ids := make([]id.ID, len(cats))
	for i, k := range cats {
		ids[i] = k.ID
		store.categories[k.ID] = k
	}
	if err := c.SetClassification(ids, tagList, now); err != nil {
		t.Fatal(err)
	}
	if err := c.Publish(true, now); err != nil {
		t.Fatal(err)
	}
	store.versions[versionKey{cid, 1}] = snapshot{course: c}
	store.courses[cid] = c
}

func TestListPublishedSearchAndFilters(t *testing.T) {
	svc, store := newCourseService(t)
	web, _ := domain.NewCategory(500, "Web", time.Now())
	putClassified(t, store, 10, "Learning Go", "basics", []domain.Category{web}, []string{"go"})
	putClassified(t, store, 20, "Rust", "systems", nil, []string{"rust"})

	page, err := svc.ListPublished(ctx, app.CatalogQuery{Limit: 10, Q: "  go  "})
	if err != nil || len(page.Courses) != 1 || page.Courses[0].ID != 10 {
		t.Fatalf("q = %+v, %v", page, err)
	}
	c := page.Courses[0]
	if len(c.Categories) != 1 || c.Categories[0].Slug != "web" || !slices.Equal(c.Tags, []string{"go"}) {
		t.Fatalf("summary = %+v", c)
	}
	if page, _ := svc.ListPublished(ctx, app.CatalogQuery{Limit: 10, Category: "WEB"}); len(page.Courses) != 1 || page.Courses[0].ID != 10 {
		t.Fatalf("category = %+v", page)
	}
	if page, _ := svc.ListPublished(ctx, app.CatalogQuery{Limit: 10, Tag: " Rust "}); len(page.Courses) != 1 || page.Courses[0].ID != 20 {
		t.Fatalf("tag = %+v", page)
	}
	for _, q := range []app.CatalogQuery{{Limit: 10, Tag: "c++"}, {Limit: 10, Category: "nope"}} {
		page, err := svc.ListPublished(ctx, q)
		if err != nil || len(page.Courses) != 0 {
			t.Errorf("%+v = %+v, %v", q, page, err)
		}
	}
	if _, err := svc.ListPublished(ctx, app.CatalogQuery{Limit: 10, Q: strings.Repeat("é", app.MaxSearchRunes+1)}); !errors.Is(err, app.ErrInvalidInput) {
		t.Fatalf("long q = %v", err)
	}
	if _, err := svc.ListPublished(ctx, app.CatalogQuery{Limit: 10, Q: strings.Repeat("é", app.MaxSearchRunes)}); err != nil {
		t.Fatalf("200 runes = %v", err)
	}
}

func TestListPublishedSearchPageCarriesRank(t *testing.T) {
	svc, store := newCourseService(t)
	putClassified(t, store, 10, "Go one", "", nil, nil)
	putClassified(t, store, 20, "Go two", "", nil, nil)
	page, err := svc.ListPublished(ctx, app.CatalogQuery{Limit: 1, Q: "go"})
	if err != nil || page.Next != 20 || page.NextRank != 1 {
		t.Fatalf("page = %+v, %v", page, err)
	}
	plain, _ := svc.ListPublished(ctx, app.CatalogQuery{Limit: 1})
	if plain.Next != 20 || plain.NextRank != 0 {
		t.Fatalf("plain = %+v", plain)
	}
}
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `go test ./internal/courseauthoring/app/`
Expected: FAIL to compile — `undefined: app.NewCategoryService`, `unknown field Categories in struct literal of type app.Repos`, `unknown field Q`.

- [ ] **Step 4: Add errors and ports**

Append to the `var (...)` block in `internal/courseauthoring/app/errors.go`:

```go
	ErrCategoryExists         = errors.New("category name or slug already exists")
	ErrUnknownCategory        = errors.New("unknown category")
```

In `internal/courseauthoring/app/ports.go`, add after `EventPublisher`:

```go
// CategoryRepository stores categories. Insert and Update return ErrCategoryExists when the
// name (ignoring case) or the slug is taken. Update, Delete and Get return ErrNotFound for an
// unknown category. Delete also removes the category from every course and published version.
// List orders by name and counts, per category, the courses whose live version has it.
// ExistAll reports whether every ID names a category; it is true for no IDs.
type CategoryRepository interface {
	Insert(ctx context.Context, c domain.Category) error
	Update(ctx context.Context, c domain.Category) error
	Delete(ctx context.Context, categoryID id.ID) error
	Get(ctx context.Context, categoryID id.ID) (domain.Category, error)
	List(ctx context.Context) ([]CategoryWithCount, error)
	ExistAll(ctx context.Context, ids []id.ID) (bool, error)
}

// CategoryWithCount is a category and how many live courses are filed under it.
type CategoryWithCount struct {
	domain.Category
	CourseCount int
}
```

and add a field to `Repos`:

```go
	Categories CategoryRepository
```

Also extend the `CourseRepository` doc comment's `ListPublished` sentence to read: "ListPublished returns up to q.Limit published courses matching the filters and, when q.Q is set, the search text, from their live versions. Without q.Q they are ordered by ID descending and start below q.After; with q.Q they are ordered by rank then ID, descending, and start below (q.AfterRank, q.After). Any start when q.After is zero."

- [ ] **Step 5: Add the category service**

`internal/courseauthoring/app/category_service.go`:

```go
package app

import (
	"context"

	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/clock"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

// CategoryService manages the course category list. Root admins write it; anyone reads it.
type CategoryService struct {
	tx    TxRunner
	ids   *id.Generator
	clock clock.Clock
}

func NewCategoryService(tx TxRunner, ids *id.Generator, c clock.Clock) *CategoryService {
	return &CategoryService{tx: tx, ids: ids, clock: c}
}

func (s *CategoryService) Create(ctx context.Context, p auth.Principal, name string) (domain.Category, error) {
	if p.Role != auth.RoleRootAdmin {
		return domain.Category{}, ErrForbidden
	}
	c, err := domain.NewCategory(s.ids.New(), name, s.clock.Now())
	if err != nil {
		return domain.Category{}, err
	}
	if err := s.tx.RunInTx(ctx, func(r Repos) error { return r.Categories.Insert(ctx, c) }); err != nil {
		return domain.Category{}, err
	}
	return c, nil
}

func (s *CategoryService) Rename(ctx context.Context, p auth.Principal, categoryID id.ID, name string) (domain.Category, error) {
	if p.Role != auth.RoleRootAdmin {
		return domain.Category{}, ErrForbidden
	}
	var out domain.Category
	err := s.tx.RunInTx(ctx, func(r Repos) error {
		c, err := r.Categories.Get(ctx, categoryID)
		if err != nil {
			return err
		}
		if err := c.Rename(name); err != nil {
			return err
		}
		if err := r.Categories.Update(ctx, c); err != nil {
			return err
		}
		out = c
		return nil
	})
	return out, err
}

// Delete removes the category from the list and from every course and published version.
func (s *CategoryService) Delete(ctx context.Context, p auth.Principal, categoryID id.ID) error {
	if p.Role != auth.RoleRootAdmin {
		return ErrForbidden
	}
	return s.tx.RunInTx(ctx, func(r Repos) error { return r.Categories.Delete(ctx, categoryID) })
}

// List returns every category by name with its live course count. It takes no principal
// because the list is public.
func (s *CategoryService) List(ctx context.Context) ([]CategoryWithCount, error) {
	var out []CategoryWithCount
	err := s.tx.RunInTx(ctx, func(r Repos) error {
		var err error
		out, err = r.Categories.List(ctx)
		return err
	})
	return out, err
}
```

- [ ] **Step 6: Classify through UpdateDetails**

In `internal/courseauthoring/app/course_service.go`, replace `DetailsInput` with:

```go
type DetailsInput struct {
	Title, Description, Level, ThumbnailURL string
	CategoryIDs                             *[]id.ID  // nil leaves the course's categories unchanged
	Tags                                    *[]string // nil leaves the course's tags unchanged
}
```

and replace `UpdateDetails` with:

```go
func (s *CourseService) UpdateDetails(ctx context.Context, p auth.Principal, courseID id.ID, in DetailsInput) error {
	title, err := newTitle(in.Title)
	if err != nil {
		return err
	}
	_, err = s.mutate(ctx, p, courseID, func(r Repos, c *domain.Course) error {
		now := s.clock.Now()
		if err := c.UpdateDetails(title, in.Description, in.Level, in.ThumbnailURL, now); err != nil {
			return err
		}
		if in.CategoryIDs == nil && in.Tags == nil {
			return nil
		}
		categoryIDs, tagList := c.CategoryIDs(), c.Tags
		if in.CategoryIDs != nil {
			categoryIDs = *in.CategoryIDs
		}
		if in.Tags != nil {
			tagList = *in.Tags
		}
		if err := c.SetClassification(categoryIDs, tagList, now); err != nil {
			return err
		}
		ok, err := r.Categories.ExistAll(ctx, c.CategoryIDs())
		if err != nil {
			return err
		}
		if !ok {
			return ErrUnknownCategory
		}
		return nil
	})
	return err
}
```

- [ ] **Step 7: Extend the catalog query**

In `internal/courseauthoring/app/catalog.go`:

1. Add imports `"strings"`, `"unicode/utf8"` and `"github.com/santoshkc2200/ioe-backend/internal/platform/tags"`.
2. After `MaxCatalogLimit`, add:

```go
// MaxSearchRunes is the longest search text.
const MaxSearchRunes = 200
```

3. Replace `CatalogQuery`, `CourseSummary` and `CatalogPage` with:

```go
// CatalogQuery selects one page of published courses. After is zero for the first page.
type CatalogQuery struct {
	Level     string // empty means any level
	Price     PriceFilter
	Q         string // search text; empty means no search and newest-first order
	Category  string // category slug; empty means any category
	Tag       string // empty means any tag
	Limit     int
	After     id.ID
	AfterRank float32 // the previous page's last rank; used only with Q
}

// CourseSummary is a published course without its outline. Rank is the search relevance; it
// is zero without search text.
type CourseSummary struct {
	ID           id.ID
	OwnerID      id.ID
	Title        string
	Description  string
	Level        string
	ThumbnailURL string
	Price        domain.Price
	Categories   []domain.CategoryRef
	Tags         []string
	LectureCount int
	SectionCount int
	Rank         float32
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// CatalogPage is one page of the catalog. Next is zero on the last page; NextRank is the rank
// of the row Next names when the query had search text.
type CatalogPage struct {
	Courses  []CourseSummary
	Next     id.ID
	NextRank float32
}
```

4. In `validate`, before `return nil`, add:

```go
	if utf8.RuneCountInString(q.Q) > MaxSearchRunes {
		return fmt.Errorf("%w: q must be at most %d characters", ErrInvalidInput, MaxSearchRunes)
	}
```

5. Replace `ListPublished` with:

```go
// ListPublished returns one page of published courses: newest first, or by relevance when q.Q
// is set. It takes no principal because published courses are public. A tag that no course
// could carry and an unknown category slug match nothing.
func (s *CourseService) ListPublished(ctx context.Context, q CatalogQuery) (CatalogPage, error) {
	q.Q = strings.TrimSpace(q.Q)
	q.Category = strings.ToLower(strings.TrimSpace(q.Category))
	if err := q.validate(); err != nil {
		return CatalogPage{}, err
	}
	if q.Tag != "" {
		t, err := tags.New(q.Tag)
		if err != nil {
			return CatalogPage{}, nil
		}
		q.Tag = t
	}
	probe := q
	probe.Limit++ // one extra row tells whether another page exists
	var rows []CourseSummary
	err := s.tx.RunInTx(ctx, func(r Repos) error {
		var err error
		rows, err = r.Courses.ListPublished(ctx, probe)
		return err
	})
	if err != nil {
		return CatalogPage{}, err
	}
	page := CatalogPage{Courses: rows}
	if len(rows) > q.Limit {
		page.Courses = rows[:q.Limit]
		last := page.Courses[q.Limit-1]
		page.Next, page.NextRank = last.ID, last.Rank
	}
	return page, nil
}
```

- [ ] **Step 8: Run the tests**

Run: `go test ./internal/courseauthoring/... && golangci-lint run ./internal/courseauthoring/...`
Expected: PASS. The postgres adapter still compiles (its `Repos` literal uses named fields). The httpapi tests still pass because none of them sends categories yet.

- [ ] **Step 9: Commit**

```bash
git add internal/courseauthoring/app
git commit -m "feat(courseauthoring): manage categories and classify courses in use cases"
```

---

### Task 4: Schema and persistence of categories and classification

**Files:**
- Create: `migrations/00014_courseauthoring_catalog_search.sql`
- Modify: `internal/courseauthoring/adapters/postgres/queries.sql`, `postgres.go`, `courses.go`
- Create: `internal/courseauthoring/adapters/postgres/categories.go`
- Generate: `internal/courseauthoring/adapters/postgres/sqlcgen/`
- Create: `internal/courseauthoring/adapters/postgres/classification_integration_test.go`

**Interfaces:**
- Consumes: Task 3 ports.
- Produces: `postgres.TxRunner` whose `Repos.Categories` implements `app.CategoryRepository`; course reads hydrate `Categories` and `Tags` from the working copy (`FindByID`, `ListByOwner`, `ListInReview`) and from the version (`FindVersion`); `Update` maps a foreign-key failure on categories to `app.ErrUnknownCategory`. SQL function `courseauthoring.course_search_document(text, text[], text) tsvector` and column `course_versions.search` (Task 5 queries them).

- [ ] **Step 1: Write the migration**

`migrations/00014_courseauthoring_catalog_search.sql`:

```sql
-- +goose Up
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
CREATE INDEX course_categories_category_idx ON courseauthoring.course_categories (category_id);

-- The searchable document of a published version. The text search configuration is chosen
-- here only; supporting another language means replacing this function and re-adding the
-- search column. array_to_string is STABLE in general but immutable for text[], so the
-- function may be IMMUTABLE and back a generated column.
-- +goose StatementBegin
CREATE FUNCTION courseauthoring.course_search_document(title text, tags text[], description text)
RETURNS tsvector LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
  SELECT setweight(to_tsvector('simple', title), 'A') ||
         setweight(to_tsvector('simple', array_to_string(tags, ' ')), 'B') ||
         setweight(to_tsvector('simple', description), 'C')
$$;
-- +goose StatementEnd

ALTER TABLE courseauthoring.course_versions
  ADD COLUMN tags text[] NOT NULL DEFAULT '{}',
  ADD COLUMN search tsvector GENERATED ALWAYS AS
    (courseauthoring.course_search_document(title, tags, description)) STORED;

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

-- +goose Down
DROP TABLE courseauthoring.course_version_categories;
ALTER TABLE courseauthoring.course_versions DROP COLUMN search, DROP COLUMN tags;
DROP FUNCTION courseauthoring.course_search_document(text, text[], text);
DROP TABLE courseauthoring.course_categories;
ALTER TABLE courseauthoring.courses DROP COLUMN tags;
DROP TABLE courseauthoring.categories;
```

- [ ] **Step 2: Update and add queries**

In `internal/courseauthoring/adapters/postgres/queries.sql`:

1. Replace `InsertCourse` with:

```sql
-- name: InsertCourse :exec
INSERT INTO courseauthoring.courses
  (id, owner_id, title, description, level, thumbnail_url, status, price_amount_minor, price_currency, version, created_at, updated_at, tags)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 1, $10, $11, $12);
```

2. Replace `UpdateCourse` with:

```sql
-- name: UpdateCourse :execrows
UPDATE courseauthoring.courses
SET title = $3, description = $4, level = $5, thumbnail_url = $6, status = $7,
    price_amount_minor = $8, price_currency = $9, updated_at = $10,
    submitted_at = $11, reviewed_at = $12, review_note = $13, last_version = $14, live_version = $15,
    tags = $16, version = version + 1
WHERE id = $1 AND version = $2;
```

3. Replace `InsertCourseVersion` with:

```sql
-- name: InsertCourseVersion :exec
INSERT INTO courseauthoring.course_versions
  (course_id, number, title, description, level, thumbnail_url, price_amount_minor, price_currency, published_by, published_at, tags)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11);
```

4. Replace `GetCourseVersion` (it must not select the `search` column) with:

```sql
-- name: GetCourseVersion :one
SELECT course_id, number, title, description, level, thumbnail_url, price_amount_minor,
       price_currency, published_by, published_at, tags
FROM courseauthoring.course_versions WHERE course_id = $1 AND number = $2;
```

5. Append:

```sql
-- name: ListCategoriesByCourseIDs :many
SELECT cc.course_id, k.id, k.name, k.slug
FROM courseauthoring.course_categories cc
JOIN courseauthoring.categories k ON k.id = cc.category_id
WHERE cc.course_id = ANY(sqlc.arg(course_ids)::bigint[])
ORDER BY cc.course_id, cc.position;

-- name: ListVersionCategories :many
SELECT k.id, k.name, k.slug
FROM courseauthoring.course_version_categories vc
JOIN courseauthoring.categories k ON k.id = vc.category_id
WHERE vc.course_id = $1 AND vc.number = $2
ORDER BY vc.position;

-- name: DeleteCourseCategories :exec
DELETE FROM courseauthoring.course_categories WHERE course_id = $1;

-- name: InsertCourseCategories :exec
INSERT INTO courseauthoring.course_categories (course_id, category_id, position)
SELECT sqlc.arg(course_id)::bigint, t.category_id, (t.ord - 1)::integer
FROM unnest(sqlc.arg(category_ids)::bigint[]) WITH ORDINALITY AS t(category_id, ord);

-- name: InsertVersionCategories :exec
INSERT INTO courseauthoring.course_version_categories (course_id, number, category_id, position)
SELECT sqlc.arg(course_id)::bigint, sqlc.arg(number)::integer, t.category_id, (t.ord - 1)::integer
FROM unnest(sqlc.arg(category_ids)::bigint[]) WITH ORDINALITY AS t(category_id, ord);

-- name: InsertCategory :exec
INSERT INTO courseauthoring.categories (id, name, slug, created_at) VALUES ($1, $2, $3, $4);

-- name: UpdateCategory :execrows
UPDATE courseauthoring.categories SET name = $2, slug = $3 WHERE id = $1;

-- name: DeleteCategory :execrows
DELETE FROM courseauthoring.categories WHERE id = $1;

-- name: GetCategory :one
SELECT id, name, slug, created_at FROM courseauthoring.categories WHERE id = $1;

-- name: ListCategoriesWithCounts :many
SELECT k.id, k.name, k.slug, k.created_at,
       (SELECT count(*) FROM courseauthoring.course_version_categories vc
        JOIN courseauthoring.courses c ON c.id = vc.course_id AND c.live_version = vc.number
        WHERE vc.category_id = k.id) AS course_count
FROM courseauthoring.categories k
ORDER BY lower(k.name), k.id;

-- name: CountCategories :one
SELECT count(*) FROM courseauthoring.categories WHERE id = ANY(sqlc.arg(ids)::bigint[]);
```

- [ ] **Step 3: Generate and check the generated code**

Run: `make sqlc && go build ./internal/courseauthoring/...`
Expected: generation succeeds and the build passes (the new `Tags` params are not set yet). Confirm `sqlcgen.ListCoursesByIDsRow` and `sqlcgen.GetCourseVersionRow` each have `Tags []string`. `CourseauthoringCourseVersion` in `models.go` gains a `Search` field of whatever type sqlc chooses for `tsvector`; nothing reads it.

- [ ] **Step 4: Write the failing integration tests**

`internal/courseauthoring/adapters/postgres/classification_integration_test.go`:

```go
//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/app"
	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

func (f fixture) seedCategory(t *testing.T, name string) domain.Category {
	t.Helper()
	k, err := domain.NewCategory(f.ids.New(), name, f.now)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.tx.RunInTx(context.Background(), func(r app.Repos) error { return r.Categories.Insert(context.Background(), k) }); err != nil {
		t.Fatal(err)
	}
	return k
}

func (f fixture) classify(t *testing.T, c *domain.Course, cats []domain.Category, tagList []string) error {
	t.Helper()
	ids := make([]id.ID, len(cats))
	for i, k := range cats {
		ids[i] = k.ID
	}
	if err := c.SetClassification(ids, tagList, f.now); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	return f.tx.RunInTx(ctx, func(r app.Repos) error { return r.Courses.Update(ctx, c) })
}

func (f fixture) find(t *testing.T, courseID id.ID, version int) domain.Course {
	t.Helper()
	ctx := context.Background()
	var c domain.Course
	if err := f.tx.RunInTx(ctx, func(r app.Repos) error {
		var err error
		if version == 0 {
			c, err = r.Courses.FindByID(ctx, courseID)
		} else {
			c, err = r.Courses.FindVersion(ctx, courseID, version)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return c
}

func (f fixture) categories(t *testing.T) []app.CategoryWithCount {
	t.Helper()
	ctx := context.Background()
	var out []app.CategoryWithCount
	if err := f.tx.RunInTx(ctx, func(r app.Repos) error {
		var err error
		out, err = r.Categories.List(ctx)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

func slugs(refs []domain.CategoryRef) []string {
	out := make([]string, len(refs))
	for i, r := range refs {
		out[i] = r.Slug
	}
	return out
}

func TestCategoryRepository(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	web := f.seedCategory(t, "Web")
	err := f.tx.RunInTx(ctx, func(r app.Repos) error {
		dupName, _ := domain.NewCategory(f.ids.New(), "WEB", f.now)
		dupName.Slug = "other"
		if err := r.Categories.Insert(ctx, dupName); !errors.Is(err, app.ErrCategoryExists) {
			t.Errorf("duplicate name = %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	err = f.tx.RunInTx(ctx, func(r app.Repos) error {
		dupSlug, _ := domain.NewCategory(f.ids.New(), "Other", f.now)
		dupSlug.Slug = "web"
		if err := r.Categories.Insert(ctx, dupSlug); !errors.Is(err, app.ErrCategoryExists) {
			t.Errorf("duplicate slug = %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	data := f.seedCategory(t, "Data")
	if err := f.tx.RunInTx(ctx, func(r app.Repos) error {
		if err := data.Rename("web"); err != nil {
			return err
		}
		if err := r.Categories.Update(ctx, data); !errors.Is(err, app.ErrCategoryExists) {
			t.Errorf("rename onto taken = %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.tx.RunInTx(ctx, func(r app.Repos) error {
		if err := data.Rename("Data Science"); err != nil {
			return err
		}
		if err := r.Categories.Update(ctx, data); err != nil {
			return err
		}
		got, err := r.Categories.Get(ctx, data.ID)
		if err != nil || got.Name != "Data Science" || got.Slug != "data-science" || !got.CreatedAt.Equal(f.now) {
			t.Errorf("get = %+v, %v", got, err)
		}
		if ok, err := r.Categories.ExistAll(ctx, []id.ID{web.ID, data.ID}); !ok || err != nil {
			t.Errorf("exist all = %v, %v", ok, err)
		}
		if ok, err := r.Categories.ExistAll(ctx, []id.ID{web.ID, 999}); ok || err != nil {
			t.Errorf("exist with unknown = %v, %v", ok, err)
		}
		if ok, err := r.Categories.ExistAll(ctx, nil); !ok || err != nil {
			t.Errorf("exist none = %v, %v", ok, err)
		}
		missing := domain.Category{ID: 999, Name: "X", Slug: "x"}
		if err := r.Categories.Update(ctx, missing); !errors.Is(err, app.ErrNotFound) {
			t.Errorf("update missing = %v", err)
		}
		if err := r.Categories.Delete(ctx, 999); !errors.Is(err, app.ErrNotFound) {
			t.Errorf("delete missing = %v", err)
		}
		if _, err := r.Categories.Get(ctx, 999); !errors.Is(err, app.ErrNotFound) {
			t.Errorf("get missing = %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	list := f.categories(t)
	if len(list) != 2 || list[0].Name != "Data Science" || list[1].Name != "Web" || list[0].CourseCount != 0 {
		t.Fatalf("list = %+v", list)
	}
}

func TestClassificationRoundTripAndSnapshot(t *testing.T) {
	f := newFixture(t)
	web, data := f.seedCategory(t, "Web"), f.seedCategory(t, "Data")
	c := f.seedCourse(t)
	if err := f.classify(t, &c, []domain.Category{data, web}, []string{"go", "web"}); err != nil {
		t.Fatal(err)
	}
	got := f.find(t, c.ID, 0)
	if !slices.Equal(slugs(got.Categories), []string{"data", "web"}) || got.Categories[0].Name != "Data" ||
		!slices.Equal(got.Tags, []string{"go", "web"}) {
		t.Fatalf("working copy = %+v %v", got.Categories, got.Tags)
	}
	f.publish(t, &c)

	if err := f.classify(t, &c, []domain.Category{web}, []string{"rust"}); err != nil {
		t.Fatal(err)
	}
	live := f.find(t, c.ID, 1)
	if !slices.Equal(slugs(live.Categories), []string{"data", "web"}) || !slices.Equal(live.Tags, []string{"go", "web"}) {
		t.Fatalf("version 1 = %+v %v", live.Categories, live.Tags)
	}
	working := f.find(t, c.ID, 0)
	if !slices.Equal(slugs(working.Categories), []string{"web"}) || !slices.Equal(working.Tags, []string{"rust"}) {
		t.Fatalf("working copy after edit = %+v %v", working.Categories, working.Tags)
	}
	if list := f.categories(t); list[0].CourseCount != 1 || list[1].CourseCount != 1 {
		t.Fatalf("counts = %+v", list)
	}
}

func TestDeleteCategoryCascadesIntoVersions(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	web := f.seedCategory(t, "Web")
	c := f.seedCourse(t)
	if err := f.classify(t, &c, []domain.Category{web}, nil); err != nil {
		t.Fatal(err)
	}
	f.publish(t, &c)
	if err := f.tx.RunInTx(ctx, func(r app.Repos) error { return r.Categories.Delete(ctx, web.ID) }); err != nil {
		t.Fatal(err)
	}
	if got := f.find(t, c.ID, 0); len(got.Categories) != 0 {
		t.Fatalf("working copy = %+v", got.Categories)
	}
	if got := f.find(t, c.ID, 1); len(got.Categories) != 0 {
		t.Fatalf("version = %+v", got.Categories)
	}
	if list := f.categories(t); len(list) != 0 {
		t.Fatalf("list = %+v", list)
	}
	if got := f.listPublished(t, app.CatalogQuery{Limit: 10}); len(got) != 1 || got[0].ID != c.ID {
		t.Fatalf("catalog = %v", summaryIDs(got))
	}
}

func TestUpdateWithMissingCategoryIsUnknownCategory(t *testing.T) {
	f := newFixture(t)
	c := f.seedCourse(t)
	ghost := domain.Category{ID: f.ids.New()} // never inserted, as if deleted concurrently
	if err := f.classify(t, &c, []domain.Category{ghost}, nil); !errors.Is(err, app.ErrUnknownCategory) {
		t.Fatalf("err = %v", err)
	}
}
```

- [ ] **Step 5: Run the integration tests to verify they fail**

Run: `go test -race -tags integration -run 'TestCategoryRepository|TestClassificationRoundTrip|TestDeleteCategoryCascades|TestUpdateWithMissingCategory' ./internal/courseauthoring/adapters/postgres/`
Expected: FAIL — `r.Categories` is nil (panic) and categories/tags are not persisted.

- [ ] **Step 6: Implement persistence**

In `internal/courseauthoring/adapters/postgres/postgres.go`:

1. Add imports `"errors"` and `"github.com/jackc/pgx/v5/pgconn"`.
2. Add `Categories: categories{q: q},` to the `app.Repos` literal in `RunInTx`.
3. Append:

```go
const (
	uniqueViolation     = "23505"
	foreignKeyViolation = "23503"
)

// pgCode returns the PostgreSQL error code of err, or "" when err is not a PostgreSQL error.
func pgCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}
```

`internal/courseauthoring/adapters/postgres/categories.go`:

```go
package postgres

import (
	"context"

	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/adapters/postgres/sqlcgen"
	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/app"
	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

type categories struct{ q *sqlcgen.Queries }

// categoryConflict maps a unique violation on name or slug to app.ErrCategoryExists.
func categoryConflict(err error) error {
	if pgCode(err) == uniqueViolation {
		return app.ErrCategoryExists
	}
	return err
}

func (r categories) Insert(ctx context.Context, c domain.Category) error {
	return categoryConflict(r.q.InsertCategory(ctx, sqlcgen.InsertCategoryParams{
		ID: int64(c.ID), Name: c.Name, Slug: c.Slug, CreatedAt: c.CreatedAt}))
}

func (r categories) Update(ctx context.Context, c domain.Category) error {
	n, err := r.q.UpdateCategory(ctx, sqlcgen.UpdateCategoryParams{ID: int64(c.ID), Name: c.Name, Slug: c.Slug})
	if err != nil {
		return categoryConflict(err)
	}
	if n == 0 {
		return app.ErrNotFound
	}
	return nil
}

func (r categories) Delete(ctx context.Context, categoryID id.ID) error {
	n, err := r.q.DeleteCategory(ctx, int64(categoryID))
	if err != nil {
		return err
	}
	if n == 0 {
		return app.ErrNotFound
	}
	return nil
}

func (r categories) Get(ctx context.Context, categoryID id.ID) (domain.Category, error) {
	row, err := r.q.GetCategory(ctx, int64(categoryID))
	if err != nil {
		return domain.Category{}, notFound(err)
	}
	return domain.Category{ID: id.ID(row.ID), Name: row.Name, Slug: row.Slug, CreatedAt: row.CreatedAt.UTC()}, nil
}

func (r categories) List(ctx context.Context) ([]app.CategoryWithCount, error) {
	rows, err := r.q.ListCategoriesWithCounts(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]app.CategoryWithCount, len(rows))
	for i, row := range rows {
		out[i] = app.CategoryWithCount{
			Category:    domain.Category{ID: id.ID(row.ID), Name: row.Name, Slug: row.Slug, CreatedAt: row.CreatedAt.UTC()},
			CourseCount: int(row.CourseCount),
		}
	}
	return out, nil
}

func (r categories) ExistAll(ctx context.Context, ids []id.ID) (bool, error) {
	if len(ids) == 0 {
		return true, nil
	}
	n, err := r.q.CountCategories(ctx, int64s(ids))
	if err != nil {
		return false, err
	}
	return int(n) == len(ids), nil
}

func int64s(ids []id.ID) []int64 {
	out := make([]int64, len(ids))
	for i, v := range ids {
		out[i] = int64(v)
	}
	return out
}

// categoryRef builds a hydrated course-category link from a query row.
func categoryRef(categoryID int64, name, slug string) domain.CategoryRef {
	return domain.CategoryRef{ID: id.ID(categoryID), Name: name, Slug: slug}
}
```

In `internal/courseauthoring/adapters/postgres/courses.go`:

1. In `load`, after the `lectures` query, add:

```go
	cats, err := r.q.ListCategoriesByCourseIDs(ctx, courseIDs)
	if err != nil {
		return nil, err
	}
```

   add `Tags: row.Tags,` to the `domain.Course{...}` literal built from each row, and after the lectures loop add:

```go
	for _, k := range cats {
		c := &out[index[k.CourseID]]
		c.Categories = append(c.Categories, categoryRef(k.ID, k.Name, k.Slug))
	}
```

2. In `InsertVersion`, add `Tags: nonNilTags(c.Tags),` to `InsertCourseVersionParams`, and after inserting the version row (before `CopyVersionSections`) add:

```go
	if err := r.q.InsertVersionCategories(ctx, sqlcgen.InsertVersionCategoriesParams{
		CourseID: cid, Number: number, CategoryIds: int64s(c.CategoryIDs())}); err != nil {
		return unknownCategory(err)
	}
```

3. In `FindVersion`, after the `lectures` query, add:

```go
	cats, err := r.q.ListVersionCategories(ctx, sqlcgen.ListVersionCategoriesParams{CourseID: cid, Number: number})
	if err != nil {
		return domain.Course{}, err
	}
```

   add `Tags: v.Tags,` to the `live := domain.Course{...}` literal, and before `return live, nil` add:

```go
	for _, k := range cats {
		live.Categories = append(live.Categories, categoryRef(k.ID, k.Name, k.Slug))
	}
```

4. In `Insert`, add `Tags: nonNilTags(c.Tags),` to `InsertCourseParams`. In `Update`, add `Tags: nonNilTags(c.Tags),` to `UpdateCourseParams`.

5. At the end of `saveChildren`, before `return nil`, add:

```go
	if err := r.q.DeleteCourseCategories(ctx, int64(c.ID)); err != nil {
		return err
	}
	if err := r.q.InsertCourseCategories(ctx, sqlcgen.InsertCourseCategoriesParams{
		CourseID: int64(c.ID), CategoryIds: int64s(c.CategoryIDs())}); err != nil {
		return unknownCategory(err)
	}
```

6. Append:

```go
// nonNilTags keeps a nil slice from being written as NULL into a NOT NULL text[] column.
func nonNilTags(tags []string) []string {
	if tags == nil {
		return []string{}
	}
	return tags
}

// unknownCategory maps a foreign-key failure on a category link, a category deleted after the
// caller checked it, to app.ErrUnknownCategory.
func unknownCategory(err error) error {
	if pgCode(err) == foreignKeyViolation {
		return app.ErrUnknownCategory
	}
	return err
}
```

- [ ] **Step 7: Run the tests**

Run: `go test -race -tags integration ./internal/courseauthoring/adapters/postgres/ && go test ./internal/courseauthoring/... && golangci-lint run ./internal/courseauthoring/...`
Expected: PASS, including the existing integration tests (`TestLiveVersionIsASnapshot`, `TestDiscardDraftAndVersionHistory`, catalog tests). Requires Docker.

- [ ] **Step 8: Check the down migration**

Run against a scratch database (`make compose-up`, then `make migrate-up && make migrate-down && make migrate-up`).
Expected: all three succeed.

- [ ] **Step 9: Commit**

```bash
git add migrations/00014_courseauthoring_catalog_search.sql internal/courseauthoring/adapters/postgres
git commit -m "feat(courseauthoring): persist categories and course classification"
```

---

### Task 5: Search, category and tag filters in the catalog query

**Files:**
- Modify: `internal/courseauthoring/adapters/postgres/queries.sql` (`ListPublishedCourses`)
- Create: `internal/courseauthoring/adapters/postgres/search.go`, `search_test.go`
- Modify: `internal/courseauthoring/adapters/postgres/courses.go` (`ListPublished`)
- Create: `internal/courseauthoring/adapters/postgres/search_integration_test.go`

**Interfaces:**
- Consumes: Task 3 `CatalogQuery` and `CourseSummary`; Task 4 schema and `seedCategory`/`classify` helpers.
- Produces: `ListPublished` honoring `Q`, `Category`, `Tag`, `AfterRank`, returning `Rank`, `Categories`, `Tags` per summary.

- [ ] **Step 1: Write the failing unit test**

`internal/courseauthoring/adapters/postgres/search_test.go`:

```go
package postgres

import "testing"

func TestSearchTerms(t *testing.T) {
	cases := []struct{ q, head, prefix string }{
		{"go", "", "go"},
		{"Learning Go", "Learning", "go"},
		{"  web   dev  ", "web", "dev"},
		{"go!", "go!", ""},
		{`"learning go"`, `"learning go"`, ""},
		{`"unterminated`, `"unterminated`, ""},
		{"rust -unsafe", "rust -unsafe", ""},
		{"c++", "c++", ""},
		{"café", "", "café"},
		{"Go 127", "Go", "127"},
		{"", "", ""},
	}
	for _, c := range cases {
		head, prefix := searchTerms(c.q)
		if head != c.head || prefix != c.prefix {
			t.Errorf("searchTerms(%q) = %q, %q; want %q, %q", c.q, head, prefix, c.head, c.prefix)
		}
	}
}
```

- [ ] **Step 2: Write the failing integration tests**

`internal/courseauthoring/adapters/postgres/search_integration_test.go`:

```go
//go:build integration

package postgres_test

import (
	"slices"
	"testing"

	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/app"
	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/contentblocks"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

// seedLive publishes a course with the given details and classification.
func (f fixture) seedLive(t *testing.T, title, description, level string, cats []domain.Category, tagList []string) domain.Course {
	t.Helper()
	c := f.seedCourse(t)
	ttl, err := contentblocks.NewTitle(title)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.UpdateDetails(ttl, description, level, "", f.now); err != nil {
		t.Fatal(err)
	}
	if err := f.classify(t, &c, cats, tagList); err != nil {
		t.Fatal(err)
	}
	f.publish(t, &c)
	return c
}

func TestCatalogSearchRanksAndPrefixes(t *testing.T) {
	f := newFixture(t)
	titled := f.seedLive(t, "Learning Go", "an introduction", "", nil, nil)
	described := f.seedLive(t, "Cooking", "we go shopping first", "", nil, nil)
	tagged := f.seedLive(t, "Systems", "low level", "", nil, []string{"go"})
	f.seedLive(t, "Rust", "ownership", "", nil, nil)

	got := f.listPublished(t, app.CatalogQuery{Limit: 10, Q: "go"})
	if ids := summaryIDs(got); !slices.Equal(ids, []id.ID{titled.ID, tagged.ID, described.ID}) {
		t.Fatalf("q=go = %v (ranks %v)", ids, ranks(got))
	}
	if !(got[0].Rank > got[1].Rank && got[1].Rank > got[2].Rank) {
		t.Fatalf("ranks not descending: %v", ranks(got))
	}
	if ids := summaryIDs(f.listPublished(t, app.CatalogQuery{Limit: 10, Q: "lea"})); !slices.Equal(ids, []id.ID{titled.ID}) {
		t.Fatalf("prefix = %v", ids)
	}
	if ids := summaryIDs(f.listPublished(t, app.CatalogQuery{Limit: 10, Q: `"learning go"`})); !slices.Equal(ids, []id.ID{titled.ID}) {
		t.Fatalf("phrase = %v", ids)
	}
	if got := f.listPublished(t, app.CatalogQuery{Limit: 10, Q: "golang"}); len(got) != 0 {
		t.Fatalf("golang = %v", summaryIDs(got))
	}
	if got := f.listPublished(t, app.CatalogQuery{Limit: 10}); got[0].Rank != 0 {
		t.Fatalf("rank without q = %v", got[0].Rank)
	}
}

func TestCatalogSearchOddInput(t *testing.T) {
	f := newFixture(t)
	f.seedLive(t, "Learning Go", "intro", "", nil, nil)
	for _, q := range []string{"go!", `"unterminated`, "c++", "!!!", "a & b | c", "go:*", `\`, "-"} {
		got := f.listPublished(t, app.CatalogQuery{Limit: 10, Q: q})
		if q == "go!" && len(got) != 1 {
			t.Errorf("%q = %v", q, summaryIDs(got))
		}
	}
}

func TestCatalogFiltersByCategoryAndTagFromLiveVersion(t *testing.T) {
	f := newFixture(t)
	web := f.seedCategory(t, "Web")
	classified := f.seedLive(t, "Go", "d", "beginner", []domain.Category{web}, []string{"go", "web"})
	plain := f.seedLive(t, "Go", "d", "beginner", nil, nil)

	got := f.listPublished(t, app.CatalogQuery{Limit: 10, Category: "web"})
	if ids := summaryIDs(got); !slices.Equal(ids, []id.ID{classified.ID}) {
		t.Fatalf("category = %v", ids)
	}
	if !slices.Equal(slugs(got[0].Categories), []string{"web"}) || got[0].Categories[0].Name != "Web" || !slices.Equal(got[0].Tags, []string{"go", "web"}) {
		t.Fatalf("summary = %+v", got[0])
	}
	if ids := summaryIDs(f.listPublished(t, app.CatalogQuery{Limit: 10, Tag: "go", Level: "beginner"})); !slices.Equal(ids, []id.ID{classified.ID}) {
		t.Fatalf("tag+level = %v", ids)
	}
	if got := f.listPublished(t, app.CatalogQuery{Limit: 10, Tag: "go", Level: "advanced"}); len(got) != 0 {
		t.Fatalf("tag+advanced = %v", summaryIDs(got))
	}
	all := f.listPublished(t, app.CatalogQuery{Limit: 10})
	if len(all) != 2 || all[0].ID != plain.ID || all[0].Tags == nil || len(all[0].Tags) != 0 || len(all[0].Categories) != 0 {
		t.Fatalf("plain summary = %+v", all[0])
	}

	// A working-copy change is invisible until republished.
	c := classified
	if err := f.classify(t, &c, nil, nil); err != nil {
		t.Fatal(err)
	}
	if ids := summaryIDs(f.listPublished(t, app.CatalogQuery{Limit: 10, Tag: "go"})); !slices.Equal(ids, []id.ID{classified.ID}) {
		t.Fatalf("after draft edit = %v", ids)
	}
	f.publish(t, &c)
	if got := f.listPublished(t, app.CatalogQuery{Limit: 10, Tag: "go"}); len(got) != 0 {
		t.Fatalf("after republish = %v", summaryIDs(got))
	}
}

func TestCatalogSearchKeysetWalk(t *testing.T) {
	f := newFixture(t)
	want := map[id.ID]bool{}
	for range 3 {
		want[f.seedLive(t, "Go", "x", "", nil, nil).ID] = true // equal ranks
	}
	for range 3 {
		want[f.seedLive(t, "Other", "go", "", nil, nil).ID] = true // equal, lower ranks
	}
	f.seedLive(t, "Rust", "x", "", nil, nil)

	var walked []app.CourseSummary
	q := app.CatalogQuery{Limit: 2, Q: "go"}
	for range 10 {
		page := f.listPublished(t, q)
		walked = append(walked, page...)
		if len(page) < q.Limit {
			break
		}
		last := page[len(page)-1]
		q.After, q.AfterRank = last.ID, last.Rank
	}
	if len(walked) != len(want) {
		t.Fatalf("walked %d rows, want %d: %v", len(walked), len(want), summaryIDs(walked))
	}
	for i, s := range walked {
		if !want[s.ID] {
			t.Fatalf("unexpected or repeated %v", s.ID)
		}
		delete(want, s.ID)
		if i > 0 && (s.Rank > walked[i-1].Rank || (s.Rank == walked[i-1].Rank && s.ID > walked[i-1].ID)) {
			t.Fatalf("out of order at %d: %v", i, summaryIDs(walked))
		}
	}
}

func ranks(ss []app.CourseSummary) []float32 {
	out := make([]float32, len(ss))
	for i, s := range ss {
		out[i] = s.Rank
	}
	return out
}
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `go test ./internal/courseauthoring/adapters/postgres/ -run TestSearchTerms; go test -race -tags integration -run 'TestCatalogSearch|TestCatalogFilters' ./internal/courseauthoring/adapters/postgres/`
Expected: FAIL — `undefined: searchTerms`; the integration tests return unfiltered results.

- [ ] **Step 4: Implement search terms**

`internal/courseauthoring/adapters/postgres/search.go`:

```go
package postgres

import (
	"strings"
	"unicode"
)

// searchTerms splits search text into websearch text and a prefix term, so the word being
// typed last also matches longer words ("lea" finds "learning"). The last word becomes the
// prefix term only when it is all letters and digits: anything else could carry websearch
// syntax (quotes, a leading "-") or to_tsquery syntax, so the text is then used whole.
func searchTerms(q string) (head, prefix string) {
	q = strings.TrimSpace(q)
	i := strings.LastIndexFunc(q, unicode.IsSpace)
	last := q[i+1:]
	if last == "" || strings.IndexFunc(last, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) >= 0 {
		return q, ""
	}
	return strings.TrimSpace(q[:i+1]), strings.ToLower(last)
}
```

- [ ] **Step 5: Replace the catalog query**

In `internal/courseauthoring/adapters/postgres/queries.sql`, replace `ListPublishedCourses` with:

```sql
-- name: ListPublishedCourses :many
-- Lists live versions. updated_at is when the live version was published. With search set,
-- rows match q_head as websearch text and q_prefix as the prefix of one more word, and are
-- ordered by rank; otherwise rank is 0 and rows are ordered by ID alone.
SELECT c.id, c.owner_id, v.title, v.description, v.level, v.thumbnail_url, v.tags,
       v.price_amount_minor, v.price_currency, c.created_at, v.published_at AS updated_at,
       (SELECT count(*) FROM courseauthoring.course_version_lectures l
        WHERE l.course_id = v.course_id AND l.number = v.number) AS lecture_count,
       (SELECT count(*) FROM courseauthoring.course_version_sections s
        WHERE s.course_id = v.course_id AND s.number = v.number) AS section_count,
       r.rank::real AS rank
FROM courseauthoring.courses c
JOIN courseauthoring.course_versions v ON v.course_id = c.id AND v.number = c.live_version
CROSS JOIN (
  SELECT websearch_to_tsquery('simple', sqlc.arg(q_head)::text) &&
         CASE WHEN sqlc.arg(q_prefix)::text = '' THEN ''::tsquery
              ELSE to_tsquery('simple', sqlc.arg(q_prefix)::text || ':*') END AS query
) s
CROSS JOIN LATERAL (
  SELECT CASE WHEN sqlc.arg(search)::bool THEN ts_rank(v.search, s.query) ELSE 0 END AS rank
) r
WHERE (NOT sqlc.arg(search)::bool OR v.search @@ s.query)
  AND (sqlc.arg(level)::text = '' OR v.level = sqlc.arg(level)::text)
  AND (sqlc.arg(price)::text = ''
       OR (sqlc.arg(price)::text = 'free' AND v.price_amount_minor = 0)
       OR (sqlc.arg(price)::text = 'paid' AND v.price_amount_minor > 0))
  AND (sqlc.arg(tag)::text = '' OR v.tags @> ARRAY[sqlc.arg(tag)::text])
  AND (sqlc.arg(category)::text = '' OR EXISTS (
        SELECT 1 FROM courseauthoring.course_version_categories vc
        JOIN courseauthoring.categories k ON k.id = vc.category_id
        WHERE vc.course_id = v.course_id AND vc.number = v.number AND k.slug = sqlc.arg(category)::text))
  AND (sqlc.arg(after)::bigint = 0
       OR (r.rank::real, c.id) < (sqlc.arg(after_rank)::real, sqlc.arg(after)::bigint))
ORDER BY r.rank DESC, c.id DESC
LIMIT sqlc.arg(row_limit)::integer;

-- name: ListLiveCategoriesByCourseIDs :many
SELECT vc.course_id, k.id, k.name, k.slug
FROM courseauthoring.course_version_categories vc
JOIN courseauthoring.courses c ON c.id = vc.course_id AND c.live_version = vc.number
JOIN courseauthoring.categories k ON k.id = vc.category_id
WHERE vc.course_id = ANY(sqlc.arg(course_ids)::bigint[])
ORDER BY vc.course_id, vc.position;
```

Run: `make sqlc`
Expected: `ListPublishedCoursesParams` has `QHead`, `QPrefix string`, `Search bool`, `Level`, `Price`, `Tag`, `Category string`, `After int64`, `AfterRank float32`, `RowLimit int32`; `ListPublishedCoursesRow` has `Tags []string` and `Rank float32`.

- [ ] **Step 6: Use the new query**

In `internal/courseauthoring/adapters/postgres/courses.go`, replace `ListPublished` with:

```go
func (r courses) ListPublished(ctx context.Context, q app.CatalogQuery) ([]app.CourseSummary, error) {
	head, prefix := searchTerms(q.Q)
	rows, err := r.q.ListPublishedCourses(ctx, sqlcgen.ListPublishedCoursesParams{
		Search: q.Q != "", QHead: head, QPrefix: prefix,
		Level: q.Level, Price: string(q.Price), Tag: q.Tag, Category: q.Category,
		After: int64(q.After), AfterRank: q.AfterRank,
		RowLimit: int32(q.Limit), //nolint:gosec // the service bounds Limit to MaxCatalogLimit+1
	})
	if err != nil {
		return nil, err
	}
	courseIDs := make([]int64, len(rows))
	for i, row := range rows {
		courseIDs[i] = row.ID
	}
	cats, err := r.q.ListLiveCategoriesByCourseIDs(ctx, courseIDs)
	if err != nil {
		return nil, err
	}
	byCourse := make(map[int64][]domain.CategoryRef, len(rows))
	for _, k := range cats {
		byCourse[k.CourseID] = append(byCourse[k.CourseID], categoryRef(k.ID, k.Name, k.Slug))
	}
	out := make([]app.CourseSummary, 0, len(rows))
	for _, row := range rows {
		out = append(out, app.CourseSummary{
			ID: id.ID(row.ID), OwnerID: id.ID(row.OwnerID), Title: row.Title, Description: row.Description,
			Level: row.Level, ThumbnailURL: row.ThumbnailUrl,
			Price:        domain.Price{AmountMinor: row.PriceAmountMinor, Currency: row.PriceCurrency},
			Categories:   byCourse[row.ID], Tags: nonNilTags(row.Tags), Rank: row.Rank,
			LectureCount: int(row.LectureCount), SectionCount: int(row.SectionCount),
			CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
		})
	}
	return out, nil
}
```

- [ ] **Step 7: Run the tests**

Run: `go test ./internal/courseauthoring/... && go test -race -tags integration ./internal/courseauthoring/adapters/postgres/ && golangci-lint run ./internal/courseauthoring/...`
Expected: PASS, including the existing `TestListPublishedFiltersAndCounts` and `TestListPublishedKeysetWalk`. If `TestCatalogSearchRanksAndPrefixes` orders `tagged` and `described` differently, print `ranks(got)` and confirm the weights in `course_search_document` are `A`, `B`, `C` for title, tags, description; do not change the expected order.

- [ ] **Step 8: Commit**

```bash
git add internal/courseauthoring/adapters/postgres
git commit -m "feat(courseauthoring): search and filter the catalog by category and tag"
```

---

### Task 6: HTTP routes, wire fields and composition

**Files:**
- Modify: `internal/courseauthoring/adapters/httpapi/httpapi.go`, `catalog.go`, `courses.go`, `wire.go`, `fakes_test.go`, `httpapi_test.go`
- Create: `internal/courseauthoring/adapters/httpapi/categories.go`, `cursor.go`, `categories_test.go`
- Modify: `cmd/api/app.go`

**Interfaces:**
- Consumes: `app.CategoryService` methods, `app.DetailsInput.CategoryIDs/Tags`, `app.CatalogQuery` fields, `app.CatalogPage.NextRank` (Task 3).
- Produces: `httpapi.New(courses CourseService, contents ContentService, categories CategoryService, cfg Config) *Handler`; routes `GET|POST /v1/categories`, `PATCH|DELETE /v1/categories/{categoryID}`; `GET /v1/courses` params `q`, `category`, `tag`; `categories` and `tags` on course and summary wires.

- [ ] **Step 1: Extend the HTTP fake**

Apply to `internal/courseauthoring/adapters/httpapi/fakes_test.go` the same changes as Task 3 Step 1, items 1–8, with these differences for this file's `RunInTx`: the `&memTx{...}` literal gains `categories: clone(m.categories),`; the `fn` call becomes `fn(app.Repos{Courses: tx, Contents: tx, Events: tx, Categories: memCategories{tx}})`; after the line `m.courses, m.headers, m.blocks, m.reviews, m.versions = tx.courses, tx.headers, tx.blocks, tx.reviews, tx.versions` add `m.categories = tx.categories`. Add `"slices"` to the imports as well as `"strings"`. The `ListPublished`, `hydrate` and `memCategories` code is identical to Task 3 Step 1.

In `internal/courseauthoring/adapters/httpapi/httpapi_test.go`, change the `httpapi.New(` call in `newServer` so its third argument is `app.NewCategoryService(store, ids, clk),` (between the content service and `httpapi.Config{`).

- [ ] **Step 2: Write the failing tests**

`internal/courseauthoring/adapters/httpapi/categories_test.go`:

```go
package httpapi_test

import (
	"net/http"
	"net/url"
	"testing"
)

func createCategory(t *testing.T, h http.Handler, name string) map[string]any {
	t.Helper()
	resp, body := call(h, "POST", "/v1/categories", admin, adminRL, `{"name":"`+name+`"}`)
	return must(t, resp, 201, body)
}

func TestCategoryRoutes(t *testing.T) {
	h := newServer(t, 100)
	resp, body := call(h, "POST", "/v1/categories", instr, instrRL, `{"name":"Web"}`)
	expectProblem(t, resp, body, 403, "forbidden")
	resp, body = call(h, "POST", "/v1/categories", "", "", `{"name":"Web"}`)
	if resp.StatusCode != 401 {
		t.Fatalf("anonymous create = %d %v", resp.StatusCode, body)
	}
	web := createCategory(t, h, "Web Development")
	if web["slug"] != "web-development" || web["name"] != "Web Development" || web["id"] == "" {
		t.Fatalf("created = %v", web)
	}
	resp, body = call(h, "POST", "/v1/categories", admin, adminRL, `{"name":"web development"}`)
	expectProblem(t, resp, body, 409, "category_exists")
	resp, body = call(h, "POST", "/v1/categories", admin, adminRL, `{"name":"  "}`)
	expectProblem(t, resp, body, 400, "invalid_category_name")

	resp, body = call(h, "PATCH", "/v1/categories/"+web["id"].(string), admin, adminRL, `{"name":"Web"}`)
	if got := must(t, resp, 200, body); got["slug"] != "web" {
		t.Fatalf("renamed = %v", got)
	}
	resp, body = call(h, "PATCH", "/v1/categories/999", admin, adminRL, `{"name":"X"}`)
	expectProblem(t, resp, body, 404, "not_found")

	resp, body = call(h, "GET", "/v1/categories", "", "", "")
	list := must(t, resp, 200, body)["categories"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["course_count"] != float64(0) {
		t.Fatalf("list = %v", body)
	}
	resp, body = call(h, "DELETE", "/v1/categories/"+web["id"].(string), admin, adminRL, "")
	must(t, resp, 204, body)
	resp, body = call(h, "DELETE", "/v1/categories/"+web["id"].(string), admin, adminRL, "")
	expectProblem(t, resp, body, 404, "not_found")
}

func TestUpdateDetailsClassificationOverHTTP(t *testing.T) {
	h := newServer(t, 100)
	web := createCategory(t, h, "Web")
	cid := newCourse(t, h)
	resp, body := call(h, "PATCH", "/v1/courses/"+cid, instr, instrRL,
		`{"title":"Go","category_ids":["`+web["id"].(string)+`"],"tags":["Go","web"]}`)
	must(t, resp, 204, body)
	resp, body = call(h, "GET", "/v1/courses/"+cid, instr, instrRL, "")
	got := must(t, resp, 200, body)
	cats := got["categories"].([]any)
	if len(cats) != 1 || cats[0].(map[string]any)["slug"] != "web" || len(got["tags"].([]any)) != 2 {
		t.Fatalf("course = %v", got)
	}
	for body, typ := range map[string]string{
		`{"title":"Go","category_ids":["999"]}`:              "unknown_category",
		`{"title":"Go","category_ids":["1","2","3","4"]}`:    "too_many_categories",
		`{"title":"Go","tags":["c++"]}`:                      "invalid_tag",
		`{"title":"Go","tags":["a","b","c","d","e","f","g","h","i","j","k"]}`: "too_many_tags",
	} {
		resp, out := call(h, "PATCH", "/v1/courses/"+cid, instr, instrRL, body)
		expectProblem(t, resp, out, 400, typ)
	}
}

func TestUpdateDetailsTagsNullVersusEmpty(t *testing.T) {
	h := newServer(t, 100)
	cid := newCourse(t, h)
	tagsOf := func() []any {
		resp, body := call(h, "GET", "/v1/courses/"+cid, instr, instrRL, "")
		return must(t, resp, 200, body)["tags"].([]any)
	}
	resp, body := call(h, "PATCH", "/v1/courses/"+cid, instr, instrRL, `{"title":"Go","tags":["go"]}`)
	must(t, resp, 204, body)
	for _, keep := range []string{`{"title":"Go","tags":null}`, `{"title":"Go"}`} {
		resp, body = call(h, "PATCH", "/v1/courses/"+cid, instr, instrRL, keep)
		must(t, resp, 204, body)
		if got := tagsOf(); len(got) != 1 {
			t.Fatalf("%s cleared tags: %v", keep, got)
		}
	}
	resp, body = call(h, "PATCH", "/v1/courses/"+cid, instr, instrRL, `{"title":"Go","tags":[]}`)
	must(t, resp, 204, body)
	if got := tagsOf(); len(got) != 0 {
		t.Fatalf("[] kept tags: %v", got)
	}
}

func TestCatalogSearchAndFilterParameters(t *testing.T) {
	h := newServer(t, 100)
	web := createCategory(t, h, "Web")
	cid := publishedCourse(t, h)
	resp, body := call(h, "PATCH", "/v1/courses/"+cid, instr, instrRL,
		`{"title":"Go","category_ids":["`+web["id"].(string)+`"],"tags":["go"]}`)
	must(t, resp, 204, body)
	publish(t, h, cid)

	for _, qs := range []string{"q=go", "category=web", "tag=Go", "q=go&category=web&tag=go"} {
		resp, body := call(h, "GET", "/v1/courses?"+qs, "", "", "")
		courses := must(t, resp, 200, body)["courses"].([]any)
		if len(courses) != 1 {
			t.Fatalf("%s = %v", qs, body)
		}
		c := courses[0].(map[string]any)
		if c["categories"].([]any)[0].(map[string]any)["name"] != "Web" || c["tags"].([]any)[0] != "go" {
			t.Fatalf("%s summary = %v", qs, c)
		}
	}
	for _, qs := range []string{"q=", "q=%20%20", "category=", "tag="} {
		resp, body := call(h, "GET", "/v1/courses?"+qs, "", "", "")
		expectProblem(t, resp, body, 400, "invalid_input")
	}
	resp, body = call(h, "GET", "/v1/courses?tag=c%2B%2B", "", "", "")
	if got := must(t, resp, 200, body)["courses"].([]any); len(got) != 0 {
		t.Fatalf("invalid tag = %v", body)
	}
}

func TestCatalogSearchCursorBoundToQuery(t *testing.T) {
	h := newServer(t, 100)
	publishedCourse(t, h)
	publishedCourse(t, h)
	resp, body := call(h, "GET", "/v1/courses?q=go&limit=1", "", "", "")
	cursor, _ := must(t, resp, 200, body)["next_cursor"].(string)
	if cursor == "" {
		t.Fatalf("no cursor: %v", body)
	}
	resp, body = call(h, "GET", "/v1/courses?q=go&limit=1&cursor="+url.QueryEscape(cursor), "", "", "")
	if got := must(t, resp, 200, body)["courses"].([]any); len(got) != 1 {
		t.Fatalf("page 2 = %v", body)
	}
	for _, qs := range []string{"q=rust&cursor=" + url.QueryEscape(cursor), "cursor=" + url.QueryEscape(cursor), "q=go&cursor=123", "q=go&cursor=%21%21"} {
		resp, body := call(h, "GET", "/v1/courses?"+qs, "", "", "")
		expectProblem(t, resp, body, 400, "invalid_input")
	}
}
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `go test ./internal/courseauthoring/adapters/httpapi/`
Expected: FAIL to compile — `too many arguments in call to httpapi.New`.

- [ ] **Step 4: Register categories and map the new errors**

In `internal/courseauthoring/adapters/httpapi/httpapi.go`:

1. Add after `ContentService`:

```go
// CategoryService is the category use-case surface the handlers call.
type CategoryService interface {
	Create(ctx context.Context, p auth.Principal, name string) (domain.Category, error)
	Rename(ctx context.Context, p auth.Principal, categoryID id.ID, name string) (domain.Category, error)
	Delete(ctx context.Context, p auth.Principal, categoryID id.ID) error
	List(ctx context.Context) ([]app.CategoryWithCount, error)
}
```

2. Replace `Handler` and `New` with:

```go
type Handler struct {
	courses    CourseService
	contents   ContentService
	categories CategoryService
	cfg        Config
}

func New(courses CourseService, contents ContentService, categories CategoryService, cfg Config) *Handler {
	return &Handler{courses: courses, contents: contents, categories: categories, cfg: cfg}
}
```

3. Change the `Register` doc comment's first sentence to "GET /v1/courses, GET /v1/courses/{courseID} and GET /v1/categories are public and rate-limited per client IP; every other route requires authentication." and add after `r.Handle("GET /v1/courses/{courseID}", public(h.getCourse))`:

```go
	r.Handle("GET /v1/categories", public(h.listCategories))
	r.Handle("POST /v1/categories", a(h.createCategory))
	r.Handle("PATCH /v1/categories/{categoryID}", a(h.renameCategory))
	r.Handle("DELETE /v1/categories/{categoryID}", a(h.deleteCategory))
```

4. In `errorMappings`, add after the `domain.ErrApprovalRequired` entry:

```go
	{app.ErrCategoryExists, http.StatusConflict, "category_exists", "Category Exists"},
	{app.ErrUnknownCategory, http.StatusBadRequest, "unknown_category", "Unknown Category"},
	{domain.ErrTooManyCategories, http.StatusBadRequest, "too_many_categories", "Too Many Categories"},
	{domain.ErrInvalidTag, http.StatusBadRequest, "invalid_tag", "Invalid Tag"},
	{domain.ErrTooManyTags, http.StatusBadRequest, "too_many_tags", "Too Many Tags"},
	{domain.ErrInvalidCategoryName, http.StatusBadRequest, "invalid_category_name", "Invalid Category Name"},
```

`internal/courseauthoring/adapters/httpapi/categories.go`:

```go
package httpapi

import (
	"net/http"

	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/app"
	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/httpserver"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

type categoryWire struct {
	ID   id.ID  `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
}

type categoryListItemWire struct {
	categoryWire
	CourseCount int `json:"course_count"`
}

type categoryListWire struct {
	Categories []categoryListItemWire `json:"categories"`
}

type categoryNameRequest struct {
	Name string `json:"name"`
}

func toCategoryWire(c domain.Category) categoryWire {
	return categoryWire{ID: c.ID, Name: c.Name, Slug: c.Slug}
}

// toCategoryRefWires never returns nil, so the field encodes as [] rather than null.
func toCategoryRefWires(refs []domain.CategoryRef) []categoryWire {
	out := make([]categoryWire, 0, len(refs))
	for _, r := range refs {
		out = append(out, categoryWire{ID: r.ID, Name: r.Name, Slug: r.Slug})
	}
	return out
}

func toCategoryListWire(cs []app.CategoryWithCount) categoryListWire {
	w := categoryListWire{Categories: make([]categoryListItemWire, 0, len(cs))}
	for _, c := range cs {
		w.Categories = append(w.Categories, categoryListItemWire{categoryWire: toCategoryWire(c.Category), CourseCount: c.CourseCount})
	}
	return w
}

func (h *Handler) listCategories(w http.ResponseWriter, r *http.Request) {
	cs, err := h.categories.List(r.Context())
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toCategoryListWire(cs))
}

func (h *Handler) createCategory(w http.ResponseWriter, r *http.Request) {
	var req categoryNameRequest
	if !httpserver.DecodeJSON(w, r, &req) {
		return
	}
	c, err := h.categories.Create(r.Context(), principal(r), req.Name)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, toCategoryWire(c))
}

func (h *Handler) renameCategory(w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "categoryID")
	if !ok {
		return
	}
	var req categoryNameRequest
	if !httpserver.DecodeJSON(w, r, &req) {
		return
	}
	c, err := h.categories.Rename(r.Context(), principal(r), ids[0], req.Name)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toCategoryWire(c))
}

func (h *Handler) deleteCategory(w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "categoryID")
	if !ok {
		return
	}
	h.noContent(w, r, h.categories.Delete(r.Context(), principal(r), ids[0]))
}
```

- [ ] **Step 5: Add the search cursor**

`internal/courseauthoring/adapters/httpapi/cursor.go`:

```go
package httpapi

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"

	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

var errForeignCursor = errors.New("cursor belongs to another search")

// queryHash ties a search cursor to the search text it was issued for.
func queryHash(q string) string {
	sum := sha256.Sum256([]byte(q))
	return hex.EncodeToString(sum[:8])
}

// encodeSearchCursor encodes the last row of a search page as base64url of
// "<id>:<rank>:<hash>". The rank is formatted to round-trip a float32 exactly, so the next
// page's (rank, id) comparison sees the same value the database computed.
func encodeSearchCursor(after id.ID, rank float32, q string) string {
	raw := after.String() + ":" + strconv.FormatFloat(float64(rank), 'g', -1, 32) + ":" + queryHash(q)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeSearchCursor(s, q string) (id.ID, float32, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return 0, 0, err
	}
	parts := strings.Split(string(raw), ":")
	if len(parts) != 3 {
		return 0, 0, errForeignCursor
	}
	if parts[2] != queryHash(q) {
		return 0, 0, errForeignCursor
	}
	after, err := id.Parse(parts[0])
	if err != nil {
		return 0, 0, err
	}
	rank, err := strconv.ParseFloat(parts[1], 32)
	if err != nil {
		return 0, 0, err
	}
	return after, float32(rank), nil
}
```

- [ ] **Step 6: Parse the new catalog parameters**

In `internal/courseauthoring/adapters/httpapi/catalog.go`, add `"strings"` to the imports and replace `listCatalog` and `catalogQuery` with:

```go
func (h *Handler) listCatalog(w http.ResponseWriter, r *http.Request) {
	q, err := catalogQuery(r.URL.Query())
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	page, err := h.courses.ListPublished(r.Context(), q)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toCatalogPageWire(page, q.Q))
}

// catalogQuery parses the catalog parameters. A parameter that is present must not be empty;
// q must not be blank. The service validates the values. Without q the cursor is a course ID;
// with q it is a search cursor bound to that q.
func catalogQuery(v url.Values) (app.CatalogQuery, error) {
	q := app.CatalogQuery{Limit: defaultCatalogLimit}
	for _, name := range []string{"level", "price", "limit", "cursor", "q", "category", "tag"} {
		if v.Has(name) && strings.TrimSpace(v.Get(name)) == "" {
			return q, fmt.Errorf("%w: %s must not be empty", app.ErrInvalidInput, name)
		}
	}
	q.Level = v.Get("level")
	q.Price = app.PriceFilter(v.Get("price"))
	q.Q = strings.TrimSpace(v.Get("q"))
	q.Category = v.Get("category")
	q.Tag = v.Get("tag")
	if s := v.Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil {
			return q, fmt.Errorf("%w: limit must be a number", app.ErrInvalidInput)
		}
		q.Limit = n
	}
	if s := v.Get("cursor"); s != "" {
		var err error
		if q.Q == "" {
			q.After, err = id.Parse(s)
		} else {
			q.After, q.AfterRank, err = decodeSearchCursor(s, q.Q)
		}
		if err != nil {
			return q, fmt.Errorf("%w: invalid cursor", app.ErrInvalidInput)
		}
	}
	return q, nil
}
```

- [ ] **Step 7: Carry categories and tags on the wires**

In `internal/courseauthoring/adapters/httpapi/wire.go`:

1. Add to `courseWire`, after `Level string ...`:

```go
	Categories   []categoryWire `json:"categories"`
	Tags         []string       `json:"tags"`
```

   and in `toCourseWire` add `Categories: toCategoryRefWires(c.Categories), Tags: nonNilTags(c.Tags),` to the literal.

2. Add `CategoryIDs *[]id.ID `json:"category_ids"`` and `Tags *[]string `json:"tags"`` to `updateDetailsRequest`.

3. Add to `courseSummaryWire`, after `IsFree`:

```go
	Categories   []categoryWire `json:"categories"`
	Tags         []string       `json:"tags"`
```

4. Replace `toCatalogPageWire` with:

```go
func toCatalogPageWire(p app.CatalogPage, q string) catalogPageWire {
	w := catalogPageWire{Courses: make([]courseSummaryWire, 0, len(p.Courses))}
	for _, c := range p.Courses {
		w.Courses = append(w.Courses, courseSummaryWire{
			ID: c.ID, OwnerID: c.OwnerID, Title: c.Title, Description: c.Description,
			Level: c.Level, ThumbnailURL: c.ThumbnailURL,
			Price: priceWire{c.Price.AmountMinor, c.Price.Currency}, IsFree: c.Price.IsFree(),
			Categories: toCategoryRefWires(c.Categories), Tags: nonNilTags(c.Tags),
			LectureCount: c.LectureCount, SectionCount: c.SectionCount,
			CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
		})
	}
	switch {
	case p.Next == 0:
	case q == "":
		w.NextCursor = p.Next.String()
	default:
		w.NextCursor = encodeSearchCursor(p.Next, p.NextRank, q)
	}
	return w
}

// nonNilTags makes an absent tag list encode as [] rather than null.
func nonNilTags(tags []string) []string {
	if tags == nil {
		return []string{}
	}
	return tags
}
```

In `internal/courseauthoring/adapters/httpapi/courses.go`, replace the `UpdateDetails` call in `updateDetails` with:

```go
	err := h.courses.UpdateDetails(r.Context(), principal(r), ids[0], app.DetailsInput{
		Title: req.Title, Description: req.Description, Level: req.Level, ThumbnailURL: req.ThumbnailURL,
		CategoryIDs: req.CategoryIDs, Tags: req.Tags})
```

- [ ] **Step 8: Wire the service**

In `cmd/api/app.go`'s `registerCourseAuthoring`, change the `courseauthoringhttp.New(` call's arguments to:

```go
	courseauthoringhttp.New(
		courses,
		contents,
		courseauthoringapp.NewCategoryService(tx, ids, clk),
		courseauthoringhttp.Config{
```

(the `Config` literal is unchanged).

- [ ] **Step 9: Run the tests**

Run: `go build ./... && go test ./internal/courseauthoring/... ./cmd/... && golangci-lint run ./...`
Expected: PASS, including every existing catalog test (`TestCatalogInvalidParameters` still sees `cursor=abc` and `cursor=0` rejected).

- [ ] **Step 10: Commit**

```bash
git add internal/courseauthoring/adapters/httpapi cmd/api/app.go
git commit -m "feat(courseauthoring): expose categories, tags and catalog search over HTTP"
```

---

### Task 7: Contract and end-to-end test

**Files:**
- Modify: `api/openapi.yaml`
- Modify: `cmd/api/e2e_integration_test.go`

**Interfaces:**
- Consumes: the routes and wire shapes from Task 6.

- [ ] **Step 1: Write the failing end-to-end test**

Append to `cmd/api/e2e_integration_test.go`:

```go
func TestCatalogSearchEndToEnd(t *testing.T) {
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
	signIn := func(sub, email string) string {
		t.Helper()
		tok := google.Sign(t, googletest.Claims(sub, email, "web-client", time.Now()))
		resp, body := c.do(http.MethodPost, "/v1/auth/google", `{"id_token":"`+tok+`"}`, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("sign in %s: %d %v", email, resp.StatusCode, body)
		}
		return body["access_token"].(string)
	}
	admin := bearer(signIn("sub-admin", "admin@example.com"))
	student := bearer(signIn("sub-student", "student@example.com"))

	mustStatus(t, c, http.MethodPost, "/v1/categories", `{"name":"Web"}`, student, http.StatusForbidden)
	web := mustStatus(t, c, http.MethodPost, "/v1/categories", `{"name":"Web Development"}`, admin, http.StatusCreated)
	course := mustStatus(t, c, http.MethodPost, "/v1/courses", `{"title":"Learning Go","description":"a gentle start"}`, admin, http.StatusCreated)
	courseID := course["id"].(string)
	mustStatus(t, c, http.MethodPatch, "/v1/courses/"+courseID,
		`{"title":"Learning Go","description":"a gentle start","category_ids":["`+web["id"].(string)+`"],"tags":["golang","Backend"]}`,
		admin, http.StatusNoContent)
	mustStatus(t, c, http.MethodPost, "/v1/courses/"+courseID+"/lectures", `{"title":"L1","text_body":"<p>x</p>"}`, admin, http.StatusCreated)
	mustStatus(t, c, http.MethodPost, "/v1/courses/"+courseID+"/publish", "", admin, http.StatusNoContent)

	for _, qs := range []string{"q=learn", "q=gentle", "q=golang", "category=web-development", "tag=backend", "q=go&tag=golang"} {
		page := mustStatus(t, c, http.MethodGet, "/v1/courses?"+qs, "", nil, http.StatusOK)
		courses := page["courses"].([]any)
		if len(courses) != 1 || courses[0].(map[string]any)["id"] != courseID {
			t.Fatalf("%s = %v", qs, page)
		}
	}
	if page := mustStatus(t, c, http.MethodGet, "/v1/courses?q=rust", "", nil, http.StatusOK); len(page["courses"].([]any)) != 0 {
		t.Fatalf("rust = %v", page)
	}
	list := mustStatus(t, c, http.MethodGet, "/v1/categories", "", nil, http.StatusOK)["categories"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["course_count"] != float64(1) {
		t.Fatalf("categories = %v", list)
	}
	mustStatus(t, c, http.MethodDelete, "/v1/categories/"+web["id"].(string), "", admin, http.StatusNoContent)
	detail := mustStatus(t, c, http.MethodGet, "/v1/courses/"+courseID, "", nil, http.StatusOK)
	if len(detail["categories"].([]any)) != 0 || len(detail["tags"].([]any)) != 2 {
		t.Fatalf("detail after delete = %v", detail)
	}
}
```

- [ ] **Step 2: Run it**

Run: `go test -race -tags integration -run TestCatalogSearchEndToEnd ./cmd/api/`
Expected: PASS (Tasks 1–6 implement everything it exercises). If it fails, fix the implementation, not the test.

- [ ] **Step 3: Document the contract**

In `api/openapi.yaml`:

1. Under `/v1/courses` → `get`, change `summary` to `List published courses, newest first, or search them by relevance`, append to its `description`: ` With q, results are ordered by relevance (title, then tags, then description), every word must match and the last word also matches as a prefix; next_cursor is then bound to that q.` and add these parameters after `price`:

```yaml
        - name: q
          in: query
          description: Search text, 1 to 200 characters after trimming.
          schema: { type: string, maxLength: 200 }
        - name: category
          in: query
          description: A category slug. An unknown slug matches no course.
          schema: { type: string }
        - name: tag
          in: query
          description: A tag; normalized before matching. A tag no course could carry matches none.
          schema: { type: string }
```

2. Add paths after `/v1/courses/{courseID}`:

```yaml
  /v1/categories:
    get:
      summary: List categories by name with their live course counts
      description: Public. An Authorization header is optional. Rate-limited per client IP.
      security: []
      responses:
        "200":
          description: OK
          content:
            application/json:
              schema: { $ref: "#/components/schemas/CategoryList" }
        "401": { $ref: "#/components/responses/Problem" }
        "405": { $ref: "#/components/responses/MethodNotAllowed" }
        "429": { $ref: "#/components/responses/Problem" }
        "500": { $ref: "#/components/responses/InternalError" }
    post:
      summary: Create a category (root admin)
      security:
        - bearer: []
      requestBody:
        required: true
        content:
          application/json:
            schema: { $ref: "#/components/schemas/CategoryNameRequest" }
      responses:
        "201":
          description: Created
          content:
            application/json:
              schema: { $ref: "#/components/schemas/Category" }
        "400": { $ref: "#/components/responses/Problem" }
        "401": { $ref: "#/components/responses/Problem" }
        "403": { $ref: "#/components/responses/Problem" }
        "409": { $ref: "#/components/responses/Problem" }
        "413": { $ref: "#/components/responses/Problem" }
        "415": { $ref: "#/components/responses/Problem" }
        "405": { $ref: "#/components/responses/MethodNotAllowed" }
        "500": { $ref: "#/components/responses/InternalError" }
  /v1/categories/{categoryID}:
    parameters:
      - name: categoryID
        in: path
        required: true
        schema: { $ref: "#/components/schemas/ID" }
    patch:
      summary: Rename a category (root admin); the slug follows the name
      security:
        - bearer: []
      requestBody:
        required: true
        content:
          application/json:
            schema: { $ref: "#/components/schemas/CategoryNameRequest" }
      responses:
        "200":
          description: OK
          content:
            application/json:
              schema: { $ref: "#/components/schemas/Category" }
        "400": { $ref: "#/components/responses/Problem" }
        "401": { $ref: "#/components/responses/Problem" }
        "403": { $ref: "#/components/responses/Problem" }
        "404": { $ref: "#/components/responses/Problem" }
        "409": { $ref: "#/components/responses/Problem" }
        "413": { $ref: "#/components/responses/Problem" }
        "415": { $ref: "#/components/responses/Problem" }
        "405": { $ref: "#/components/responses/MethodNotAllowed" }
        "500": { $ref: "#/components/responses/InternalError" }
    delete:
      summary: Delete a category (root admin); it is removed from every course and published version
      security:
        - bearer: []
      responses:
        "204": { description: No Content }
        "401": { $ref: "#/components/responses/Problem" }
        "403": { $ref: "#/components/responses/Problem" }
        "404": { $ref: "#/components/responses/Problem" }
        "405": { $ref: "#/components/responses/MethodNotAllowed" }
        "500": { $ref: "#/components/responses/InternalError" }
```

3. Under `components/schemas`, add `categories` and `tags` to the `required` list and `properties` of both `Course` and `CourseSummary`:

```yaml
        categories:
          type: array
          items: { $ref: "#/components/schemas/Category" }
        tags:
          type: array
          items: { type: string }
```

   add to `UpdateCourseDetailsRequest` `properties`:

```yaml
        category_ids:
          type: array
          maxItems: 3
          description: Replaces the course's categories; omit or send null to keep them.
          items: { $ref: "#/components/schemas/ID" }
        tags:
          type: array
          maxItems: 10
          description: >
            Replaces the course's tags; omit or send null to keep them. Each tag is trimmed,
            lowercased and must be 1-32 characters of a-z, 0-9 and single hyphens.
          items: { type: string }
```

   and add these schemas:

```yaml
    Category:
      type: object
      required: [id, name, slug]
      properties:
        id: { $ref: "#/components/schemas/ID" }
        name: { type: string }
        slug: { type: string }
    CategoryList:
      type: object
      required: [categories]
      properties:
        categories:
          type: array
          items:
            allOf:
              - $ref: "#/components/schemas/Category"
              - type: object
                required: [course_count]
                properties:
                  course_count: { type: integer }
    CategoryNameRequest:
      type: object
      additionalProperties: false
      required: [name]
      properties:
        name: { type: string, minLength: 1, maxLength: 60 }
```

4. Under `/v1/courses/{courseID}` → `patch`, append to the operation a `description`: `Errors: unknown_category, too_many_categories, invalid_tag, too_many_tags (400) in addition to the existing ones.`

- [ ] **Step 4: Validate the YAML**

Run: `go run github.com/mikefarah/yq/v4@v4.44.3 '.paths | keys' api/openapi.yaml >/dev/null`
Expected: exit 0 (the file still parses).

- [ ] **Step 5: Commit**

```bash
git add api/openapi.yaml cmd/api/e2e_integration_test.go
git commit -m "docs(api): document catalog search, categories and tags"
```

---

### Task 8: Gates

- [ ] **Step 1: Run every gate**

Run, in order:

```bash
make check
make test-integration
docker compose config >/dev/null
make docker-build
git diff --check
```

Expected: all pass. `make test-integration` needs a running Docker daemon; if Docker is unavailable, report that the integration suite did not run — never that it passed.

- [ ] **Step 2: Fix and commit anything the gates find**

Commit fixes with a Conventional Commit message naming what the gate caught, for example `fix(courseauthoring): satisfy lint on catalog search`.
