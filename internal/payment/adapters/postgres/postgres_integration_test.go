//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/santoshkc2200/ioe-backend/internal/payment/adapters/postgres"
	"github.com/santoshkc2200/ioe-backend/internal/payment/app"
	"github.com/santoshkc2200/ioe-backend/internal/payment/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
	"github.com/santoshkc2200/ioe-backend/internal/platform/migrate"
	"github.com/santoshkc2200/ioe-backend/internal/platform/postgres/pgtest"
)

var (
	ctx = context.Background()
	npr = domain.Money{AmountMinor: 150000, Currency: "NPR"}
)

type fixture struct {
	pool *pgxpool.Pool
	tx   *postgres.TxRunner
	ids  *id.Generator
	now  time.Time
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	ids, err := id.NewGenerator(0)
	if err != nil {
		t.Fatal(err)
	}
	pool := pgtest.New(t)
	return fixture{pool: pool, tx: postgres.NewTxRunner(pool), ids: ids, now: time.Now().UTC().Truncate(time.Microsecond)}
}

// samePurchase compares purchases field by field, using time.Equal for timestamps.
func samePurchase(a, b domain.Purchase) bool {
	return a.CreatedAt.Equal(b.CreatedAt) && a.SettledAt.Equal(b.SettledAt) && a.GrantedAt.Equal(b.GrantedAt) &&
		withoutTimes(a) == withoutTimes(b)
}

func withoutTimes(p domain.Purchase) domain.Purchase {
	p.CreatedAt, p.SettledAt, p.GrantedAt = time.Time{}, time.Time{}, time.Time{}
	return p
}

func (f fixture) insert(t *testing.T, userID, courseID id.ID, at time.Time) domain.Purchase {
	t.Helper()
	p, ev, err := domain.NewPurchase(f.ids.New(), userID, courseID, "Go", npr, "esewa", at)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.tx.RunInTx(ctx, func(r app.Repos) error {
		if err := r.Purchases.Insert(ctx, &p); err != nil {
			return err
		}
		return r.Events.Publish(ctx, ev)
	}); err != nil {
		t.Fatal(err)
	}
	return p
}

func (f fixture) update(t *testing.T, p *domain.Purchase) error {
	t.Helper()
	return f.tx.RunInTx(ctx, func(r app.Repos) error { return r.Purchases.Update(ctx, p) })
}

func (f fixture) find(t *testing.T, purchaseID id.ID) (domain.Purchase, bool) {
	t.Helper()
	var (
		p     domain.Purchase
		found bool
	)
	if err := f.tx.RunInTx(ctx, func(r app.Repos) error {
		var err error
		p, found, err = r.Purchases.Find(ctx, purchaseID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return p, found
}

func TestRoundTripAndVersionConflict(t *testing.T) {
	f := newFixture(t)
	p := f.insert(t, 200, 10, f.now)
	if p.Version != 1 {
		t.Fatalf("version = %d", p.Version)
	}
	got, found := f.find(t, p.ID)
	if !found || !samePurchase(got, p) {
		t.Fatalf("found=%v got=%+v want=%+v", found, got, p)
	}

	stale := p
	if _, err := p.MarkPaid("0001TS9", f.now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	p.MarkGranted(f.now.Add(2 * time.Minute))
	if err := f.update(t, &p); err != nil || p.Version != 2 {
		t.Fatalf("update: v=%d err=%v", p.Version, err)
	}
	if err := f.update(t, &stale); !errors.Is(err, app.ErrConcurrentModification) {
		t.Fatalf("stale err = %v", err)
	}
	got, _ = f.find(t, p.ID)
	if !samePurchase(got, p) {
		t.Fatalf("got=%+v want=%+v", got, p)
	}
	if _, found := f.find(t, 424242); found {
		t.Fatal("found a missing purchase")
	}

	var n int
	if err := f.pool.QueryRow(ctx, "SELECT count(*) FROM platform.outbox_messages WHERE payload->>'destination_topic' = 'payment.purchase.initiated'").Scan(&n); err != nil || n != 1 {
		t.Fatalf("outbox = %d err=%v", n, err)
	}
}

func TestGatewayRefIsUnique(t *testing.T) {
	f := newFixture(t)
	p := f.insert(t, 200, 10, f.now)
	dup := p
	dup.ID = f.ids.New()
	err := f.tx.RunInTx(ctx, func(r app.Repos) error { return r.Purchases.Insert(ctx, &dup) })
	if err == nil {
		t.Fatal("duplicate gateway reference accepted")
	}
}

func TestConstraintsRejectInconsistentRows(t *testing.T) {
	f := newFixture(t)
	p := f.insert(t, 200, 10, f.now)
	stmts := []string{
		"UPDATE payment.purchases SET status = 'paid', settled_at = now() WHERE id = $1",                      // paid without txn
		"UPDATE payment.purchases SET status = 'failed' WHERE id = $1",                                        // settled without settled_at
		"UPDATE payment.purchases SET granted_at = now() WHERE id = $1",                                       // granted while pending
		"UPDATE payment.purchases SET gateway_txn = 'T', status = 'failed', settled_at = now() WHERE id = $1", // failed with txn
	}
	for _, s := range stmts {
		if _, err := f.pool.Exec(ctx, s, int64(p.ID)); err == nil {
			t.Fatalf("accepted: %s", s)
		}
	}
}

func TestCountPaidAndListUnsettled(t *testing.T) {
	f := newFixture(t)
	old := f.insert(t, 200, 10, f.now.Add(-time.Hour))
	recent := f.insert(t, 201, 10, f.now)
	paidUngranted := f.insert(t, 202, 10, f.now)
	if _, err := paidUngranted.MarkPaid("T1", f.now); err != nil {
		t.Fatal(err)
	}
	if err := f.update(t, &paidUngranted); err != nil {
		t.Fatal(err)
	}
	paidGranted := f.insert(t, 202, 10, f.now.Add(-time.Hour))
	if _, err := paidGranted.MarkPaid("T2", f.now); err != nil {
		t.Fatal(err)
	}
	paidGranted.MarkGranted(f.now)
	if err := f.update(t, &paidGranted); err != nil {
		t.Fatal(err)
	}

	var (
		count int
		list  []domain.Purchase
		page2 []domain.Purchase
	)
	if err := f.tx.RunInTx(ctx, func(r app.Repos) error {
		var err error
		if count, err = r.Purchases.CountPaid(ctx, 202, 10); err != nil {
			return err
		}
		if list, err = r.Purchases.ListUnsettled(ctx, f.now.Add(-15*time.Minute), 0, 10); err != nil {
			return err
		}
		page2, err = r.Purchases.ListUnsettled(ctx, f.now.Add(-15*time.Minute), old.ID, 10)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("count = %d", count)
	}
	if len(list) != 2 || list[0].ID != old.ID || list[1].ID != paidUngranted.ID {
		t.Fatalf("list = %+v (recent %d)", list, recent.ID)
	}
	if len(page2) != 1 || page2[0].ID != paidUngranted.ID {
		t.Fatalf("page2 = %+v", page2)
	}
}

func (f fixture) insertManual(t *testing.T, userID, courseID id.ID) domain.Purchase {
	t.Helper()
	p, ev, err := domain.RecordManualPurchase(f.ids.New(), userID, courseID, "Go", npr,
		domain.ManualPayment{Method: domain.MethodBankTransfer, Reference: "V-1", Note: "n", RecordedBy: 1}, f.now)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.tx.RunInTx(ctx, func(r app.Repos) error {
		if err := r.Purchases.Insert(ctx, &p); err != nil {
			return err
		}
		return r.Events.Publish(ctx, ev)
	}); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestManualPurchaseRoundTrip(t *testing.T) {
	f := newFixture(t)
	p := f.insertManual(t, 200, 10)
	got, found := f.find(t, p.ID)
	if !found || !samePurchase(got, p) {
		t.Fatalf("got=%+v want=%+v", got, p)
	}
	esewa := f.insert(t, 200, 11, f.now)
	got, _ = f.find(t, esewa.ID)
	if got.CourseTitle != "Go" || got.ManualMethod != "" || got.RecordedBy != 0 || got.Note != "" {
		t.Fatalf("esewa row = %+v", got)
	}
	var paid int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM platform.outbox_messages
		WHERE payload->>'destination_topic' = 'payment.purchase.paid'`).Scan(&paid); err != nil || paid != 1 {
		t.Fatalf("paid events = %d err=%v", paid, err)
	}
}

func TestManualConstraints(t *testing.T) {
	f := newFixture(t)
	esewa := f.insert(t, 200, 10, f.now)
	manual := f.insertManual(t, 201, 10)
	cases := []struct {
		sql string
		id  id.ID
	}{
		{"UPDATE payment.purchases SET manual_method = 'cash', recorded_by = 1 WHERE id = $1", esewa.ID}, // method on a gateway purchase
		{"UPDATE payment.purchases SET manual_method = NULL WHERE id = $1", manual.ID},                   // manual without method
		{"UPDATE payment.purchases SET recorded_by = NULL WHERE id = $1", manual.ID},                     // manual without recorder
		{"UPDATE payment.purchases SET manual_method = 'cheque' WHERE id = $1", manual.ID},               // unknown method
	}
	for _, c := range cases {
		if _, err := f.pool.Exec(ctx, c.sql, int64(c.id)); err == nil {
			t.Fatalf("accepted: %s", c.sql)
		}
	}
}

func TestListByUser(t *testing.T) {
	f := newFixture(t)
	var mine []domain.Purchase
	for range 5 {
		mine = append(mine, f.insert(t, 200, 10, f.now))
	}
	f.insert(t, 201, 10, f.now)
	list := func(before id.ID, limit int) []domain.Purchase {
		t.Helper()
		var out []domain.Purchase
		if err := f.tx.RunInTx(ctx, func(r app.Repos) error {
			var err error
			out, err = r.Purchases.ListByUser(ctx, 200, before, limit)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return out
	}
	first := list(0, 3)
	if len(first) != 3 || first[0].ID != mine[4].ID || first[2].ID != mine[2].ID {
		t.Fatalf("first page = %v", first)
	}
	rest := list(first[2].ID, 3)
	if len(rest) != 2 || rest[0].ID != mine[1].ID || rest[1].ID != mine[0].ID {
		t.Fatalf("second page = %v", rest)
	}
	if got := list(0, 10); len(got) != 5 {
		t.Fatalf("all = %d", len(got))
	}
}

func TestMigrationBackfillsCourseTitle(t *testing.T) {
	f := newFixture(t)
	p, err := migrate.NewProvider(stdlib.OpenDBFromPool(f.pool))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.DownTo(ctx, 11); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO courseauthoring.courses (id, owner_id, title, status, version, created_at, updated_at)
		VALUES (77, 1, 'Backfilled', 'draft', 1, now(), now())`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO payment.purchases (id, user_id, course_id, amount_minor, currency, gateway,
		gateway_ref, status, created_at, version) VALUES (900, 200, 77, 100, 'NPR', 'esewa', '900', 'pending', now(), 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Up(ctx); err != nil {
		t.Fatal(err)
	}
	var title string
	if err := f.pool.QueryRow(ctx, "SELECT course_title FROM payment.purchases WHERE id = 900").Scan(&title); err != nil || title != "Backfilled" {
		t.Fatalf("title = %q err=%v", title, err)
	}
}
