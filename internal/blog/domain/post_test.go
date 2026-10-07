package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/blog/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
)

var t0 = time.Date(2026, 10, 7, 9, 0, 0, 0, time.UTC)

func newPost(t *testing.T) domain.Post {
	t.Helper()
	d, err := domain.NewDetails("Hello", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	return domain.NewPost(1, 10, d, domain.SlugFromTitle("Hello", 1), t0)
}

func TestRoles(t *testing.T) {
	p := newPost(t)
	for role, want := range map[auth.Role]bool{auth.RoleStudent: false, auth.RoleInstructor: true, auth.RoleRootAdmin: true} {
		if got := domain.CanAuthor(auth.Principal{UserID: 99, Role: role}); got != want {
			t.Errorf("CanAuthor(%s) = %v", role, got)
		}
	}
	if !p.IsManagedBy(auth.Principal{UserID: 10, Role: auth.RoleInstructor}) {
		t.Error("author cannot manage")
	}
	if p.IsManagedBy(auth.Principal{UserID: 11, Role: auth.RoleInstructor}) {
		t.Error("other instructor manages")
	}
	if !p.IsManagedBy(auth.Principal{UserID: 11, Role: auth.RoleRootAdmin}) {
		t.Error("root admin cannot manage")
	}
}

func TestLifecycle(t *testing.T) {
	p := newPost(t)
	if err := p.Unpublish(t0); !errors.Is(err, domain.ErrInvalidStatusTransition) {
		t.Fatalf("unpublish draft: %v", err)
	}
	if err := p.DiscardDraft(p.Details, t0); !errors.Is(err, domain.ErrInvalidStatusTransition) {
		t.Fatalf("discard non-live: %v", err)
	}
	if err := p.Publish(t0); err != nil {
		t.Fatal(err)
	}
	if !p.IsLive() || p.Live.Number != 1 || p.LastVersion != 1 || !p.FirstPublishedAt.Equal(t0) {
		t.Fatalf("after publish %+v", p)
	}
	t1 := t0.Add(time.Hour)
	if err := p.Publish(t1); err != nil {
		t.Fatal(err)
	}
	if p.Live.Number != 2 || !p.Live.PublishedAt.Equal(t1) || !p.FirstPublishedAt.Equal(t0) {
		t.Fatalf("republish %+v", p)
	}
	if err := p.Unpublish(t1); err != nil || p.IsLive() || p.Status != domain.StatusDraft || p.LastVersion != 2 {
		t.Fatalf("unpublish %+v %v", p, err)
	}
	if err := p.Publish(t1); err != nil || p.Live.Number != 3 || !p.FirstPublishedAt.Equal(t0) {
		t.Fatalf("publish after unpublish %+v %v", p, err)
	}
	if err := p.Archive(t1); err != nil || p.IsLive() || p.Status != domain.StatusArchived {
		t.Fatalf("archive %+v %v", p, err)
	}
	for name, op := range map[string]func() error{
		"publish": func() error { return p.Publish(t1) },
		"archive": func() error { return p.Archive(t1) },
		"details": func() error { return p.UpdateDetails(p.Details, t1) },
		"slug":    func() error { return p.SetSlug(p.Slug, t1) },
	} {
		if err := op(); err == nil {
			t.Errorf("%s on archived post succeeded", name)
		}
	}
}

func TestDiscardDraftRestoresLiveDetails(t *testing.T) {
	p := newPost(t)
	if err := p.Publish(t0); err != nil {
		t.Fatal(err)
	}
	live := p.Details
	edited, _ := domain.NewDetails("Edited", "s", "", []string{"x"})
	if err := p.UpdateDetails(edited, t0); err != nil {
		t.Fatal(err)
	}
	if err := p.DiscardDraft(live, t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if p.Details.Title.String() != "Hello" || len(p.Details.Tags) != 0 {
		t.Fatalf("details %+v", p.Details)
	}
}
