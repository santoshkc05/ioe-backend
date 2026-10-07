package domain_test

import (
	"errors"
	"strings"
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
	if _, err := c.Submit(1, 100, t0); !errors.Is(err, domain.ErrCourseHasNoLectures) {
		t.Fatalf("submit empty err = %v", err)
	}
	_ = c.AddLecture(20, title(t, "L"), true, false, t0)
	if err := c.Publish(false, t0); !errors.Is(err, domain.ErrApprovalRequired) {
		t.Fatalf("publish draft err = %v", err)
	}
	approve(t, &c)
	if err := c.Publish(false, t0); err != nil || c.Status != domain.StatusPublished {
		t.Fatalf("publish = %v, %s", err, c.Status)
	}
	if err := c.Publish(false, t0); !errors.Is(err, domain.ErrInvalidStatusTransition) {
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

func approve(t *testing.T, c *domain.Course) {
	t.Helper()
	if _, err := c.Submit(1, c.OwnerID, t0); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if _, err := c.Approve(2, 999, "", t0); err != nil {
		t.Fatalf("approve: %v", err)
	}
}

func TestReviewLoop(t *testing.T) {
	c := newCourse(t)
	_ = c.AddLecture(20, title(t, "L"), true, false, t0)
	t1 := t0.Add(time.Hour)

	r, err := c.Submit(1, 100, t1)
	if err != nil || c.Status != domain.StatusInReview || !c.SubmittedAt.Equal(t1) {
		t.Fatalf("submit = %v, %s, %v", err, c.Status, c.SubmittedAt)
	}
	if r != (domain.Review{ID: 1, CourseID: 1, ActorID: 100, Decision: domain.DecisionSubmitted, CreatedAt: t1}) {
		t.Fatalf("submit review = %+v", r)
	}
	if _, err := c.Submit(1, 100, t1); !errors.Is(err, domain.ErrInvalidStatusTransition) {
		t.Fatalf("resubmit in review err = %v", err)
	}
	if err := c.RenameLecture(20, title(t, "x"), t1); !errors.Is(err, domain.ErrCourseNotEditable) {
		t.Fatalf("edit in review err = %v", err)
	}
	if _, err := c.RequestChanges(2, 999, "  ", t1); !errors.Is(err, domain.ErrReviewNoteRequired) {
		t.Fatalf("blank note err = %v", err)
	}
	if _, err := c.RequestChanges(2, 999, strings.Repeat("x", domain.MaxReviewNoteRunes+1), t1); !errors.Is(err, domain.ErrReviewNoteTooLong) {
		t.Fatalf("long note err = %v", err)
	}
	r, err = c.RequestChanges(2, 999, " Fix lecture 1 ", t1)
	if err != nil || c.Status != domain.StatusChangesRequested || c.ReviewNote != "Fix lecture 1" || !c.ReviewedAt.Equal(t1) {
		t.Fatalf("request changes = %v, %+v", err, c)
	}
	if r.Decision != domain.DecisionChangesRequested || r.ActorID != 999 || r.Note != "Fix lecture 1" {
		t.Fatalf("request changes review = %+v", r)
	}
	if err := c.RenameLecture(20, title(t, "Fixed"), t1); err != nil {
		t.Fatalf("changes_requested must be editable: %v", err)
	}
	if err := c.Publish(false, t1); !errors.Is(err, domain.ErrApprovalRequired) {
		t.Fatalf("publish changes_requested err = %v", err)
	}

	if _, err := c.Submit(3, 100, t1); err != nil || c.ReviewNote != "" {
		t.Fatalf("resubmit = %v, note %q", err, c.ReviewNote)
	}
	r, err = c.Approve(4, 999, " ok ", t1)
	if err != nil || c.Status != domain.StatusApproved || r.Note != "ok" || r.Decision != domain.DecisionApproved {
		t.Fatalf("approve = %v, %s, %+v", err, c.Status, r)
	}
	if err := c.SetPrice(domain.Price{}, t1); !errors.Is(err, domain.ErrCourseNotEditable) {
		t.Fatalf("edit approved err = %v", err)
	}
	if _, err := c.Unpublish(5, 999, "x", t1); !errors.Is(err, domain.ErrInvalidStatusTransition) {
		t.Fatalf("unpublish approved err = %v", err)
	}
	if err := c.Publish(false, t1); err != nil {
		t.Fatalf("publish = %v", err)
	}

	if _, err := c.Unpublish(5, 999, "", t1); !errors.Is(err, domain.ErrReviewNoteRequired) {
		t.Fatalf("unpublish without note err = %v", err)
	}
	r, err = c.Unpublish(5, 999, "Outdated", t1)
	if err != nil || c.Status != domain.StatusChangesRequested || r.Decision != domain.DecisionUnpublished {
		t.Fatalf("unpublish = %v, %s, %+v", err, c.Status, r)
	}
	if _, err := c.Approve(6, 999, "", t1); !errors.Is(err, domain.ErrInvalidStatusTransition) {
		t.Fatalf("approve changes_requested err = %v", err)
	}
}

func TestReviewerPublishesWithoutReview(t *testing.T) {
	c := newCourse(t)
	if err := c.Publish(true, t0); !errors.Is(err, domain.ErrCourseHasNoLectures) {
		t.Fatalf("empty publish err = %v", err)
	}
	_ = c.AddLecture(20, title(t, "L"), true, false, t0)
	if err := c.Publish(true, t0); err != nil || c.Live.Number != 1 {
		t.Fatalf("reviewer publish = %v, %+v", err, c.Live)
	}
	if err := c.Publish(true, t0); !errors.Is(err, domain.ErrInvalidStatusTransition) {
		t.Fatalf("republish unchanged err = %v", err)
	}
	if _, err := c.Submit(1, 100, t0); !errors.Is(err, domain.ErrInvalidStatusTransition) {
		t.Fatalf("submit unchanged err = %v", err)
	}
}

func TestEditingLiveCourseOpensNextVersion(t *testing.T) {
	c := newCourse(t)
	_ = c.AddLecture(20, title(t, "L"), true, false, t0)
	approve(t, &c)
	if err := c.Publish(false, t0); err != nil {
		t.Fatal(err)
	}
	if !c.IsLive() || c.HasDraftChanges() || c.Live != (domain.LiveVersion{Number: 1, PublishedAt: t0}) {
		t.Fatalf("after publish = %s %+v", c.Status, c.Live)
	}

	t1 := t0.Add(time.Hour)
	if err := c.RenameLecture(20, title(t, "Edited"), t1); err != nil {
		t.Fatal(err)
	}
	if c.Status != domain.StatusDraft || !c.HasDraftChanges() || c.Live.Number != 1 {
		t.Fatalf("after edit = %s %+v", c.Status, c.Live)
	}
	if err := c.Publish(false, t1); !errors.Is(err, domain.ErrApprovalRequired) {
		t.Fatalf("publish edit without review err = %v", err)
	}
	approve(t, &c)
	if c.Live.Number != 1 {
		t.Fatalf("approval must not change the live version: %+v", c.Live)
	}
	if err := c.Publish(false, t1); err != nil || c.Live != (domain.LiveVersion{Number: 2, PublishedAt: t1}) || c.HasDraftChanges() {
		t.Fatalf("second publish = %v, %+v", err, c.Live)
	}

	// Unpublishing takes the live version down even while a new draft is open.
	_ = c.RenameLecture(20, title(t, "Again"), t1)
	if _, err := c.Unpublish(9, 999, "Outdated", t1); err != nil || c.IsLive() || c.Status != domain.StatusChangesRequested {
		t.Fatalf("unpublish draft = %v, %s %+v", err, c.Status, c.Live)
	}
	if _, err := c.Unpublish(9, 999, "Again", t1); !errors.Is(err, domain.ErrInvalidStatusTransition) {
		t.Fatalf("unpublish offline err = %v", err)
	}
	approveAgain := func() {
		if _, err := c.Submit(10, 100, t1); err != nil {
			t.Fatal(err)
		}
		if _, err := c.Approve(11, 999, "", t1); err != nil {
			t.Fatal(err)
		}
	}
	approveAgain()
	if err := c.Publish(false, t1); err != nil || c.Live.Number != 3 || c.LastVersion != 3 {
		t.Fatalf("republish = %v, %+v", err, c)
	}
	if err := c.Archive(t1); err != nil || c.IsLive() {
		t.Fatalf("archive = %v, %+v", err, c.Live)
	}
}

func TestDiscardDraft(t *testing.T) {
	c := newCourse(t)
	_ = c.AddLecture(20, title(t, "L"), true, false, t0)
	if err := c.DiscardDraft(c, t0); !errors.Is(err, domain.ErrInvalidStatusTransition) {
		t.Fatalf("discard never published err = %v", err)
	}
	if err := c.Publish(true, t0); err != nil {
		t.Fatal(err)
	}
	live := c
	live.Lectures = append([]domain.Lecture(nil), c.Lectures...)
	if err := c.DiscardDraft(live, t0); !errors.Is(err, domain.ErrInvalidStatusTransition) {
		t.Fatalf("discard unchanged err = %v", err)
	}

	t1 := t0.Add(time.Hour)
	_ = c.RenameLecture(20, title(t, "Edited"), t1)
	_ = c.AddLecture(21, title(t, "New"), false, false, t1)
	_ = c.SetPrice(domain.Price{AmountMinor: 100, Currency: "NPR"}, t1)
	if _, err := c.Submit(1, 100, t1); err != nil {
		t.Fatal(err)
	}
	if err := c.DiscardDraft(live, t1); !errors.Is(err, domain.ErrInvalidStatusTransition) {
		t.Fatalf("discard in review err = %v", err)
	}
	if _, err := c.RequestChanges(2, 999, "No", t1); err != nil {
		t.Fatal(err)
	}
	if err := c.DiscardDraft(live, t1); err != nil {
		t.Fatal(err)
	}
	if c.Status != domain.StatusPublished || c.ReviewNote != "" || len(c.Lectures) != 1 ||
		c.Lectures[0].Title.String() != "L" || !c.Price.IsFree() || c.Live.Number != 1 || c.HasDraftChanges() {
		t.Fatalf("after discard = %+v", c)
	}
}
