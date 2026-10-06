package app_test

import (
	"context"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/assessment/app"
	"github.com/santoshkc2200/ioe-backend/internal/assessment/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

type fixedClock struct{ now time.Time }

func (c *fixedClock) Now() time.Time { return c.now }

// memStore mimics the postgres repository.
type memStore struct {
	mu       sync.Mutex
	quizzes  map[id.ID]domain.Quiz
	attempts []domain.QuizAttempt
}

func newMemStore() *memStore { return &memStore{quizzes: map[id.ID]domain.Quiz{}} }

func (m *memStore) RunInTx(_ context.Context, fn func(app.QuizRepository) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return fn(m)
}

func (m *memStore) Find(_ context.Context, quizID id.ID) (domain.Quiz, error) {
	q, ok := m.quizzes[quizID]
	if !ok {
		return domain.Quiz{}, app.ErrNotFound
	}
	return q, nil
}

func (m *memStore) ListByLecture(_ context.Context, courseID, lectureID id.ID) ([]domain.Quiz, error) {
	var out []domain.Quiz
	for _, q := range m.quizzes {
		if q.CourseID == courseID && q.LectureID == lectureID {
			out = append(out, q)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Position != out[j].Position {
			return out[i].Position < out[j].Position
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func (m *memStore) Insert(_ context.Context, q domain.Quiz) error {
	m.quizzes[q.ID] = q
	return nil
}

func (m *memStore) Replace(_ context.Context, q domain.Quiz) error {
	if _, ok := m.quizzes[q.ID]; !ok {
		return app.ErrNotFound
	}
	m.quizzes[q.ID] = q
	return nil
}

func (m *memStore) Delete(_ context.Context, quizID id.ID) error {
	delete(m.quizzes, quizID)
	kept := m.attempts[:0]
	for _, a := range m.attempts {
		if a.QuizID != quizID {
			kept = append(kept, a)
		}
	}
	m.attempts = kept
	return nil
}

func (m *memStore) LecturesOf(_ context.Context, courseID id.ID, quizIDs []id.ID) (map[id.ID]id.ID, error) {
	out := map[id.ID]id.ID{}
	for _, v := range quizIDs {
		if q, ok := m.quizzes[v]; ok && q.CourseID == courseID {
			out[v] = q.LectureID
		}
	}
	return out, nil
}

func (m *memStore) RecordAttempt(_ context.Context, a domain.QuizAttempt) (id.ID, error) {
	if a.IdempotencyKey != "" {
		for _, prev := range m.attempts {
			if prev.QuizID == a.QuizID && prev.UserID == a.UserID && prev.IdempotencyKey == a.IdempotencyKey {
				return prev.ID, nil
			}
		}
	}
	m.attempts = append(m.attempts, a)
	return a.ID, nil
}

// outsideTx fails the test when a cross-context port is called inside a store transaction.
func outsideTx(t *testing.T, s *memStore) {
	t.Helper()
	if !s.mu.TryLock() {
		t.Error("cross-context port called inside the assessment transaction")
		return
	}
	s.mu.Unlock()
}

// courseAccess applies courseauthoring's rules to a fixed course layout.
type courseAccess struct {
	t        *testing.T
	store    *memStore
	owner    id.ID
	lectures map[id.ID]id.ID // lecture → course
	archived map[id.ID]bool  // course → archived
	free     map[id.ID]bool  // free-preview lectures
	enrolled enrollments
}

func (a *courseAccess) CanManageLecture(_ context.Context, p auth.Principal, courseID, lectureID id.ID) error {
	outsideTx(a.t, a.store)
	switch {
	case a.lectures[lectureID] != courseID:
		return app.ErrNotFound
	case p.UserID != a.owner && p.Role != auth.RoleRootAdmin:
		return app.ErrForbidden
	case a.archived[courseID]:
		return app.ErrCourseNotEditable
	}
	return nil
}

func (a *courseAccess) CanReadLecture(_ context.Context, p auth.Principal, courseID, lectureID id.ID) error {
	outsideTx(a.t, a.store)
	switch {
	case a.lectures[lectureID] != courseID:
		return app.ErrNotFound
	case p.UserID == a.owner || p.Role == auth.RoleRootAdmin || a.free[lectureID] || a.enrolled[[2]id.ID{courseID, p.UserID}]:
		return nil
	}
	return app.ErrEnrollmentRequired
}

// enrollments reports {course, user} pairs as actively enrolled.
type enrollments map[[2]id.ID]bool

type enrollmentProbe struct {
	t     *testing.T
	store *memStore
	set   enrollments
}

func (e enrollmentProbe) IsActivelyEnrolled(_ context.Context, courseID, userID id.ID) (bool, error) {
	outsideTx(e.t, e.store)
	return e.set[[2]id.ID{courseID, userID}], nil
}
