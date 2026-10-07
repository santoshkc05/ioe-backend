package app_test

import (
	"errors"
	"testing"

	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/app"
	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
)

func TestReviewWorkflow(t *testing.T) {
	svc, store := newCourseService(t)
	c, _ := svc.Create(ctx, owner, app.CreateCourseInput{Title: "Go"})
	_, _ = svc.AddLecture(ctx, owner, c.ID, app.AddLectureInput{Title: "L1"})

	if err := svc.Submit(ctx, otherInstr, c.ID); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("foreign submit err = %v", err)
	}
	if err := svc.Submit(ctx, owner, c.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.Publish(ctx, owner, c.ID); !errors.Is(err, domain.ErrInvalidStatusTransition) {
		t.Fatalf("publish in review err = %v", err)
	}
	queue, err := svc.ListInReview(ctx, admin)
	if err != nil || len(queue) != 1 || queue[0].ID != c.ID {
		t.Fatalf("queue = %+v, %v", queue, err)
	}
	if _, err := svc.ListInReview(ctx, owner); !errors.Is(err, app.ErrForbidden) {
		t.Fatalf("instructor queue err = %v", err)
	}

	// The owner manages the course but may not review it.
	if err := svc.Approve(ctx, owner, c.ID, ""); !errors.Is(err, app.ErrForbidden) {
		t.Fatalf("owner approve err = %v", err)
	}
	if err := svc.RequestChanges(ctx, otherInstr, c.ID, "x"); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("foreign request changes err = %v", err)
	}
	if err := svc.RequestChanges(ctx, admin, c.ID, "Add a summary"); err != nil {
		t.Fatal(err)
	}
	if got, _ := svc.Get(ctx, owner, c.ID); got.Status != domain.StatusChangesRequested || got.ReviewNote != "Add a summary" {
		t.Fatalf("after request changes = %s %q", got.Status, got.ReviewNote)
	}
	if err := publish(svc, c.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.Unpublish(ctx, owner, c.ID, "x"); !errors.Is(err, app.ErrForbidden) {
		t.Fatalf("owner unpublish err = %v", err)
	}
	if err := svc.Unpublish(ctx, admin, c.ID, "Outdated"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Get(ctx, student, c.ID); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("unpublished read err = %v", err)
	}
	last := store.published[len(store.published)-1]
	if ev, ok := last.(domain.CourseUnpublished); !ok || ev.CourseID != c.ID || ev.OwnerID != owner.UserID {
		t.Fatalf("last event = %+v", last)
	}

	trail, err := svc.ListReviews(ctx, owner, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		actor    auth.Principal
		decision domain.ReviewDecision
		note     string
	}{
		{owner, domain.DecisionSubmitted, ""},
		{admin, domain.DecisionChangesRequested, "Add a summary"},
		{owner, domain.DecisionSubmitted, ""},
		{admin, domain.DecisionApproved, ""},
		{admin, domain.DecisionUnpublished, "Outdated"},
	}
	if len(trail) != len(want) {
		t.Fatalf("trail = %+v", trail)
	}
	for i, w := range want {
		r := trail[i]
		if r.CourseID != c.ID || r.ActorID != w.actor.UserID || r.Decision != w.decision || r.Note != w.note || r.ID.IsZero() {
			t.Fatalf("trail[%d] = %+v, want %+v", i, r, w)
		}
	}
	if _, err := svc.ListReviews(ctx, otherInstr, c.ID); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("foreign trail err = %v", err)
	}
}

func TestFailedReviewRecordsNothing(t *testing.T) {
	svc, store := newCourseService(t)
	c, _ := svc.Create(ctx, owner, app.CreateCourseInput{Title: "Go"})
	if err := svc.Submit(ctx, owner, c.ID); !errors.Is(err, domain.ErrCourseHasNoLectures) {
		t.Fatalf("empty submit err = %v", err)
	}
	if len(store.reviews) != 0 {
		t.Fatalf("reviews = %+v", store.reviews)
	}
}
