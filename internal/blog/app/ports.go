// Package app contains the blog use cases and the ports they depend on.
package app

import (
	"context"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/blog/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/contentblocks"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

// PostRepository stores the Post aggregate. FindByID returns ErrNotFound. Insert sets
// p.Version to 1; Update returns ErrConcurrentModification when p.Version is stale and
// increments it on success. InsertVersion snapshots p.Details and the stored blocks as
// version p.Live.Number. FindVersion returns ErrNotFound for an unknown number and no
// blocks; ListVersions is newest first. ListByAuthor is newest first.
type PostRepository interface {
	FindByID(ctx context.Context, postID id.ID) (domain.Post, error)
	ListByAuthor(ctx context.Context, authorID id.ID) ([]domain.Post, error)
	Insert(ctx context.Context, p *domain.Post) error
	Update(ctx context.Context, p *domain.Post) error
	InsertVersion(ctx context.Context, p *domain.Post, readingMinutes int, publishedBy id.ID) error
	FindVersion(ctx context.Context, postID id.ID, number int) (VersionView, error)
	ListVersions(ctx context.Context, postID id.ID) ([]VersionSummary, error)
}

type VersionSummary struct {
	Number      int
	PublishedBy id.ID
	PublishedAt time.Time
}

// VersionView is one published snapshot.
type VersionView struct {
	Number         int
	Details        domain.Details
	ReadingMinutes int
	PublishedBy    id.ID
	PublishedAt    time.Time
	Blocks         []contentblocks.Block
}

// ContentHeader is a post's content revision without its blocks.
type ContentHeader struct {
	PostID          id.ID
	ContentRevision int64
}

// BlockWritePlan is a validated content diff. Order is the full desired order of client block IDs.
type BlockWritePlan struct {
	Upserts []contentblocks.Block
	Deletes []string
	Order   []string
}

// ContentRepository reads and writes a post's draft blocks without touching the post's
// optimistic-concurrency version. FindHeader* return ErrNotFound. FindHeaderForUpdate
// locks the post row until the transaction ends. ApplyPatch returns
// ErrConcurrentModification when baseRevision is stale. ReplaceBlocks bumps the revision
// unconditionally.
type ContentRepository interface {
	FindHeader(ctx context.Context, postID id.ID) (ContentHeader, error)
	FindHeaderForUpdate(ctx context.Context, postID id.ID) (ContentHeader, error)
	ListBlocks(ctx context.Context, postID id.ID) ([]contentblocks.Block, error)
	ListVersionBlocks(ctx context.Context, postID id.ID, number int) ([]contentblocks.Block, error)
	ApplyPatch(ctx context.Context, postID id.ID, baseRevision int64, plan BlockWritePlan) (int64, error)
	ReplaceBlocks(ctx context.Context, postID id.ID, blocks []contentblocks.Block) (int64, error)
}

// SlugRepository owns slug history. Reserve records slug for postID and returns
// ErrSlugTaken when it belongs to another post, without failing the transaction.
// Reserving a slug the post already owns succeeds. Resolve returns ErrNotFound.
type SlugRepository interface {
	Reserve(ctx context.Context, slug domain.Slug, postID id.ID) error
	Resolve(ctx context.Context, slug string) (id.ID, error)
}

// Cursor is a keyset position in (FirstPublishedAt DESC, PostID DESC) order.
type Cursor struct {
	FirstPublishedAt time.Time
	PostID           id.ID
}

// LivePost is a post's live version as readers see it, without blocks.
type LivePost struct {
	PostID           id.ID
	AuthorID         id.ID
	Slug             string // the post's current slug
	Number           int
	Details          domain.Details
	ReadingMinutes   int
	FirstPublishedAt time.Time
	PublishedAt      time.Time
}

type TagCount struct {
	Tag   string
	Count int
}

type IndexEntry struct {
	PostID           id.ID
	Slug             string
	FirstPublishedAt time.Time
	PublishedAt      time.Time
}

// PublicRepository reads live versions only. ListLive and ListIndex return up to limit
// rows after the cursor (from the start when nil) in (FirstPublishedAt DESC, PostID DESC)
// order; ListLive filters on tag when it is not empty. FindLive returns ErrNotFound when
// the post is not live. ListTags counts live posts per tag, most used first, then by tag.
type PublicRepository interface {
	ListLive(ctx context.Context, tag string, after *Cursor, limit int) ([]LivePost, error)
	FindLive(ctx context.Context, postID id.ID) (LivePost, error)
	ListTags(ctx context.Context) ([]TagCount, error)
	ListIndex(ctx context.Context, after *Cursor, limit int) ([]IndexEntry, error)
}

// EventPublisher records events in the current transaction.
type EventPublisher interface {
	Publish(ctx context.Context, events ...domain.Event) error
}

// Repos are bound to one transaction.
type Repos struct {
	Posts    PostRepository
	Contents ContentRepository
	Slugs    SlugRepository
	Public   PublicRepository
	Events   EventPublisher
}

// TxRunner commits when fn returns nil and rolls back otherwise.
type TxRunner interface {
	RunInTx(ctx context.Context, fn func(Repos) error) error
}

// UserDirectory is backed by identity. Unknown users are absent from the result.
type UserDirectory interface {
	Names(ctx context.Context, userIDs []id.ID) (map[id.ID]string, error)
}
