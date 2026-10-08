//go:build integration

package postgres_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/certificate/adapters/postgres"
	"github.com/santoshkc2200/ioe-backend/internal/certificate/app"
	"github.com/santoshkc2200/ioe-backend/internal/certificate/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
	"github.com/santoshkc2200/ioe-backend/internal/platform/postgres/pgtest"
)

var (
	ctx = context.Background()
	t0  = time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC)
)

func newTx(t *testing.T) *postgres.TxRunner {
	t.Helper()
	return postgres.NewTxRunner(pgtest.New(t))
}

func issue(t *testing.T, tx *postgres.TxRunner, certID, userID, courseID id.ID) (domain.Certificate, bool) {
	t.Helper()
	c := domain.NewCertificate(certID, domain.NewCode(), userID, courseID, "Asha Rai", "Go", t0)
	var inserted bool
	if err := tx.RunInTx(ctx, func(r app.Repository) error {
		var err error
		inserted, err = r.Insert(ctx, c)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return c, inserted
}

func find(t *testing.T, tx *postgres.TxRunner, courseID, userID id.ID) (domain.Certificate, bool) {
	t.Helper()
	var (
		c  domain.Certificate
		ok bool
	)
	if err := tx.RunInTx(ctx, func(r app.Repository) error {
		var err error
		c, ok, err = r.FindValid(ctx, courseID, userID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return c, ok
}

func TestPolicyRoundTrip(t *testing.T) {
	tx := newTx(t)
	read := func() (domain.Policy, bool) {
		var (
			p  domain.Policy
			ok bool
		)
		if err := tx.RunInTx(ctx, func(r app.Repository) error {
			var err error
			p, ok, err = r.FindPolicy(ctx, 10)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return p, ok
	}
	if _, ok := read(); ok {
		t.Fatal("policy found before any write")
	}
	for _, want := range []domain.Policy{
		{CourseID: 10, Mode: domain.ModeCompletionAndExam, ExamID: 700},
		{CourseID: 10, Mode: domain.ModeCompletion},
		{CourseID: 10, Mode: domain.ModeOff},
	} {
		if err := tx.RunInTx(ctx, func(r app.Repository) error { return r.UpsertPolicy(ctx, want, t0) }); err != nil {
			t.Fatal(err)
		}
		if got, ok := read(); !ok || got != want {
			t.Fatalf("policy = %+v, %v; want %+v", got, ok, want)
		}
	}
}

func TestOneValidCertificatePerUserAndCourse(t *testing.T) {
	tx := newTx(t)
	first, ok := issue(t, tx, 1, 200, 10)
	if !ok {
		t.Fatal("first insert rejected")
	}
	if _, ok := issue(t, tx, 2, 200, 10); ok {
		t.Fatal("second valid certificate for the same user and course inserted")
	}
	if _, ok := issue(t, tx, 3, 201, 10); !ok {
		t.Fatal("another user rejected")
	}
	if _, ok := issue(t, tx, 4, 200, 11); !ok {
		t.Fatal("another course rejected")
	}
	got, found := find(t, tx, 10, 200)
	if !found || got.ID != first.ID || got.Code != first.Code || got.Revoked() || !got.IssuedAt.Equal(t0) {
		t.Fatalf("FindValid = %+v, %v", got, found)
	}
}

func TestRevokeThenReissue(t *testing.T) {
	tx := newTx(t)
	first, _ := issue(t, tx, 1, 200, 10)
	revokedAt := t0.Add(time.Hour)
	revoke := func() {
		if err := tx.RunInTx(ctx, func(r app.Repository) error { return r.RevokeValid(ctx, 10, 200, t0, revokedAt) }); err != nil {
			t.Fatal(err)
		}
	}
	revoke()
	revoke() // idempotent
	if _, ok := find(t, tx, 10, 200); ok {
		t.Fatal("revoked certificate still valid")
	}
	var byCode domain.Certificate
	if err := tx.RunInTx(ctx, func(r app.Repository) error {
		var ok bool
		var err error
		byCode, ok, err = r.FindByCode(ctx, first.Code)
		if err == nil && !ok {
			t.Error("revoked certificate not found by code")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if !byCode.Revoked() || !byCode.RevokedAt.Equal(revokedAt) {
		t.Fatalf("by code = %+v", byCode)
	}
	second, ok := issue(t, tx, 2, 200, 10)
	if !ok {
		t.Fatal("reissue after revoke rejected")
	}
	var list []domain.Certificate
	if err := tx.RunInTx(ctx, func(r app.Repository) error {
		var err error
		list, err = r.ListByUser(ctx, 200)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].ID != second.ID || list[1].ID != first.ID {
		t.Fatalf("list = %+v", list)
	}
	var unknown bool
	if err := tx.RunInTx(ctx, func(r app.Repository) error {
		var err error
		_, unknown, err = r.FindByCode(ctx, domain.NewCode())
		return err
	}); err != nil || unknown {
		t.Fatalf("unknown code: found=%v err=%v", unknown, err)
	}
}

func TestRevokeSparesCertificatesIssuedAfterTheRefund(t *testing.T) {
	tx := newTx(t)
	issue(t, tx, 1, 200, 10) // issued at t0
	revoke := func(issuedBy time.Time) {
		t.Helper()
		if err := tx.RunInTx(ctx, func(r app.Repository) error {
			return r.RevokeValid(ctx, 10, 200, issuedBy, t0.Add(time.Hour))
		}); err != nil {
			t.Fatal(err)
		}
	}
	revoke(t0.Add(-time.Minute))
	if _, ok := find(t, tx, 10, 200); !ok {
		t.Fatal("certificate issued after the refund was revoked")
	}
	revoke(t0) // the bound is inclusive
	if _, ok := find(t, tx, 10, 200); ok {
		t.Fatal("certificate issued at the refund instant still valid")
	}
}

func TestConcurrentClaimsConverge(t *testing.T) {
	tx := newTx(t)
	var inserted atomic.Int32
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := domain.NewCertificate(id.ID(100+i), domain.NewCode(), 200, 10, "Asha Rai", "Go", t0)
			_ = tx.RunInTx(ctx, func(r app.Repository) error {
				ok, err := r.Insert(ctx, c)
				if ok {
					inserted.Add(1)
				}
				return err
			})
		}()
	}
	wg.Wait()
	if n := inserted.Load(); n != 1 {
		t.Fatalf("%d certificates inserted, want 1", n)
	}
}
