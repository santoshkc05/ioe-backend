package app_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/certificate/app"
	"github.com/santoshkc2200/ioe-backend/internal/certificate/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

var (
	ctx     = context.Background()
	t0      = time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC)
	owner   = auth.Principal{UserID: 100, Role: auth.RoleInstructor}
	student = auth.Principal{UserID: 200, Role: auth.RoleStudent}
	other   = auth.Principal{UserID: 300, Role: auth.RoleStudent}
)

const (
	course      id.ID = 10
	exam        id.ID = 700
	foreignExam id.ID = 800 // belongs to another course
	unknownUser id.ID = 999
)

type fixture struct {
	svc      *app.Service
	store    *memStore
	enrolled enrollments
	done     progress
	exams    exams
	clock    *fixedClock
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	gen, err := id.NewGenerator(1)
	if err != nil {
		t.Fatal(err)
	}
	f := fixture{
		store:    newMemStore(),
		enrolled: enrollments{{course, student.UserID}: true},
		done:     progress{},
		exams:    exams{inCourse: map[pair]bool{{course, exam}: true}, passed: map[[3]id.ID]bool{}},
		clock:    &fixedClock{now: t0},
	}
	f.svc = app.NewService(f.store, manager{course: course, owner: owner.UserID}, f.enrolled, f.done, f.exams, directory{}, gen, f.clock)
	return f
}

func (f fixture) setPolicy(t *testing.T, mode domain.Mode, examID id.ID) {
	t.Helper()
	if _, err := f.svc.SetPolicy(ctx, owner, course, mode, examID); err != nil {
		t.Fatal(err)
	}
}

func TestSetPolicy(t *testing.T) {
	f := newFixture(t)
	cases := []struct {
		name     string
		who      auth.Principal
		courseID id.ID
		mode     domain.Mode
		exam     id.ID
		want     error
	}{
		{"owner sets completion", owner, course, domain.ModeCompletion, 0, nil},
		{"owner sets exam mode", owner, course, domain.ModeCompletionAndExam, exam, nil},
		{"owner turns it off", owner, course, domain.ModeOff, 0, nil},
		{"student cannot manage", student, course, domain.ModeCompletion, 0, app.ErrForbidden},
		{"unknown course", owner, 11, domain.ModeCompletion, 0, app.ErrNotFound},
		{"unknown mode", owner, course, domain.Mode("always"), 0, app.ErrInvalidInput},
		{"exam mode without exam", owner, course, domain.ModeCompletionAndExam, 0, app.ErrInvalidInput},
		{"exam from another course", owner, course, domain.ModeCompletionAndExam, foreignExam, app.ErrInvalidInput},
		{"exam on completion mode", owner, course, domain.ModeCompletion, exam, app.ErrInvalidInput},
	}
	for _, c := range cases {
		p, err := f.svc.SetPolicy(ctx, c.who, c.courseID, c.mode, c.exam)
		if !errors.Is(err, c.want) {
			t.Errorf("%s: error = %v, want %v", c.name, err, c.want)
			continue
		}
		if err == nil && (p.Mode != c.mode || p.ExamID != c.exam || p.CourseID != c.courseID) {
			t.Errorf("%s: policy = %+v", c.name, p)
		}
	}
	// A rejected write leaves the last good policy in place.
	f.setPolicy(t, domain.ModeCompletion, 0)
	_, _ = f.svc.SetPolicy(ctx, owner, course, domain.ModeCompletionAndExam, foreignExam)
	if p, err := f.svc.GetPolicy(ctx, owner, course); err != nil || p.Mode != domain.ModeCompletion {
		t.Fatalf("policy after rejected write = %+v, %v", p, err)
	}
}

func TestGetPolicy(t *testing.T) {
	f := newFixture(t)
	p, err := f.svc.GetPolicy(ctx, owner, course)
	if err != nil || p.Mode != domain.ModeOff || p.CourseID != course || !p.ExamID.IsZero() {
		t.Fatalf("default policy = %+v, %v", p, err)
	}
	if _, err := f.svc.GetPolicy(ctx, student, course); !errors.Is(err, app.ErrForbidden) {
		t.Fatalf("student read = %v", err)
	}
}

func TestClaimEligibility(t *testing.T) {
	cases := []struct {
		name  string
		setup func(f fixture, t *testing.T)
		want  error
	}{
		{"no policy", func(fixture, *testing.T) {}, app.ErrCertificatesDisabled},
		{"policy off", func(f fixture, t *testing.T) { f.setPolicy(t, domain.ModeOff, 0) }, app.ErrCertificatesDisabled},
		{"not enrolled", func(f fixture, t *testing.T) {
			f.setPolicy(t, domain.ModeCompletion, 0)
			delete(f.enrolled, pair{course, student.UserID})
		}, app.ErrNotEnrolled},
		{"progress incomplete", func(f fixture, t *testing.T) { f.setPolicy(t, domain.ModeCompletion, 0) }, app.ErrProgressIncomplete},
		{"completion met", func(f fixture, t *testing.T) {
			f.setPolicy(t, domain.ModeCompletion, 0)
			f.done[pair{course, student.UserID}] = true
		}, nil},
		{"exam mode, exam not passed", func(f fixture, t *testing.T) {
			f.setPolicy(t, domain.ModeCompletionAndExam, exam)
			f.done[pair{course, student.UserID}] = true
		}, app.ErrExamNotPassed},
		{"exam mode, exam passed but course incomplete", func(f fixture, t *testing.T) {
			f.setPolicy(t, domain.ModeCompletionAndExam, exam)
			f.exams.passed[[3]id.ID{course, student.UserID, exam}] = true
		}, app.ErrProgressIncomplete},
		{"exam mode, exam since removed from the course", func(f fixture, t *testing.T) {
			f.setPolicy(t, domain.ModeCompletionAndExam, exam)
			f.done[pair{course, student.UserID}] = true
			f.exams.passed[[3]id.ID{course, student.UserID, exam}] = true
			delete(f.exams.inCourse, pair{course, exam})
		}, app.ErrCertificatesDisabled},
		{"exam mode, both met", func(f fixture, t *testing.T) {
			f.setPolicy(t, domain.ModeCompletionAndExam, exam)
			f.done[pair{course, student.UserID}] = true
			f.exams.passed[[3]id.ID{course, student.UserID, exam}] = true
		}, nil},
	}
	for _, c := range cases {
		f := newFixture(t)
		c.setup(f, t)
		cert, created, err := f.svc.Claim(ctx, student, course)
		if !errors.Is(err, c.want) {
			t.Errorf("%s: error = %v, want %v", c.name, err, c.want)
			continue
		}
		if c.want != nil {
			continue
		}
		if !created || !domain.ValidCode(cert.Code) || cert.UserID != student.UserID || cert.CourseID != course ||
			cert.StudentName != "Asha Rai" || cert.CourseTitle != "Go" || !cert.IssuedAt.Equal(t0) || cert.Revoked() {
			t.Errorf("%s: certificate = %+v created=%v", c.name, cert, created)
		}
	}
}

func TestClaimTwiceReturnsTheSameCertificate(t *testing.T) {
	f := newFixture(t)
	f.setPolicy(t, domain.ModeCompletion, 0)
	f.done[pair{course, student.UserID}] = true
	first, created, err := f.svc.Claim(ctx, student, course)
	if err != nil || !created {
		t.Fatalf("first claim: %v created=%v", err, created)
	}
	second, created, err := f.svc.Claim(ctx, student, course)
	if err != nil || created || second.Code != first.Code || second.ID != first.ID {
		t.Fatalf("second claim: %+v created=%v err=%v", second, created, err)
	}
	if len(f.store.certs) != 1 {
		t.Fatalf("%d certificates stored", len(f.store.certs))
	}
}

func TestClaimStillWorksWhenTheCertificateOutlivesAPolicyChange(t *testing.T) {
	f := newFixture(t)
	f.setPolicy(t, domain.ModeCompletion, 0)
	f.done[pair{course, student.UserID}] = true
	first, _, err := f.svc.Claim(ctx, student, course)
	if err != nil {
		t.Fatal(err)
	}
	f.setPolicy(t, domain.ModeOff, 0)
	if _, _, err := f.svc.Claim(ctx, student, course); !errors.Is(err, app.ErrCertificatesDisabled) {
		t.Fatalf("claim with policy off = %v", err)
	}
	got, err := f.svc.GetMine(ctx, student, course)
	if err != nil || got.Code != first.Code {
		t.Fatalf("GetMine after policy off = %+v, %v", got, err)
	}
}

func TestClaimUnknownStudentIsNotFound(t *testing.T) {
	f := newFixture(t)
	f.setPolicy(t, domain.ModeCompletion, 0)
	ghost := auth.Principal{UserID: unknownUser, Role: auth.RoleStudent}
	f.enrolled[pair{course, ghost.UserID}] = true
	f.done[pair{course, ghost.UserID}] = true
	if _, _, err := f.svc.Claim(ctx, ghost, course); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("claim = %v", err)
	}
}

func TestRevokeForRefund(t *testing.T) {
	f := newFixture(t)
	f.setPolicy(t, domain.ModeCompletion, 0)
	f.done[pair{course, student.UserID}] = true
	first, _, err := f.svc.Claim(ctx, student, course)
	if err != nil {
		t.Fatal(err)
	}

	f.clock.now = t0.Add(time.Hour)
	refundedAt := t0.Add(time.Minute)
	if err := f.svc.RevokeForRefund(ctx, other.UserID, course, refundedAt); err != nil {
		t.Fatalf("revoke for a user without a certificate: %v", err)
	}
	if err := f.svc.RevokeForRefund(ctx, student.UserID, course+1, refundedAt); err != nil {
		t.Fatalf("revoke for another course: %v", err)
	}
	if _, ok, _ := f.store.FindValid(ctx, course, student.UserID); !ok {
		t.Fatal("unrelated revokes touched the certificate")
	}
	for range 2 { // redelivery is safe
		if err := f.svc.RevokeForRefund(ctx, student.UserID, course, refundedAt); err != nil {
			t.Fatal(err)
		}
	}
	revoked, err := f.svc.Verify(ctx, first.Code)
	if err != nil || !revoked.Revoked() || !revoked.RevokedAt.Equal(t0.Add(time.Hour)) {
		t.Fatalf("verify after revoke = %+v, %v", revoked, err)
	}
	if _, err := f.svc.GetMine(ctx, student, course); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("GetMine after revoke = %v", err)
	}

	// Enrollment returns; the student qualifies again and gets a new certificate.
	again, created, err := f.svc.Claim(ctx, student, course)
	if err != nil || !created || again.Code == first.Code || again.ID == first.ID {
		t.Fatalf("reissue = %+v created=%v err=%v", again, created, err)
	}
	if old, err := f.svc.Verify(ctx, first.Code); err != nil || !old.Revoked() {
		t.Fatalf("old certificate after reissue = %+v, %v", old, err)
	}
	list, err := f.svc.ListMine(ctx, student)
	if err != nil || len(list) != 2 || list[0].Code != again.Code {
		t.Fatalf("ListMine = %+v, %v", list, err)
	}

	// A late redelivery of the old refund leaves the certificate earned after it alone.
	if err := f.svc.RevokeForRefund(ctx, student.UserID, course, refundedAt); err != nil {
		t.Fatal(err)
	}
	if got, err := f.svc.GetMine(ctx, student, course); err != nil || got.Code != again.Code {
		t.Fatalf("GetMine after stale revoke = %+v, %v", got, err)
	}
}

func TestVerify(t *testing.T) {
	f := newFixture(t)
	f.setPolicy(t, domain.ModeCompletion, 0)
	f.done[pair{course, student.UserID}] = true
	cert, _, err := f.svc.Claim(ctx, student, course)
	if err != nil {
		t.Fatal(err)
	}
	got, err := f.svc.Verify(ctx, cert.Code)
	if err != nil || got.Code != cert.Code || got.Revoked() {
		t.Fatalf("verify = %+v, %v", got, err)
	}
	for _, bad := range []string{"", "short", cert.Code + "A", domain.NewCode(), "a" + cert.Code[1:]} {
		if _, err := f.svc.Verify(ctx, bad); !errors.Is(err, app.ErrNotFound) {
			t.Errorf("verify(%q) = %v, want ErrNotFound", bad, err)
		}
	}
}
