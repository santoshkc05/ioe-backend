package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/santoshkc2200/ioe-backend/internal/blog/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/clock"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

const maxSlugAttempts = 50

// PostService implements post authoring and lifecycle use cases.
type PostService struct {
	tx    TxRunner
	ids   *id.Generator
	clock clock.Clock
}

func NewPostService(tx TxRunner, ids *id.Generator, c clock.Clock) *PostService {
	return &PostService{tx: tx, ids: ids, clock: c}
}

type DetailsInput struct {
	Title, Summary, CoverURL string
	Tags                     []string
}

// newDetails passes domain validation errors through for the HTTP layer to map and wraps
// the rest (title validation from contentblocks) as invalid input.
func newDetails(in DetailsInput) (domain.Details, error) {
	d, err := domain.NewDetails(in.Title, in.Summary, in.CoverURL, in.Tags)
	if err != nil && !isDomainValidation(err) {
		return domain.Details{}, fmt.Errorf("%w: %w", ErrInvalidInput, err)
	}
	return d, err
}

func isDomainValidation(err error) bool {
	for _, e := range []error{domain.ErrInvalidSummary, domain.ErrInvalidCoverURL, domain.ErrInvalidTag, domain.ErrTooManyTags} {
		if errors.Is(err, e) {
			return true
		}
	}
	return false
}

// loadManaged returns the post for a write: ErrNotFound when the caller cannot see it
// (a non-live post they do not manage), ErrForbidden when it is live but not theirs.
func loadManaged(ctx context.Context, r Repos, p auth.Principal, postID id.ID) (domain.Post, error) {
	post, err := r.Posts.FindByID(ctx, postID)
	if err != nil {
		return domain.Post{}, err
	}
	if !post.IsManagedBy(p) {
		if post.IsLive() {
			return domain.Post{}, ErrForbidden
		}
		return domain.Post{}, ErrNotFound
	}
	return post, nil
}

// mutate loads a managed post, applies fn, and saves it in one transaction.
func (s *PostService) mutate(ctx context.Context, p auth.Principal, postID id.ID, fn func(r Repos, post *domain.Post) error) (domain.Post, error) {
	var out domain.Post
	err := s.tx.RunInTx(ctx, func(r Repos) error {
		post, err := loadManaged(ctx, r, p, postID)
		if err != nil {
			return err
		}
		if err := fn(r, &post); err != nil {
			return err
		}
		if err := r.Posts.Update(ctx, &post); err != nil {
			return err
		}
		out = post
		return nil
	})
	return out, err
}

// reserveSlug claims base, or base-2, base-3, … for postID.
func reserveSlug(ctx context.Context, r Repos, base domain.Slug, postID id.ID) (domain.Slug, error) {
	for n := 1; n <= maxSlugAttempts; n++ {
		s := base
		if n > 1 {
			s = base.WithSuffix(n)
		}
		err := r.Slugs.Reserve(ctx, s, postID)
		if err == nil {
			return s, nil
		}
		if !errors.Is(err, ErrSlugTaken) {
			return domain.Slug{}, err
		}
	}
	return domain.Slug{}, ErrSlugTaken
}

func (s *PostService) Create(ctx context.Context, p auth.Principal, in DetailsInput) (domain.Post, error) {
	if !domain.CanAuthor(p) {
		return domain.Post{}, ErrForbidden
	}
	d, err := newDetails(in)
	if err != nil {
		return domain.Post{}, err
	}
	postID := s.ids.New()
	var post domain.Post
	err = s.tx.RunInTx(ctx, func(r Repos) error {
		slug, err := reserveSlug(ctx, r, domain.SlugFromTitle(d.Title.String(), postID), postID)
		if err != nil {
			return err
		}
		post = domain.NewPost(postID, p.UserID, d, slug, s.clock.Now())
		return r.Posts.Insert(ctx, &post)
	})
	return post, err
}

// Get returns a post's draft to its managers.
func (s *PostService) Get(ctx context.Context, p auth.Principal, postID id.ID) (domain.Post, error) {
	var post domain.Post
	err := s.tx.RunInTx(ctx, func(r Repos) error {
		var err error
		post, err = loadManaged(ctx, r, p, postID)
		return err
	})
	return post, err
}

// ListByAuthor returns an author's posts to the author and to root admins.
func (s *PostService) ListByAuthor(ctx context.Context, p auth.Principal, authorID id.ID) ([]domain.Post, error) {
	if p.UserID != authorID && p.Role != auth.RoleRootAdmin {
		return nil, ErrForbidden
	}
	var out []domain.Post
	err := s.tx.RunInTx(ctx, func(r Repos) error {
		var err error
		out, err = r.Posts.ListByAuthor(ctx, authorID)
		return err
	})
	return out, err
}

// UpdateDetails replaces the draft's details when version matches the stored one.
func (s *PostService) UpdateDetails(ctx context.Context, p auth.Principal, postID id.ID, version int64, in DetailsInput) (domain.Post, error) {
	d, err := newDetails(in)
	if err != nil {
		return domain.Post{}, err
	}
	return s.mutate(ctx, p, postID, func(_ Repos, post *domain.Post) error {
		if post.Version != version {
			return ErrConcurrentModification
		}
		return post.UpdateDetails(d, s.clock.Now())
	})
}

// SetSlug moves the post to a new slug. Its previous slugs keep resolving to it.
func (s *PostService) SetSlug(ctx context.Context, p auth.Principal, postID id.ID, raw string) (domain.Post, error) {
	slug, err := domain.NewSlug(raw)
	if err != nil {
		return domain.Post{}, err
	}
	return s.mutate(ctx, p, postID, func(r Repos, post *domain.Post) error {
		if err := post.SetSlug(slug, s.clock.Now()); err != nil {
			return err
		}
		return r.Slugs.Reserve(ctx, slug, postID)
	})
}

// Publish snapshots the draft's details and blocks as the next live version.
func (s *PostService) Publish(ctx context.Context, p auth.Principal, postID id.ID) (domain.Post, error) {
	return s.mutate(ctx, p, postID, func(r Repos, post *domain.Post) error {
		blocks, err := r.Contents.ListBlocks(ctx, postID)
		if err != nil {
			return err
		}
		if len(blocks) == 0 {
			return domain.ErrEmptyPost
		}
		now := s.clock.Now()
		if err := post.Publish(now); err != nil {
			return err
		}
		if err := r.Posts.InsertVersion(ctx, post, domain.ReadingMinutes(blocks), p.UserID); err != nil {
			return err
		}
		return r.Events.Publish(ctx, domain.PostPublished{PostID: post.ID, AuthorID: post.AuthorID,
			Version: post.Live.Number, Slug: post.Slug.String(), OccurredAt: now})
	})
}

// Unpublish takes a live post off the public site. Root admins only.
func (s *PostService) Unpublish(ctx context.Context, p auth.Principal, postID id.ID) (domain.Post, error) {
	return s.mutate(ctx, p, postID, func(r Repos, post *domain.Post) error {
		if p.Role != auth.RoleRootAdmin {
			return ErrForbidden
		}
		now := s.clock.Now()
		if err := post.Unpublish(now); err != nil {
			return err
		}
		return r.Events.Publish(ctx, domain.PostUnpublished{PostID: post.ID, ActorID: p.UserID, OccurredAt: now})
	})
}

// Archive retires the post. Archiving a live post also unpublishes it.
func (s *PostService) Archive(ctx context.Context, p auth.Principal, postID id.ID) (domain.Post, error) {
	return s.mutate(ctx, p, postID, func(r Repos, post *domain.Post) error {
		wasLive, now := post.IsLive(), s.clock.Now()
		if err := post.Archive(now); err != nil {
			return err
		}
		if !wasLive {
			return nil
		}
		return r.Events.Publish(ctx, domain.PostUnpublished{PostID: post.ID, ActorID: p.UserID, OccurredAt: now})
	})
}

func (s *PostService) ListVersions(ctx context.Context, p auth.Principal, postID id.ID) ([]VersionSummary, error) {
	var out []VersionSummary
	err := s.tx.RunInTx(ctx, func(r Repos) error {
		if _, err := loadManaged(ctx, r, p, postID); err != nil {
			return err
		}
		var err error
		out, err = r.Posts.ListVersions(ctx, postID)
		return err
	})
	return out, err
}

func (s *PostService) GetVersion(ctx context.Context, p auth.Principal, postID id.ID, number int) (VersionView, error) {
	var v VersionView
	err := s.tx.RunInTx(ctx, func(r Repos) error {
		if _, err := loadManaged(ctx, r, p, postID); err != nil {
			return err
		}
		var err error
		if v, err = r.Posts.FindVersion(ctx, postID, number); err != nil {
			return err
		}
		v.Blocks, err = r.Contents.ListVersionBlocks(ctx, postID, number)
		return err
	})
	return v, err
}

// DiscardDraft restores details and blocks from the live version. The content revision
// moves on, so an editor holding the discarded content gets a revision conflict.
func (s *PostService) DiscardDraft(ctx context.Context, p auth.Principal, postID id.ID) (domain.Post, error) {
	return s.mutate(ctx, p, postID, func(r Repos, post *domain.Post) error {
		if !post.IsLive() {
			return domain.ErrInvalidStatusTransition
		}
		live, err := r.Posts.FindVersion(ctx, postID, post.Live.Number)
		if err != nil {
			return err
		}
		if err := post.DiscardDraft(live.Details, s.clock.Now()); err != nil {
			return err
		}
		blocks, err := r.Contents.ListVersionBlocks(ctx, postID, post.Live.Number)
		if err != nil {
			return err
		}
		_, err = r.Contents.ReplaceBlocks(ctx, postID, blocks)
		return err
	})
}
