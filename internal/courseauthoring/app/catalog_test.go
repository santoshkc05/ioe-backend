package app_test

import (
	"errors"
	"testing"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/app"
	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/contentblocks"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

// putCourse stores a course with one lecture directly in the fake.
func putCourse(t *testing.T, store *memStore, cid id.ID, status domain.Status, level string, price domain.Price) {
	t.Helper()
	ttl, _ := contentblocks.NewTitle("Go")
	now := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	c := domain.NewCourse(cid, owner.UserID, ttl, "d", now)
	if err := c.UpdateDetails(ttl, "d", level, "", now); err != nil {
		t.Fatal(err)
	}
	if err := c.SetPrice(price, now); err != nil {
		t.Fatal(err)
	}
	lt, _ := contentblocks.NewTitle("L1")
	if err := c.AddLecture(cid+1000, lt, false, false, now); err != nil {
		t.Fatal(err)
	}
	c.Status = status
	if status == domain.StatusPublished {
		c.LastVersion, c.Live = 1, domain.LiveVersion{Number: 1, PublishedAt: now}
		store.versions[versionKey{cid, 1}] = snapshot{course: c}
	}
	store.courses[cid] = c
}

func pageIDs(p app.CatalogPage) []id.ID {
	out := make([]id.ID, 0, len(p.Courses))
	for _, c := range p.Courses {
		out = append(out, c.ID)
	}
	return out
}

func TestListPublishedValidation(t *testing.T) {
	svc, _ := newCourseService(t)
	for _, q := range []app.CatalogQuery{
		{Limit: 0},
		{Limit: app.MaxCatalogLimit + 1},
		{Limit: -1},
		{Limit: 10, Level: "expert"},
		{Limit: 10, Price: "cheap"},
	} {
		if _, err := svc.ListPublished(ctx, q); !errors.Is(err, app.ErrInvalidInput) {
			t.Errorf("%+v: err = %v", q, err)
		}
	}
}

func TestListPublishedPagesAndFilters(t *testing.T) {
	svc, store := newCourseService(t)
	paid := domain.Price{AmountMinor: 50000, Currency: "NPR"}
	putCourse(t, store, 10, domain.StatusPublished, "beginner", domain.Price{})
	putCourse(t, store, 20, domain.StatusPublished, "advanced", paid)
	putCourse(t, store, 30, domain.StatusDraft, "beginner", domain.Price{})
	putCourse(t, store, 40, domain.StatusArchived, "beginner", domain.Price{})
	putCourse(t, store, 50, domain.StatusPublished, "beginner", paid)

	first, err := svc.ListPublished(ctx, app.CatalogQuery{Limit: 2})
	if err != nil || len(first.Courses) != 2 || first.Courses[0].ID != 50 || first.Courses[1].ID != 20 || first.Next != 20 {
		t.Fatalf("first = %v %+v", err, first)
	}
	if first.Courses[0].LectureCount != 1 || first.Courses[0].Price != paid {
		t.Fatalf("summary = %+v", first.Courses[0])
	}
	last, err := svc.ListPublished(ctx, app.CatalogQuery{Limit: 2, After: first.Next})
	if err != nil || len(last.Courses) != 1 || last.Courses[0].ID != 10 || last.Next != 0 {
		t.Fatalf("last = %v %+v", err, last)
	}
	exact, err := svc.ListPublished(ctx, app.CatalogQuery{Limit: 3})
	if err != nil || len(exact.Courses) != 3 || exact.Next != 0 {
		t.Fatalf("exact = %v %+v", err, exact)
	}

	free, _ := svc.ListPublished(ctx, app.CatalogQuery{Limit: 10, Price: app.PriceFree})
	paidOnly, _ := svc.ListPublished(ctx, app.CatalogQuery{Limit: 10, Price: app.PricePaid, Level: "beginner"})
	if got := pageIDs(free); len(got) != 1 || got[0] != 10 {
		t.Errorf("free = %v", got)
	}
	if got := pageIDs(paidOnly); len(got) != 1 || got[0] != 50 {
		t.Errorf("paid beginner = %v", got)
	}
}
