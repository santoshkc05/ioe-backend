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
	// A conflict aborts the transaction, so each case runs in its own and returns the error.
	insert := func(name, slug string) error {
		k, _ := domain.NewCategory(f.ids.New(), name, f.now)
		k.Slug = slug
		return f.tx.RunInTx(ctx, func(r app.Repos) error { return r.Categories.Insert(ctx, k) })
	}
	if err := insert("WEB", "other"); !errors.Is(err, app.ErrCategoryExists) {
		t.Errorf("duplicate name = %v", err)
	}
	if err := insert("Other", "web"); !errors.Is(err, app.ErrCategoryExists) {
		t.Errorf("duplicate slug = %v", err)
	}
	data := f.seedCategory(t, "Data")
	taken := data
	if err := taken.Rename("web"); err != nil {
		t.Fatal(err)
	}
	if err := f.tx.RunInTx(ctx, func(r app.Repos) error { return r.Categories.Update(ctx, taken) }); !errors.Is(err, app.ErrCategoryExists) {
		t.Errorf("rename onto taken = %v", err)
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
