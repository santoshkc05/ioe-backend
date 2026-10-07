package app_test

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/app"
	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

func TestAdminPublishesWithoutReview(t *testing.T) {
	svc, _ := newCourseService(t)
	c, _ := svc.Create(ctx, admin, app.CreateCourseInput{Title: "Go"})
	_, _ = svc.AddLecture(ctx, admin, c.ID, app.AddLectureInput{Title: "L1"})
	if err := svc.Publish(ctx, admin, c.ID); err != nil {
		t.Fatal(err)
	}
	mine, _ := svc.Create(ctx, owner, app.CreateCourseInput{Title: "Owned"})
	_, _ = svc.AddLecture(ctx, owner, mine.ID, app.AddLectureInput{Title: "L1"})
	if err := svc.Publish(ctx, owner, mine.ID); !errors.Is(err, domain.ErrApprovalRequired) {
		t.Fatalf("owner publish err = %v", err)
	}
	if err := svc.Publish(ctx, admin, mine.ID); err != nil {
		t.Fatalf("admin publishes an instructor's draft: %v", err)
	}
}

func TestLiveVersionSurvivesEditsUntilRepublished(t *testing.T) {
	f := newContentFixture(t, enrolled{})
	svc, cid := f.courses, f.course.ID
	if _, err := svc.SetPrice(ctx, owner, cid, 50000, "NPR"); err != nil {
		t.Fatal(err)
	}
	if err := publish(svc, cid); err != nil {
		t.Fatal(err)
	}

	// Edit structure, price and content of the live course.
	if err := svc.RenameLecture(ctx, owner, cid, f.free, "Renamed"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetPrice(ctx, owner, cid, 90000, "NPR"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AddLecture(ctx, owner, cid, app.AddLectureInput{Title: "New"}); err != nil {
		t.Fatal(err)
	}
	if err := f.contents.Replace(ctx, owner, cid, f.free, nil, app.LegacyContent{TextBody: "<p>edited</p>"}); err != nil {
		t.Fatal(err)
	}

	working, _ := svc.Get(ctx, owner, cid)
	if working.Status != domain.StatusDraft || !working.HasDraftChanges() || len(working.Lectures) != 3 || working.Live.Number != 1 {
		t.Fatalf("working = %s %d lectures, live %+v", working.Status, len(working.Lectures), working.Live)
	}
	for _, p := range []struct {
		name string
		get  func() (domain.Course, error)
	}{
		{"student", func() (domain.Course, error) { return svc.Get(ctx, student, cid) }},
		{"owner live", func() (domain.Course, error) { return svc.GetLive(ctx, owner, cid) }},
	} {
		live, err := p.get()
		if err != nil || live.Status != domain.StatusPublished || len(live.Lectures) != 2 ||
			live.Lectures[0].Title.String() != "Free" || live.Price.AmountMinor != 50000 {
			t.Fatalf("%s live = %v, %+v", p.name, err, live)
		}
	}
	v, err := f.contents.Get(ctx, student, cid, f.free)
	if err != nil || len(v.Blocks) != 1 || v.Title != "Free" {
		t.Fatalf("student content = %v, %+v", err, v)
	}
	if body, _ := v.Blocks[0].Text(); body.String() != "<p>free</p>" {
		t.Fatalf("student sees draft content: %q", body.String())
	}
	if facts, _ := svc.Facts(ctx, cid); !facts.Published || facts.Price.AmountMinor != 50000 || len(facts.LectureIDs) != 2 {
		t.Fatalf("facts = %+v", facts)
	}

	if err := publish(svc, cid); err != nil {
		t.Fatal(err)
	}
	live, _ := svc.Get(ctx, student, cid)
	if len(live.Lectures) != 3 || live.Lectures[0].Title.String() != "Renamed" || live.Price.AmountMinor != 90000 || live.Live.Number != 2 {
		t.Fatalf("republished = %+v", live)
	}
	v, _ = f.contents.Get(ctx, student, cid, f.free)
	if body, _ := v.Blocks[0].Text(); body.String() != "<p>edited</p>" {
		t.Fatalf("republished content = %q", body.String())
	}
}

func TestContentPatchReopensPublishedCourse(t *testing.T) {
	f := newContentFixture(t, enrolled{})
	if err := publish(f.courses, f.course.ID); err != nil {
		t.Fatal(err)
	}
	v, _ := f.contents.Get(ctx, owner, f.course.ID, f.locked)
	if _, err := f.contents.Patch(ctx, owner, f.course.ID, f.locked, app.PatchInput{
		BaseRevision: ptr(v.ContentRevision), Order: []string{},
		Deletes: []string{v.Blocks[0].ClientBlockID()},
	}); err != nil {
		t.Fatal(err)
	}
	if c, _ := f.courses.Get(ctx, owner, f.course.ID); c.Status != domain.StatusDraft || !c.IsLive() {
		t.Fatalf("after patch = %s %+v", c.Status, c.Live)
	}
	if v, err := f.contents.GetLive(ctx, owner, f.course.ID, f.locked); err != nil || len(v.Blocks) != 1 {
		t.Fatalf("live content = %v, %+v", err, v)
	}
}

func TestDiscardDraftRestoresLiveVersion(t *testing.T) {
	f := newContentFixture(t, enrolled{})
	svc, cid := f.courses, f.course.ID
	if _, err := svc.DiscardDraft(ctx, owner, cid); !errors.Is(err, domain.ErrInvalidStatusTransition) {
		t.Fatalf("discard before publish err = %v", err)
	}
	if err := publish(svc, cid); err != nil {
		t.Fatal(err)
	}
	before, _ := f.contents.Get(ctx, owner, cid, f.free)

	// Edit outline and content: rename, remove a lecture, add one, rewrite content.
	_ = svc.RenameLecture(ctx, owner, cid, f.free, "Renamed")
	_ = svc.RemoveLecture(ctx, owner, cid, f.locked)
	_, _ = svc.AddLecture(ctx, owner, cid, app.AddLectureInput{Title: "New"})
	if err := f.contents.Replace(ctx, owner, cid, f.free, nil, app.LegacyContent{TextBody: "<p>edited</p>"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.DiscardDraft(ctx, otherInstr, cid); !errors.Is(err, app.ErrForbidden) {
		t.Fatalf("foreign discard err = %v", err)
	}

	c, err := svc.DiscardDraft(ctx, owner, cid)
	if err != nil {
		t.Fatal(err)
	}
	if c.Status != domain.StatusPublished || c.HasDraftChanges() || len(c.Lectures) != 2 ||
		c.Lectures[0].Title.String() != "Free" || c.Lectures[1].ID != f.locked {
		t.Fatalf("restored = %+v", c)
	}
	after, err := f.contents.Get(ctx, owner, cid, f.free)
	if err != nil || len(after.Blocks) != 1 || after.ContentRevision <= before.ContentRevision {
		t.Fatalf("restored content = %v, %+v (before revision %d)", err, after, before.ContentRevision)
	}
	if body, _ := after.Blocks[0].Text(); body.String() != "<p>free</p>" {
		t.Fatalf("restored body = %q", body.String())
	}
	if locked, err := f.contents.Get(ctx, owner, cid, f.locked); err != nil || len(locked.Blocks) != 1 {
		t.Fatalf("re-created lecture content = %v, %+v", err, locked)
	}
	// A stale editor patching the discarded revision conflicts.
	var conflict *app.RevisionConflictError
	if _, err := f.contents.Patch(ctx, owner, cid, f.free, app.PatchInput{BaseRevision: ptr(before.ContentRevision + 1), Order: []string{}}); !errors.As(err, &conflict) {
		t.Fatalf("stale patch err = %v", err)
	}
}

func TestVersionHistory(t *testing.T) {
	svc, _ := newCourseService(t)
	c, _ := svc.Create(ctx, owner, app.CreateCourseInput{Title: "Go"})
	_, _ = svc.AddLecture(ctx, owner, c.ID, app.AddLectureInput{Title: "L1"})
	if err := svc.Publish(ctx, admin, c.ID); err != nil {
		t.Fatal(err)
	}
	_ = svc.RenameLecture(ctx, owner, c.ID, mustGet(t, svc, c.ID).Lectures[0].ID, "L1 v2")
	if err := publish(svc, c.ID); err != nil {
		t.Fatal(err)
	}

	vs, err := svc.ListVersions(ctx, owner, c.ID)
	if err != nil || len(vs) != 2 || vs[0].Number != 2 || vs[1].Number != 1 || vs[1].PublishedBy != admin.UserID || vs[0].PublishedBy != owner.UserID {
		t.Fatalf("versions = %v, %+v", err, vs)
	}
	v1, err := svc.GetVersion(ctx, owner, c.ID, 1)
	if err != nil || v1.Lectures[0].Title.String() != "L1" || v1.Status != domain.StatusPublished {
		t.Fatalf("v1 = %v, %+v", err, v1)
	}
	if _, err := svc.GetVersion(ctx, owner, c.ID, 3); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("missing version err = %v", err)
	}
	if _, err := svc.ListVersions(ctx, student, c.ID); !errors.Is(err, app.ErrForbidden) {
		t.Fatalf("student versions err = %v", err)
	}
}

func mustGet(t *testing.T, svc *app.CourseService, cid id.ID) domain.Course {
	t.Helper()
	c, err := svc.Get(ctx, owner, cid)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

type versionFixture struct {
	courses     *app.CourseService
	contents    *app.ContentService
	store       *memStore
	assessments *fakeAssessments
	quizCatalog quizCatalog
	courseID    id.ID
	lectureID   id.ID
}

func newVersionFixture(t *testing.T) *versionFixture {
	t.Helper()
	store := newMemStore()
	clk := fixedClock{time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)}
	ass := &fakeAssessments{heads: map[id.ID][]domain.AssessmentPin{}}
	qc := quizCatalog{}
	courses := app.NewCourseService(store, testIDs(t), clk, assetCatalog{}, qc, ass)
	contents := app.NewContentService(store, testIDs(t), enrolled{}, assetCatalog{}, qc)
	c, err := courses.Create(ctx, owner, app.CreateCourseInput{Title: "Go"})
	if err != nil {
		t.Fatal(err)
	}
	c, err = courses.AddLecture(ctx, owner, c.ID, app.AddLectureInput{Title: "L1"})
	if err != nil {
		t.Fatal(err)
	}
	return &versionFixture{
		courses:     courses,
		contents:    contents,
		store:       store,
		assessments: ass,
		quizCatalog: qc,
		courseID:    c.ID,
		lectureID:   c.Lectures[0].ID,
	}
}

func TestSubmitCapturesPinsAndPublishCopiesThem(t *testing.T) {
	f := newVersionFixture(t)
	quizPin := domain.AssessmentPin{Kind: domain.AssessmentQuiz, ID: 700, Revision: 2}
	examPin := domain.AssessmentPin{Kind: domain.AssessmentExam, ID: 800, Revision: 1}
	f.assessments.heads[f.courseID] = []domain.AssessmentPin{quizPin, examPin}

	if err := f.courses.Submit(ctx, owner, f.courseID); err != nil {
		t.Fatal(err)
	}
	// An edit landing after submit must not reach the published version.
	f.assessments.heads[f.courseID] = []domain.AssessmentPin{{Kind: domain.AssessmentQuiz, ID: 700, Revision: 3}, examPin}
	if err := f.courses.Approve(ctx, admin, f.courseID, ""); err != nil {
		t.Fatal(err)
	}
	if err := f.courses.Publish(ctx, owner, f.courseID); err != nil {
		t.Fatal(err)
	}
	pins, live, err := f.courses.LivePins(ctx, f.courseID)
	if err != nil || !live {
		t.Fatalf("live pins: %v, live=%v", err, live)
	}
	if want := []domain.AssessmentPin{examPin, quizPin}; !slices.Equal(pins, want) {
		t.Fatalf("pins = %+v, want %+v", pins, want)
	}
}

func TestReviewerDirectPublishPinsHeads(t *testing.T) {
	f := newVersionFixture(t)
	head := domain.AssessmentPin{Kind: domain.AssessmentQuiz, ID: 700, Revision: 4}
	f.assessments.heads[f.courseID] = []domain.AssessmentPin{head}
	if err := f.courses.Publish(ctx, admin, f.courseID); err != nil { // draft, reviewer bypass
		t.Fatal(err)
	}
	pins, _, err := f.courses.LivePins(ctx, f.courseID)
	if err != nil || !slices.Equal(pins, []domain.AssessmentPin{head}) {
		t.Fatalf("pins = %+v, %v", pins, err)
	}
}

func TestSubmitRejectsDanglingQuizBlock(t *testing.T) {
	f := newVersionFixture(t)
	f.quizCatalog[[2]id.ID{f.courseID, 700}] = f.lectureID
	if err := f.contents.Replace(ctx, owner, f.courseID, f.lectureID, []app.BlockInput{{ClientBlockID: "q", Type: "quiz", QuizID: "700"}}, app.LegacyContent{}); err != nil {
		t.Fatal(err)
	}
	delete(f.quizCatalog, [2]id.ID{f.courseID, 700}) // quiz 700 deleted meanwhile
	err := f.courses.Submit(ctx, owner, f.courseID)
	if !errors.Is(err, app.ErrInvalidQuizReference) {
		t.Fatalf("submit = %v, want ErrInvalidQuizReference", err)
	}
}

func TestDiscardDraftPublishesLivePins(t *testing.T) {
	f := newVersionFixture(t)
	pin := domain.AssessmentPin{Kind: domain.AssessmentExam, ID: 800, Revision: 1}
	f.assessments.heads[f.courseID] = []domain.AssessmentPin{pin}
	if err := f.courses.Publish(ctx, admin, f.courseID); err != nil {
		t.Fatal(err)
	}
	if err := f.courses.UpdateDetails(ctx, owner, f.courseID, app.DetailsInput{Title: "Edited"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.courses.DiscardDraft(ctx, owner, f.courseID); err != nil {
		t.Fatal(err)
	}
	last := f.store.published[len(f.store.published)-1]
	ev, ok := last.(domain.DraftDiscarded)
	if !ok || ev.CourseID != f.courseID || ev.ActorID != owner.UserID || !slices.Equal(ev.Pins, []domain.AssessmentPin{pin}) {
		t.Fatalf("event = %+v", last)
	}
}

func TestBeginAssessmentEdit(t *testing.T) {
	f := newVersionFixture(t)
	if err := f.courses.Publish(ctx, admin, f.courseID); err != nil {
		t.Fatal(err)
	}
	if err := f.courses.BeginAssessmentEdit(ctx, owner, f.courseID, f.lectureID); err != nil {
		t.Fatal(err)
	}
	c, _ := f.courses.Get(ctx, owner, f.courseID)
	if c.Status != domain.StatusDraft {
		t.Fatalf("status = %s, want draft", c.Status)
	}
	if err := f.courses.Submit(ctx, owner, f.courseID); err != nil {
		t.Fatal(err)
	}
	if err := f.courses.BeginAssessmentEdit(ctx, owner, f.courseID, f.lectureID); !errors.Is(err, domain.ErrCourseNotEditable) {
		t.Fatalf("in_review begin edit err = %v, want ErrCourseNotEditable", err)
	}
	f2 := newVersionFixture(t)
	if err := f2.courses.BeginAssessmentEdit(ctx, owner, f2.courseID, 99999); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("unknown lecture err = %v, want ErrNotFound", err)
	}
}
