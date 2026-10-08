package app_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/enrollment/app"
	"github.com/santoshkc2200/ioe-backend/internal/enrollment/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

var (
	ctx     = context.Background()
	t0      = time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	owner   = auth.Principal{UserID: 100, Role: auth.RoleInstructor}
	admin   = auth.Principal{UserID: 1, Role: auth.RoleRootAdmin}
	student = auth.Principal{UserID: 200, Role: auth.RoleStudent}
	other   = auth.Principal{UserID: 300, Role: auth.RoleStudent}
)

const (
	freeCourse  id.ID = 10
	paidCourse  id.ID = 11
	draftCourse id.ID = 12
)

type fixture struct {
	svc   *app.Service
	store *memStore
	clock *fixedClock
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	ids, err := id.NewGenerator(0)
	if err != nil {
		t.Fatal(err)
	}
	store := newMemStore()
	clk := &fixedClock{now: t0}
	courses := catalog{
		freeCourse:  {Published: true, Free: true, OwnerID: owner.UserID},
		paidCourse:  {Published: true, Free: false, OwnerID: owner.UserID},
		draftCourse: {Published: false, Free: true, OwnerID: owner.UserID},
	}
	return fixture{svc: app.NewService(store, courses, ids, clk), store: store, clock: clk}
}

func (f fixture) now() time.Time { return f.clock.now }

func TestEnrollSelfInFreeCourse(t *testing.T) {
	f := newFixture(t)
	e, activated, err := f.svc.Enroll(ctx, student, freeCourse, student.UserID)
	if err != nil || !activated || !e.IsActive() || e.Version != 1 || e.CourseID != freeCourse || e.UserID != student.UserID {
		t.Fatalf("e=%+v activated=%v err=%v", e, activated, err)
	}
	if len(f.store.published) != 1 {
		t.Fatalf("events = %v", f.store.published)
	}
	if _, ok := f.store.published[0].(domain.EnrollmentActivated); !ok {
		t.Fatalf("event = %#v", f.store.published[0])
	}
}

func TestEnrollAlreadyActiveIsIdempotent(t *testing.T) {
	f := newFixture(t)
	first, _, _ := f.svc.Enroll(ctx, student, freeCourse, student.UserID)
	again, activated, err := f.svc.Enroll(ctx, student, freeCourse, student.UserID)
	if err != nil || activated || again.ID != first.ID || again.Version != first.Version {
		t.Fatalf("again=%+v activated=%v err=%v", again, activated, err)
	}
	if len(f.store.published) != 1 {
		t.Fatalf("events = %v", f.store.published)
	}
}

func TestEnrollReactivatesCanceled(t *testing.T) {
	f := newFixture(t)
	first, _, _ := f.svc.Enroll(ctx, student, freeCourse, student.UserID)
	if _, err := f.svc.Cancel(ctx, student, freeCourse, student.UserID, "busy"); err != nil {
		t.Fatal(err)
	}
	f.clock.now = t0.Add(time.Hour)
	e, activated, err := f.svc.Enroll(ctx, student, freeCourse, student.UserID)
	if err != nil || !activated || e.ID != first.ID || !e.IsActive() || e.CancelReason != "" || !e.CanceledAt.IsZero() || !e.EnrolledAt.Equal(f.now()) || e.Version != 3 {
		t.Fatalf("e=%+v activated=%v err=%v", e, activated, err)
	}
	if len(f.store.published) != 3 {
		t.Fatalf("events = %v", f.store.published)
	}
}

func TestEnrollRetriesAfterConcurrentInsert(t *testing.T) {
	f := newFixture(t)
	winner, _ := domain.NewEnrollment(999, freeCourse, student.UserID, t0)
	winner.Version = 1
	f.store.race = &winner
	e, activated, err := f.svc.Enroll(ctx, student, freeCourse, student.UserID)
	if err != nil || activated || e.ID != 999 {
		t.Fatalf("e=%+v activated=%v err=%v", e, activated, err)
	}
	if len(f.store.published) != 0 {
		t.Fatalf("events = %v", f.store.published)
	}
}

func TestEnrollAuthorization(t *testing.T) {
	f := newFixture(t)
	cases := []struct {
		name   string
		p      auth.Principal
		course id.ID
		user   id.ID
		want   error
	}{
		{"paid self", student, paidCourse, student.UserID, domain.ErrPaymentRequired},
		{"draft self", student, draftCourse, student.UserID, domain.ErrCourseHidden},
		{"missing course", student, 404, student.UserID, app.ErrNotFound},
		{"enroll someone else", student, freeCourse, other.UserID, domain.ErrForbidden},
		{"manager on draft", owner, draftCourse, student.UserID, domain.ErrCourseNotPublished},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := f.svc.Enroll(ctx, tc.p, tc.course, tc.user); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
	if _, activated, err := f.svc.Enroll(ctx, owner, paidCourse, student.UserID); err != nil || !activated {
		t.Fatalf("owner comp: activated=%v err=%v", activated, err)
	}
	if _, activated, err := f.svc.Enroll(ctx, admin, paidCourse, other.UserID); err != nil || !activated {
		t.Fatalf("admin comp: activated=%v err=%v", activated, err)
	}
}

func TestCancel(t *testing.T) {
	f := newFixture(t)
	_, _, _ = f.svc.Enroll(ctx, student, freeCourse, student.UserID)
	_, _, _ = f.svc.Enroll(ctx, other, freeCourse, other.UserID)

	if _, err := f.svc.Cancel(ctx, other, freeCourse, student.UserID, ""); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("stranger err = %v", err)
	}
	e, err := f.svc.Cancel(ctx, student, freeCourse, student.UserID, "  done ")
	if err != nil || e.Status != domain.StatusCanceled || e.CancelReason != "done" {
		t.Fatalf("self cancel e=%+v err=%v", e, err)
	}
	again, err := f.svc.Cancel(ctx, student, freeCourse, student.UserID, "x")
	if err != nil || again.Version != e.Version || again.CancelReason != "done" {
		t.Fatalf("repeat cancel e=%+v err=%v", again, err)
	}
	if _, err := f.svc.Cancel(ctx, owner, freeCourse, other.UserID, ""); err != nil {
		t.Fatalf("owner cancel err = %v", err)
	}
	if len(f.store.published) != 4 { // 2 activations + 2 cancellations
		t.Fatalf("events = %v", f.store.published)
	}
	if _, err := f.svc.Cancel(ctx, student, paidCourse, student.UserID, ""); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("no enrollment err = %v", err)
	}
	if _, err := f.svc.Cancel(ctx, owner, 404, student.UserID, ""); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("missing course err = %v", err)
	}
}

func TestCancelRejectsLongReason(t *testing.T) {
	f := newFixture(t)
	_, _, _ = f.svc.Enroll(ctx, student, freeCourse, student.UserID)
	reason := strings.Repeat("x", domain.MaxReasonRunes+1)
	_, err := f.svc.Cancel(ctx, student, freeCourse, student.UserID, reason)
	if !errors.Is(err, app.ErrInvalidInput) || !errors.Is(err, domain.ErrInvalidReason) {
		t.Fatalf("err = %v", err)
	}
}

func TestCancelOtherUserHidesUnpublishedCourse(t *testing.T) {
	f := newFixture(t)
	if _, err := f.svc.Cancel(ctx, other, draftCourse, student.UserID, ""); !errors.Is(err, domain.ErrCourseHidden) {
		t.Fatalf("err = %v", err)
	}
}

func TestListByCourse(t *testing.T) {
	f := newFixture(t)
	for _, uid := range []id.ID{201, 202, 203} {
		if _, _, err := f.svc.Enroll(ctx, owner, freeCourse, uid); err != nil {
			t.Fatal(err)
		}
	}
	_, _ = f.svc.Cancel(ctx, owner, freeCourse, 202, "")

	page, total, err := f.svc.ListByCourse(ctx, owner, freeCourse, 1, 1)
	if err != nil || total != 2 || len(page) != 1 || page[0].UserID != 203 {
		t.Fatalf("page=%+v total=%d err=%v", page, total, err)
	}
	if _, _, err := f.svc.ListByCourse(ctx, student, freeCourse, 50, 0); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("student err = %v", err)
	}
	if _, _, err := f.svc.ListByCourse(ctx, student, draftCourse, 50, 0); !errors.Is(err, domain.ErrCourseHidden) {
		t.Fatalf("draft err = %v", err)
	}
	for _, bad := range [][2]int{{0, 0}, {app.MaxPageLimit + 1, 0}, {10, -1}} {
		if _, _, err := f.svc.ListByCourse(ctx, owner, freeCourse, bad[0], bad[1]); !errors.Is(err, app.ErrInvalidInput) {
			t.Fatalf("limit=%d offset=%d err = %v", bad[0], bad[1], err)
		}
	}
}

func TestListByUser(t *testing.T) {
	f := newFixture(t)
	_, _, _ = f.svc.Enroll(ctx, student, freeCourse, student.UserID)
	_, _, _ = f.svc.Enroll(ctx, owner, paidCourse, student.UserID)
	got, err := f.svc.ListByUser(ctx, student, student.UserID)
	if err != nil || len(got) != 2 {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	if _, err := f.svc.ListByUser(ctx, admin, student.UserID); err != nil {
		t.Fatalf("admin err = %v", err)
	}
	if _, err := f.svc.ListByUser(ctx, owner, student.UserID); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("instructor err = %v", err)
	}
}

func TestAccessQuery(t *testing.T) {
	f := newFixture(t)
	q := app.NewAccessQuery(f.store)
	if ok, err := q.IsActivelyEnrolled(ctx, freeCourse, student.UserID); err != nil || ok {
		t.Fatalf("before: %v %v", ok, err)
	}
	_, _, _ = f.svc.Enroll(ctx, student, freeCourse, student.UserID)
	if ok, err := q.IsActivelyEnrolled(ctx, freeCourse, student.UserID); err != nil || !ok {
		t.Fatalf("enrolled: %v %v", ok, err)
	}
	_, _ = f.svc.Cancel(ctx, student, freeCourse, student.UserID, "")
	if ok, err := q.IsActivelyEnrolled(ctx, freeCourse, student.UserID); err != nil || ok {
		t.Fatalf("canceled: %v %v", ok, err)
	}
}

func TestEnrollPurchasedInPaidCourse(t *testing.T) {
	f := newFixture(t)
	if err := f.svc.EnrollPurchased(ctx, paidCourse, student.UserID); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.EnrollPurchased(ctx, paidCourse, student.UserID); err != nil {
		t.Fatalf("again: %v", err)
	}
	active, err := app.NewAccessQuery(f.store).IsActivelyEnrolled(ctx, paidCourse, student.UserID)
	if err != nil || !active {
		t.Fatalf("active=%v err=%v", active, err)
	}
	if len(f.store.published) != 1 {
		t.Fatalf("events = %v", f.store.published)
	}
}

func TestEnrollPurchasedReactivatesCanceled(t *testing.T) {
	f := newFixture(t)
	if err := f.svc.EnrollPurchased(ctx, paidCourse, student.UserID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Cancel(ctx, student, paidCourse, student.UserID, ""); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.EnrollPurchased(ctx, paidCourse, student.UserID); err != nil {
		t.Fatal(err)
	}
	if len(f.store.published) != 3 {
		t.Fatalf("events = %v", f.store.published)
	}
	if _, ok := f.store.published[2].(domain.EnrollmentActivated); !ok {
		t.Fatalf("last event = %#v", f.store.published[2])
	}
}

func TestEnrollPurchasedIgnoresPublication(t *testing.T) {
	f := newFixture(t)
	if err := f.svc.EnrollPurchased(ctx, draftCourse, student.UserID); err != nil {
		t.Fatalf("unpublished course: %v", err)
	}
	if err := f.svc.EnrollPurchased(ctx, 999, student.UserID); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("missing course err = %v", err)
	}
}

func TestEnrollPurchasedSurvivesConcurrentFirstEnrollment(t *testing.T) {
	f := newFixture(t)
	f.store.race = &domain.Enrollment{ID: 77, CourseID: paidCourse, UserID: student.UserID, Status: domain.StatusActive, EnrolledAt: t0, Version: 1}
	if err := f.svc.EnrollPurchased(ctx, paidCourse, student.UserID); err != nil {
		t.Fatal(err)
	}
	if len(f.store.published) != 0 {
		t.Fatalf("events = %v", f.store.published)
	}
}

func TestCancelPurchased(t *testing.T) {
	f := newFixture(t)
	if err := f.svc.EnrollPurchased(ctx, paidCourse, student.UserID); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.CancelPurchased(ctx, paidCourse, student.UserID); err != nil {
		t.Fatal(err)
	}
	e, found, _ := f.store.find(paidCourse, student.UserID)
	if !found || e.Status != domain.StatusCanceled || e.CancelReason != app.RefundedReason {
		t.Fatalf("e=%+v found=%v", e, found)
	}
	events := len(f.store.published)
	if err := f.svc.CancelPurchased(ctx, paidCourse, student.UserID); err != nil || len(f.store.published) != events {
		t.Fatalf("repeat err=%v events=%d", err, len(f.store.published))
	}
	if err := f.svc.CancelPurchased(ctx, paidCourse, other.UserID); err != nil {
		t.Fatalf("missing enrollment err = %v", err)
	}
	if err := f.svc.CancelPurchased(ctx, 404, student.UserID); err != nil {
		t.Fatalf("missing course err = %v", err)
	}
}
