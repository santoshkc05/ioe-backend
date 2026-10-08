package app_test

import (
	"context"
	"slices"
	"sort"
	"strings"
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
	mu          sync.Mutex
	courses     map[id.ID]domain.Course
	headers     map[id.ID]app.LectureHeader
	blocks      map[id.ID][]contentblocks.Block
	reviews     []domain.Review
	versions    map[versionKey]snapshot
	submitted   map[id.ID][]domain.AssessmentPin
	versionPins map[[2]int64][]domain.AssessmentPin
	published   []domain.Event
	categories  map[id.ID]domain.Category
}

type versionKey struct {
	course id.ID
	number int
}

// snapshot is a published version: the course as readers see it and its lectures' blocks.
type snapshot struct {
	by      id.ID
	course  domain.Course
	headers map[id.ID]app.LectureHeader
	blocks  map[id.ID][]contentblocks.Block
}

func newMemStore() *memStore {
	return &memStore{courses: map[id.ID]domain.Course{}, headers: map[id.ID]app.LectureHeader{}, blocks: map[id.ID][]contentblocks.Block{},
		versions: map[versionKey]snapshot{}, submitted: map[id.ID][]domain.AssessmentPin{}, versionPins: map[[2]int64][]domain.AssessmentPin{},
		categories: map[id.ID]domain.Category{}}
}

func (m *memStore) RunInTx(_ context.Context, fn func(app.Repos) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	submittedClone := make(map[id.ID][]domain.AssessmentPin, len(m.submitted))
	for k, v := range m.submitted {
		submittedClone[k] = append([]domain.AssessmentPin(nil), v...)
	}
	versionPinsClone := make(map[[2]int64][]domain.AssessmentPin, len(m.versionPins))
	for k, v := range m.versionPins {
		versionPinsClone[k] = append([]domain.AssessmentPin(nil), v...)
	}
	tx := &memTx{store: m, courses: clone(m.courses), headers: clone(m.headers), blocks: clone(m.blocks),
		reviews: append([]domain.Review(nil), m.reviews...), versions: clone(m.versions),
		submitted: submittedClone, versionPins: versionPinsClone, categories: clone(m.categories)}
	if err := fn(app.Repos{Courses: tx, Contents: tx, Events: tx, Categories: memCategories{tx}}); err != nil {
		return err
	}
	m.courses, m.headers, m.blocks, m.reviews, m.versions = tx.courses, tx.headers, tx.blocks, tx.reviews, tx.versions
	m.submitted, m.versionPins = tx.submitted, tx.versionPins
	m.categories = tx.categories
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
	store       *memStore
	courses     map[id.ID]domain.Course
	headers     map[id.ID]app.LectureHeader
	blocks      map[id.ID][]contentblocks.Block
	reviews     []domain.Review
	versions    map[versionKey]snapshot
	submitted   map[id.ID][]domain.AssessmentPin
	versionPins map[[2]int64][]domain.AssessmentPin
	categories  map[id.ID]domain.Category
	events      []domain.Event
}

func (t *memTx) FindByID(_ context.Context, cid id.ID) (domain.Course, error) {
	c, ok := t.courses[cid]
	if !ok {
		return domain.Course{}, app.ErrNotFound
	}
	c.Sections = append([]domain.Section(nil), c.Sections...)
	c.Lectures = append([]domain.Lecture(nil), c.Lectures...)
	return t.hydrate(c), nil
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

func (t *memTx) ListPublished(_ context.Context, q app.CatalogQuery) ([]app.CourseSummary, error) {
	var out []app.CourseSummary
	for _, w := range t.courses {
		if !w.IsLive() {
			continue
		}
		c := t.hydrate(t.versions[versionKey{w.ID, w.Live.Number}].course)
		if (q.After != 0 && c.ID >= q.After) ||
			(q.Level != "" && c.Level != q.Level) ||
			(q.Price == app.PriceFree && !c.Price.IsFree()) || (q.Price == app.PricePaid && c.Price.IsFree()) ||
			(q.Category != "" && !slices.ContainsFunc(c.Categories, func(r domain.CategoryRef) bool { return r.Slug == q.Category })) ||
			(q.Tag != "" && !slices.Contains(c.Tags, q.Tag)) ||
			(q.Q != "" && !strings.Contains(strings.ToLower(c.Title.String()+" "+c.Description), strings.ToLower(q.Q))) {
			continue
		}
		var rank float32
		if q.Q != "" {
			rank = 1 // the fake ranks every match alike; ordering by rank is a PostgreSQL concern
		}
		out = append(out, app.CourseSummary{
			ID: c.ID, OwnerID: c.OwnerID, Title: c.Title.String(), Description: c.Description,
			Level: c.Level, ThumbnailURL: c.ThumbnailURL, Price: c.Price,
			Categories: c.Categories, Tags: c.Tags, Rank: rank,
			LectureCount: len(c.Lectures), SectionCount: len(c.Sections),
			CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	if len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return out, nil
}

func (t *memTx) ListInReview(_ context.Context) ([]domain.Course, error) {
	var out []domain.Course
	for _, c := range t.courses {
		if c.Status == domain.StatusInReview {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SubmittedAt.Before(out[j].SubmittedAt) })
	return out, nil
}

func (t *memTx) FindVersion(ctx context.Context, cid id.ID, number int) (domain.Course, error) {
	c, err := t.FindByID(ctx, cid)
	if err != nil {
		return domain.Course{}, err
	}
	snap, ok := t.versions[versionKey{cid, number}]
	if !ok {
		return domain.Course{}, app.ErrNotFound
	}
	v := snap.course
	v.Sections = append([]domain.Section(nil), v.Sections...)
	v.Lectures = append([]domain.Lecture(nil), v.Lectures...)
	v.Status, v.Version, v.LastVersion, v.Live = domain.StatusPublished, c.Version, c.LastVersion, c.Live
	v.ReviewNote, v.SubmittedAt, v.ReviewedAt, v.UpdatedAt = "", time.Time{}, time.Time{}, snap.course.Live.PublishedAt
	return t.hydrate(v), nil
}

func (t *memTx) ListVersions(_ context.Context, cid id.ID) ([]app.VersionSummary, error) {
	var out []app.VersionSummary
	for k, snap := range t.versions {
		if k.course == cid {
			out = append(out, app.VersionSummary{Number: k.number, PublishedBy: snap.by, PublishedAt: snap.course.Live.PublishedAt})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Number > out[j].Number })
	return out, nil
}

func (t *memTx) InsertVersion(_ context.Context, c *domain.Course, by id.ID) error {
	snap := snapshot{by: by, course: *c, headers: map[id.ID]app.LectureHeader{}, blocks: map[id.ID][]contentblocks.Block{}}
	snap.course.Sections = append([]domain.Section(nil), c.Sections...)
	snap.course.Lectures = append([]domain.Lecture(nil), c.Lectures...)
	for _, l := range c.Lectures {
		h := t.headers[l.ID]
		h.ContentRevision = 0
		snap.headers[l.ID] = h
		snap.blocks[l.ID] = append([]contentblocks.Block(nil), t.blocks[l.ID]...)
	}
	t.versions[versionKey{c.ID, c.Live.Number}] = snap
	return nil
}

func (t *memTx) FindVersionLecture(_ context.Context, cid id.ID, number int, lid id.ID) (app.LectureHeader, error) {
	h, ok := t.versions[versionKey{cid, number}].headers[lid]
	if !ok {
		return app.LectureHeader{}, app.ErrNotFound
	}
	return h, nil
}

func (t *memTx) ListVersionBlocks(_ context.Context, cid id.ID, number int, lid id.ID) ([]contentblocks.Block, error) {
	return append([]contentblocks.Block(nil), t.versions[versionKey{cid, number}].blocks[lid]...), nil
}

func (t *memTx) LockForUpdate(_ context.Context, cid id.ID) error {
	if _, ok := t.courses[cid]; !ok {
		return app.ErrNotFound
	}
	return nil
}

func (t *memTx) InsertReview(_ context.Context, r domain.Review) error {
	t.reviews = append(t.reviews, r)
	return nil
}

func (t *memTx) ListReviews(_ context.Context, cid id.ID) ([]domain.Review, error) {
	var out []domain.Review
	for _, r := range t.reviews {
		if r.CourseID == cid {
			out = append(out, r)
		}
	}
	return out, nil
}

func (t *memTx) ReplaceSubmittedPins(_ context.Context, courseID id.ID, pins []domain.AssessmentPin) error {
	t.submitted[courseID] = append([]domain.AssessmentPin(nil), pins...)
	return nil
}

func (t *memTx) ListSubmittedPins(_ context.Context, courseID id.ID) ([]domain.AssessmentPin, error) {
	out := append([]domain.AssessmentPin(nil), t.submitted[courseID]...)
	sortPins(out)
	return out, nil
}

func (t *memTx) InsertVersionPins(_ context.Context, courseID id.ID, number int, pins []domain.AssessmentPin) error {
	t.versionPins[[2]int64{int64(courseID), int64(number)}] = append([]domain.AssessmentPin(nil), pins...)
	return nil
}

func (t *memTx) ListVersionPins(_ context.Context, courseID id.ID, number int) ([]domain.AssessmentPin, error) {
	stored, ok := t.versionPins[[2]int64{int64(courseID), int64(number)}]
	if !ok {
		return nil, nil
	}
	out := append([]domain.AssessmentPin(nil), stored...)
	sortPins(out)
	return out, nil
}

func sortPins(pins []domain.AssessmentPin) {
	sort.Slice(pins, func(i, j int) bool {
		if pins[i].Kind != pins[j].Kind {
			return pins[i].Kind < pins[j].Kind
		}
		return pins[i].ID < pins[j].ID
	})
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

// assetCatalog maps {courseID, assetID} to a kind.
type assetCatalog map[[2]id.ID]app.AssetKind

var _ app.AssetCatalog = assetCatalog(nil)

func (c assetCatalog) Kinds(_ context.Context, courseID id.ID, ids []id.ID) (map[id.ID]app.AssetKind, error) {
	out := map[id.ID]app.AssetKind{}
	for _, v := range ids {
		if k, ok := c[[2]id.ID{courseID, v}]; ok {
			out[v] = k
		}
	}
	return out, nil
}

// quizCatalog maps {courseID, quizID} to the quiz's lecture.
type quizCatalog map[[2]id.ID]id.ID

var _ app.QuizCatalog = quizCatalog(nil)

func (c quizCatalog) Lectures(_ context.Context, courseID id.ID, ids []id.ID) (map[id.ID]id.ID, error) {
	out := map[id.ID]id.ID{}
	for _, v := range ids {
		if l, ok := c[[2]id.ID{courseID, v}]; ok {
			out[v] = l
		}
	}
	return out, nil
}

// testIDs returns one generator shared by every test: separate generators on the same node
// can mint the same ID within a millisecond.
func testIDs(t *testing.T) *id.Generator {
	t.Helper()
	g, err := sharedIDs()
	if err != nil {
		t.Fatal(err)
	}
	return g
}

var sharedIDs = sync.OnceValues(func() (*id.Generator, error) { return id.NewGenerator(0) })

// publish takes a course through review and publishes it.
func publish(svc *app.CourseService, courseID id.ID) error {
	if err := svc.Submit(ctx, owner, courseID); err != nil {
		return err
	}
	if err := svc.Approve(ctx, admin, courseID, ""); err != nil {
		return err
	}
	return svc.Publish(ctx, owner, courseID)
}

type fakeAssessments struct {
	heads map[id.ID][]domain.AssessmentPin
	calls int
}

func (f *fakeAssessments) Heads(_ context.Context, courseID id.ID) ([]domain.AssessmentPin, error) {
	f.calls++
	return slices.Clone(f.heads[courseID]), nil
}

// hydrate fills category names and drops categories that no longer exist, as the database's
// join and cascade do.
func (t *memTx) hydrate(c domain.Course) domain.Course {
	refs := make([]domain.CategoryRef, 0, len(c.Categories))
	for _, r := range c.Categories {
		if k, ok := t.categories[r.ID]; ok {
			refs = append(refs, domain.CategoryRef{ID: k.ID, Name: k.Name, Slug: k.Slug})
		}
	}
	c.Categories, c.Tags = refs, append([]string{}, c.Tags...)
	return c
}

// memCategories is the category repository over a memTx.
type memCategories struct{ t *memTx }

func (m memCategories) conflicts(c domain.Category) bool {
	for _, k := range m.t.categories {
		if k.ID != c.ID && (strings.EqualFold(k.Name, c.Name) || k.Slug == c.Slug) {
			return true
		}
	}
	return false
}

func (m memCategories) Insert(_ context.Context, c domain.Category) error {
	if m.conflicts(c) {
		return app.ErrCategoryExists
	}
	m.t.categories[c.ID] = c
	return nil
}

func (m memCategories) Update(_ context.Context, c domain.Category) error {
	if _, ok := m.t.categories[c.ID]; !ok {
		return app.ErrNotFound
	}
	if m.conflicts(c) {
		return app.ErrCategoryExists
	}
	m.t.categories[c.ID] = c
	return nil
}

func (m memCategories) Delete(_ context.Context, categoryID id.ID) error {
	if _, ok := m.t.categories[categoryID]; !ok {
		return app.ErrNotFound
	}
	delete(m.t.categories, categoryID)
	return nil
}

func (m memCategories) Get(_ context.Context, categoryID id.ID) (domain.Category, error) {
	c, ok := m.t.categories[categoryID]
	if !ok {
		return domain.Category{}, app.ErrNotFound
	}
	return c, nil
}

func (m memCategories) List(_ context.Context) ([]app.CategoryWithCount, error) {
	out := make([]app.CategoryWithCount, 0, len(m.t.categories))
	for _, k := range m.t.categories {
		n := 0
		for _, w := range m.t.courses {
			if w.IsLive() && slices.ContainsFunc(m.t.versions[versionKey{w.ID, w.Live.Number}].course.Categories,
				func(r domain.CategoryRef) bool { return r.ID == k.ID }) {
				n++
			}
		}
		out = append(out, app.CategoryWithCount{Category: k, CourseCount: n})
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out, nil
}

func (m memCategories) ExistAll(_ context.Context, ids []id.ID) (bool, error) {
	for _, cid := range ids {
		if _, ok := m.t.categories[cid]; !ok {
			return false, nil
		}
	}
	return true, nil
}
