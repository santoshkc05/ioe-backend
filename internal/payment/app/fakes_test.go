package app_test

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/payment/app"
	"github.com/santoshkc2200/ioe-backend/internal/payment/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

type fixedClock struct{ now time.Time }

func (c *fixedClock) Now() time.Time { return c.now }

// memStore is a single-goroutine fake; RunInTx copies state and commits only on success.
type memStore struct {
	mu        sync.Mutex
	rows      map[id.ID]domain.Purchase
	published []domain.Event
	// conflicts makes the next Update calls fail with ErrConcurrentModification.
	conflicts int
}

func newMemStore() *memStore { return &memStore{rows: map[id.ID]domain.Purchase{}} }

func (m *memStore) RunInTx(_ context.Context, fn func(app.Repos) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	tx := &memTx{store: m, rows: make(map[id.ID]domain.Purchase, len(m.rows))}
	for k, v := range m.rows {
		tx.rows[k] = v
	}
	if err := fn(app.Repos{Purchases: tx, Events: tx}); err != nil {
		return err
	}
	m.rows = tx.rows
	m.published = append(m.published, tx.events...)
	return nil
}

func (m *memStore) get(purchaseID id.ID) domain.Purchase {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.rows[purchaseID]
}

type memTx struct {
	store  *memStore
	rows   map[id.ID]domain.Purchase
	events []domain.Event
}

func (t *memTx) Find(_ context.Context, purchaseID id.ID) (domain.Purchase, bool, error) {
	p, ok := t.rows[purchaseID]
	return p, ok, nil
}

func (t *memTx) CountPaid(_ context.Context, userID, courseID id.ID) (int, error) {
	n := 0
	for _, p := range t.rows {
		if p.UserID == userID && p.CourseID == courseID && p.Status == domain.StatusPaid {
			n++
		}
	}
	return n, nil
}

func (t *memTx) ListUnsettled(_ context.Context, pendingBefore time.Time, afterID id.ID, limit int) ([]domain.Purchase, error) {
	var out []domain.Purchase
	for _, p := range t.rows {
		stale := p.Status == domain.StatusPending && p.CreatedAt.Before(pendingBefore)
		if p.ID > afterID && (stale || p.NeedsGrant()) {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (t *memTx) Insert(_ context.Context, p *domain.Purchase) error {
	p.Version = 1
	t.rows[p.ID] = *p
	return nil
}

func (t *memTx) Update(_ context.Context, p *domain.Purchase) error {
	if t.store.conflicts > 0 {
		t.store.conflicts--
		return app.ErrConcurrentModification
	}
	cur, ok := t.rows[p.ID]
	if !ok || cur.Version != p.Version {
		return app.ErrConcurrentModification
	}
	p.Version++
	t.rows[p.ID] = *p
	return nil
}

func (t *memTx) Publish(_ context.Context, evs ...domain.Event) error {
	t.events = append(t.events, evs...)
	return nil
}

type catalog map[id.ID]app.CourseFacts

func (c catalog) CourseFacts(_ context.Context, courseID id.ID) (app.CourseFacts, error) {
	f, ok := c[courseID]
	if !ok {
		return app.CourseFacts{}, app.ErrNotFound
	}
	return f, nil
}

type fakeEnrollments struct {
	enrolled map[[2]id.ID]bool // course, user
	grantErr error
	grants   int
}

func (e *fakeEnrollments) IsEnrolled(_ context.Context, courseID, userID id.ID) (bool, error) {
	return e.enrolled[[2]id.ID{courseID, userID}], nil
}

func (e *fakeEnrollments) GrantPurchased(_ context.Context, courseID, userID id.ID) error {
	e.grants++
	if e.grantErr != nil {
		return e.grantErr
	}
	e.enrolled[[2]id.ID{courseID, userID}] = true
	return nil
}

// fakeGateway reports results by gateway reference; a missing result is pending.
type fakeGateway struct {
	checkoutErr error
	results     map[string]app.Result
	statusErr   error
	statusCalls int
}

func (g *fakeGateway) StartCheckout(_ context.Context, p domain.Purchase) (app.Checkout, error) {
	if g.checkoutErr != nil {
		return app.Checkout{}, g.checkoutErr
	}
	return app.Checkout{Method: "POST", URL: "https://pay.test/form", Fields: map[string]string{"ref": p.GatewayRef}}, nil
}

func (g *fakeGateway) FetchStatus(_ context.Context, p domain.Purchase) (app.Result, error) {
	g.statusCalls++
	if g.statusErr != nil {
		return app.Result{}, g.statusErr
	}
	return g.results[p.GatewayRef], nil
}

func (t *memTx) ListByUser(_ context.Context, userID, before id.ID, limit int) ([]domain.Purchase, error) {
	var out []domain.Purchase
	for _, p := range t.rows {
		if p.UserID == userID && (before == 0 || p.ID < before) {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
