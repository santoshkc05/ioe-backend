//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/adapters/postgres"
	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/app"
	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/clock"
	"github.com/santoshkc2200/ioe-backend/internal/platform/contentblocks"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
	"github.com/santoshkc2200/ioe-backend/internal/platform/postgres/pgtest"
)

type fixture struct {
	tx  *postgres.TxRunner
	ids *id.Generator
	now time.Time
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	ids, err := id.NewGenerator(0)
	if err != nil {
		t.Fatal(err)
	}
	return fixture{tx: postgres.NewTxRunner(pgtest.New(t), clock.System{}), ids: ids, now: time.Now().UTC().Truncate(time.Microsecond)}
}

func (f fixture) seedCourse(t *testing.T) domain.Course {
	t.Helper()
	ttl, _ := contentblocks.NewTitle("Go")
	c := domain.NewCourse(f.ids.New(), 100, ttl, "d", f.now)
	sec, _ := contentblocks.NewTitle("Intro")
	if err := c.AddSection(f.ids.New(), sec, f.now); err != nil {
		t.Fatal(err)
	}
	lt, _ := contentblocks.NewTitle("L1")
	if err := c.AddLecture(f.ids.New(), lt, false, false, f.now); err != nil {
		t.Fatal(err)
	}
	if err := c.MoveLectureToSection(c.Lectures[0].ID, c.Sections[0].ID, f.now); err != nil {
		t.Fatal(err)
	}
	if err := f.tx.RunInTx(context.Background(), func(r app.Repos) error { return r.Courses.Insert(context.Background(), &c) }); err != nil {
		t.Fatal(err)
	}
	return c
}

func textBlock(t *testing.T, blockID id.ID, client string, pos int, html string) contentblocks.Block {
	t.Helper()
	body, err := contentblocks.NewSanitizedTextBody(html)
	if err != nil {
		t.Fatal(err)
	}
	return contentblocks.NewTextBlock(blockID, client, pos, body)
}

func TestCourseRoundTripAndVersionConflict(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	c := f.seedCourse(t)

	var loaded domain.Course
	if err := f.tx.RunInTx(ctx, func(r app.Repos) error {
		var err error
		loaded, err = r.Courses.FindByID(ctx, c.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if loaded.Version != 1 || len(loaded.Sections) != 1 || len(loaded.Lectures) != 1 || loaded.Lectures[0].SectionID != c.Sections[0].ID {
		t.Fatalf("loaded = %+v", loaded)
	}

	stale := loaded
	if err := loaded.SetPrice(domain.Price{AmountMinor: 50000, Currency: "NPR"}, f.now); err != nil {
		t.Fatal(err)
	}
	if err := f.tx.RunInTx(ctx, func(r app.Repos) error { return r.Courses.Update(ctx, &loaded) }); err != nil || loaded.Version != 2 {
		t.Fatalf("update = %v, version %d", err, loaded.Version)
	}
	if err := f.tx.RunInTx(ctx, func(r app.Repos) error { return r.Courses.Update(ctx, &stale) }); !errors.Is(err, app.ErrConcurrentModification) {
		t.Fatalf("stale update err = %v", err)
	}

	if err := f.tx.RunInTx(ctx, func(r app.Repos) error {
		_, err := r.Courses.FindByID(ctx, 42)
		return err
	}); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("missing err = %v", err)
	}
}

func TestRemoveSectionAndLectureAreDeleted(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	c := f.seedCourse(t)
	if err := c.RemoveSection(c.Sections[0].ID, f.now); err != nil {
		t.Fatal(err)
	}
	if err := f.tx.RunInTx(ctx, func(r app.Repos) error { return r.Courses.Update(ctx, &c) }); err != nil {
		t.Fatal(err)
	}
	var got domain.Course
	_ = f.tx.RunInTx(ctx, func(r app.Repos) error { var err error; got, err = r.Courses.FindByID(ctx, c.ID); return err })
	if len(got.Sections) != 0 || got.Lectures[0].SectionID != 0 {
		t.Fatalf("got = %+v", got)
	}
	if err := c.RemoveLecture(c.Lectures[0].ID, f.now); err != nil {
		t.Fatal(err)
	}
	if err := f.tx.RunInTx(ctx, func(r app.Repos) error { return r.Courses.Update(ctx, &c) }); err != nil {
		t.Fatal(err)
	}
	_ = f.tx.RunInTx(ctx, func(r app.Repos) error { var err error; got, err = r.Courses.FindByID(ctx, c.ID); return err })
	if len(got.Lectures) != 0 {
		t.Fatalf("lecture not deleted: %+v", got.Lectures)
	}
}

func TestPatchRevisionAndReorder(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	c := f.seedCourse(t)
	lid := c.Lectures[0].ID

	var rev int64
	err := f.tx.RunInTx(ctx, func(r app.Repos) error {
		var err error
		rev, err = r.Contents.ReplaceBlocks(ctx, c.ID, lid, []contentblocks.Block{
			textBlock(t, f.ids.New(), "a", 0, "<p>a</p>"),
			textBlock(t, f.ids.New(), "b", 1, "<p>b</p>"),
		})
		return err
	})
	if err != nil || rev != 1 {
		t.Fatalf("replace = %d, %v", rev, err)
	}

	plan := app.BlockWritePlan{Upserts: []contentblocks.Block{textBlock(t, f.ids.New(), "c", 0, "<p>c</p>")}, Order: []string{"c", "b", "a"}}
	if err := f.tx.RunInTx(ctx, func(r app.Repos) error {
		var err error
		rev, err = r.Contents.ApplyPatch(ctx, c.ID, lid, 1, plan)
		return err
	}); err != nil || rev != 2 {
		t.Fatalf("patch = %d, %v", rev, err)
	}
	if err := f.tx.RunInTx(ctx, func(r app.Repos) error {
		_, err := r.Contents.ApplyPatch(ctx, c.ID, lid, 1, app.BlockWritePlan{Order: []string{"a", "b", "c"}})
		return err
	}); !errors.Is(err, app.ErrConcurrentModification) {
		t.Fatalf("stale patch err = %v", err)
	}

	var blocks []contentblocks.Block
	_ = f.tx.RunInTx(ctx, func(r app.Repos) error { var err error; blocks, err = r.Contents.ListBlocks(ctx, lid); return err })
	if len(blocks) != 3 || blocks[0].ClientBlockID() != "c" || blocks[1].ClientBlockID() != "b" || blocks[2].ClientBlockID() != "a" {
		t.Fatalf("blocks = %+v", blocks)
	}

	var course domain.Course
	_ = f.tx.RunInTx(ctx, func(r app.Repos) error { var err error; course, err = r.Courses.FindByID(ctx, c.ID); return err })
	if !course.Lectures[0].HasText || course.Lectures[0].HasVideo {
		t.Fatalf("flags = %+v", course.Lectures[0])
	}
}

func TestConcurrentPatchesExactlyOneWins(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	c := f.seedCourse(t)
	lid := c.Lectures[0].ID

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = f.tx.RunInTx(ctx, func(r app.Repos) error {
				if _, err := r.Contents.FindLectureForUpdate(ctx, c.ID, lid); err != nil {
					return err
				}
				_, err := r.Contents.ApplyPatch(ctx, c.ID, lid, 0, app.BlockWritePlan{
					Upserts: []contentblocks.Block{textBlock(t, f.ids.New(), "x", 0, "<p>x</p>")}, Order: []string{"x"}})
				return err
			})
		}()
	}
	wg.Wait()
	wins := 0
	for _, err := range errs {
		switch {
		case err == nil:
			wins++
		case !errors.Is(err, app.ErrConcurrentModification):
			t.Fatalf("unexpected err %v", err)
		}
	}
	if wins != 1 {
		t.Fatalf("wins = %d, errs = %v", wins, errs)
	}
}

// seedPublished inserts a course with one section and one lecture, then publishes it with
// the given level and price.
func (f fixture) seedPublished(t *testing.T, level string, price domain.Price) domain.Course {
	t.Helper()
	c := f.seedCourse(t)
	ttl, _ := contentblocks.NewTitle("Go")
	if err := c.UpdateDetails(ttl, "d", level, "", f.now); err != nil {
		t.Fatal(err)
	}
	if err := c.SetPrice(price, f.now); err != nil {
		t.Fatal(err)
	}
	f.publish(t, &c)
	return c
}

// publish publishes c as a reviewer and saves it.
func (f fixture) publish(t *testing.T, c *domain.Course) {
	t.Helper()
	ctx := context.Background()
	if err := c.Publish(true, f.now); err != nil {
		t.Fatal(err)
	}
	if err := f.tx.RunInTx(ctx, func(r app.Repos) error {
		if err := r.Courses.InsertVersion(ctx, c, 1); err != nil {
			return err
		}
		return r.Courses.Update(ctx, c)
	}); err != nil {
		t.Fatal(err)
	}
}

func (f fixture) listPublished(t *testing.T, q app.CatalogQuery) []app.CourseSummary {
	t.Helper()
	ctx := context.Background()
	var out []app.CourseSummary
	if err := f.tx.RunInTx(ctx, func(r app.Repos) error {
		var err error
		out, err = r.Courses.ListPublished(ctx, q)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

func summaryIDs(ss []app.CourseSummary) []id.ID {
	out := make([]id.ID, 0, len(ss))
	for _, s := range ss {
		out = append(out, s.ID)
	}
	return out
}

func TestListPublishedFiltersAndCounts(t *testing.T) {
	f := newFixture(t)
	paid := domain.Price{AmountMinor: 50000, Currency: "NPR"}
	freeBeginner := f.seedPublished(t, "beginner", domain.Price{})
	paidAdvanced := f.seedPublished(t, "advanced", paid)
	f.seedCourse(t) // draft, never listed

	all := f.listPublished(t, app.CatalogQuery{Limit: 10})
	if got := summaryIDs(all); len(got) != 2 || got[0] != paidAdvanced.ID || got[1] != freeBeginner.ID {
		t.Fatalf("all = %v", got)
	}
	s := all[0]
	if s.LectureCount != 1 || s.SectionCount != 1 || s.Price != paid || s.Level != "advanced" ||
		s.Title != "Go" || s.OwnerID != 100 || !s.CreatedAt.Equal(f.now) {
		t.Fatalf("summary = %+v", s)
	}
	if got := summaryIDs(f.listPublished(t, app.CatalogQuery{Limit: 10, Price: app.PriceFree})); len(got) != 1 || got[0] != freeBeginner.ID {
		t.Errorf("free = %v", got)
	}
	if got := summaryIDs(f.listPublished(t, app.CatalogQuery{Limit: 10, Price: app.PricePaid})); len(got) != 1 || got[0] != paidAdvanced.ID {
		t.Errorf("paid = %v", got)
	}
	if got := summaryIDs(f.listPublished(t, app.CatalogQuery{Limit: 10, Level: "beginner"})); len(got) != 1 || got[0] != freeBeginner.ID {
		t.Errorf("beginner = %v", got)
	}
	if got := f.listPublished(t, app.CatalogQuery{Limit: 10, Level: "intermediate"}); len(got) != 0 {
		t.Errorf("intermediate = %v", summaryIDs(got))
	}
}

func TestListPublishedKeysetWalk(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	var seeded []domain.Course
	for range 5 {
		seeded = append(seeded, f.seedPublished(t, "", domain.Price{}))
	}
	// newest first: seeded[4] .. seeded[0]
	page1 := f.listPublished(t, app.CatalogQuery{Limit: 2})
	if got := summaryIDs(page1); len(got) != 2 || got[0] != seeded[4].ID || got[1] != seeded[3].ID {
		t.Fatalf("page1 = %v", got)
	}

	// Archive a course that belongs to the next page; the walk must neither repeat nor skip.
	archived := seeded[2]
	if err := archived.Archive(f.now); err != nil {
		t.Fatal(err)
	}
	if err := f.tx.RunInTx(ctx, func(r app.Repos) error { return r.Courses.Update(ctx, &archived) }); err != nil {
		t.Fatal(err)
	}
	page2 := f.listPublished(t, app.CatalogQuery{Limit: 2, After: page1[1].ID})
	if got := summaryIDs(page2); len(got) != 2 || got[0] != seeded[1].ID || got[1] != seeded[0].ID {
		t.Fatalf("page2 = %v", got)
	}
	if got := f.listPublished(t, app.CatalogQuery{Limit: 2, After: page2[1].ID}); len(got) != 0 {
		t.Fatalf("page3 = %v", summaryIDs(got))
	}

	// A cursor that names no course still pages by ID.
	gone := seeded[3].ID + 1
	if got := summaryIDs(f.listPublished(t, app.CatalogQuery{Limit: 10, After: gone})); len(got) != 3 || got[0] != seeded[3].ID {
		t.Fatalf("unknown cursor = %v", got)
	}
}

func TestReviewStateTrailAndQueue(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	first, second := f.seedCourse(t), f.seedCourse(t)

	// second is submitted earlier, so it heads the queue.
	submit := func(c *domain.Course, at time.Time) {
		t.Helper()
		rev, err := c.Submit(f.ids.New(), c.OwnerID, at)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.tx.RunInTx(ctx, func(r app.Repos) error {
			if err := r.Courses.Update(ctx, c); err != nil {
				return err
			}
			return r.Courses.InsertReview(ctx, rev)
		}); err != nil {
			t.Fatal(err)
		}
	}
	submit(&second, f.now.Add(-time.Hour))
	submit(&first, f.now)

	sent, err := first.RequestChanges(f.ids.New(), 1, "Add a summary", f.now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	var queue []domain.Course
	var loaded domain.Course
	var trail []domain.Review
	if err := f.tx.RunInTx(ctx, func(r app.Repos) error {
		if err := r.Courses.Update(ctx, &first); err != nil {
			return err
		}
		if err := r.Courses.InsertReview(ctx, sent); err != nil {
			return err
		}
		var err error
		if queue, err = r.Courses.ListInReview(ctx); err != nil {
			return err
		}
		if loaded, err = r.Courses.FindByID(ctx, first.ID); err != nil {
			return err
		}
		trail, err = r.Courses.ListReviews(ctx, first.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(queue) != 1 || queue[0].ID != second.ID || !queue[0].SubmittedAt.Equal(f.now.Add(-time.Hour)) {
		t.Fatalf("queue = %+v", queue)
	}
	if loaded.Status != domain.StatusChangesRequested || loaded.ReviewNote != "Add a summary" ||
		!loaded.SubmittedAt.Equal(f.now) || !loaded.ReviewedAt.Equal(f.now.Add(time.Minute)) {
		t.Fatalf("loaded = %+v", loaded)
	}
	if len(trail) != 2 || trail[0].Decision != domain.DecisionSubmitted || trail[1] != sent {
		t.Fatalf("trail = %+v", trail)
	}
}

func TestLockForUpdateBlocksStatusChange(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	c := f.seedCourse(t)

	if err := f.tx.RunInTx(ctx, func(r app.Repos) error { return r.Courses.LockForUpdate(ctx, 424242) }); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("missing course err = %v", err)
	}

	locked, release := make(chan struct{}), make(chan struct{})
	holder := make(chan error, 1)
	go func() {
		holder <- f.tx.RunInTx(ctx, func(r app.Repos) error {
			if err := r.Courses.LockForUpdate(ctx, c.ID); err != nil {
				return err
			}
			close(locked)
			<-release
			return nil
		})
	}()
	<-locked
	updated := make(chan error, 1)
	go func() {
		updated <- f.tx.RunInTx(ctx, func(r app.Repos) error {
			if _, err := c.Submit(f.ids.New(), c.OwnerID, f.now); err != nil {
				return err
			}
			return r.Courses.Update(ctx, &c)
		})
	}()
	select {
	case err := <-updated:
		t.Fatalf("update finished while locked: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	if err := <-holder; err != nil {
		t.Fatal(err)
	}
	if err := <-updated; err != nil {
		t.Fatal(err)
	}
}

func TestLiveVersionIsASnapshot(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	c := f.seedCourse(t)
	lectureID := c.Lectures[0].ID
	if err := f.tx.RunInTx(ctx, func(r app.Repos) error {
		_, err := r.Contents.ReplaceBlocks(ctx, c.ID, lectureID, []contentblocks.Block{textBlock(t, f.ids.New(), "a", 0, "<p>v1</p>")})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	f.publish(t, &c)

	// Edit the working copy: rename, add a lecture, replace the content, drop the section.
	renamed, _ := contentblocks.NewTitle("Renamed")
	if err := c.RenameLecture(lectureID, renamed, f.now); err != nil {
		t.Fatal(err)
	}
	if err := c.AddLecture(f.ids.New(), renamed, false, false, f.now); err != nil {
		t.Fatal(err)
	}
	if err := c.RemoveSection(c.Sections[0].ID, f.now); err != nil {
		t.Fatal(err)
	}
	if err := f.tx.RunInTx(ctx, func(r app.Repos) error {
		if err := r.Courses.Update(ctx, &c); err != nil {
			return err
		}
		_, err := r.Contents.ReplaceBlocks(ctx, c.ID, lectureID, []contentblocks.Block{textBlock(t, f.ids.New(), "b", 0, "<p>v2</p>")})
		return err
	}); err != nil {
		t.Fatal(err)
	}

	var working, live domain.Course
	var header app.LectureHeader
	var blocks []contentblocks.Block
	if err := f.tx.RunInTx(ctx, func(r app.Repos) error {
		var err error
		if working, err = r.Courses.FindByID(ctx, c.ID); err != nil {
			return err
		}
		if live, err = r.Courses.FindVersion(ctx, c.ID, 1); err != nil {
			return err
		}
		if header, err = r.Contents.FindVersionLecture(ctx, c.ID, 1, lectureID); err != nil {
			return err
		}
		blocks, err = r.Contents.ListVersionBlocks(ctx, c.ID, 1, lectureID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if working.Status != domain.StatusDraft || working.Live != (domain.LiveVersion{Number: 1, PublishedAt: f.now}) || working.LastVersion != 1 {
		t.Fatalf("working = %s %+v %d", working.Status, working.Live, working.LastVersion)
	}
	if live.Status != domain.StatusPublished || len(live.Sections) != 1 || len(live.Lectures) != 1 ||
		live.Lectures[0].Title.String() != "L1" || live.Lectures[0].SectionID != live.Sections[0].ID || !live.Lectures[0].HasText {
		t.Fatalf("live = %+v", live)
	}
	if header.Title != "L1" || len(blocks) != 1 || blocks[0].ClientBlockID() != "a" {
		t.Fatalf("live lecture = %+v %+v", header, blocks)
	}
	if got := f.listPublished(t, app.CatalogQuery{Limit: 10}); len(got) != 1 || got[0].LectureCount != 1 || got[0].SectionCount != 1 {
		t.Fatalf("catalog = %+v", got)
	}

	if err := c.Archive(f.now); err != nil {
		t.Fatal(err)
	}
	if err := f.tx.RunInTx(ctx, func(r app.Repos) error { return r.Courses.Update(ctx, &c) }); err != nil {
		t.Fatal(err)
	}
	var archived domain.Course
	if err := f.tx.RunInTx(ctx, func(r app.Repos) error {
		var err error
		archived, err = r.Courses.FindByID(ctx, c.ID)
		return err
	}); err != nil || archived.IsLive() {
		t.Fatalf("archived = %v, %+v", err, archived.Live)
	}
	if got := f.listPublished(t, app.CatalogQuery{Limit: 10}); len(got) != 0 {
		t.Fatalf("archived in catalog = %+v", got)
	}
}

func TestDiscardDraftAndVersionHistory(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	ids, _ := id.NewGenerator(1)
	svc := app.NewCourseService(f.tx, ids, clock.System{}, nil, nil, nil)
	owner := auth.Principal{UserID: 100, Role: auth.RoleInstructor}
	c := f.seedCourse(t)
	section, lecture := c.Sections[0], c.Lectures[0]
	if err := f.tx.RunInTx(ctx, func(r app.Repos) error {
		_, err := r.Contents.ReplaceBlocks(ctx, c.ID, lecture.ID, []contentblocks.Block{textBlock(t, f.ids.New(), "a", 0, "<p>v1</p>")})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	f.publish(t, &c)

	// Remove the section and the lecture, then reuse the section's title for a new one.
	if err := svc.RemoveSection(ctx, owner, c.ID, section.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.RemoveLecture(ctx, owner, c.ID, lecture.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AddSection(ctx, owner, c.ID, section.Title.String()); err != nil {
		t.Fatal(err)
	}

	restored, err := svc.DiscardDraft(ctx, owner, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Status != domain.StatusPublished || len(restored.Sections) != 1 || restored.Sections[0].ID != section.ID ||
		len(restored.Lectures) != 1 || restored.Lectures[0].ID != lecture.ID || restored.Lectures[0].SectionID != section.ID {
		t.Fatalf("restored = %+v", restored)
	}
	var reloaded domain.Course
	var blocks []contentblocks.Block
	var header app.LectureHeader
	var versions []app.VersionSummary
	if err := f.tx.RunInTx(ctx, func(r app.Repos) error {
		var err error
		if reloaded, err = r.Courses.FindByID(ctx, c.ID); err != nil {
			return err
		}
		if header, err = r.Contents.FindLecture(ctx, c.ID, lecture.ID); err != nil {
			return err
		}
		if blocks, err = r.Contents.ListBlocks(ctx, lecture.ID); err != nil {
			return err
		}
		versions, err = r.Courses.ListVersions(ctx, c.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if reloaded.Status != domain.StatusPublished || len(reloaded.Lectures) != 1 || !reloaded.Lectures[0].HasText {
		t.Fatalf("reloaded = %+v", reloaded)
	}
	if len(blocks) != 1 || blocks[0].ClientBlockID() != "a" || header.ContentRevision < 1 {
		t.Fatalf("restored content = %+v rev %d", blocks, header.ContentRevision)
	}
	if len(versions) != 1 || versions[0].Number != 1 || versions[0].PublishedBy != 1 || !versions[0].PublishedAt.Equal(f.now) {
		t.Fatalf("versions = %+v", versions)
	}
	if _, err := svc.GetVersion(ctx, owner, c.ID, 2); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("missing version err = %v", err)
	}
}

func TestPinStorageAndSubmittedPins(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	c := f.seedCourse(t)

	if err := c.Publish(true, f.now); err != nil {
		t.Fatal(err)
	}

	quizPin := domain.AssessmentPin{Kind: domain.AssessmentQuiz, ID: 700, Revision: 2}
	examPin := domain.AssessmentPin{Kind: domain.AssessmentExam, ID: 800, Revision: 1}
	pins := []domain.AssessmentPin{quizPin, examPin}

	if err := f.tx.RunInTx(ctx, func(r app.Repos) error {
		if err := r.Courses.InsertVersion(ctx, &c, 1); err != nil {
			return err
		}
		return r.Courses.InsertVersionPins(ctx, c.ID, 1, pins)
	}); err != nil {
		t.Fatal(err)
	}

	if err := f.tx.RunInTx(ctx, func(r app.Repos) error {
		got, err := r.Courses.ListVersionPins(ctx, c.ID, 1)
		if err != nil {
			return err
		}
		want := []domain.AssessmentPin{examPin, quizPin}
		if !slices.Equal(got, want) {
			t.Fatalf("version 1 pins = %+v, want %+v", got, want)
		}

		got2, err := r.Courses.ListVersionPins(ctx, c.ID, 2)
		if err != nil {
			return err
		}
		if len(got2) != 0 {
			t.Fatalf("version 2 pins = %+v, want empty", got2)
		}

		firstSet := []domain.AssessmentPin{quizPin}
		if err := r.Courses.ReplaceSubmittedPins(ctx, c.ID, firstSet); err != nil {
			return err
		}
		secondSet := []domain.AssessmentPin{examPin}
		if err := r.Courses.ReplaceSubmittedPins(ctx, c.ID, secondSet); err != nil {
			return err
		}
		sub, err := r.Courses.ListSubmittedPins(ctx, c.ID)
		if err != nil {
			return err
		}
		if !slices.Equal(sub, secondSet) {
			t.Fatalf("submitted pins = %+v, want %+v", sub, secondSet)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
