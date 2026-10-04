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
