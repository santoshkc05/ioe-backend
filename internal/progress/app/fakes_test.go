package app_test

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
	"github.com/santoshkc2200/ioe-backend/internal/progress/app"
	"github.com/santoshkc2200/ioe-backend/internal/progress/domain"
)

type fixedClock struct{ now time.Time }

func (c *fixedClock) Now() time.Time { return c.now }

type courseUser [2]id.ID

// memStore mimics the postgres repository, including the no-regression upsert rule.
type memStore struct {
	mu      sync.Mutex
	courses map[courseUser]domain.CourseProgress
	writes  int
}

func newMemStore() *memStore { return &memStore{courses: map[courseUser]domain.CourseProgress{}} }

func (m *memStore) RunInTx(_ context.Context, fn func(app.Repository) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return fn(m)
}

func (m *memStore) RecordLecture(_ context.Context, courseID, userID id.ID, p domain.LectureProgress) error {
	m.writes++
	k := courseUser{courseID, userID}
	cp := m.courses[k]
	cp.CourseID, cp.UserID = courseID, userID
	lectures := append([]domain.LectureProgress(nil), cp.Lectures...)
	i := sort.Search(len(lectures), func(i int) bool { return lectures[i].LectureID >= p.LectureID })
	switch {
	case i < len(lectures) && lectures[i].LectureID == p.LectureID:
		if lectures[i].State == domain.LectureStateCompleted && p.State == domain.LectureStateInProgress {
			return nil
		}
		lectures[i] = p
	default:
		lectures = append(lectures[:i], append([]domain.LectureProgress{p}, lectures[i:]...)...)
	}
	cp.Lectures, cp.LastLectureID, cp.UpdatedAt = lectures, p.LectureID, p.UpdatedAt
	m.courses[k] = cp
	return nil
}

func (m *memStore) FindCourse(_ context.Context, courseID, userID id.ID) (domain.CourseProgress, bool, error) {
	cp, ok := m.courses[courseUser{courseID, userID}]
	return cp, ok, nil
}

func (m *memStore) FindByUser(_ context.Context, userID id.ID) ([]domain.CourseProgress, error) {
	var out []domain.CourseProgress
	for k, cp := range m.courses {
		if k[1] == userID {
			out = append(out, cp)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out, nil
}

func (m *memStore) ActivityByUser(_ context.Context, userID id.ID) ([]domain.ActivityDay, error) {
	counts := map[time.Time]int{}
	for k, cp := range m.courses {
		if k[1] != userID {
			continue
		}
		for _, l := range cp.Lectures {
			d := l.UpdatedAt.UTC()
			counts[time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, time.UTC)]++
		}
	}
	out := make([]domain.ActivityDay, 0, len(counts))
	for d, n := range counts {
		out = append(out, domain.ActivityDay{Date: d, LectureCount: n})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Date.After(out[j].Date) })
	return out, nil
}

type catalog map[id.ID]domain.CourseFacts

func (c catalog) CourseFacts(_ context.Context, courseID id.ID) (domain.CourseFacts, error) {
	f, ok := c[courseID]
	if !ok {
		return domain.CourseFacts{}, app.ErrNotFound
	}
	return f, nil
}

// enrollments reports (course, user) pairs as actively enrolled.
type enrollments map[courseUser]bool

func (e enrollments) IsActivelyEnrolled(_ context.Context, courseID, userID id.ID) (bool, error) {
	return e[courseUser{courseID, userID}], nil
}
