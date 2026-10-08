package app_test

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/enrollment/app"
	"github.com/santoshkc2200/ioe-backend/internal/enrollment/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time { return c.now }

type key [2]id.ID // course, user

// memStore is a single-goroutine fake; RunInTx copies state and commits only on success.
type memStore struct {
	mu        sync.Mutex
	rows      map[key]domain.Enrollment
	published []domain.Event
	// race, when set, is committed by the next Insert, which then fails with ErrDuplicate,
	// simulating a concurrent first enrollment that won the unique constraint.
	race *domain.Enrollment
}

func newMemStore() *memStore { return &memStore{rows: map[key]domain.Enrollment{}} }

func (m *memStore) find(courseID, userID id.ID) (domain.Enrollment, bool, error) {
	var (
		e     domain.Enrollment
		found bool
	)
	err := m.RunInTx(context.Background(), func(r app.Repos) error {
		var err error
		e, found, err = r.Enrollments.FindByCourseAndUser(context.Background(), courseID, userID)
		return err
	})
	return e, found, err
}

func (m *memStore) RunInTx(_ context.Context, fn func(app.Repos) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	tx := &memTx{store: m, rows: make(map[key]domain.Enrollment, len(m.rows))}
	for k, v := range m.rows {
		tx.rows[k] = v
	}
	if err := fn(app.Repos{Enrollments: tx, Events: tx}); err != nil {
		return err
	}
	m.rows = tx.rows
	m.published = append(m.published, tx.events...)
	return nil
}

type memTx struct {
	store  *memStore
	rows   map[key]domain.Enrollment
	events []domain.Event
}

func (t *memTx) FindByCourseAndUser(_ context.Context, courseID, userID id.ID) (domain.Enrollment, bool, error) {
	e, ok := t.rows[key{courseID, userID}]
	return e, ok, nil
}

func (t *memTx) active(match func(domain.Enrollment) bool) []domain.Enrollment {
	var out []domain.Enrollment
	for _, e := range t.rows {
		if e.IsActive() && match(e) {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (t *memTx) ListActiveByCourse(_ context.Context, courseID id.ID, limit, offset int) ([]domain.Enrollment, int, error) {
	all := t.active(func(e domain.Enrollment) bool { return e.CourseID == courseID })
	if offset > len(all) {
		offset = len(all)
	}
	end := min(offset+limit, len(all))
	return all[offset:end], len(all), nil
}

func (t *memTx) ListActiveByUser(_ context.Context, userID id.ID) ([]domain.Enrollment, error) {
	return t.active(func(e domain.Enrollment) bool { return e.UserID == userID }), nil
}

func (t *memTx) Insert(_ context.Context, e *domain.Enrollment) error {
	k := key{e.CourseID, e.UserID}
	if r := t.store.race; r != nil {
		t.store.race = nil
		t.store.rows[key{r.CourseID, r.UserID}] = *r
		return app.ErrDuplicate
	}
	if _, ok := t.rows[k]; ok {
		return app.ErrDuplicate
	}
	e.Version = 1
	t.rows[k] = *e
	return nil
}

func (t *memTx) Update(_ context.Context, e *domain.Enrollment) error {
	k := key{e.CourseID, e.UserID}
	cur, ok := t.rows[k]
	if !ok || cur.Version != e.Version {
		return app.ErrConcurrentModification
	}
	e.Version++
	t.rows[k] = *e
	return nil
}

func (t *memTx) Publish(_ context.Context, evs ...domain.Event) error {
	t.events = append(t.events, evs...)
	return nil
}

type catalog map[id.ID]domain.CourseFacts

func (c catalog) CourseFacts(_ context.Context, courseID id.ID) (domain.CourseFacts, error) {
	f, ok := c[courseID]
	if !ok {
		return domain.CourseFacts{}, app.ErrNotFound
	}
	return f, nil
}
