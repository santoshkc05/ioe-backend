package app_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/payment/app"
	"github.com/santoshkc2200/ioe-backend/internal/payment/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

var (
	ctx     = context.Background()
	t0      = time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC)
	price   = domain.Money{AmountMinor: 150000, Currency: "NPR"}
	admin   = auth.Principal{UserID: 1, Role: auth.RoleRootAdmin}
	student = auth.Principal{UserID: 200, Role: auth.RoleStudent}
	other   = auth.Principal{UserID: 300, Role: auth.RoleStudent}
	third   = auth.Principal{UserID: 400, Role: auth.RoleStudent}
)

const (
	freeCourse  id.ID = 10
	paidCourse  id.ID = 11
	draftCourse id.ID = 12
)

type fixture struct {
	svc    *app.Service
	store  *memStore
	gw     *fakeGateway
	enroll *fakeEnrollments
	clock  *fixedClock
	logs   *bytes.Buffer
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	ids, err := id.NewGenerator(0)
	if err != nil {
		t.Fatal(err)
	}
	f := fixture{
		store:  newMemStore(),
		gw:     &fakeGateway{results: map[string]app.Result{}},
		enroll: &fakeEnrollments{enrolled: map[[2]id.ID]bool{}},
		clock:  &fixedClock{now: t0},
		logs:   &bytes.Buffer{},
	}
	courses := catalog{
		freeCourse:  {Published: true, Title: "Free"},
		paidCourse:  {Published: true, Title: "Go", Price: price},
		draftCourse: {Published: false, Title: "Draft", Price: price},
	}
	f.svc = app.NewService(f.store, courses, fakeUsers{admin.UserID: true, student.UserID: true, other.UserID: true, third.UserID: true},
		f.enroll, map[string]app.Gateway{"esewa": f.gw}, ids, f.clock,
		slog.New(slog.NewTextHandler(f.logs, nil)))
	return f
}

func (f fixture) checkout(t *testing.T, p auth.Principal) domain.Purchase {
	t.Helper()
	purchase, _, err := f.svc.Checkout(ctx, p, paidCourse, "esewa")
	if err != nil {
		t.Fatal(err)
	}
	return purchase
}

func (f fixture) complete(p domain.Purchase, txn string) {
	f.gw.results[p.GatewayRef] = app.Result{Kind: app.ResultComplete, Txn: txn}
}

func eventNames(evs []domain.Event) []string {
	out := make([]string, len(evs))
	for i, e := range evs {
		out[i] = e.EventName()
	}
	return out
}

func TestCheckoutCreatesPendingPurchase(t *testing.T) {
	f := newFixture(t)
	p, co, err := f.svc.Checkout(ctx, student, paidCourse, "esewa")
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != domain.StatusPending || p.Price != price || p.UserID != student.UserID || p.CourseID != paidCourse ||
		p.Gateway != "esewa" || p.GatewayRef != p.ID.String() || p.Version != 1 || !p.CreatedAt.Equal(t0) {
		t.Fatalf("purchase = %+v", p)
	}
	if p.CourseTitle != "Go" {
		t.Fatalf("title = %q", p.CourseTitle)
	}
	if co.URL != "https://pay.test/form" || co.Fields["ref"] != p.GatewayRef {
		t.Fatalf("checkout = %+v", co)
	}
	if got := eventNames(f.store.published); len(got) != 1 || got[0] != "payment.purchase.initiated" {
		t.Fatalf("events = %v", got)
	}
}

func TestCheckoutRejects(t *testing.T) {
	cases := []struct {
		name    string
		setup   func(f fixture)
		course  id.ID
		gateway string
		want    error
	}{
		{"empty gateway", nil, paidCourse, "", app.ErrInvalidInput},
		{"unknown gateway", nil, paidCourse, "stripe", app.ErrGatewayUnavailable},
		{"missing course", nil, 999, "esewa", app.ErrNotFound},
		{"unpublished course", nil, draftCourse, "esewa", app.ErrNotFound},
		{"free course", nil, freeCourse, "esewa", app.ErrCourseFree},
		{"already enrolled", func(f fixture) { f.enroll.enrolled[[2]id.ID{paidCourse, student.UserID}] = true }, paidCourse, "esewa", app.ErrAlreadyEnrolled},
		{"already purchased", func(f fixture) {
			f.store.rows[5] = domain.Purchase{ID: 5, UserID: student.UserID, CourseID: paidCourse, Price: price,
				Gateway: "esewa", GatewayRef: "5", GatewayTxn: "T", Status: domain.StatusPaid, CreatedAt: t0, SettledAt: t0, Version: 1}
		}, paidCourse, "esewa", app.ErrAlreadyPurchased},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			if c.setup != nil {
				c.setup(f)
			}
			before := len(f.store.rows)
			if _, _, err := f.svc.Checkout(ctx, student, c.course, c.gateway); !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
			if len(f.store.rows) != before || len(f.store.published) != 0 {
				t.Fatalf("rows=%d events=%v", len(f.store.rows), f.store.published)
			}
		})
	}
}

func TestCheckoutGatewayErrorLeavesPendingPurchase(t *testing.T) {
	f := newFixture(t)
	f.gw.checkoutErr = errors.New("bad amount")
	if _, _, err := f.svc.Checkout(ctx, student, paidCourse, "esewa"); !errors.Is(err, app.ErrGatewayUnavailable) {
		t.Fatalf("err = %v", err)
	}
	if len(f.store.rows) != 1 {
		t.Fatalf("rows = %d", len(f.store.rows))
	}
}

func TestConfirmCompletesAndGrants(t *testing.T) {
	f := newFixture(t)
	p := f.checkout(t, student)
	f.complete(p, "0001TS9")
	f.clock.now = t0.Add(time.Minute)
	got, err := f.svc.Confirm(ctx, student, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.StatusPaid || got.GatewayTxn != "0001TS9" || !got.SettledAt.Equal(f.clock.now) ||
		!got.GrantedAt.Equal(f.clock.now) || got.NeedsGrant() {
		t.Fatalf("purchase = %+v", got)
	}
	if !f.enroll.enrolled[[2]id.ID{paidCourse, student.UserID}] || f.enroll.grants != 1 {
		t.Fatalf("grants = %d enrolled = %v", f.enroll.grants, f.enroll.enrolled)
	}
	if stored := f.store.get(p.ID); stored != got {
		t.Fatalf("stored = %+v, returned = %+v", stored, got)
	}
	if got := eventNames(f.store.published); len(got) != 2 || got[1] != "payment.purchase.paid" {
		t.Fatalf("events = %v", got)
	}
}

func TestConfirmPendingAndFailed(t *testing.T) {
	f := newFixture(t)
	p := f.checkout(t, student)
	got, err := f.svc.Confirm(ctx, student, p.ID)
	if err != nil || got.Status != domain.StatusPending || len(f.store.published) != 1 {
		t.Fatalf("pending: %+v err=%v events=%v", got, err, f.store.published)
	}
	f.gw.results[p.GatewayRef] = app.Result{Kind: app.ResultFailed}
	got, err = f.svc.Confirm(ctx, student, p.ID)
	if err != nil || got.Status != domain.StatusFailed || f.enroll.grants != 0 {
		t.Fatalf("failed: %+v err=%v grants=%d", got, err, f.enroll.grants)
	}
	if names := eventNames(f.store.published); names[len(names)-1] != "payment.purchase.failed" {
		t.Fatalf("events = %v", names)
	}
}

func TestConfirmLateCompletionAfterFailure(t *testing.T) {
	f := newFixture(t)
	p := f.checkout(t, student)
	f.gw.results[p.GatewayRef] = app.Result{Kind: app.ResultFailed}
	if _, err := f.svc.Confirm(ctx, student, p.ID); err != nil {
		t.Fatal(err)
	}
	f.complete(p, "LATE")
	got, err := f.svc.Confirm(ctx, student, p.ID)
	if err != nil || got.Status != domain.StatusPaid || got.NeedsGrant() {
		t.Fatalf("late: %+v err=%v", got, err)
	}
}

func TestConfirmPaidIsIdempotent(t *testing.T) {
	f := newFixture(t)
	p := f.checkout(t, student)
	f.complete(p, "T1")
	first, err := f.svc.Confirm(ctx, student, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	calls, events := f.gw.statusCalls, len(f.store.published)
	again, err := f.svc.Confirm(ctx, student, p.ID)
	if err != nil || again != first || f.gw.statusCalls != calls || len(f.store.published) != events || f.enroll.grants != 1 {
		t.Fatalf("again=%+v err=%v calls=%d events=%d grants=%d", again, err, f.gw.statusCalls, len(f.store.published), f.enroll.grants)
	}
}

func TestGrantFailureIsRetried(t *testing.T) {
	f := newFixture(t)
	p := f.checkout(t, student)
	f.complete(p, "T1")
	f.enroll.grantErr = errors.New("database unavailable")
	got, err := f.svc.Confirm(ctx, student, p.ID)
	if err != nil || got.Status != domain.StatusPaid || !got.NeedsGrant() {
		t.Fatalf("first confirm: %+v err=%v", got, err)
	}
	f.enroll.grantErr = nil
	got, err = f.svc.Confirm(ctx, student, p.ID)
	if err != nil || got.NeedsGrant() || f.enroll.grants != 2 || f.gw.statusCalls != 1 {
		t.Fatalf("retry: %+v err=%v grants=%d calls=%d", got, err, f.enroll.grants, f.gw.statusCalls)
	}
	if !strings.Contains(f.logs.String(), "enrollment grant failed") {
		t.Fatalf("logs = %s", f.logs)
	}
}

func TestConfirmAccess(t *testing.T) {
	f := newFixture(t)
	p := f.checkout(t, student)
	if _, err := f.svc.Confirm(ctx, other, p.ID); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("other err = %v", err)
	}
	if _, err := f.svc.Get(ctx, other, p.ID); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("other get err = %v", err)
	}
	if _, err := f.svc.Confirm(ctx, student, 999); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("missing err = %v", err)
	}
	if got, err := f.svc.Get(ctx, admin, p.ID); err != nil || got.ID != p.ID {
		t.Fatalf("admin get = %+v, %v", got, err)
	}
	if f.gw.statusCalls != 0 {
		t.Fatalf("status calls = %d", f.gw.statusCalls)
	}
}

func TestConfirmGatewayError(t *testing.T) {
	f := newFixture(t)
	p := f.checkout(t, student)
	f.gw.statusErr = errors.New("timeout")
	if _, err := f.svc.Confirm(ctx, student, p.ID); !errors.Is(err, app.ErrGatewayUnavailable) {
		t.Fatalf("err = %v", err)
	}
	if f.store.get(p.ID).Status != domain.StatusPending {
		t.Fatal("purchase changed")
	}
}

func TestDuplicatePaidPurchaseLogsWarning(t *testing.T) {
	f := newFixture(t)
	a := f.checkout(t, student)
	b := f.checkout(t, student) // second tab, before either is paid
	f.complete(a, "TA")
	f.complete(b, "TB")
	if _, err := f.svc.Confirm(ctx, student, a.ID); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(f.logs.String(), "duplicate paid purchase") {
		t.Fatal("warned on the first payment")
	}
	got, err := f.svc.Confirm(ctx, student, b.ID)
	if err != nil || got.Status != domain.StatusPaid {
		t.Fatalf("second: %+v err=%v", got, err)
	}
	if !strings.Contains(f.logs.String(), "duplicate paid purchase") {
		t.Fatalf("logs = %s", f.logs)
	}
}

func TestStaleVersionIsRetried(t *testing.T) {
	f := newFixture(t)
	p := f.checkout(t, student)
	f.complete(p, "T1")
	f.store.conflicts = 1
	got, err := f.svc.Confirm(ctx, student, p.ID)
	if err != nil || got.Status != domain.StatusPaid || got.NeedsGrant() {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}

func TestReconcileSettlesOldPendingAndRetriesGrants(t *testing.T) {
	f := newFixture(t)
	old := f.checkout(t, student)

	ungranted := f.checkout(t, other)
	f.complete(ungranted, "T2")
	f.enroll.grantErr = errors.New("down")
	if _, err := f.svc.Confirm(ctx, other, ungranted.ID); err != nil {
		t.Fatal(err)
	}
	f.enroll.grantErr = nil

	f.clock.now = t0.Add(10 * time.Minute)
	recent := f.checkout(t, third)
	f.complete(old, "T1")
	f.complete(recent, "T3")

	f.clock.now = t0.Add(20 * time.Minute)
	f.gw.statusCalls = 0
	if err := f.svc.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if f.gw.statusCalls != 1 {
		t.Fatalf("status calls = %d, want 1 (only the old pending purchase)", f.gw.statusCalls)
	}
	if got := f.store.get(old.ID); got.Status != domain.StatusPaid || got.NeedsGrant() {
		t.Fatalf("old = %+v", got)
	}
	if got := f.store.get(ungranted.ID); got.NeedsGrant() {
		t.Fatalf("ungranted = %+v", got)
	}
	if got := f.store.get(recent.ID); got.Status != domain.StatusPending {
		t.Fatalf("recent = %+v", got)
	}
}

func TestReconcilePagesAndWarnsOnStalePending(t *testing.T) {
	f := newFixture(t)
	for i := range 60 {
		f.checkout(t, auth.Principal{UserID: id.ID(1000 + i), Role: auth.RoleStudent})
	}
	f.clock.now = t0.Add(25 * time.Hour)
	if err := f.svc.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if f.gw.statusCalls != 60 {
		t.Fatalf("status calls = %d, want 60", f.gw.statusCalls)
	}
	if !strings.Contains(f.logs.String(), "purchase pending for over 24 hours") {
		t.Fatalf("logs = %s", f.logs)
	}
}

func TestReconcileContinuesPastGatewayErrors(t *testing.T) {
	f := newFixture(t)
	f.checkout(t, student)
	f.checkout(t, other)
	f.gw.statusErr = errors.New("timeout")
	f.clock.now = t0.Add(time.Hour)
	if err := f.svc.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if f.gw.statusCalls != 2 {
		t.Fatalf("status calls = %d", f.gw.statusCalls)
	}
}

var cashInput = app.ManualInput{UserID: 200, CourseID: paidCourse, AmountMinor: 100000, Currency: "NPR",
	Method: domain.MethodCash, Reference: "R-1", Note: "desk"}

func TestRecordManualCreatesPaidPurchaseAndGrants(t *testing.T) {
	f := newFixture(t)
	p, err := f.svc.RecordManual(ctx, admin, cashInput)
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != domain.StatusPaid || p.Gateway != domain.GatewayManual || p.GrantedAt.IsZero() ||
		p.Price != (domain.Money{AmountMinor: 100000, Currency: "NPR"}) || p.CourseTitle != "Go" ||
		p.RecordedBy != admin.UserID || p.GatewayTxn != "R-1" || p.ManualMethod != "cash" {
		t.Fatalf("purchase = %+v", p)
	}
	if !f.enroll.enrolled[[2]id.ID{paidCourse, student.UserID}] {
		t.Fatal("not enrolled")
	}
	if got := eventNames(f.store.published); len(got) != 1 || got[0] != "payment.purchase.paid" {
		t.Fatalf("events = %v", got)
	}
}

func TestRecordManualAllowsAlreadyEnrolled(t *testing.T) {
	f := newFixture(t)
	f.enroll.enrolled[[2]id.ID{paidCourse, student.UserID}] = true
	if _, err := f.svc.RecordManual(ctx, admin, cashInput); err != nil {
		t.Fatal(err)
	}
}

func TestRecordManualRejects(t *testing.T) {
	with := func(f func(*app.ManualInput)) app.ManualInput { in := cashInput; f(&in); return in }
	cases := []struct {
		name  string
		p     auth.Principal
		in    app.ManualInput
		setup func(f fixture)
		want  error
	}{
		{"not admin", student, cashInput, nil, app.ErrForbidden},
		{"missing course", admin, with(func(in *app.ManualInput) { in.CourseID = 999 }), nil, app.ErrNotFound},
		{"unpublished", admin, with(func(in *app.ManualInput) { in.CourseID = draftCourse }), nil, app.ErrNotFound},
		{"free course", admin, with(func(in *app.ManualInput) { in.CourseID = freeCourse }), nil, app.ErrCourseFree},
		{"currency mismatch", admin, with(func(in *app.ManualInput) { in.Currency = "USD" }), nil, app.ErrInvalidInput},
		{"unknown user", admin, with(func(in *app.ManualInput) { in.UserID = 999 }), nil, app.ErrNotFound},
		{"zero amount", admin, with(func(in *app.ManualInput) { in.AmountMinor = 0 }), nil, app.ErrInvalidInput},
		{"bad method", admin, with(func(in *app.ManualInput) { in.Method = "cheque" }), nil, app.ErrInvalidInput},
		{"blank reference", admin, with(func(in *app.ManualInput) { in.Reference = "  " }), nil, app.ErrInvalidInput},
		{"already purchased", admin, cashInput, func(f fixture) {
			f.store.rows[5] = domain.Purchase{ID: 5, UserID: student.UserID, CourseID: paidCourse, Price: price,
				Gateway: "esewa", GatewayRef: "5", GatewayTxn: "T", Status: domain.StatusPaid, CreatedAt: t0, SettledAt: t0, Version: 1}
		}, app.ErrAlreadyPurchased},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			if c.setup != nil {
				c.setup(f)
			}
			before := len(f.store.rows)
			if _, err := f.svc.RecordManual(ctx, c.p, c.in); !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
			if len(f.store.rows) != before || len(f.store.published) != 0 || f.enroll.grants != 0 {
				t.Fatalf("rows=%d events=%v grants=%d", len(f.store.rows), f.store.published, f.enroll.grants)
			}
		})
	}
}

func TestRecordManualGrantFailureIsReconciled(t *testing.T) {
	f := newFixture(t)
	f.enroll.grantErr = errors.New("enrollment down")
	p, err := f.svc.RecordManual(ctx, admin, cashInput)
	if err != nil {
		t.Fatal(err)
	}
	if !p.GrantedAt.IsZero() {
		t.Fatal("granted despite failure")
	}
	f.enroll.grantErr = nil
	// Confirm on a manual purchase never reaches a gateway; "manual" is not registered.
	if got, err := f.svc.Confirm(ctx, student, p.ID); err != nil || got.GrantedAt.IsZero() {
		t.Fatalf("confirm = %+v, %v", got, err)
	}
	f.store.rows[p.ID] = func() domain.Purchase { q := f.store.get(p.ID); q.GrantedAt = time.Time{}; return q }()
	if err := f.svc.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if f.store.get(p.ID).GrantedAt.IsZero() || f.gw.statusCalls != 0 {
		t.Fatalf("reconcile: %+v statusCalls=%d", f.store.get(p.ID), f.gw.statusCalls)
	}
}

func TestListByUserPagesNewestFirst(t *testing.T) {
	f := newFixture(t)
	var ids []id.ID
	for range 3 {
		ids = append(ids, f.checkout(t, student).ID)
	}
	f.checkout(t, other)
	page, next, err := f.svc.ListByUser(ctx, student, student.UserID, 0, 2)
	if err != nil || len(page) != 2 || page[0].ID != ids[2] || page[1].ID != ids[1] || next != ids[1] {
		t.Fatalf("page=%v next=%v err=%v", page, next, err)
	}
	page, next, err = f.svc.ListByUser(ctx, student, student.UserID, next, 2)
	if err != nil || len(page) != 1 || page[0].ID != ids[0] || next != 0 {
		t.Fatalf("last page=%v next=%v err=%v", page, next, err)
	}
	if page, next, err = f.svc.ListByUser(ctx, admin, 999, 0, 20); err != nil || len(page) != 0 || next != 0 {
		t.Fatalf("unknown user page=%v next=%v err=%v", page, next, err)
	}
}

func TestListByUserAccess(t *testing.T) {
	f := newFixture(t)
	f.checkout(t, student)
	if _, _, err := f.svc.ListByUser(ctx, other, student.UserID, 0, 20); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("other err = %v", err)
	}
	if page, _, err := f.svc.ListByUser(ctx, admin, student.UserID, 0, 20); err != nil || len(page) != 1 {
		t.Fatalf("admin page=%v err=%v", page, err)
	}
	for _, limit := range []int{0, 51} {
		if _, _, err := f.svc.ListByUser(ctx, student, student.UserID, 0, limit); !errors.Is(err, app.ErrInvalidInput) {
			t.Fatalf("limit %d err = %v", limit, err)
		}
	}
}
