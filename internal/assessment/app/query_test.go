package app_test

import (
	"testing"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/assessment/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

func TestExamInCourse(t *testing.T) {
	f := newFixture(t)
	e := f.createExam(t, examInput())
	for _, c := range []struct {
		name     string
		courseID id.ID
		examID   id.ID
		want     bool
	}{
		{"exam in its course", course, e.ID, true},
		{"exam in another course", course + 1, e.ID, false},
		{"unknown exam", course, e.ID + 999, false},
	} {
		got, err := f.query.ExamInCourse(ctx, c.courseID, c.examID)
		if err != nil || got != c.want {
			t.Errorf("%s: got %v, %v; want %v", c.name, got, err, c.want)
		}
	}
}

func TestHasPassed(t *testing.T) {
	f := newFixture(t)
	e := f.createExam(t, examInput())
	submitted := time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC)
	yes, no := true, false
	put := func(attemptID id.ID, userID id.ID, passed *bool) {
		f.store.examAttempts[attemptID] = domain.ExamAttempt{
			ID: attemptID, ExamID: e.ID, CourseID: course, UserID: userID,
			StartedAt: submitted, SubmittedAt: &submitted, Passed: passed,
		}
	}
	has := func(userID id.ID) bool {
		t.Helper()
		got, err := f.query.HasPassed(ctx, course, userID, e.ID)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if has(student.UserID) {
		t.Fatal("passed with no attempts")
	}
	put(900, student.UserID, &no)
	if has(student.UserID) {
		t.Fatal("passed with only a failed attempt")
	}
	open := domain.ExamAttempt{ID: 901, ExamID: e.ID, CourseID: course, UserID: student.UserID, StartedAt: submitted}
	f.store.examAttempts[901] = open
	if has(student.UserID) {
		t.Fatal("passed with an open attempt")
	}
	put(902, student.UserID, &yes)
	if !has(student.UserID) {
		t.Fatal("not passed after a passing attempt")
	}
	if has(student.UserID + 1) {
		t.Fatal("another user's attempt counted")
	}
	if got, err := f.query.HasPassed(ctx, course, student.UserID, e.ID+999); err != nil || got {
		t.Fatalf("other exam: %v, %v", got, err)
	}
}
