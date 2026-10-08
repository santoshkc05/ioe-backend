package app_test

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/certificate/app"
	"github.com/santoshkc2200/ioe-backend/internal/certificate/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

type fixedClock struct{ now time.Time }

func (c *fixedClock) Now() time.Time { return c.now }

// memStore mimics the postgres repository, including the one-valid-certificate rule.
type memStore struct {
	mu       sync.Mutex
	policies map[id.ID]domain.Policy
	certs    []domain.Certificate
}

func newMemStore() *memStore { return &memStore{policies: map[id.ID]domain.Policy{}} }

func (m *memStore) RunInTx(_ context.Context, fn func(app.Repository) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return fn(m)
}

func (m *memStore) FindPolicy(_ context.Context, courseID id.ID) (domain.Policy, bool, error) {
	p, ok := m.policies[courseID]
	return p, ok, nil
}

func (m *memStore) UpsertPolicy(_ context.Context, p domain.Policy, _ time.Time) error {
	m.policies[p.CourseID] = p
	return nil
}

func (m *memStore) FindValid(_ context.Context, courseID, userID id.ID) (domain.Certificate, bool, error) {
	for _, c := range m.certs {
		if c.CourseID == courseID && c.UserID == userID && !c.Revoked() {
			return c, true, nil
		}
	}
	return domain.Certificate{}, false, nil
}

func (m *memStore) Insert(ctx context.Context, c domain.Certificate) (bool, error) {
	if _, ok, _ := m.FindValid(ctx, c.CourseID, c.UserID); ok {
		return false, nil
	}
	m.certs = append(m.certs, c)
	return true, nil
}

func (m *memStore) FindByCode(_ context.Context, code string) (domain.Certificate, bool, error) {
	for _, c := range m.certs {
		if c.Code == code {
			return c, true, nil
		}
	}
	return domain.Certificate{}, false, nil
}

func (m *memStore) ListByUser(_ context.Context, userID id.ID) ([]domain.Certificate, error) {
	var out []domain.Certificate
	for _, c := range m.certs {
		if c.UserID == userID {
			out = append(out, c)
		}
	}
	slices.Reverse(out)
	return out, nil
}

func (m *memStore) RevokeValid(_ context.Context, courseID, userID id.ID, issuedBy, now time.Time) error {
	for i, c := range m.certs {
		if c.CourseID == courseID && c.UserID == userID && !c.Revoked() && !c.IssuedAt.After(issuedBy) {
			m.certs[i].RevokedAt = now
		}
	}
	return nil
}

// manager allows owner to manage the one course it knows.
type manager struct {
	course id.ID
	owner  id.ID
}

func (m manager) CanManage(_ context.Context, p auth.Principal, courseID id.ID) error {
	switch {
	case courseID != m.course:
		return app.ErrNotFound
	case p.UserID != m.owner:
		return app.ErrForbidden
	}
	return nil
}

type pair [2]id.ID

type enrollments map[pair]bool

func (e enrollments) IsActivelyEnrolled(_ context.Context, courseID, userID id.ID) (bool, error) {
	return e[pair{courseID, userID}], nil
}

// progress reports (course, user) pairs as complete.
type progress map[pair]bool

func (p progress) IsComplete(_ context.Context, courseID, userID id.ID) (bool, error) {
	return p[pair{courseID, userID}], nil
}

// exams knows which exams belong to which course and who passed which exam.
type exams struct {
	inCourse map[pair]bool // {course, exam}
	passed   map[[3]id.ID]bool
}

func (e exams) ExamInCourse(_ context.Context, courseID, examID id.ID) (bool, error) {
	return e.inCourse[pair{courseID, examID}], nil
}

func (e exams) HasPassed(_ context.Context, courseID, userID, examID id.ID) (bool, error) {
	return e.passed[[3]id.ID{courseID, userID, examID}], nil
}

type directory struct{}

func (directory) StudentName(_ context.Context, userID id.ID) (string, error) {
	if userID == unknownUser {
		return "", app.ErrNotFound
	}
	return "Asha Rai", nil
}

func (directory) CourseTitle(context.Context, id.ID) (string, error) { return "Go", nil }
