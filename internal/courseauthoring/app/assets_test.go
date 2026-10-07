package app_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/app"
	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

const (
	videoAsset   id.ID = 9001
	imageAsset   id.ID = 9002
	foreignAsset id.ID = 9003
)

type assetFixture struct {
	contentFixture
	cat assetCatalog
}

func newAssetFixture(t *testing.T) assetFixture {
	t.Helper()
	f := newContentFixture(t, enrolled{})
	cat := assetCatalog{
		{f.course.ID, videoAsset}: app.AssetVideo,
		{f.course.ID, imageAsset}: app.AssetImage,
		{777, foreignAsset}:       app.AssetVideo,
	}
	f.contents = app.NewContentService(f.store, testIDs(t), enrolled{}, cat, quizCatalog{})
	f.courses = app.NewCourseService(f.store, testIDs(t), fixedClock{time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)}, cat, quizCatalog{}, &fakeAssessments{heads: map[id.ID][]domain.AssessmentPin{}})
	return assetFixture{contentFixture: f, cat: cat}
}

func videoBlock(cid string, asset id.ID) app.BlockInput {
	return app.BlockInput{ClientBlockID: cid, Type: "video", MediaAssetID: asset.String(), DurationMs: 60000}
}

func imageBlock(cid string, asset id.ID) app.BlockInput {
	return app.BlockInput{ClientBlockID: cid, Type: "image", MediaAssetID: asset.String()}
}

func deckBlock(cid string, asset id.ID) app.BlockInput {
	return app.BlockInput{ClientBlockID: cid, Type: "flashcard", Cards: []app.FlashcardCardInput{
		{Front: "f", Back: "b"}, {Front: "f2", Back: "b2", MediaAssetID: asset.String()}}}
}

func TestReplaceValidatesMediaReferences(t *testing.T) {
	f := newAssetFixture(t)
	ok := []app.BlockInput{videoBlock("v", videoAsset), imageBlock("i", imageAsset), deckBlock("d", imageAsset)}
	if err := f.contents.Replace(ctx, owner, f.course.ID, f.locked, ok, app.LegacyContent{}); err != nil {
		t.Fatalf("valid refs err = %v", err)
	}
	bad := map[string]app.BlockInput{
		"video holds image":    videoBlock("v", imageAsset),
		"image holds video":    imageBlock("i", videoAsset),
		"card holds video":     deckBlock("d", videoAsset),
		"other course's asset": videoBlock("v", foreignAsset),
		"unknown asset":        imageBlock("i", 424242),
	}
	for name, block := range bad {
		err := f.contents.Replace(ctx, owner, f.course.ID, f.locked, []app.BlockInput{block}, app.LegacyContent{})
		if !errors.Is(err, app.ErrInvalidMediaReference) || !strings.Contains(err.Error(), block.ClientBlockID) {
			t.Fatalf("%s: err = %v", name, err)
		}
		if errors.Is(err, app.ErrInvalidInput) {
			t.Fatalf("%s: must not also be invalid_input", name)
		}
	}
	urlVideo := app.BlockInput{ClientBlockID: "u", Type: "video", URL: "https://videos.test/a.mp4"}
	if err := f.contents.Replace(ctx, owner, f.course.ID, f.locked, []app.BlockInput{urlVideo}, app.LegacyContent{}); err != nil {
		t.Fatalf("url video err = %v", err)
	}
}

func TestPatchValidatesOnlyUpserts(t *testing.T) {
	f := newAssetFixture(t)
	if err := f.contents.Replace(ctx, owner, f.course.ID, f.locked, []app.BlockInput{videoBlock("v", videoAsset)}, app.LegacyContent{}); err != nil {
		t.Fatal(err)
	}
	v, _ := f.contents.Get(ctx, owner, f.course.ID, f.locked)
	// The catalog forgets the video: untouched blocks are not re-checked.
	f.contents = app.NewContentService(f.store, testIDs(t), enrolled{}, assetCatalog{}, quizCatalog{})
	rev, err := f.contents.Patch(ctx, owner, f.course.ID, f.locked, app.PatchInput{
		BaseRevision: ptr(v.ContentRevision), Order: []string{"v", "t"},
		Upserts: []app.BlockInput{{ClientBlockID: "t", Type: "text", Body: "<p>t</p>"}},
	})
	if err != nil {
		t.Fatalf("patch without media upserts err = %v", err)
	}
	_, err = f.contents.Patch(ctx, owner, f.course.ID, f.locked, app.PatchInput{
		BaseRevision: ptr(rev), Order: []string{"v", "t", "i"},
		Upserts: []app.BlockInput{imageBlock("i", imageAsset)},
	})
	if !errors.Is(err, app.ErrInvalidMediaReference) {
		t.Fatalf("patch with unknown upsert err = %v", err)
	}
}

func TestAddLectureValidatesMediaReferences(t *testing.T) {
	f := newAssetFixture(t)
	if _, err := f.courses.AddLecture(ctx, owner, f.course.ID, app.AddLectureInput{Title: "V", Blocks: []app.BlockInput{videoBlock("v", videoAsset)}}); err != nil {
		t.Fatalf("valid err = %v", err)
	}
	if _, err := f.courses.AddLecture(ctx, owner, f.course.ID, app.AddLectureInput{Title: "X", Blocks: []app.BlockInput{videoBlock("v", foreignAsset)}}); !errors.Is(err, app.ErrInvalidMediaReference) {
		t.Fatalf("foreign err = %v", err)
	}
}

func TestMediaReferenceDoesNotPreemptAuthorization(t *testing.T) {
	f := newAssetFixture(t)
	foreign := []app.BlockInput{videoBlock("v", foreignAsset)}
	// Draft course: a non-manager cannot see it at all.
	if err := f.contents.Replace(ctx, otherInstr, f.course.ID, f.locked, foreign, app.LegacyContent{}); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("draft replace err = %v", err)
	}
	if _, err := f.courses.AddLecture(ctx, otherInstr, f.course.ID, app.AddLectureInput{Title: "X", Blocks: foreign}); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("draft add err = %v", err)
	}
	if err := publish(f.courses, f.course.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.contents.Replace(ctx, student, f.course.ID, f.locked, foreign, app.LegacyContent{}); !errors.Is(err, app.ErrForbidden) {
		t.Fatalf("published replace err = %v", err)
	}
	v, _ := f.contents.Get(ctx, owner, f.course.ID, f.locked)
	first := v.Blocks[0].ClientBlockID()
	_, err := f.contents.Patch(ctx, student, f.course.ID, f.locked, app.PatchInput{
		BaseRevision: ptr(v.ContentRevision), Order: []string{first, "v"}, Upserts: foreign})
	if !errors.Is(err, app.ErrForbidden) {
		t.Fatalf("published patch err = %v", err)
	}
}

// catalogProbe fails the test if the catalog is consulted inside a store transaction.
type catalogProbe struct {
	t     *testing.T
	store *memStore
}

func (q catalogProbe) Kinds(context.Context, id.ID, []id.ID) (map[id.ID]app.AssetKind, error) {
	if !q.store.mu.TryLock() {
		q.t.Error("asset catalog consulted inside the content transaction")
		return nil, nil
	}
	q.store.mu.Unlock()
	return map[id.ID]app.AssetKind{}, nil
}

func (q catalogProbe) Lectures(context.Context, id.ID, []id.ID) (map[id.ID]id.ID, error) {
	if !q.store.mu.TryLock() {
		q.t.Error("quiz catalog consulted inside the content transaction")
		return nil, nil
	}
	q.store.mu.Unlock()
	return map[id.ID]id.ID{}, nil
}

func (q catalogProbe) Heads(context.Context, id.ID) ([]domain.AssessmentPin, error) {
	if !q.store.mu.TryLock() {
		q.t.Error("assessment catalog consulted inside the content transaction")
		return nil, nil
	}
	q.store.mu.Unlock()
	return nil, nil
}

func TestCatalogIsConsultedOutsideTx(t *testing.T) {
	f := newContentFixture(t, enrolled{})
	probe := catalogProbe{t: t, store: f.store}
	contents := app.NewContentService(f.store, testIDs(t), enrolled{}, probe, probe)
	courses := app.NewCourseService(f.store, testIDs(t), fixedClock{time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)}, probe, probe, probe)
	_ = contents.Replace(ctx, owner, f.course.ID, f.locked, []app.BlockInput{videoBlock("v", videoAsset)}, app.LegacyContent{})
	v, _ := contents.Get(ctx, owner, f.course.ID, f.locked)
	_, _ = contents.Patch(ctx, owner, f.course.ID, f.locked, app.PatchInput{BaseRevision: ptr(v.ContentRevision),
		Order: []string{v.Blocks[0].ClientBlockID(), "v"}, Upserts: []app.BlockInput{videoBlock("v", videoAsset)}})
	_, _ = courses.AddLecture(ctx, owner, f.course.ID, app.AddLectureInput{Title: "V", Blocks: []app.BlockInput{videoBlock("v", videoAsset)}})
	quiz := []app.BlockInput{{ClientBlockID: "q", Type: "quiz", QuizID: "4242"}}
	_ = contents.Replace(ctx, owner, f.course.ID, f.locked, quiz, app.LegacyContent{})
	_, _ = courses.AddLecture(ctx, owner, f.course.ID, app.AddLectureInput{Title: "Q", Blocks: quiz})
}

func TestCheckManage(t *testing.T) {
	f := newAssetFixture(t)
	if err := f.courses.CheckManage(ctx, owner, f.course.ID); err != nil {
		t.Fatalf("owner err = %v", err)
	}
	if err := f.courses.CheckManage(ctx, admin, f.course.ID); err != nil {
		t.Fatalf("admin err = %v", err)
	}
	if err := f.courses.CheckManage(ctx, otherInstr, f.course.ID); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("other on draft err = %v", err)
	}
	_ = publish(f.courses, f.course.ID)
	if err := f.courses.CheckManage(ctx, otherInstr, f.course.ID); !errors.Is(err, app.ErrForbidden) {
		t.Fatalf("other on published err = %v", err)
	}
	_ = f.courses.Archive(ctx, owner, f.course.ID)
	if err := f.courses.CheckManage(ctx, owner, f.course.ID); !errors.Is(err, domain.ErrCourseNotEditable) {
		t.Fatalf("archived err = %v", err)
	}
	if err := f.courses.CheckManage(ctx, owner, 424242); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("missing err = %v", err)
	}
}

func TestCheckManagerRead(t *testing.T) {
	f := newAssetFixture(t)
	if err := f.courses.CheckManagerRead(ctx, owner, f.course.ID); err != nil {
		t.Fatalf("owner err = %v", err)
	}
	if err := f.courses.CheckManagerRead(ctx, otherInstr, f.course.ID); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("other on draft err = %v", err)
	}
	_ = publish(f.courses, f.course.ID)
	if err := f.courses.CheckManagerRead(ctx, otherInstr, f.course.ID); !errors.Is(err, app.ErrForbidden) {
		t.Fatalf("other on published err = %v", err)
	}
	_ = f.courses.Archive(ctx, owner, f.course.ID)
	if err := f.courses.CheckManagerRead(ctx, owner, f.course.ID); err != nil {
		t.Fatalf("archived err = %v", err)
	}
	if err := f.courses.CheckManagerRead(ctx, admin, f.course.ID); err != nil {
		t.Fatalf("admin on archived err = %v", err)
	}
	if err := f.courses.CheckManagerRead(ctx, owner, 424242); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("missing err = %v", err)
	}
}

func TestCheckAssetRead(t *testing.T) {
	f := newAssetFixture(t)
	blocks := []app.BlockInput{videoBlock("v", videoAsset), deckBlock("d", imageAsset)}
	if err := f.contents.Replace(ctx, owner, f.course.ID, f.locked, blocks, app.LegacyContent{}); err != nil {
		t.Fatal(err)
	}
	_ = publish(f.courses, f.course.ID)
	if err := f.contents.CheckAssetRead(ctx, owner, f.course.ID, f.locked, videoAsset); err != nil {
		t.Fatalf("owner video err = %v", err)
	}
	if err := f.contents.CheckAssetRead(ctx, owner, f.course.ID, f.locked, imageAsset); err != nil {
		t.Fatalf("owner card image err = %v", err)
	}
	if err := f.contents.CheckAssetRead(ctx, owner, f.course.ID, f.free, videoAsset); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("unreferenced err = %v", err)
	}
	if err := f.contents.CheckAssetRead(ctx, student, f.course.ID, f.locked, videoAsset); !errors.Is(err, app.ErrEnrollmentRequired) {
		t.Fatalf("not enrolled err = %v", err)
	}
	enrolledContents := app.NewContentService(f.store, testIDs(t), enrolled{{f.course.ID, student.UserID}: true}, f.cat, quizCatalog{})
	if err := enrolledContents.CheckAssetRead(ctx, student, f.course.ID, f.locked, videoAsset); err != nil {
		t.Fatalf("enrolled err = %v", err)
	}
}

func (f *assetFixture) putVideoBlock(t *testing.T, lectureID, assetID id.ID) {
	t.Helper()
	f.cat[[2]id.ID{f.course.ID, assetID}] = app.AssetVideo
	if err := f.contents.Replace(ctx, owner, f.course.ID, lectureID, []app.BlockInput{videoBlock("v", assetID)}, app.LegacyContent{}); err != nil {
		t.Fatal(err)
	}
}

func TestAssetUsage(t *testing.T) {
	f := newAssetFixture(t)
	f.putVideoBlock(t, f.locked, 555) // working copy references asset 555
	got, err := f.contents.AssetUsage(ctx, f.course.ID, 555)
	if err != nil || !slices.Equal(got, []id.ID{f.locked}) {
		t.Fatalf("draft usage = %v, %v", got, err)
	}
	if err := publish(f.courses, f.course.ID); err != nil {
		t.Fatal(err)
	}
	f.putVideoBlock(t, f.locked, 556) // draft drops 555; live still has it
	got, err = f.contents.AssetUsage(ctx, f.course.ID, 555)
	if err != nil || !slices.Equal(got, []id.ID{f.locked}) {
		t.Fatalf("live usage = %v, %v", got, err)
	}
	if got, _ := f.contents.AssetUsage(ctx, f.course.ID, 999); len(got) != 0 {
		t.Fatalf("unused asset reported in use: %v", got)
	}
}
