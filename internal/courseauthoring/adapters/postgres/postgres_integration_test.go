//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/adapters/postgres"
	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/app"
	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/domain"
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
