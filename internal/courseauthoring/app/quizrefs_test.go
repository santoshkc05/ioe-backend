package app_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/app"
	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

const (
	lockedQuiz  id.ID = 8001 // a quiz of the locked lecture
	freeQuiz    id.ID = 8002 // a quiz of the free lecture
	foreignQuiz id.ID = 8003 // a quiz of another course
)

func newQuizFixture(t *testing.T) contentFixture {
	t.Helper()
	f := newContentFixture(t, enrolled{})
	cat := quizCatalog{
		{f.course.ID, lockedQuiz}: f.locked,
		{f.course.ID, freeQuiz}:   f.free,
		{777, foreignQuiz}:        4242,
	}
	f.contents = app.NewContentService(f.store, testIDs(t), enrolled{}, assetCatalog{}, cat)
	f.courses = app.NewCourseService(f.store, testIDs(t), fixedClock{time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)}, assetCatalog{}, cat, &fakeAssessments{heads: map[id.ID][]domain.AssessmentPin{}})
	return f
}

func quizBlock(cid string, quiz id.ID) app.BlockInput {
	return app.BlockInput{ClientBlockID: cid, Type: "quiz", QuizID: quiz.String()}
}

func TestReplaceValidatesQuizReferences(t *testing.T) {
	f := newQuizFixture(t)
	if err := f.contents.Replace(ctx, owner, f.course.ID, f.locked, []app.BlockInput{quizBlock("q", lockedQuiz)}, app.LegacyContent{}); err != nil {
		t.Fatalf("own quiz: %v", err)
	}
	bad := map[string]app.BlockInput{
		"quiz of another lecture": quizBlock("q", freeQuiz),
		"quiz of another course":  quizBlock("q", foreignQuiz),
		"unknown quiz":            quizBlock("q", 424242),
	}
	for name, b := range bad {
		err := f.contents.Replace(ctx, owner, f.course.ID, f.locked, []app.BlockInput{b}, app.LegacyContent{})
		if !errors.Is(err, app.ErrInvalidQuizReference) || !strings.Contains(err.Error(), `"q"`) || errors.Is(err, app.ErrInvalidInput) {
			t.Fatalf("%s: err = %v", name, err)
		}
	}
}

func TestPatchValidatesOnlyQuizUpserts(t *testing.T) {
	f := newQuizFixture(t)
	if err := f.contents.Replace(ctx, owner, f.course.ID, f.locked, []app.BlockInput{quizBlock("q", lockedQuiz)}, app.LegacyContent{}); err != nil {
		t.Fatal(err)
	}
	v, _ := f.contents.Get(ctx, owner, f.course.ID, f.locked)
	// The quiz was deleted: the stale block stays, and patches that leave it alone still work.
	f.contents = app.NewContentService(f.store, testIDs(t), enrolled{}, assetCatalog{}, quizCatalog{})
	rev, err := f.contents.Patch(ctx, owner, f.course.ID, f.locked, app.PatchInput{
		BaseRevision: ptr(v.ContentRevision), Order: []string{"q", "t"},
		Upserts: []app.BlockInput{{ClientBlockID: "t", Type: "text", Body: "<p>t</p>"}},
	})
	if err != nil {
		t.Fatalf("patch without quiz upserts: %v", err)
	}
	_, err = f.contents.Patch(ctx, owner, f.course.ID, f.locked, app.PatchInput{
		BaseRevision: ptr(rev), Order: []string{"q", "t"}, Upserts: []app.BlockInput{quizBlock("q", lockedQuiz)},
	})
	if !errors.Is(err, app.ErrInvalidQuizReference) {
		t.Fatalf("patch upserting the stale quiz: %v", err)
	}
}

func TestAddLectureRejectsQuizBlocks(t *testing.T) {
	f := newQuizFixture(t)
	_, err := f.courses.AddLecture(ctx, owner, f.course.ID, app.AddLectureInput{Title: "Q", Blocks: []app.BlockInput{quizBlock("q", lockedQuiz)}})
	if !errors.Is(err, app.ErrInvalidQuizReference) {
		t.Fatalf("err = %v", err)
	}
}

func TestQuizReferenceDoesNotPreemptAuthorization(t *testing.T) {
	f := newQuizFixture(t)
	foreign := []app.BlockInput{quizBlock("q", foreignQuiz)}
	if err := f.contents.Replace(ctx, otherInstr, f.course.ID, f.locked, foreign, app.LegacyContent{}); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("draft: %v", err)
	}
	if err := publish(f.courses, f.course.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.contents.Replace(ctx, student, f.course.ID, f.locked, foreign, app.LegacyContent{}); !errors.Is(err, app.ErrForbidden) {
		t.Fatalf("published: %v", err)
	}
}

func TestCheckLectureManage(t *testing.T) {
	f := newQuizFixture(t)
	if err := f.courses.CheckLectureManage(ctx, owner, f.course.ID, f.locked); err != nil {
		t.Fatalf("owner: %v", err)
	}
	if err := f.courses.CheckLectureManage(ctx, admin, f.course.ID, f.locked); err != nil {
		t.Fatalf("admin: %v", err)
	}
	if err := f.courses.CheckLectureManage(ctx, owner, f.course.ID, 424242); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("missing lecture: %v", err)
	}
	if err := f.courses.CheckLectureManage(ctx, otherInstr, f.course.ID, f.locked); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("other on draft: %v", err)
	}
	_ = publish(f.courses, f.course.ID)
	if err := f.courses.CheckLectureManage(ctx, otherInstr, f.course.ID, f.locked); !errors.Is(err, app.ErrForbidden) {
		t.Fatalf("other on published: %v", err)
	}
	_ = f.courses.Archive(ctx, owner, f.course.ID)
	if err := f.courses.CheckLectureManage(ctx, owner, f.course.ID, f.locked); !errors.Is(err, domain.ErrCourseNotEditable) {
		t.Fatalf("archived: %v", err)
	}
}

func TestCheckLectureRead(t *testing.T) {
	f := newQuizFixture(t)
	if err := f.contents.CheckLectureRead(ctx, student, f.course.ID, f.free); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("draft: %v", err)
	}
	_ = publish(f.courses, f.course.ID)
	if err := f.contents.CheckLectureRead(ctx, student, f.course.ID, f.free); err != nil {
		t.Fatalf("free preview: %v", err)
	}
	if err := f.contents.CheckLectureRead(ctx, student, f.course.ID, f.locked); !errors.Is(err, app.ErrEnrollmentRequired) {
		t.Fatalf("locked: %v", err)
	}
	if err := f.contents.CheckLectureRead(ctx, owner, f.course.ID, f.locked); err != nil {
		t.Fatalf("owner: %v", err)
	}
}
