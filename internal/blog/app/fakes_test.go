package app_test

import (
	"cmp"
	"context"
	"maps"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/blog/app"
	"github.com/santoshkc2200/ioe-backend/internal/blog/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/clock"
	"github.com/santoshkc2200/ioe-backend/internal/platform/contentblocks"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

var (
	t0         = time.Date(2026, 10, 7, 9, 0, 0, 0, time.UTC)
	author     = auth.Principal{UserID: 10, Role: auth.RoleInstructor}
	otherInstr = auth.Principal{UserID: 11, Role: auth.RoleInstructor}
	student    = auth.Principal{UserID: 12, Role: auth.RoleStudent}
	admin      = auth.Principal{UserID: 1, Role: auth.RoleRootAdmin}
)

type vkey struct {
	post   id.ID
	number int
}

type memVersion struct {
	view   app.VersionView
	blocks []contentblocks.Block
}

// memStore is a TxRunner whose transactions copy state and commit only on success.
type memStore struct {
	mu       sync.Mutex
	posts    map[id.ID]domain.Post
	revs     map[id.ID]int64
	blocks   map[id.ID][]contentblocks.Block
	versions map[vkey]memVersion
	slugs    map[string]id.ID
	events   []domain.Event
}

func newMemStore() *memStore {
	return &memStore{posts: map[id.ID]domain.Post{}, revs: map[id.ID]int64{}, blocks: map[id.ID][]contentblocks.Block{},
		versions: map[vkey]memVersion{}, slugs: map[string]id.ID{}}
}

type memTx struct {
	posts    map[id.ID]domain.Post
	revs     map[id.ID]int64
	blocks   map[id.ID][]contentblocks.Block
	versions map[vkey]memVersion
	slugs    map[string]id.ID
	events   []domain.Event
}

func (m *memStore) RunInTx(_ context.Context, fn func(app.Repos) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	tx := &memTx{posts: maps.Clone(m.posts), revs: maps.Clone(m.revs), blocks: maps.Clone(m.blocks),
		versions: maps.Clone(m.versions), slugs: maps.Clone(m.slugs)}
	if err := fn(app.Repos{Posts: tx, Contents: tx, Slugs: tx, Public: tx, Events: tx}); err != nil {
		return err
	}
	m.posts, m.revs, m.blocks, m.versions, m.slugs = tx.posts, tx.revs, tx.blocks, tx.versions, tx.slugs
	m.events = append(m.events, tx.events...)
	return nil
}

// --- PostRepository

func (t *memTx) FindByID(_ context.Context, postID id.ID) (domain.Post, error) {
	p, ok := t.posts[postID]
	if !ok {
		return domain.Post{}, app.ErrNotFound
	}
	return p, nil
}

func (t *memTx) ListByAuthor(_ context.Context, authorID id.ID) ([]domain.Post, error) {
	var out []domain.Post
	for _, p := range t.posts {
		if p.AuthorID == authorID {
			out = append(out, p)
		}
	}
	slices.SortFunc(out, func(a, b domain.Post) int { return cmp.Compare(b.ID, a.ID) })
	return out, nil
}

func (t *memTx) Insert(_ context.Context, p *domain.Post) error {
	p.Version = 1
	t.posts[p.ID] = *p
	return nil
}

func (t *memTx) Update(_ context.Context, p *domain.Post) error {
	if cur, ok := t.posts[p.ID]; !ok || cur.Version != p.Version {
		return app.ErrConcurrentModification
	}
	p.Version++
	t.posts[p.ID] = *p
	return nil
}

func (t *memTx) InsertVersion(_ context.Context, p *domain.Post, minutes int, by id.ID) error {
	t.versions[vkey{p.ID, p.Live.Number}] = memVersion{
		view:   app.VersionView{Number: p.Live.Number, Details: p.Details, ReadingMinutes: minutes, PublishedBy: by, PublishedAt: p.Live.PublishedAt},
		blocks: slices.Clone(t.blocks[p.ID]),
	}
	return nil
}

func (t *memTx) FindVersion(_ context.Context, postID id.ID, number int) (app.VersionView, error) {
	v, ok := t.versions[vkey{postID, number}]
	if !ok {
		return app.VersionView{}, app.ErrNotFound
	}
	return v.view, nil
}

func (t *memTx) ListVersions(_ context.Context, postID id.ID) ([]app.VersionSummary, error) {
	var out []app.VersionSummary
	for k, v := range t.versions {
		if k.post == postID {
			out = append(out, app.VersionSummary{Number: k.number, PublishedBy: v.view.PublishedBy, PublishedAt: v.view.PublishedAt})
		}
	}
	slices.SortFunc(out, func(a, b app.VersionSummary) int { return cmp.Compare(b.Number, a.Number) })
	return out, nil
}

// --- ContentRepository

func (t *memTx) FindHeader(_ context.Context, postID id.ID) (app.ContentHeader, error) {
	if _, ok := t.posts[postID]; !ok {
		return app.ContentHeader{}, app.ErrNotFound
	}
	return app.ContentHeader{PostID: postID, ContentRevision: t.revs[postID]}, nil
}

func (t *memTx) FindHeaderForUpdate(ctx context.Context, postID id.ID) (app.ContentHeader, error) {
	return t.FindHeader(ctx, postID)
}

func (t *memTx) ListBlocks(_ context.Context, postID id.ID) ([]contentblocks.Block, error) {
	return slices.Clone(t.blocks[postID]), nil
}

func (t *memTx) ListVersionBlocks(_ context.Context, postID id.ID, number int) ([]contentblocks.Block, error) {
	return slices.Clone(t.versions[vkey{postID, number}].blocks), nil
}

func (t *memTx) ApplyPatch(_ context.Context, postID id.ID, base int64, plan app.BlockWritePlan) (int64, error) {
	if t.revs[postID] != base {
		return 0, app.ErrConcurrentModification
	}
	byClient := map[string]contentblocks.Block{}
	for _, b := range t.blocks[postID] {
		byClient[b.ClientBlockID()] = b
	}
	for _, d := range plan.Deletes {
		delete(byClient, d)
	}
	for _, u := range plan.Upserts {
		byClient[u.ClientBlockID()] = u
	}
	out := make([]contentblocks.Block, 0, len(plan.Order))
	for i, cid := range plan.Order {
		b := byClient[cid]
		out = append(out, b.WithIdentity(b.ID(), cid, i))
	}
	t.blocks[postID] = out
	t.revs[postID] = base + 1
	return base + 1, nil
}

func (t *memTx) ReplaceBlocks(_ context.Context, postID id.ID, blocks []contentblocks.Block) (int64, error) {
	t.blocks[postID] = slices.Clone(blocks)
	t.revs[postID]++
	return t.revs[postID], nil
}

// --- SlugRepository

func (t *memTx) Reserve(_ context.Context, s domain.Slug, postID id.ID) error {
	if owner, ok := t.slugs[s.String()]; ok && owner != postID {
		return app.ErrSlugTaken
	}
	t.slugs[s.String()] = postID
	return nil
}

func (t *memTx) Resolve(_ context.Context, slug string) (id.ID, error) {
	owner, ok := t.slugs[slug]
	if !ok {
		return 0, app.ErrNotFound
	}
	return owner, nil
}

// --- PublicRepository

func (t *memTx) live() []app.LivePost {
	var out []app.LivePost
	for _, p := range t.posts {
		if !p.IsLive() {
			continue
		}
		v := t.versions[vkey{p.ID, p.Live.Number}].view
		out = append(out, app.LivePost{PostID: p.ID, AuthorID: p.AuthorID, Slug: p.Slug.String(), Number: v.Number,
			Details: v.Details, ReadingMinutes: v.ReadingMinutes, FirstPublishedAt: p.FirstPublishedAt, PublishedAt: v.PublishedAt})
	}
	slices.SortFunc(out, func(a, b app.LivePost) int {
		return cmp.Or(b.FirstPublishedAt.Compare(a.FirstPublishedAt), cmp.Compare(b.PostID, a.PostID))
	})
	return out
}

func before(c *app.Cursor, at time.Time, pid id.ID) bool {
	return c == nil || at.Before(c.FirstPublishedAt) || (at.Equal(c.FirstPublishedAt) && pid < c.PostID)
}

func (t *memTx) ListLive(_ context.Context, tag string, after *app.Cursor, limit int) ([]app.LivePost, error) {
	var out []app.LivePost
	for _, lp := range t.live() {
		if (tag == "" || slices.Contains(lp.Details.Tags, tag)) && before(after, lp.FirstPublishedAt, lp.PostID) && len(out) < limit {
			out = append(out, lp)
		}
	}
	return out, nil
}

func (t *memTx) FindLive(_ context.Context, postID id.ID) (app.LivePost, error) {
	for _, lp := range t.live() {
		if lp.PostID == postID {
			return lp, nil
		}
	}
	return app.LivePost{}, app.ErrNotFound
}

func (t *memTx) ListTags(context.Context) ([]app.TagCount, error) {
	counts := map[string]int{}
	for _, lp := range t.live() {
		for _, tag := range lp.Details.Tags {
			counts[tag]++
		}
	}
	out := make([]app.TagCount, 0, len(counts))
	for tag, n := range counts {
		out = append(out, app.TagCount{Tag: tag, Count: n})
	}
	slices.SortFunc(out, func(a, b app.TagCount) int { return cmp.Or(cmp.Compare(b.Count, a.Count), cmp.Compare(a.Tag, b.Tag)) })
	return out, nil
}

func (t *memTx) ListIndex(_ context.Context, after *app.Cursor, limit int) ([]app.IndexEntry, error) {
	var out []app.IndexEntry
	for _, lp := range t.live() {
		if before(after, lp.FirstPublishedAt, lp.PostID) && len(out) < limit {
			out = append(out, app.IndexEntry{PostID: lp.PostID, Slug: lp.Slug, FirstPublishedAt: lp.FirstPublishedAt, PublishedAt: lp.PublishedAt})
		}
	}
	return out, nil
}

// --- EventPublisher

func (t *memTx) Publish(_ context.Context, evs ...domain.Event) error {
	t.events = append(t.events, evs...)
	return nil
}

// --- helpers

type names map[id.ID]string

func (n names) Names(_ context.Context, ids []id.ID) (map[id.ID]string, error) {
	out := map[id.ID]string{}
	for _, i := range ids {
		if v, ok := n[i]; ok {
			out[i] = v
		}
	}
	return out, nil
}

// fixture grows with the services: Task 4 adds contents, Task 5 adds public.
type fixture struct {
	ids      *id.Generator
	store    *memStore
	clock    *clock.Fake
	posts    *app.PostService
	contents *app.ContentService
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	ids, err := id.NewGenerator(0)
	if err != nil {
		t.Fatal(err)
	}
	store, clk := newMemStore(), clock.NewFake(t0)
	return fixture{ids: ids, store: store, clock: clk, posts: app.NewPostService(store, ids, clk), contents: app.NewContentService(store, ids)}
}

func textBlock(cid, body string) app.BlockInput {
	return app.BlockInput{ClientBlockID: cid, Type: "text", Body: body}
}

// setBlocks writes a post's draft blocks directly and bumps its revision, as a content
// write would.
func (f fixture) setBlocks(t *testing.T, postID id.ID, cids ...string) {
	t.Helper()
	f.store.mu.Lock()
	defer f.store.mu.Unlock()
	blocks := make([]contentblocks.Block, len(cids))
	for i, cid := range cids {
		body, err := contentblocks.NewSanitizedTextBody("<p>hello world</p>")
		if err != nil {
			t.Fatal(err)
		}
		blocks[i] = contentblocks.NewTextBlock(f.ids.New(), cid, i, body)
	}
	f.store.blocks[postID] = blocks
	f.store.revs[postID]++
}

// draft creates a post with one text block "b1".
func (f fixture) draft(t *testing.T, p auth.Principal, title string, tags ...string) domain.Post {
	t.Helper()
	post, err := f.posts.Create(context.Background(), p, app.DetailsInput{Title: title, Tags: tags})
	if err != nil {
		t.Fatal(err)
	}
	f.setBlocks(t, post.ID, "b1")
	return post
}

func (f fixture) published(t *testing.T, p auth.Principal, title string, tags ...string) domain.Post {
	t.Helper()
	post, err := f.posts.Publish(context.Background(), p, f.draft(t, p, title, tags...).ID)
	if err != nil {
		t.Fatal(err)
	}
	return post
}
