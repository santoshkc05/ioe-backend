package domain

import (
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

type Status string

const (
	StatusDraft     Status = "draft"
	StatusPublished Status = "published"
	StatusArchived  Status = "archived"
)

// LiveVersion identifies the published snapshot readers are served. Number is 0 when the
// post is not live.
type LiveVersion struct {
	Number      int
	PublishedAt time.Time
}

// Post is the authoring aggregate. Blocks are stored and versioned outside it.
// A post is live exactly when its status is published.
type Post struct {
	ID               id.ID
	AuthorID         id.ID
	Details          Details
	Slug             Slug
	Status           Status
	LastVersion      int         // latest published version number; 0 before the first publish
	Live             LiveVersion // zero when not live
	FirstPublishedAt time.Time   // set by the first publish and never changed
	Version          int64       // optimistic-concurrency token; 0 until first insert
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

func NewPost(postID, authorID id.ID, d Details, slug Slug, now time.Time) Post {
	return Post{ID: postID, AuthorID: authorID, Details: d.clone(), Slug: slug, Status: StatusDraft,
		CreatedAt: now, UpdatedAt: now}
}

// CanAuthor reports whether p may create posts.
func CanAuthor(p auth.Principal) bool {
	return p.Role == auth.RoleInstructor || p.Role == auth.RoleRootAdmin
}

// IsManagedBy reports whether pr may edit, publish, archive or discard the post's draft.
func (p *Post) IsManagedBy(pr auth.Principal) bool {
	return pr.Role == auth.RoleRootAdmin || pr.UserID == p.AuthorID
}

func (p *Post) IsLive() bool { return p.Live.Number > 0 }

func (p *Post) editable() error {
	if p.Status == StatusArchived {
		return ErrPostArchived
	}
	return nil
}

func (p *Post) UpdateDetails(d Details, now time.Time) error {
	if err := p.editable(); err != nil {
		return err
	}
	p.Details, p.UpdatedAt = d.clone(), now
	return nil
}

func (p *Post) SetSlug(s Slug, now time.Time) error {
	if err := p.editable(); err != nil {
		return err
	}
	p.Slug, p.UpdatedAt = s, now
	return nil
}

// Publish makes the next version live. The caller snapshots details and blocks as
// version p.Live.Number.
func (p *Post) Publish(now time.Time) error {
	if p.Status == StatusArchived {
		return ErrInvalidStatusTransition
	}
	p.LastVersion++
	p.Live = LiveVersion{Number: p.LastVersion, PublishedAt: now}
	if p.FirstPublishedAt.IsZero() {
		p.FirstPublishedAt = now
	}
	p.Status, p.UpdatedAt = StatusPublished, now
	return nil
}

// Unpublish takes the post off the public site; its versions are kept.
func (p *Post) Unpublish(now time.Time) error {
	if p.Status != StatusPublished {
		return ErrInvalidStatusTransition
	}
	p.Live, p.Status, p.UpdatedAt = LiveVersion{}, StatusDraft, now
	return nil
}

// Archive retires the post for good. Its slugs stay reserved.
func (p *Post) Archive(now time.Time) error {
	if p.Status == StatusArchived {
		return ErrInvalidStatusTransition
	}
	p.Live, p.Status, p.UpdatedAt = LiveVersion{}, StatusArchived, now
	return nil
}

// DiscardDraft restores the details of the live version. The caller restores its blocks.
func (p *Post) DiscardDraft(live Details, now time.Time) error {
	if !p.IsLive() {
		return ErrInvalidStatusTransition
	}
	p.Details, p.UpdatedAt = live.clone(), now
	return nil
}
