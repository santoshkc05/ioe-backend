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
