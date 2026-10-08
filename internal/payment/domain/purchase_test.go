package domain_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/payment/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
)

var (
	t0  = time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC)
	npr = domain.Money{AmountMinor: 150000, Currency: "NPR"}
)

func newPending(t *testing.T) domain.Purchase {
	t.Helper()
	p, _, err := domain.NewPurchase(42, 200, 10, "Go", npr, "esewa", t0)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestNewPurchase(t *testing.T) {
	p, ev, err := domain.NewPurchase(42, 200, 10, "Go", npr, "esewa", t0)
	if err != nil {
		t.Fatal(err)
	}
	want := domain.Purchase{ID: 42, UserID: 200, CourseID: 10, CourseTitle: "Go", Price: npr, Gateway: "esewa", GatewayRef: "42",
		Status: domain.StatusPending, CreatedAt: t0}
	if p != want {
		t.Fatalf("purchase = %+v", p)
	}
	got, ok := ev.(domain.PurchaseInitiated)
	if !ok || got != (domain.PurchaseInitiated{PurchaseID: 42, UserID: 200, CourseID: 10, AmountMinor: 150000,
		Currency: "NPR", Gateway: "esewa", OccurredAt: t0}) {
		t.Fatalf("event = %#v", ev)
	}
	if ev.EventName() != "payment.purchase.initiated" {
		t.Fatalf("name = %s", ev.EventName())
	}
}

func TestNewPurchaseRejects(t *testing.T) {
	cases := []struct {
		name    string
		price   domain.Money
		gateway string
		want    error
	}{
		{"zero amount", domain.Money{Currency: "NPR"}, "esewa", domain.ErrFreePrice},
		{"negative amount", domain.Money{AmountMinor: -1, Currency: "NPR"}, "esewa", domain.ErrFreePrice},
		{"empty currency", domain.Money{AmountMinor: 100}, "esewa", domain.ErrInvalidPurchase},
		{"empty gateway", npr, "", domain.ErrInvalidPurchase},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, ev, err := domain.NewPurchase(1, 2, 3, "Go", c.price, c.gateway, t0); !errors.Is(err, c.want) || ev != nil {
				t.Fatalf("err = %v, ev = %v", err, ev)
			}
		})
	}
}

func TestMarkPaid(t *testing.T) {
	p := newPending(t)
	at := t0.Add(time.Minute)
	ev, err := p.MarkPaid("0001TS9", at)
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != domain.StatusPaid || p.GatewayTxn != "0001TS9" || !p.SettledAt.Equal(at) || !p.NeedsGrant() {
		t.Fatalf("purchase = %+v", p)
	}
	paid, ok := ev.(domain.PurchasePaid)
	if !ok || paid.GatewayTxn != "0001TS9" || paid.CourseTitle != "Go" || paid.AmountMinor != 150000 || paid.Currency != "NPR" || !paid.OccurredAt.Equal(at) {
		t.Fatalf("event = %#v", ev)
	}
	before := p
	if ev, err := p.MarkPaid("OTHER", at.Add(time.Hour)); err != nil || ev != nil || p != before {
		t.Fatalf("second MarkPaid: ev=%v err=%v p=%+v", ev, err, p)
	}
}

func TestMarkPaidRequiresTxn(t *testing.T) {
	p := newPending(t)
	if _, err := p.MarkPaid("", t0); !errors.Is(err, domain.ErrInvalidPurchase) || p.Status != domain.StatusPending {
		t.Fatalf("err = %v status = %s", err, p.Status)
	}
}

func TestMarkPaidAfterFailure(t *testing.T) {
	p := newPending(t)
	if ev := p.MarkFailed(t0.Add(time.Minute)); ev == nil {
		t.Fatal("no failed event")
	}
	late := t0.Add(time.Hour)
	ev, err := p.MarkPaid("LATE1", late)
	if err != nil || ev == nil || p.Status != domain.StatusPaid || !p.SettledAt.Equal(late) {
		t.Fatalf("ev=%v err=%v p=%+v", ev, err, p)
	}
}

func TestMarkFailed(t *testing.T) {
	p := newPending(t)
	at := t0.Add(time.Minute)
	ev := p.MarkFailed(at)
	if f, ok := ev.(domain.PurchaseFailed); !ok || f.PurchaseID != 42 || f.Gateway != "esewa" || !f.OccurredAt.Equal(at) {
		t.Fatalf("event = %#v", ev)
	}
	if p.Status != domain.StatusFailed || !p.SettledAt.Equal(at) {
		t.Fatalf("purchase = %+v", p)
	}
	if ev := p.MarkFailed(at.Add(time.Minute)); ev != nil {
		t.Fatalf("second MarkFailed event = %v", ev)
	}

	paid := newPending(t)
	if _, err := paid.MarkPaid("T", at); err != nil {
		t.Fatal(err)
	}
	if ev := paid.MarkFailed(at); ev != nil || paid.Status != domain.StatusPaid {
		t.Fatalf("MarkFailed on paid: ev=%v status=%s", ev, paid.Status)
	}
}

func TestMarkGranted(t *testing.T) {
	p := newPending(t)
	p.MarkGranted(t0)
	if !p.GrantedAt.IsZero() || p.NeedsGrant() {
		t.Fatalf("pending purchase granted: %+v", p)
	}
	if _, err := p.MarkPaid("T", t0); err != nil {
		t.Fatal(err)
	}
	at := t0.Add(time.Minute)
	p.MarkGranted(at)
	p.MarkGranted(at.Add(time.Hour))
	if !p.GrantedAt.Equal(at) || p.NeedsGrant() {
		t.Fatalf("purchase = %+v", p)
	}
}

func TestCanView(t *testing.T) {
	p := newPending(t)
	cases := []struct {
		pr   auth.Principal
		want bool
	}{
		{auth.Principal{UserID: 200, Role: auth.RoleStudent}, true},
		{auth.Principal{UserID: 1, Role: auth.RoleRootAdmin}, true},
		{auth.Principal{UserID: 300, Role: auth.RoleStudent}, false},
		{auth.Principal{UserID: 400, Role: auth.RoleInstructor}, false},
	}
	for _, c := range cases {
		if got := p.CanView(c.pr); got != c.want {
			t.Fatalf("CanView(%+v) = %v", c.pr, got)
		}
	}
}

func TestEventPayloads(t *testing.T) {
	b, err := json.Marshal(domain.PurchasePaid{PurchaseID: 1, UserID: 2, CourseID: 3, AmountMinor: 100, Currency: "NPR",
		Gateway: "esewa", GatewayTxn: "T", OccurredAt: t0})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"purchase_id", "user_id", "course_id", "amount_minor", "currency", "gateway", "gateway_txn", "occurred_at"} {
		if _, ok := got[k]; !ok {
			t.Fatalf("missing %s in %s", k, b)
		}
	}
	if got["purchase_id"] != "1" {
		t.Fatalf("purchase_id = %v", got["purchase_id"])
	}
	if (domain.PurchasePaid{}).EventName() != "payment.purchase.paid" || (domain.PurchaseFailed{}).EventName() != "payment.purchase.failed" {
		t.Fatal("event names")
	}
}

func TestPaidEventCarriesTitleAndMethod(t *testing.T) {
	p := newPending(t)
	ev, err := p.MarkPaid("T1", t0)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if m["course_title"] != "Go" || m["manual_method"] != "" {
		t.Fatalf("payload = %s", b)
	}
}

var cash = domain.ManualPayment{Method: domain.MethodCash, Reference: "  R-1  ", Note: "paid at desk", RecordedBy: 1}

func TestRecordManualPurchase(t *testing.T) {
	p, ev, err := domain.RecordManualPurchase(43, 200, 10, "Go", npr, cash, t0)
	if err != nil {
		t.Fatal(err)
	}
	want := domain.Purchase{ID: 43, UserID: 200, CourseID: 10, CourseTitle: "Go", Price: npr, Gateway: domain.GatewayManual,
		GatewayRef: "43", GatewayTxn: "R-1", Status: domain.StatusPaid, CreatedAt: t0, SettledAt: t0,
		ManualMethod: "cash", RecordedBy: 1, Note: "paid at desk"}
	if p != want {
		t.Fatalf("purchase = %+v", p)
	}
	if !p.NeedsGrant() {
		t.Fatal("manual purchase should need a grant")
	}
	got, ok := ev.(domain.PurchasePaid)
	if !ok || got != (domain.PurchasePaid{PurchaseID: 43, UserID: 200, CourseID: 10, CourseTitle: "Go", AmountMinor: 150000,
		Currency: "NPR", Gateway: "manual", GatewayTxn: "R-1", ManualMethod: "cash", OccurredAt: t0}) {
		t.Fatalf("event = %#v", ev)
	}
}

func TestRecordManualPurchaseRejects(t *testing.T) {
	with := func(f func(*domain.ManualPayment)) domain.ManualPayment { m := cash; f(&m); return m }
	cases := []struct {
		name  string
		price domain.Money
		m     domain.ManualPayment
		want  error
	}{
		{"zero amount", domain.Money{Currency: "NPR"}, cash, domain.ErrFreePrice},
		{"empty currency", domain.Money{AmountMinor: 100}, cash, domain.ErrInvalidPurchase},
		{"unknown method", npr, with(func(m *domain.ManualPayment) { m.Method = "cheque" }), domain.ErrInvalidPurchase},
		{"empty method", npr, with(func(m *domain.ManualPayment) { m.Method = "" }), domain.ErrInvalidPurchase},
		{"blank reference", npr, with(func(m *domain.ManualPayment) { m.Reference = " \t " }), domain.ErrInvalidPurchase},
		{"long reference", npr, with(func(m *domain.ManualPayment) { m.Reference = strings.Repeat("r", 201) }), domain.ErrInvalidPurchase},
		{"long note", npr, with(func(m *domain.ManualPayment) { m.Note = strings.Repeat("n", 1001) }), domain.ErrInvalidPurchase},
		{"no recorder", npr, with(func(m *domain.ManualPayment) { m.RecordedBy = 0 }), domain.ErrInvalidPurchase},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, _, err := domain.RecordManualPurchase(43, 200, 10, "Go", c.price, c.m, t0); !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
		})
	}
	if _, _, err := domain.RecordManualPurchase(43, 200, 10, "Go", npr,
		with(func(m *domain.ManualPayment) {
			m.Reference = strings.Repeat("r", 200)
			m.Note = strings.Repeat("n", 1000)
		}), t0); err != nil {
		t.Fatalf("limits inclusive: %v", err)
	}
}

func newPaid(t *testing.T) domain.Purchase {
	t.Helper()
	p := newPending(t)
	if _, err := p.MarkPaid("T1", t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	p.MarkGranted(t0.Add(2 * time.Minute))
	return p
}

var refund = domain.Refund{Reference: " RF-1 ", Note: " dup ", RefundedBy: 1}

func TestRefund(t *testing.T) {
	p := newPaid(t)
	now := t0.Add(time.Hour)
	ev, err := p.Refund(refund, false, now)
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != domain.StatusRefunded || !p.RefundedAt.Equal(now) || p.RefundedBy != 1 ||
		p.RefundReference != "RF-1" || p.RefundNote != "dup" || !p.RevokedAt.IsZero() {
		t.Fatalf("purchase = %+v", p)
	}
	if !p.NeedsRevoke() || p.NeedsGrant() || p.AwaitsGateway() {
		t.Fatalf("needsRevoke=%v needsGrant=%v awaits=%v", p.NeedsRevoke(), p.NeedsGrant(), p.AwaitsGateway())
	}
	want := domain.PurchaseRefunded{PurchaseID: 42, UserID: 200, CourseID: 10, CourseTitle: "Go", AmountMinor: 150000,
		Currency: "NPR", Gateway: "esewa", RefundReference: "RF-1", AccessRevoked: true, OccurredAt: now}
	if got, ok := ev.(domain.PurchaseRefunded); !ok || got != want {
		t.Fatalf("event = %#v", ev)
	}
	if ev.EventName() != "payment.purchase.refunded" {
		t.Fatalf("name = %s", ev.EventName())
	}
	p.MarkRevoked(now.Add(time.Minute))
	if p.NeedsRevoke() || !p.RevokedAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("after revoke = %+v", p)
	}
	p.MarkRevoked(now.Add(time.Hour))
	if !p.RevokedAt.Equal(now.Add(time.Minute)) {
		t.Fatal("MarkRevoked changed an already revoked purchase")
	}
}

func TestRefundWithOtherPaidKeepsAccess(t *testing.T) {
	p := newPaid(t)
	ev, err := p.Refund(refund, true, t0)
	if err != nil {
		t.Fatal(err)
	}
	if p.NeedsRevoke() || !p.RevokedAt.Equal(t0) || ev.(domain.PurchaseRefunded).AccessRevoked {
		t.Fatalf("purchase=%+v event=%+v", p, ev)
	}
}

func TestRefundRejects(t *testing.T) {
	failed := newPending(t)
	failed.MarkFailed(t0)
	refunded := newPaid(t)
	if _, err := refunded.Refund(refund, false, t0); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		p    domain.Purchase
		r    domain.Refund
		want error
	}{
		{"pending", newPending(t), refund, domain.ErrNotRefundable},
		{"failed", failed, refund, domain.ErrNotRefundable},
		{"already refunded", refunded, refund, domain.ErrNotRefundable},
		{"blank reference", newPaid(t), domain.Refund{Reference: "  ", RefundedBy: 1}, domain.ErrInvalidPurchase},
		{"long reference", newPaid(t), domain.Refund{Reference: strings.Repeat("r", 201), RefundedBy: 1}, domain.ErrInvalidPurchase},
		{"long note", newPaid(t), domain.Refund{Reference: "r", Note: strings.Repeat("n", 1001), RefundedBy: 1}, domain.ErrInvalidPurchase},
		{"no admin", newPaid(t), domain.Refund{Reference: "r"}, domain.ErrInvalidPurchase},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			before := c.p
			ev, err := c.p.Refund(c.r, false, t0.Add(time.Hour))
			if !errors.Is(err, c.want) || ev != nil || c.p != before {
				t.Fatalf("err=%v ev=%v changed=%v", err, ev, c.p != before)
			}
		})
	}
}

func TestMarkPaidIgnoresRefunded(t *testing.T) {
	p := newPaid(t)
	if _, err := p.Refund(refund, false, t0); err != nil {
		t.Fatal(err)
	}
	before := p
	ev, err := p.MarkPaid("T2", t0.Add(time.Hour))
	if err != nil || ev != nil || p != before {
		t.Fatalf("ev=%v err=%v p=%+v", ev, err, p)
	}
}

func TestAwaitsGateway(t *testing.T) {
	failed := newPending(t)
	failed.MarkFailed(t0)
	paid := newPaid(t)
	if !newPending(t).AwaitsGateway() || !failed.AwaitsGateway() || paid.AwaitsGateway() {
		t.Fatal("AwaitsGateway wrong")
	}
}
