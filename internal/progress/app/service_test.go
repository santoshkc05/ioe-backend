package app_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
	"github.com/santoshkc2200/ioe-backend/internal/progress/app"
	"github.com/santoshkc2200/ioe-backend/internal/progress/domain"
)

var (
	ctx     = context.Background()
	t0      = time.Date(2026, 10, 6, 9, 0, 0, 0, time.UTC)
	owner   = auth.Principal{UserID: 100, Role: auth.RoleInstructor}
	admin   = auth.Principal{UserID: 1, Role: auth.RoleRootAdmin}
	student = auth.Principal{UserID: 200, Role: auth.RoleStudent}
	other   = auth.Principal{UserID: 300, Role: auth.RoleStudent}
)

const (
	course      id.ID = 10 // published, lectures 50 and 51
	otherCourse id.ID = 11 // published, lecture 60
	archived    id.ID = 12 // unpublished, lecture 70
	missing     id.ID = 13
)

type fixture struct {
	svc   *app.Service
	store *memStore
	clock *fixedClock
	enr   enrollments
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	store := newMemStore()
	clk := &fixedClock{now: t0}
	courses := catalog{
		course:      {Published: true, OwnerID: owner.UserID, LectureIDs: []id.ID{50, 51}},
		otherCourse: {Published: true, OwnerID: owner.UserID, LectureIDs: []id.ID{60}},
		archived:    {Published: false, OwnerID: owner.UserID, LectureIDs: []id.ID{70}},
	}
	enr := enrollments{{course, student.UserID}: true, {otherCourse, student.UserID}: true, {archived, student.UserID}: true}
	return fixture{svc: app.NewService(store, courses, enr, clk), store: store, clock: clk, enr: enr}
}

func TestRecordAndReadOwnProgress(t *testing.T) {
	f := newFixture(t)
	if err := f.svc.RecordLecture(ctx, student, course, 51, student.UserID, domain.LectureStateInProgress, 1200); err != nil {
		t.Fatal(err)
	}
	f.clock.now = t0.Add(time.Minute)
	if err := f.svc.RecordLecture(ctx, student, course, 50, student.UserID, domain.LectureStateCompleted, 0); err != nil {
		t.Fatal(err)
	}
	cp, err := f.svc.CourseProgress(ctx, student, course, student.UserID)
	if err != nil || cp.LastLectureID != 50 || !cp.UpdatedAt.Equal(t0.Add(time.Minute)) || len(cp.Lectures) != 2 ||
		cp.Lectures[1].PositionMs != 1200 || !slices.Equal(cp.CompletedLectureIDs(), []id.ID{50}) {
		t.Fatalf("cp=%+v err=%v", cp, err)
	}
}

func TestRecordValidation(t *testing.T) {
	f := newFixture(t)
	if err := f.svc.RecordLecture(ctx, student, course, 50, student.UserID, "done", 0); !errors.Is(err, app.ErrInvalidInput) || !errors.Is(err, domain.ErrInvalidLectureState) {
		t.Fatalf("state err = %v", err)
	}
	if err := f.svc.RecordLecture(ctx, student, course, 50, student.UserID, domain.LectureStateInProgress, -5); !errors.Is(err, app.ErrInvalidInput) {
		t.Fatalf("position err = %v", err)
	}
	if f.store.writes != 0 {
		t.Fatalf("writes = %d", f.store.writes)
	}
}

func TestRecordRejections(t *testing.T) {
	cases := []struct {
		name    string
		p       auth.Principal
		course  id.ID
		lecture id.ID
		target  id.ID
		want    error
	}{
		{"for another user", student, course, 50, other.UserID, domain.ErrForbidden},
		{"manager for a student", owner, course, 50, student.UserID, domain.ErrForbidden},
		{"admin for a student", admin, course, 50, student.UserID, domain.ErrForbidden},
		{"lecture of another course", student, course, 60, student.UserID, domain.ErrLectureNotFound},
		{"unpublished course", student, archived, 70, student.UserID, domain.ErrCourseHidden},
		{"missing course", student, missing, 50, student.UserID, app.ErrNotFound},
		{"not enrolled", other, course, 50, other.UserID, app.ErrEnrollmentRequired},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			err := f.svc.RecordLecture(ctx, tc.p, tc.course, tc.lecture, tc.target, domain.LectureStateCompleted, 0)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if f.store.writes != 0 {
				t.Fatalf("writes = %d", f.store.writes)
			}
		})
	}
}

func TestRecordAfterCancellationNeedsEnrollment(t *testing.T) {
	f := newFixture(t)
	if err := f.svc.RecordLecture(ctx, student, course, 50, student.UserID, domain.LectureStateCompleted, 0); err != nil {
		t.Fatal(err)
	}
	delete(f.enr, courseUser{course, student.UserID})
	if err := f.svc.RecordLecture(ctx, student, course, 51, student.UserID, domain.LectureStateCompleted, 0); !errors.Is(err, app.ErrEnrollmentRequired) {
		t.Fatalf("err = %v", err)
	}
	// Progress is kept and still readable.
	cp, err := f.svc.CourseProgress(ctx, student, course, student.UserID)
	if err != nil || !slices.Equal(cp.CompletedLectureIDs(), []id.ID{50}) {
		t.Fatalf("cp=%+v err=%v", cp, err)
	}
}

func TestReadCourseProgressAccess(t *testing.T) {
	f := newFixture(t)
	if err := f.svc.RecordLecture(ctx, student, course, 50, student.UserID, domain.LectureStateCompleted, 0); err != nil {
		t.Fatal(err)
	}
	// Record on the course before it was archived, through the store, then read after.
	if err := f.store.RecordLecture(ctx, archived, student.UserID, domain.LectureProgress{LectureID: 70, State: domain.LectureStateCompleted, UpdatedAt: t0}); err != nil {
		t.Fatal(err)
	}

	for _, p := range []auth.Principal{owner, admin} {
		if cp, err := f.svc.CourseProgress(ctx, p, course, student.UserID); err != nil || len(cp.Lectures) != 1 {
			t.Fatalf("%v read: cp=%+v err=%v", p.Role, cp, err)
		}
	}
	if _, err := f.svc.CourseProgress(ctx, other, course, student.UserID); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("stranger err = %v", err)
	}
	if cp, err := f.svc.CourseProgress(ctx, student, archived, student.UserID); err != nil || len(cp.Lectures) != 1 {
		t.Fatalf("own archived: cp=%+v err=%v", cp, err)
	}
	if _, err := f.svc.CourseProgress(ctx, other, archived, student.UserID); !errors.Is(err, domain.ErrCourseHidden) {
		t.Fatalf("stranger archived err = %v", err)
	}
	if _, err := f.svc.CourseProgress(ctx, other, missing, student.UserID); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("stranger missing err = %v", err)
	}
}

func TestReadCourseProgressEmpty(t *testing.T) {
	f := newFixture(t)
	cp, err := f.svc.CourseProgress(ctx, student, missing, student.UserID)
	if err != nil || cp.CourseID != missing || cp.UserID != student.UserID || !cp.UpdatedAt.IsZero() || cp.LastLectureID != 0 || len(cp.Lectures) != 0 {
		t.Fatalf("cp=%+v err=%v", cp, err)
	}
}

func TestUserProgress(t *testing.T) {
	f := newFixture(t)
	if err := f.svc.RecordLecture(ctx, student, course, 50, student.UserID, domain.LectureStateCompleted, 0); err != nil {
		t.Fatal(err)
	}
	f.clock.now = t0.Add(24 * time.Hour)
	if err := f.svc.RecordLecture(ctx, student, otherCourse, 60, student.UserID, domain.LectureStateInProgress, 10); err != nil {
		t.Fatal(err)
	}
	courses, days, err := f.svc.UserProgress(ctx, student, student.UserID)
	if err != nil || len(courses) != 2 || courses[0].CourseID != otherCourse || len(days) != 2 || days[0].LectureCount != 1 {
		t.Fatalf("courses=%+v days=%+v err=%v", courses, days, err)
	}
	if _, _, err := f.svc.UserProgress(ctx, admin, student.UserID); err != nil {
		t.Fatalf("admin err = %v", err)
	}
	for _, p := range []auth.Principal{owner, other} {
		if _, _, err := f.svc.UserProgress(ctx, p, student.UserID); !errors.Is(err, domain.ErrForbidden) {
			t.Fatalf("%d err = %v", p.UserID, err)
		}
	}
}
