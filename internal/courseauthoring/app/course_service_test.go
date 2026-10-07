package app_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/app"
	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
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
	return app.NewCourseService(store, testIDs(t), fixedClock{time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)}, assetCatalog{}, quizCatalog{}, &fakeAssessments{heads: map[id.ID][]domain.AssessmentPin{}}), store
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
	if err := publish(svc, c.ID); err != nil {
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
	_ = publish(svc, c.ID)

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
	if err := publish(svc, c.ID); !errors.Is(err, domain.ErrCourseHasNoLectures) {
		t.Fatalf("empty publish err = %v", err)
	}
	if _, err := svc.AddLecture(ctx, owner, c.ID, app.AddLectureInput{Title: "L1", Legacy: app.LegacyContent{TextBody: "<p>x</p>"}}); err != nil {
		t.Fatal(err)
	}
	if err := publish(svc, c.ID); err != nil {
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

func TestFacts(t *testing.T) {
	svc, _ := newCourseService(t)
	c, err := svc.Create(ctx, owner, app.CreateCourseInput{Title: "Go"})
	if err != nil {
		t.Fatal(err)
	}
	f, err := svc.Facts(ctx, c.ID)
	if err != nil || f.Published || !f.Free || !f.Price.IsFree() || f.OwnerID != owner.UserID || f.LectureIDs == nil || len(f.LectureIDs) != 0 {
		t.Fatalf("draft facts = %+v, %v", f, err)
	}
	if _, err := svc.SetPrice(ctx, owner, c.ID, 150000, "NPR"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AddLecture(ctx, owner, c.ID, app.AddLectureInput{Title: "L1"}); err != nil {
		t.Fatal(err)
	}
	withTwo, err := svc.AddLecture(ctx, owner, c.ID, app.AddLectureInput{Title: "L2"})
	if err != nil {
		t.Fatal(err)
	}
	if err := publish(svc, c.ID); err != nil {
		t.Fatal(err)
	}
	f, err = svc.Facts(ctx, c.ID)
	want := []id.ID{withTwo.Lectures[0].ID, withTwo.Lectures[1].ID}
	if err != nil || !f.Published || f.Free || f.Price != (domain.Price{AmountMinor: 150000, Currency: "NPR"}) ||
		f.OwnerID != owner.UserID || !slices.Equal(f.LectureIDs, want) {
		t.Fatalf("published facts = %+v, %v; want lecture IDs %v", f, err, want)
	}
	if _, err := svc.Facts(ctx, 424242); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("missing err = %v", err)
	}
}
