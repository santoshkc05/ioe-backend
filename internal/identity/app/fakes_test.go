package app_test

import (
	"bytes"
	"context"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/santoshkc2200/ioe-backend/internal/identity/app"
	"github.com/santoshkc2200/ioe-backend/internal/identity/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
)

type memState struct {
	users  map[uuid.UUID]domain.User
	tokens map[uuid.UUID]domain.RefreshToken
	events []domain.Event
}

func (s *memState) clone() *memState {
	return &memState{users: maps.Clone(s.users), tokens: maps.Clone(s.tokens), events: slices.Clone(s.events)}
}

// memStore commits a transaction's changes only when fn succeeds, like a real database.
type memStore struct {
	mu            sync.Mutex
	state         *memState
	conflictsLeft int
}

func newMemStore() *memStore {
	return &memStore{state: &memState{users: map[uuid.UUID]domain.User{}, tokens: map[uuid.UUID]domain.RefreshToken{}}}
}

func (m *memStore) RunInTx(_ context.Context, fn func(app.Repos) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	tx := m.state.clone()
	if err := fn(app.Repos{Users: &memUsers{s: tx, store: m}, Tokens: memTokens{s: tx}, Events: memEvents{s: tx}}); err != nil {
		return err
	}
	m.state = tx
	return nil
}

func (m *memStore) snapshot() *memState {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state.clone()
}

func (m *memStore) setRole(id uuid.UUID, r auth.Role) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u := m.state.users[id]
	u.Role = r
	m.state.users[id] = u
}

type memUsers struct {
	s     *memState
	store *memStore
}

func (u *memUsers) FindByGoogleSubject(_ context.Context, sub string) (domain.User, error) {
	for _, x := range u.s.users {
		if x.GoogleSubject == sub {
			return x, nil
		}
	}
	return domain.User{}, app.ErrNotFound
}

func (u *memUsers) FindByID(_ context.Context, id uuid.UUID) (domain.User, error) {
	x, ok := u.s.users[id]
	if !ok {
		return domain.User{}, app.ErrNotFound
	}
	return x, nil
}

func (u *memUsers) Insert(_ context.Context, x domain.User) error {
	if u.store.conflictsLeft > 0 {
		u.store.conflictsLeft--
		return app.ErrConflict
	}
	for _, e := range u.s.users {
		if e.GoogleSubject == x.GoogleSubject {
			return app.ErrConflict
		}
	}
	u.s.users[x.ID] = x
	return nil
}

func (u *memUsers) Update(_ context.Context, x domain.User) error {
	if _, ok := u.s.users[x.ID]; !ok {
		return app.ErrNotFound
	}
	u.s.users[x.ID] = x
	return nil
}

type memTokens struct{ s *memState }

func (m memTokens) Insert(_ context.Context, t domain.RefreshToken) error {
	m.s.tokens[t.ID] = t
	return nil
}

func (m memTokens) FindByHashForUpdate(_ context.Context, hash []byte) (domain.RefreshToken, error) {
	for _, t := range m.s.tokens {
		if bytes.Equal(t.TokenHash, hash) {
			return t, nil
		}
	}
	return domain.RefreshToken{}, app.ErrNotFound
}

func (m memTokens) MarkUsed(_ context.Context, id uuid.UUID, at time.Time) error {
	t := m.s.tokens[id]
	t.UsedAt = &at
	m.s.tokens[id] = t
	return nil
}

func (m memTokens) RevokeFamily(_ context.Context, family uuid.UUID, at time.Time) error {
	for id, t := range m.s.tokens {
		if t.FamilyID == family && t.RevokedAt == nil {
			revokedAt := at
			t.RevokedAt = &revokedAt
			m.s.tokens[id] = t
		}
	}
	return nil
}

type memEvents struct{ s *memState }

func (m memEvents) Publish(_ context.Context, evs ...domain.Event) error {
	m.s.events = append(m.s.events, evs...)
	return nil
}

type fakeGoogle map[string]domain.GoogleIdentity

func (f fakeGoogle) Verify(_ context.Context, token string) (domain.GoogleIdentity, error) {
	id, ok := f[token]
	if !ok {
		return domain.GoogleIdentity{}, fmt.Errorf("%w: unknown test token", app.ErrInvalidToken)
	}
	return id, nil
}

type fakeIssuer struct{}

func (fakeIssuer) Issue(id uuid.UUID, r auth.Role) (string, time.Duration, error) {
	return "access:" + id.String() + ":" + string(r), 15 * time.Minute, nil
}
