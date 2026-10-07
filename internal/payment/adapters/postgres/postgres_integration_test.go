//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/santoshkc2200/ioe-backend/internal/payment/adapters/postgres"
	"github.com/santoshkc2200/ioe-backend/internal/payment/app"
	"github.com/santoshkc2200/ioe-backend/internal/payment/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
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
