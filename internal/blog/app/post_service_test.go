package app_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/blog/app"
	"github.com/santoshkc2200/ioe-backend/internal/blog/domain"
)

var ctx = context.Background()

func TestCreateRequiresAuthorRole(t *testing.T) {
	f := newFixture(t)
	if _, err := f.posts.Create(ctx, student, app.DetailsInput{Title: "Hi there"}); !errors.Is(err, app.ErrForbidden) {
		t.Fatalf("student create: %v", err)
	}
	p, err := f.posts.Create(ctx, author, app.DetailsInput{Title: "Hello World", Tags: []string{"Go"}})
	if err != nil {
		t.Fatal(err)
	}
	if p.Slug.String() != "hello-world" || p.Status != domain.StatusDraft || p.Version != 1 || p.Details.Tags[0] != "go" {
		t.Fatalf("created %+v", p)
	}
	if _, err := f.posts.Create(ctx, author, app.DetailsInput{Title: " "}); !errors.Is(err, app.ErrInvalidInput) {
		t.Fatalf("blank title: %v", err)
	}
}

func TestCreateSuffixesTakenSlugs(t *testing.T) {
	f := newFixture(t)
	a := f.draft(t, author, "Hello World")
	b := f.draft(t, otherInstr, "Hello World")
	c := f.draft(t, author, "Hello World")
	if a.Slug.String() != "hello-world" || b.Slug.String() != "hello-world-2" || c.Slug.String() != "hello-world-3" {
		t.Fatalf("slugs %s %s %s", a.Slug, b.Slug, c.Slug)
	}
}

func TestOnlyManagersSeeAndEditDrafts(t *testing.T) {
	f := newFixture(t)
	p := f.draft(t, author, "Draft post")
	if _, err := f.posts.Get(ctx, otherInstr, p.ID); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("other get draft: %v", err)
	}
	if _, err := f.posts.Get(ctx, admin, p.ID); err != nil {
		t.Fatalf("admin get: %v", err)
	}
	if _, err := f.posts.UpdateDetails(ctx, otherInstr, p.ID, p.Version, app.DetailsInput{Title: "x"}); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("other edit draft: %v", err)
	}
	live, _ := f.posts.Publish(ctx, author, p.ID)
	if _, err := f.posts.UpdateDetails(ctx, otherInstr, p.ID, live.Version, app.DetailsInput{Title: "x"}); !errors.Is(err, app.ErrForbidden) {
		t.Fatalf("other edit live: %v", err)
	}
}

func TestUpdateDetailsChecksVersion(t *testing.T) {
	f := newFixture(t)
	p := f.draft(t, author, "First title")
	got, err := f.posts.UpdateDetails(ctx, author, p.ID, p.Version, app.DetailsInput{Title: "Second", Summary: "s"})
	if err != nil || got.Details.Title.String() != "Second" || got.Version != p.Version+1 {
		t.Fatalf("update %+v %v", got, err)
	}
	if _, err := f.posts.UpdateDetails(ctx, author, p.ID, p.Version, app.DetailsInput{Title: "Third"}); !errors.Is(err, app.ErrConcurrentModification) {
		t.Fatalf("stale version: %v", err)
	}
	if got.Slug != p.Slug {
		t.Fatal("title change moved the slug")
	}
}

func TestSetSlugKeepsHistory(t *testing.T) {
	f := newFixture(t)
	a := f.draft(t, author, "Alpha post")
	b := f.draft(t, author, "Beta post")
	if _, err := f.posts.SetSlug(ctx, author, a.ID, "renamed"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.posts.SetSlug(ctx, author, b.ID, "alpha-post"); !errors.Is(err, app.ErrSlugTaken) {
		t.Fatalf("taking another post's old slug: %v", err)
	}
	if _, err := f.posts.SetSlug(ctx, author, b.ID, "Not Valid!"); !errors.Is(err, domain.ErrInvalidSlug) {
		t.Fatalf("invalid slug: %v", err)
	}
}

func TestSetSlugBackToOwnOldSlug(t *testing.T) {
	f := newFixture(t)
	a := f.draft(t, author, "Alpha post")
	if _, err := f.posts.SetSlug(ctx, author, a.ID, "beta"); err != nil {
		t.Fatal(err)
	}
	got, err := f.posts.SetSlug(ctx, author, a.ID, "alpha-post")
	if err != nil || got.Slug.String() != "alpha-post" {
		t.Fatalf("back to own slug: %+v %v", got.Slug, err)
	}
}

func TestPublishSnapshotsAndEmits(t *testing.T) {
	f := newFixture(t)
	p := f.draft(t, author, "Publish me", "go")
	empty, _ := f.posts.Create(ctx, author, app.DetailsInput{Title: "Nothing here"})
	if _, err := f.posts.Publish(ctx, author, empty.ID); !errors.Is(err, domain.ErrEmptyPost) {
		t.Fatalf("empty publish: %v", err)
	}
	if _, err := f.posts.Publish(ctx, student, p.ID); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("student publish: %v", err)
	}
	live, err := f.posts.Publish(ctx, author, p.ID)
	if err != nil || live.Live.Number != 1 || !live.FirstPublishedAt.Equal(t0) {
		t.Fatalf("publish %+v %v", live, err)
	}
	v, err := f.posts.GetVersion(ctx, author, p.ID, 1)
	if err != nil || v.ReadingMinutes != 1 || len(v.Blocks) != 1 || v.PublishedBy != author.UserID || v.Details.Tags[0] != "go" {
		t.Fatalf("version %+v %v", v, err)
	}
	ev, ok := f.store.events[len(f.store.events)-1].(domain.PostPublished)
	if !ok || ev.PostID != p.ID || ev.Version != 1 || ev.Slug != "publish-me" {
		t.Fatalf("event %#v", f.store.events)
	}
	f.clock.Advance(time.Hour)
	again, _ := f.posts.Publish(ctx, author, p.ID)
	if again.Live.Number != 2 || !again.FirstPublishedAt.Equal(t0) {
		t.Fatalf("republish %+v", again)
	}
	vs, _ := f.posts.ListVersions(ctx, author, p.ID)
	if len(vs) != 2 || vs[0].Number != 2 {
		t.Fatalf("versions %+v", vs)
	}
	if _, err := f.posts.GetVersion(ctx, author, p.ID, 3); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("missing version: %v", err)
	}
}

func TestUnpublishIsRootAdminOnly(t *testing.T) {
	f := newFixture(t)
	p := f.published(t, author, "Live post")
	if _, err := f.posts.Unpublish(ctx, author, p.ID); !errors.Is(err, app.ErrForbidden) {
		t.Fatalf("author unpublish: %v", err)
	}
	got, err := f.posts.Unpublish(ctx, admin, p.ID)
	if err != nil || got.IsLive() || got.Status != domain.StatusDraft {
		t.Fatalf("admin unpublish %+v %v", got, err)
	}
	if _, ok := f.store.events[len(f.store.events)-1].(domain.PostUnpublished); !ok {
		t.Fatalf("event %#v", f.store.events)
	}
}

func TestArchiveIsTerminal(t *testing.T) {
	f := newFixture(t)
	p := f.published(t, author, "Old post")
	got, err := f.posts.Archive(ctx, author, p.ID)
	if err != nil || got.Status != domain.StatusArchived || got.IsLive() {
		t.Fatalf("archive %+v %v", got, err)
	}
	if _, err := f.posts.Publish(ctx, author, p.ID); !errors.Is(err, domain.ErrInvalidStatusTransition) {
		t.Fatalf("publish archived: %v", err)
	}
	if _, err := f.posts.UpdateDetails(ctx, author, p.ID, got.Version, app.DetailsInput{Title: "x"}); !errors.Is(err, domain.ErrPostArchived) {
		t.Fatalf("edit archived: %v", err)
	}
}

func TestDiscardDraftRestoresLiveVersion(t *testing.T) {
	f := newFixture(t)
	p := f.published(t, author, "Stable title")
	edited, _ := f.posts.UpdateDetails(ctx, author, p.ID, p.Version, app.DetailsInput{Title: "Edited title"})
	f.setBlocks(t, p.ID, "b9")
	revBefore := f.store.revs[p.ID]
	got, err := f.posts.DiscardDraft(ctx, author, p.ID)
	if err != nil || got.Details.Title.String() != "Stable title" || got.Version != edited.Version+1 {
		t.Fatalf("discard %+v %v", got, err)
	}
	blocks := f.store.blocks[p.ID]
	if len(blocks) != 1 || blocks[0].ClientBlockID() != "b1" || f.store.revs[p.ID] != revBefore+1 {
		t.Fatalf("content after discard %v rev %d (before %d)", blocks, f.store.revs[p.ID], revBefore)
	}
	draft := f.draft(t, author, "Never published")
	if _, err := f.posts.DiscardDraft(ctx, author, draft.ID); !errors.Is(err, domain.ErrInvalidStatusTransition) {
		t.Fatalf("discard non-live: %v", err)
	}
}

func TestListByAuthor(t *testing.T) {
	f := newFixture(t)
	f.draft(t, author, "One post")
	f.draft(t, author, "Two post")
	f.draft(t, otherInstr, "Three post")
	got, err := f.posts.ListByAuthor(ctx, author, author.UserID)
	if err != nil || len(got) != 2 {
		t.Fatalf("own list %d %v", len(got), err)
	}
	if _, err := f.posts.ListByAuthor(ctx, otherInstr, author.UserID); !errors.Is(err, app.ErrForbidden) {
		t.Fatalf("other list: %v", err)
	}
	if got, _ := f.posts.ListByAuthor(ctx, admin, author.UserID); len(got) != 2 {
		t.Fatalf("admin list %d", len(got))
	}
}
