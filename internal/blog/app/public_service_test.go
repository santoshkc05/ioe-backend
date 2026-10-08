package app_test

import (
	"errors"
	"testing"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/blog/app"
)

func TestGetBySlugServesLiveVersionAndRedirectsOldSlugs(t *testing.T) {
	f := newFixture(t)
	p := f.published(t, author, "First Post", "go")
	if _, err := f.posts.UpdateDetails(ctx, author, p.ID, p.Version, app.DetailsInput{Title: "Unpublished edit"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.posts.SetSlug(ctx, author, p.ID, "renamed"); err != nil {
		t.Fatal(err)
	}
	got, err := f.public.GetBySlug(ctx, "first-post")
	if err != nil {
		t.Fatal(err)
	}
	if got.Slug != "renamed" || got.RequestedSlug != "first-post" || got.Details.Title.String() != "First Post" ||
		got.AuthorName != "Asha" || len(got.Blocks) != 1 {
		t.Fatalf("got %+v", got)
	}
}

func TestGetBySlugIsCaseInsensitive(t *testing.T) {
	f := newFixture(t)
	f.published(t, author, "Mixed Case")
	if got, err := f.public.GetBySlug(ctx, "Mixed-Case"); err != nil || got.Slug != "mixed-case" {
		t.Fatalf("got %+v %v", got, err)
	}
}

func TestGetBySlugHidesNonLivePosts(t *testing.T) {
	f := newFixture(t)
	draft := f.draft(t, author, "Draft only")
	archived := f.published(t, author, "Archived one")
	_, _ = f.posts.Archive(ctx, author, archived.ID)
	for _, slug := range []string{draft.Slug.String(), archived.Slug.String(), "never-existed", "Not a slug!"} {
		if _, err := f.public.GetBySlug(ctx, slug); !errors.Is(err, app.ErrNotFound) {
			t.Errorf("%q: %v", slug, err)
		}
	}
}

func TestListPagesNewestFirstAndFiltersTags(t *testing.T) {
	f := newFixture(t)
	a := f.published(t, author, "Post A", "go")
	f.clock.Advance(time.Minute)
	b := f.published(t, otherInstr, "Post B", "web")
	f.clock.Advance(time.Minute)
	c := f.published(t, author, "Post C", "go", "web")
	page, err := f.public.List(ctx, "", nil, 2)
	if err != nil || len(page.Items) != 2 || page.Items[0].PostID != c.ID || page.Items[1].PostID != b.ID || page.Next == nil {
		t.Fatalf("page1 %+v %v", page, err)
	}
	if page.Items[1].AuthorName != "Bikash" {
		t.Fatalf("author name %q", page.Items[1].AuthorName)
	}
	page, err = f.public.List(ctx, "", page.Next, 2)
	if err != nil || len(page.Items) != 1 || page.Items[0].PostID != a.ID || page.Next != nil {
		t.Fatalf("page2 %+v %v", page, err)
	}
	page, _ = f.public.List(ctx, "go", nil, 10)
	if len(page.Items) != 2 || page.Items[0].PostID != c.ID || page.Items[1].PostID != a.ID {
		t.Fatalf("tag go %+v", page.Items)
	}
	if _, err := f.public.List(ctx, "", nil, 51); !errors.Is(err, app.ErrInvalidInput) {
		t.Fatalf("limit 51: %v", err)
	}
	if _, err := f.public.List(ctx, "c++", nil, 0); !errors.Is(err, app.ErrInvalidInput) {
		t.Fatalf("bad tag: %v", err)
	}
}

func TestListNormalizesTag(t *testing.T) {
	f := newFixture(t)
	f.published(t, author, "Tagged", "go")
	if page, err := f.public.List(ctx, " Go ", nil, 0); err != nil || len(page.Items) != 1 {
		t.Fatalf("got %+v %v", page, err)
	}
}

func TestRepublishKeepsListPosition(t *testing.T) {
	f := newFixture(t)
	old := f.published(t, author, "Older post")
	f.clock.Advance(time.Minute)
	f.published(t, author, "Newer post")
	f.clock.Advance(time.Minute)
	if _, err := f.posts.Publish(ctx, author, old.ID); err != nil {
		t.Fatal(err)
	}
	page, _ := f.public.List(ctx, "", nil, 0)
	if page.Items[1].PostID != old.ID || page.Items[1].Number != 2 {
		t.Fatalf("order %+v", page.Items)
	}
}

func TestTagsAndIndexUseLiveVersionsOnly(t *testing.T) {
	f := newFixture(t)
	p := f.published(t, author, "Live tags", "go", "web")
	f.draft(t, author, "Draft tags", "draft-only")
	if _, err := f.posts.UpdateDetails(ctx, author, p.ID, p.Version, app.DetailsInput{Title: "Live tags", Tags: []string{"changed"}}); err != nil {
		t.Fatal(err)
	}
	tags, err := f.public.Tags(ctx)
	if err != nil || len(tags) != 2 || tags[0].Tag != "go" || tags[1].Tag != "web" {
		t.Fatalf("tags %+v %v", tags, err)
	}
	idx, err := f.public.Index(ctx, nil, 0)
	if err != nil || len(idx.Items) != 1 || idx.Items[0].Slug != "live-tags" || idx.Next != nil {
		t.Fatalf("index %+v %v", idx, err)
	}
	if _, err := f.public.Index(ctx, nil, 501); !errors.Is(err, app.ErrInvalidInput) {
		t.Fatalf("index limit: %v", err)
	}
}
