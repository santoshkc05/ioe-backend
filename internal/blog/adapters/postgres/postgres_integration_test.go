//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/blog/adapters/postgres"
	"github.com/santoshkc2200/ioe-backend/internal/blog/app"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/clock"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
	"github.com/santoshkc2200/ioe-backend/internal/platform/postgres/pgtest"
)

var (
	ctx    = context.Background()
	author = auth.Principal{UserID: 10, Role: auth.RoleInstructor}
	admin  = auth.Principal{UserID: 1, Role: auth.RoleRootAdmin}
)

type noNames struct{}

func (noNames) Names(context.Context, []id.ID) (map[id.ID]string, error) {
	return map[id.ID]string{}, nil
}

type fixture struct {
	clock    *clock.Fake
	posts    *app.PostService
	contents *app.ContentService
	public   *app.PublicService
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	ids, err := id.NewGenerator(0)
	if err != nil {
		t.Fatal(err)
	}
	clk := clock.NewFake(time.Date(2026, 10, 7, 9, 0, 0, 0, time.UTC))
	tx := postgres.NewTxRunner(pgtest.New(t), clk)
	return fixture{clock: clk, posts: app.NewPostService(tx, ids, clk), contents: app.NewContentService(tx, ids),
		public: app.NewPublicService(tx, noNames{})}
}

func (f fixture) publish(t *testing.T, title string, tags ...string) id.ID {
	t.Helper()
	p, err := f.posts.Create(ctx, author, app.DetailsInput{Title: title, Tags: tags})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.contents.Replace(ctx, author, p.ID, []app.BlockInput{{ClientBlockID: "b1", Type: "text", Body: "<p>hello</p>"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.posts.Publish(ctx, author, p.ID); err != nil {
		t.Fatal(err)
	}
	return p.ID
}

func TestPublishCopiesVersionAndServesLive(t *testing.T) {
	f := newFixture(t)
	postID := f.publish(t, "Hello Postgres", "go")
	if _, err := f.contents.Replace(ctx, author, postID, []app.BlockInput{{ClientBlockID: "b2", Type: "text", Body: "<p>draft</p>"}}); err != nil {
		t.Fatal(err)
	}
	got, err := f.public.GetBySlug(ctx, "hello-postgres")
	if err != nil {
		t.Fatal(err)
	}
	if got.Number != 1 || len(got.Blocks) != 1 || got.Blocks[0].ClientBlockID() != "b1" || got.Details.Tags[0] != "go" || got.ReadingMinutes != 1 {
		t.Fatalf("live %+v", got)
	}
	v, err := f.posts.GetVersion(ctx, author, postID, 1)
	if err != nil || len(v.Blocks) != 1 {
		t.Fatalf("version %+v %v", v, err)
	}
	discarded, err := f.posts.DiscardDraft(ctx, author, postID)
	if err != nil || !discarded.IsLive() {
		t.Fatalf("discard %+v %v", discarded, err)
	}
	c, _ := f.contents.Get(ctx, author, postID)
	if len(c.Blocks) != 1 || c.Blocks[0].ClientBlockID() != "b1" {
		t.Fatalf("restored %+v", c.Blocks)
	}
}

func TestSlugHistoryIsGlobalAndPermanent(t *testing.T) {
	f := newFixture(t)
	a := f.publish(t, "Same Title")
	b, err := f.posts.Create(ctx, author, app.DetailsInput{Title: "Same Title"})
	if err != nil || b.Slug.String() != "same-title-2" {
		t.Fatalf("second %+v %v", b.Slug, err)
	}
	if _, err := f.posts.SetSlug(ctx, author, a, "moved"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.posts.SetSlug(ctx, author, b.ID, "same-title"); !errors.Is(err, app.ErrSlugTaken) {
		t.Fatalf("take old slug: %v", err)
	}
	if got, err := f.public.GetBySlug(ctx, "same-title"); err != nil || got.Slug != "moved" {
		t.Fatalf("redirect %+v %v", got, err)
	}
}

func TestPublicIndexPagesAcrossMicrosecondTimestamps(t *testing.T) {
	f := newFixture(t)
	var want []id.ID
	for i := range 5 {
		f.clock.Advance(time.Microsecond)
		want = append([]id.ID{f.publish(t, "Post number "+string(rune('a'+i)))}, want...)
	}
	var got []id.ID
	var after *app.Cursor
	for {
		page, err := f.public.Index(ctx, after, 2)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range page.Items {
			got = append(got, e.PostID)
		}
		if page.Next == nil {
			break
		}
		after = page.Next
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestPublicIndexStableAcrossRepublish(t *testing.T) {
	f := newFixture(t)
	first := f.publish(t, "First one")
	f.clock.Advance(time.Second)
	f.publish(t, "Second one")
	f.clock.Advance(time.Second)
	if _, err := f.posts.Publish(ctx, author, first); err != nil {
		t.Fatal(err)
	}
	page, _ := f.public.Index(ctx, nil, 0)
	if len(page.Items) != 2 || page.Items[1].PostID != first || !page.Items[1].PublishedAt.After(page.Items[1].FirstPublishedAt) {
		t.Fatalf("index %+v", page.Items)
	}
}

func TestTagsCountLiveVersionsOnly(t *testing.T) {
	f := newFixture(t)
	a := f.publish(t, "Tagged one", "go", "web")
	f.publish(t, "Tagged two", "go")
	p, _ := f.posts.Get(ctx, author, a)
	if _, err := f.posts.UpdateDetails(ctx, author, a, p.Version, app.DetailsInput{Title: "Tagged one", Tags: []string{"draft"}}); err != nil {
		t.Fatal(err)
	}
	tags, err := f.public.Tags(ctx)
	if err != nil || len(tags) != 2 || tags[0] != (app.TagCount{Tag: "go", Count: 2}) || tags[1] != (app.TagCount{Tag: "web", Count: 1}) {
		t.Fatalf("tags %+v %v", tags, err)
	}
	if page, _ := f.public.List(ctx, "web", nil, 0); len(page.Items) != 1 {
		t.Fatalf("tag filter %+v", page.Items)
	}
	if _, err := f.posts.Unpublish(ctx, admin, a); err != nil {
		t.Fatal(err)
	}
	if tags, _ := f.public.Tags(ctx); len(tags) != 1 {
		t.Fatalf("after unpublish %+v", tags)
	}
}

func TestConcurrentPatchOneWins(t *testing.T) {
	f := newFixture(t)
	p, _ := f.posts.Create(ctx, author, app.DetailsInput{Title: "Race post"})
	base := int64(0)
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cid := []string{"x", "y"}[i]
			_, errs[i] = f.contents.Patch(ctx, author, p.ID, app.PatchInput{BaseRevision: &base, Order: []string{cid},
				Upserts: []app.BlockInput{{ClientBlockID: cid, Type: "text", Body: "<p>" + cid + "</p>"}}})
		}()
	}
	wg.Wait()
	var conflict *app.RevisionConflictError
	ok, conflicted := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.As(err, &conflict):
			conflicted++
		default:
			t.Fatalf("unexpected %v", err)
		}
	}
	if ok != 1 || conflicted != 1 {
		t.Fatalf("ok=%d conflicted=%d", ok, conflicted)
	}
}

func TestArchivedPostIsHidden(t *testing.T) {
	f := newFixture(t)
	postID := f.publish(t, "Archive me")
	if _, err := f.posts.Archive(ctx, author, postID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.public.GetBySlug(ctx, "archive-me"); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("archived visible: %v", err)
	}
}
