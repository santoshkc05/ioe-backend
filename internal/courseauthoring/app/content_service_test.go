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
