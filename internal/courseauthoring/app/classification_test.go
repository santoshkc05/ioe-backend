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
	if err := svc.UpdateDetails(ctx, otherInstr, c.ID, app.DetailsInput{Title: "Go", Tags: &[]string{"x"}}); !errors.Is(err, app.ErrNotFound) {
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
