package app_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/app"
	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/contentblocks"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time { return c.now }

// memStore is a single-goroutine fake; RunInTx copies state and commits only on success.
type memStore struct {
	mu        sync.Mutex
	courses   map[id.ID]domain.Course
	headers   map[id.ID]app.LectureHeader
	blocks    map[id.ID][]contentblocks.Block
	published []domain.Event
}

func newMemStore() *memStore {
	return &memStore{courses: map[id.ID]domain.Course{}, headers: map[id.ID]app.LectureHeader{}, blocks: map[id.ID][]contentblocks.Block{}}
}

func (m *memStore) RunInTx(_ context.Context, fn func(app.Repos) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	tx := &memTx{store: m, courses: clone(m.courses), headers: clone(m.headers), blocks: clone(m.blocks)}
	if err := fn(app.Repos{Courses: tx, Contents: tx, Events: tx}); err != nil {
		return err
	}
	m.courses, m.headers, m.blocks = tx.courses, tx.headers, tx.blocks
	m.published = append(m.published, tx.events...)
	return nil
}

func clone[K comparable, V any](in map[K]V) map[K]V {
	out := make(map[K]V, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

type memTx struct {
	store   *memStore
	courses map[id.ID]domain.Course
	headers map[id.ID]app.LectureHeader
	blocks  map[id.ID][]contentblocks.Block
	events  []domain.Event
}

func (t *memTx) FindByID(_ context.Context, cid id.ID) (domain.Course, error) {
	c, ok := t.courses[cid]
	if !ok {
		return domain.Course{}, app.ErrNotFound
	}
	c.Sections = append([]domain.Section(nil), c.Sections...)
	c.Lectures = append([]domain.Lecture(nil), c.Lectures...)
	return c, nil
}

func (t *memTx) ListByOwner(_ context.Context, owner id.ID) ([]domain.Course, error) {
	var out []domain.Course
	for _, c := range t.courses {
		if c.OwnerID == owner {
			out = append(out, c)
		}
	}
	return out, nil
}

func (t *memTx) Insert(_ context.Context, c *domain.Course) error {
	c.Version = 1
	t.courses[c.ID] = *c
	t.syncHeaders(*c)
	return nil
}

func (t *memTx) Update(_ context.Context, c *domain.Course) error {
	if t.courses[c.ID].Version != c.Version {
		return app.ErrConcurrentModification
	}
	c.Version++
	t.courses[c.ID] = *c
	t.syncHeaders(*c)
	return nil
}

func (t *memTx) syncHeaders(c domain.Course) {
	for lid, h := range t.headers {
		if h.CourseID == c.ID {
			if _, ok := c.Lecture(lid); !ok {
				delete(t.headers, lid)
				delete(t.blocks, lid)
			}
		}
	}
	for _, l := range c.Lectures {
		h := t.headers[l.ID]
		h.LectureID, h.CourseID, h.Title, h.FreePreview = l.ID, c.ID, l.Title.String(), l.FreePreview
		t.headers[l.ID] = h
	}
}

func (t *memTx) FindLecture(_ context.Context, cid, lid id.ID) (app.LectureHeader, error) {
	h, ok := t.headers[lid]
	if !ok || h.CourseID != cid {
		return app.LectureHeader{}, app.ErrNotFound
	}
	return h, nil
}

func (t *memTx) FindLectureForUpdate(ctx context.Context, cid, lid id.ID) (app.LectureHeader, error) {
	return t.FindLecture(ctx, cid, lid)
}

func (t *memTx) ListBlocks(_ context.Context, lid id.ID) ([]contentblocks.Block, error) {
	return append([]contentblocks.Block(nil), t.blocks[lid]...), nil
}

func (t *memTx) ApplyPatch(_ context.Context, _, lid id.ID, base int64, plan app.BlockWritePlan) (int64, error) {
	h := t.headers[lid]
	if h.ContentRevision != base {
		return 0, app.ErrConcurrentModification
	}
	byClient := map[string]contentblocks.Block{}
	for _, b := range t.blocks[lid] {
		byClient[b.ClientBlockID()] = b
	}
	for _, d := range plan.Deletes {
		delete(byClient, d)
	}
	for _, u := range plan.Upserts {
		byClient[u.ClientBlockID()] = u
	}
	next := make([]contentblocks.Block, 0, len(plan.Order))
	for i, cb := range plan.Order {
		b := byClient[cb]
		next = append(next, b.WithIdentity(b.ID(), cb, i))
	}
	t.blocks[lid] = next
	h.ContentRevision++
	t.headers[lid] = h
	return h.ContentRevision, nil
}

func (t *memTx) ReplaceBlocks(_ context.Context, _, lid id.ID, blocks []contentblocks.Block) (int64, error) {
	t.blocks[lid] = append([]contentblocks.Block(nil), blocks...)
	h := t.headers[lid]
	h.ContentRevision++
	t.headers[lid] = h
	return h.ContentRevision, nil
}

func (t *memTx) Publish(_ context.Context, evs ...domain.Event) error {
	t.events = append(t.events, evs...)
	return nil
}

type enrolled map[[2]id.ID]bool

var _ app.EnrollmentQuery = enrolled(nil)

func (e enrolled) IsActivelyEnrolled(_ context.Context, courseID, userID id.ID) (bool, error) {
	return e[[2]id.ID{courseID, userID}], nil
}

func testIDs(t *testing.T) *id.Generator {
	t.Helper()
	g, err := id.NewGenerator(0)
	if err != nil {
		t.Fatal(err)
	}
	return g
}
