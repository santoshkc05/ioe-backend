package app_test

import (
	"errors"
	"testing"

	"github.com/santoshkc2200/ioe-backend/internal/blog/app"
	"github.com/santoshkc2200/ioe-backend/internal/blog/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/contentblocks"
)

func rev(n int64) *int64 { return &n }

func TestPatchContent(t *testing.T) {
	f := newFixture(t)
	p := f.draft(t, author, "Patch me") // revision 1, blocks [b1]
	got, err := f.contents.Patch(ctx, author, p.ID, app.PatchInput{BaseRevision: rev(1), Order: []string{"b2", "b1"},
		Upserts: []app.BlockInput{textBlock("b2", "<p>second</p>")}})
	if err != nil || got != 2 {
		t.Fatalf("patch %d %v", got, err)
	}
	c, _ := f.contents.Get(ctx, author, p.ID)
	if len(c.Blocks) != 2 || c.Blocks[0].ClientBlockID() != "b2" || c.ContentRevision != 2 {
		t.Fatalf("content %+v", c)
	}
	_, err = f.contents.Patch(ctx, author, p.ID, app.PatchInput{BaseRevision: rev(1), Order: []string{"b1"}, Deletes: []string{"b2"}})
	var conflict *app.RevisionConflictError
	if !errors.As(err, &conflict) || conflict.Current.ContentRevision != 2 {
		t.Fatalf("stale patch: %v", err)
	}
}

func TestPatchValidation(t *testing.T) {
	f := newFixture(t)
	p := f.draft(t, author, "Validate me")
	cases := map[string]struct {
		in   app.PatchInput
		want error
	}{
		"no revision":  {app.PatchInput{Order: []string{"b1"}}, app.ErrRevisionRequired},
		"set mismatch": {app.PatchInput{BaseRevision: rev(1), Order: []string{}}, contentblocks.ErrBlockSetMismatch},
		"overlap":      {app.PatchInput{BaseRevision: rev(1), Order: []string{"b1"}, Deletes: []string{"b1"}}, contentblocks.ErrOrderDeleteOverlap},
		"quiz kind": {app.PatchInput{BaseRevision: rev(1), Order: []string{"b1", "q"},
			Upserts: []app.BlockInput{{ClientBlockID: "q", Type: "quiz"}}}, app.ErrInvalidInput},
		"http image": {app.PatchInput{BaseRevision: rev(1), Order: []string{"b1", "i"},
			Upserts: []app.BlockInput{{ClientBlockID: "i", Type: "image", URL: "http://x.example/a.png", Layout: "inline", Alignment: "center"}}}, domain.ErrInvalidImage},
		"unsafe html": {app.PatchInput{BaseRevision: rev(1), Order: []string{"b1", "x"},
			Upserts: []app.BlockInput{textBlock("x", "<script>alert(1)</script>")}}, contentblocks.ErrUnsafeContent},
	}
	for name, tc := range cases {
		if _, err := f.contents.Patch(ctx, author, p.ID, tc.in); !errors.Is(err, tc.want) {
			t.Errorf("%s: got %v, want %v", name, err, tc.want)
		}
	}
}

func TestContentAccess(t *testing.T) {
	f := newFixture(t)
	p := f.draft(t, author, "Private draft")
	if _, err := f.contents.Get(ctx, otherInstr, p.ID); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("other get: %v", err)
	}
	if _, err := f.contents.Replace(ctx, student, p.ID, nil); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("student replace: %v", err)
	}
	if _, err := f.contents.Replace(ctx, admin, p.ID, []app.BlockInput{textBlock("a", "<p>admin</p>")}); err != nil {
		t.Fatalf("admin replace: %v", err)
	}
}

func TestContentWriteAfterArchiveFails(t *testing.T) {
	f := newFixture(t)
	p := f.draft(t, author, "Soon archived")
	if _, err := f.posts.Archive(ctx, author, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.contents.Replace(ctx, author, p.ID, []app.BlockInput{textBlock("z", "<p>late</p>")}); !errors.Is(err, domain.ErrPostArchived) {
		t.Fatalf("replace archived: %v", err)
	}
}

func TestContentEditDoesNotTouchPostVersion(t *testing.T) {
	f := newFixture(t)
	p := f.draft(t, author, "Version stable")
	if _, err := f.contents.Patch(ctx, author, p.ID, app.PatchInput{BaseRevision: rev(1), Order: []string{"b1"},
		Upserts: []app.BlockInput{textBlock("b1", "<p>edited</p>")}}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.posts.UpdateDetails(ctx, author, p.ID, p.Version, app.DetailsInput{Title: "Still fine"}); err != nil {
		t.Fatalf("details after content edit: %v", err)
	}
}
