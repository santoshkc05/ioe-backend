# Payment Refund Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A root admin records a full refund of a paid purchase; the buyer's enrollment is canceled (unless another paid purchase keeps it) and the buyer gets a refund email.

**Architecture:** `domain.Purchase` gains a `refunded` status and a `RevokedAt` stamp. `app.Service.Refund` commits the refund and its outbox event, then cancels the enrollment through the `EnrollmentGranter` port backed by a new `enrollment/app.Service.CancelPurchased`; failed cancels are retried by `Reconcile`, exactly like failed grants. The notification context sends the email from `payment.purchase.refunded`.

**Tech Stack:** Go, PostgreSQL, goose migrations, sqlc (pgx/v5), net/http, watermill outbox.

**Spec:** `docs/superpowers/specs/2026-10-08-payment-refund-design.md`

## Global Constraints

- A context never imports another context; cross-context calls go through ports wired in `cmd/api`.
- The outbox event is written in the same transaction as the state change.
- Root admin only; any other caller gets 404 `not_found`.
- Only `paid` purchases are refundable; always the full price; a repeat refund is 409 `not_refundable`.
- Refund reference: required, trimmed, at most 200 bytes. Refund note: optional, trimmed, at most 1000 bytes (same limits as manual purchases, `maxReferenceLen` / `maxNoteLen`).
- Enrollment cancel reason: `refunded`.
- Event name `payment.purchase.refunded`; email idempotency key `ioe:payment.purchase.refunded:<eventID>:refunded-v1`.
- Migration number `00015`.
- Use Conventional Commits. Gates before finishing: `make check`, `make test-integration`, `git diff --check`.

## Review Focus

1. Confirming (`POST /v1/purchases/{id}/confirm`) a refunded **manual** purchase must return 200, not 503 — test in Task 4 (`TestConfirmRefundedSkipsGateway`).
2. A late eSewa `COMPLETE` report on a refunded purchase must not flip it back to `paid` — test in Task 1 (`TestMarkPaidIgnoresRefunded`).
3. Refunding the second of two paid purchases must keep the student enrolled and never call `RevokePurchased` — test in Task 4 (`TestRefundKeepsAccessWhenAnotherPaid`).
4. Refunding a paid purchase whose grant never succeeded must stop future grants from the reconciler — test in Task 4 (`TestRefundOfUngrantedStopsGrant`).
5. A refunded row with an empty note must round-trip through Postgres (NULL note) and still pass `purchases_refund_consistent` — test in Task 2 (`TestRefundRoundTrip`).

Accepted residual risk (not tested): if `settle` loaded a purchase before the refund committed and its `GrantPurchased` call lands after the revoke, the student is enrolled again. Grants of long-paid purchases are rare; a manager can cancel the enrollment.

---

### Task 1: Payment domain — refund and revoke state

**Files:**
- Modify: `internal/payment/domain/purchase.go`
- Modify: `internal/payment/domain/events.go`
- Modify: `internal/payment/domain/errors.go`
- Test: `internal/payment/domain/purchase_test.go`

**Interfaces:**
- Produces:
  - `const StatusRefunded Status = "refunded"`
  - `type Refund struct { Reference, Note string; RefundedBy id.ID }`
  - `Purchase` fields `RefundedAt time.Time`, `RefundedBy id.ID`, `RefundReference string`, `RefundNote string`, `RevokedAt time.Time`
  - `func (p *Purchase) Refund(r Refund, otherPaid bool, now time.Time) (Event, error)`
  - `func (p *Purchase) NeedsRevoke() bool`, `func (p *Purchase) MarkRevoked(now time.Time)`, `func (p *Purchase) AwaitsGateway() bool`
  - `var ErrNotRefundable`
  - `type PurchaseRefunded struct` with `EventName() == "payment.purchase.refunded"`

- [ ] **Step 1: Write the failing tests**

Append to `internal/payment/domain/purchase_test.go`:

```go
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/payment/domain/`
Expected: FAIL to compile — `undefined: domain.Refund`, `domain.StatusRefunded`, `domain.ErrNotRefundable`.

- [ ] **Step 3: Implement**

In `internal/payment/domain/errors.go` add to the `var` block:

```go
	ErrNotRefundable   = errors.New("only a paid purchase can be refunded")
```

In `internal/payment/domain/purchase.go`:

Add the status constant:

```go
const (
	StatusPending  Status = "pending"
	StatusPaid     Status = "paid"
	StatusFailed   Status = "failed"
	StatusRefunded Status = "refunded"
)
```

Add the fields at the end of `Purchase`, before `Version`:

```go
	RefundedAt      time.Time // zero unless refunded
	RefundedBy      id.ID     // root admin who recorded the refund; zero unless refunded
	RefundReference string    // eSewa portal or bank reference of the refund
	RefundNote      string    // admin's remark on the refund; may be empty
	RevokedAt       time.Time // set once the refund's enrollment cancel succeeded or was not needed
```

Change `MarkPaid` so a refund is never undone:

```go
// MarkPaid records the gateway's confirmation. A failed purchase can still become paid when the
// gateway completes it late. Marking a paid or refunded purchase changes nothing and returns no event.
func (p *Purchase) MarkPaid(txn string, now time.Time) (Event, error) {
	if txn == "" {
		return nil, ErrInvalidPurchase
	}
	if p.Status == StatusPaid || p.Status == StatusRefunded {
		return nil, nil
	}
```

(leave the rest of `MarkPaid` unchanged.)

Append after `NeedsGrant`:

```go
// AwaitsGateway reports whether only the gateway can settle the purchase.
func (p *Purchase) AwaitsGateway() bool { return p.Status == StatusPending || p.Status == StatusFailed }

// Refund describes a refund a root admin records after paying the buyer back outside this system.
type Refund struct {
	Reference  string // eSewa portal or bank reference
	Note       string
	RefundedBy id.ID
}

// Refund records a full refund of a paid purchase. When otherPaid is true the buyer holds
// another paid purchase of the course, so access is kept and no revocation is needed.
func (p *Purchase) Refund(r Refund, otherPaid bool, now time.Time) (Event, error) {
	if p.Status != StatusPaid {
		return nil, ErrNotRefundable
	}
	ref, note := strings.TrimSpace(r.Reference), strings.TrimSpace(r.Note)
	if ref == "" || len(ref) > maxReferenceLen || len(note) > maxNoteLen || r.RefundedBy == 0 {
		return nil, ErrInvalidPurchase
	}
	p.Status, p.RefundedAt, p.RefundedBy, p.RefundReference, p.RefundNote = StatusRefunded, now, r.RefundedBy, ref, note
	if otherPaid {
		p.RevokedAt = now
	}
	return PurchaseRefunded{
		PurchaseID: p.ID, UserID: p.UserID, CourseID: p.CourseID, CourseTitle: p.CourseTitle,
		AmountMinor: p.Price.AmountMinor, Currency: p.Price.Currency, Gateway: p.Gateway,
		ManualMethod: p.ManualMethod, RefundReference: ref, AccessRevoked: !otherPaid, OccurredAt: now,
	}, nil
}

// NeedsRevoke reports whether the purchase is refunded but the buyer's enrollment is not yet canceled.
func (p *Purchase) NeedsRevoke() bool { return p.Status == StatusRefunded && p.RevokedAt.IsZero() }

// MarkRevoked records that the buyer's enrollment was canceled. It changes only a purchase that needs it.
func (p *Purchase) MarkRevoked(now time.Time) {
	if p.NeedsRevoke() {
		p.RevokedAt = now
	}
}
```

Append to `internal/payment/domain/events.go`:

```go
// PurchaseRefunded is emitted when a root admin records a full refund.
type PurchaseRefunded struct {
	PurchaseID      id.ID     `json:"purchase_id"`
	UserID          id.ID     `json:"user_id"`
	CourseID        id.ID     `json:"course_id"`
	CourseTitle     string    `json:"course_title"`
	AmountMinor     int64     `json:"amount_minor"`
	Currency        string    `json:"currency"`
	Gateway         string    `json:"gateway"`
	ManualMethod    string    `json:"manual_method"` // empty for gateway purchases
	RefundReference string    `json:"refund_reference"`
	AccessRevoked   bool      `json:"access_revoked"` // false when another paid purchase keeps access
	OccurredAt      time.Time `json:"occurred_at"`
}

func (PurchaseRefunded) EventName() string { return "payment.purchase.refunded" }
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/payment/domain/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/payment/domain
git commit -m "feat(payment): model full refunds on purchases"
```

---

### Task 2: Payment persistence — refund columns

**Files:**
- Create: `migrations/00015_payment_refund.sql`
- Modify: `sqlc.yaml` (payment `overrides`)
- Modify: `internal/payment/adapters/postgres/queries.sql`
- Regenerate: `internal/payment/adapters/postgres/sqlcgen/*`
- Modify: `internal/payment/adapters/postgres/purchases.go`
- Test: `internal/payment/adapters/postgres/postgres_integration_test.go`

**Interfaces:**
- Consumes: Task 1 fields and methods.
- Produces: `Repository.ListUnsettled` also returns refunded, unrevoked purchases; `Update` persists refund fields and `revoked_at`.

- [ ] **Step 1: Write the failing integration tests**

In `postgres_integration_test.go`, extend `samePurchase`/`withoutTimes` to cover the new timestamps:

```go
// samePurchase compares purchases field by field, using time.Equal for timestamps.
func samePurchase(a, b domain.Purchase) bool {
	return a.CreatedAt.Equal(b.CreatedAt) && a.SettledAt.Equal(b.SettledAt) && a.GrantedAt.Equal(b.GrantedAt) &&
		a.RefundedAt.Equal(b.RefundedAt) && a.RevokedAt.Equal(b.RevokedAt) && withoutTimes(a) == withoutTimes(b)
}

func withoutTimes(p domain.Purchase) domain.Purchase {
	p.CreatedAt, p.SettledAt, p.GrantedAt, p.RefundedAt, p.RevokedAt = time.Time{}, time.Time{}, time.Time{}, time.Time{}, time.Time{}
	return p
}
```

Add a helper and tests:

```go
func (f fixture) paid(t *testing.T, userID, courseID id.ID, txn string) domain.Purchase {
	t.Helper()
	p := f.insert(t, userID, courseID, f.now)
	if _, err := p.MarkPaid(txn, f.now); err != nil {
		t.Fatal(err)
	}
	p.MarkGranted(f.now)
	if err := f.update(t, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRefundRoundTrip(t *testing.T) {
	f := newFixture(t)
	p := f.paid(t, 200, 10, "T1")
	if _, err := p.Refund(domain.Refund{Reference: "RF-1", RefundedBy: 1}, false, f.now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := f.update(t, &p); err != nil {
		t.Fatal(err)
	}
	got, _ := f.find(t, p.ID)
	if !samePurchase(got, p) || got.RefundNote != "" || !got.RevokedAt.IsZero() {
		t.Fatalf("got=%+v want=%+v", got, p)
	}
	p.MarkRevoked(f.now.Add(2 * time.Minute))
	if err := f.update(t, &p); err != nil {
		t.Fatal(err)
	}
	if got, _ = f.find(t, p.ID); !samePurchase(got, p) {
		t.Fatalf("after revoke got=%+v want=%+v", got, p)
	}
}

func TestRefundConstraints(t *testing.T) {
	f := newFixture(t)
	p := f.paid(t, 200, 10, "T1")
	pending := f.insert(t, 201, 10, f.now)
	stmts := []struct {
		sql string
		id  id.ID
	}{
		{"UPDATE payment.purchases SET status = 'refunded' WHERE id = $1", p.ID},                                // refunded without refund fields
		{"UPDATE payment.purchases SET refunded_at = now(), refunded_by = 1, refund_reference = 'r' WHERE id = $1", p.ID}, // refund fields while paid
		{"UPDATE payment.purchases SET revoked_at = now() WHERE id = $1", p.ID},                                 // revoked while paid
		{"UPDATE payment.purchases SET status = 'refunded', refunded_at = now(), refunded_by = 1, refund_reference = 'r' WHERE id = $1", pending.ID}, // refunded without txn
	}
	for _, s := range stmts {
		if _, err := f.pool.Exec(ctx, s.sql, int64(s.id)); err == nil {
			t.Fatalf("accepted: %s", s.sql)
		}
	}
}

func TestListUnsettledReturnsUnrevokedRefunds(t *testing.T) {
	f := newFixture(t)
	unrevoked := f.paid(t, 200, 10, "T1")
	if _, err := unrevoked.Refund(domain.Refund{Reference: "RF-1", RefundedBy: 1}, false, f.now); err != nil {
		t.Fatal(err)
	}
	if err := f.update(t, &unrevoked); err != nil {
		t.Fatal(err)
	}
	revoked := f.paid(t, 201, 10, "T2")
	if _, err := revoked.Refund(domain.Refund{Reference: "RF-2", RefundedBy: 1}, true, f.now); err != nil {
		t.Fatal(err)
	}
	if err := f.update(t, &revoked); err != nil {
		t.Fatal(err)
	}
	var (
		list  []domain.Purchase
		count int
	)
	if err := f.tx.RunInTx(ctx, func(r app.Repos) error {
		var err error
		if list, err = r.Purchases.ListUnsettled(ctx, f.now.Add(-15*time.Minute), 0, 10); err != nil {
			return err
		}
		count, err = r.Purchases.CountPaid(ctx, 200, 10)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != unrevoked.ID || count != 0 {
		t.Fatalf("list=%+v count=%d", list, count)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -tags integration -run 'TestRefund|TestListUnsettledReturnsUnrevokedRefunds' ./internal/payment/adapters/postgres/`
Expected: FAIL — `UpdatePurchase` does not persist refund fields; `status` check rejects `refunded`.

- [ ] **Step 3: Write the migration**

Create `migrations/00015_payment_refund.sql`. First confirm the inline status check name:

Run: `docker compose exec postgres psql -U postgres -c "\d payment.purchases"` (or read it in an integration test DB). Expected constraint name `purchases_status_check`. If it differs, use the actual name below.

```sql
-- +goose Up
ALTER TABLE payment.purchases
  ADD COLUMN refunded_at      timestamptz,
  ADD COLUMN refunded_by      bigint,
  ADD COLUMN refund_reference text,
  ADD COLUMN refund_note      text,
  ADD COLUMN revoked_at       timestamptz,
  DROP CONSTRAINT purchases_status_check,
  ADD CONSTRAINT purchases_status_check CHECK (status IN ('pending', 'paid', 'failed', 'refunded')),
  DROP CONSTRAINT purchases_paid_txn,
  ADD CONSTRAINT purchases_paid_txn CHECK ((status IN ('paid', 'refunded')) = (gateway_txn <> '')),
  DROP CONSTRAINT purchases_granted_paid,
  ADD CONSTRAINT purchases_granted_paid CHECK (granted_at IS NULL OR status IN ('paid', 'refunded')),
  ADD CONSTRAINT purchases_refund_consistent CHECK (
    (status = 'refunded') = (refunded_at IS NOT NULL AND refunded_by IS NOT NULL AND refund_reference IS NOT NULL)),
  ADD CONSTRAINT purchases_revoked_refunded CHECK (revoked_at IS NULL OR status = 'refunded');

DROP INDEX payment.purchases_unsettled;
CREATE INDEX purchases_unsettled ON payment.purchases (id)
  WHERE status = 'pending' OR (status = 'paid' AND granted_at IS NULL)
     OR (status = 'refunded' AND revoked_at IS NULL);

-- +goose Down
-- Fails while refunded rows exist, so refund records are never silently dropped.
DROP INDEX payment.purchases_unsettled;
CREATE INDEX purchases_unsettled ON payment.purchases (id)
  WHERE status = 'pending' OR (status = 'paid' AND granted_at IS NULL);

ALTER TABLE payment.purchases
  DROP CONSTRAINT purchases_revoked_refunded,
  DROP CONSTRAINT purchases_refund_consistent,
  DROP CONSTRAINT purchases_granted_paid,
  ADD CONSTRAINT purchases_granted_paid CHECK (granted_at IS NULL OR status = 'paid'),
  DROP CONSTRAINT purchases_paid_txn,
  ADD CONSTRAINT purchases_paid_txn CHECK ((status = 'paid') = (gateway_txn <> '')),
  DROP CONSTRAINT purchases_status_check,
  ADD CONSTRAINT purchases_status_check CHECK (status IN ('pending', 'paid', 'failed')),
  DROP COLUMN revoked_at,
  DROP COLUMN refund_note,
  DROP COLUMN refund_reference,
  DROP COLUMN refunded_by,
  DROP COLUMN refunded_at;
```

- [ ] **Step 4: Update queries and sqlc overrides**

In `sqlc.yaml`, under the payment `overrides`, after `payment.purchases.recorded_by`, add:

```yaml
          - column: payment.purchases.refunded_by
            go_type:
              type: int64
              pointer: true
          - column: payment.purchases.refund_reference
            go_type:
              type: string
              pointer: true
          - column: payment.purchases.refund_note
            go_type:
              type: string
              pointer: true
```

In `internal/payment/adapters/postgres/queries.sql` replace `ListUnsettledPurchases` and `UpdatePurchase`:

```sql
-- name: ListUnsettledPurchases :many
SELECT * FROM payment.purchases
WHERE id > sqlc.arg(after_id)::bigint
  AND ((status = 'pending' AND created_at < sqlc.arg(pending_before)::timestamptz)
       OR (status = 'paid' AND granted_at IS NULL)
       OR (status = 'refunded' AND revoked_at IS NULL))
ORDER BY id
LIMIT sqlc.arg(page_limit)::bigint;
```

```sql
-- name: UpdatePurchase :execrows
UPDATE payment.purchases
SET gateway_txn = $3, status = $4, settled_at = $5, granted_at = $6,
    refunded_at = $7, refunded_by = $8, refund_reference = $9, refund_note = $10, revoked_at = $11,
    version = version + 1
WHERE id = $1 AND version = $2;
```

Run: `sqlc generate`
Expected: `sqlcgen.PaymentPurchase` and `UpdatePurchaseParams` gain `RefundedAt *time.Time`, `RefundedBy *int64`, `RefundReference *string`, `RefundNote *string`, `RevokedAt *time.Time`.

- [ ] **Step 5: Map the new columns**

In `internal/payment/adapters/postgres/purchases.go`, `Update`:

```go
func (r purchases) Update(ctx context.Context, p *domain.Purchase) error {
	n, err := r.q.UpdatePurchase(ctx, sqlcgen.UpdatePurchaseParams{
		ID: int64(p.ID), Version: p.Version, GatewayTxn: p.GatewayTxn, Status: string(p.Status),
		SettledAt: optionalTime(p.SettledAt), GrantedAt: optionalTime(p.GrantedAt),
		RefundedAt: optionalTime(p.RefundedAt), RefundedBy: optionalID(p.RefundedBy),
		RefundReference: optionalString(p.RefundReference), RefundNote: optionalString(p.RefundNote),
		RevokedAt: optionalTime(p.RevokedAt),
	})
```

(rest unchanged). An empty refund note is stored as NULL, like other absent optional strings.

In `toDomain`, before `return p`:

```go
	if r.RefundedAt != nil {
		p.RefundedAt = r.RefundedAt.UTC()
	}
	if r.RefundedBy != nil {
		p.RefundedBy = id.ID(*r.RefundedBy)
	}
	if r.RefundReference != nil {
		p.RefundReference = *r.RefundReference
	}
	if r.RefundNote != nil {
		p.RefundNote = *r.RefundNote
	}
	if r.RevokedAt != nil {
		p.RevokedAt = r.RevokedAt.UTC()
	}
```

`InsertPurchase` stays as is: new purchases are never refunded.

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test -tags integration ./internal/payment/adapters/postgres/`
Expected: PASS (all existing tests too).

- [ ] **Step 7: Commit**

```bash
git add migrations/00015_payment_refund.sql sqlc.yaml internal/payment/adapters/postgres
git commit -m "feat(payment): persist refunds and pending revocations"
```

---

### Task 3: Enrollment — cancel after refund

**Files:**
- Modify: `internal/enrollment/app/service.go:99-140`
- Test: `internal/enrollment/app/service_test.go`

**Interfaces:**
- Produces: `func (s *Service) CancelPurchased(ctx context.Context, courseID, userID id.ID) error` and `const RefundedReason = "refunded"` in `internal/enrollment/app`.

- [ ] **Step 1: Write the failing test**

Append to `internal/enrollment/app/service_test.go`:

```go
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
```

Add a `find` helper to `memStore` in `internal/enrollment/app/fakes_test.go` (it has none today):

```go
func (m *memStore) find(courseID, userID id.ID) (domain.Enrollment, bool, error) {
	var (
		e     domain.Enrollment
		found bool
	)
	err := m.RunInTx(context.Background(), func(r app.Repos) error {
		var err error
		e, found, err = r.Enrollments.FindByCourseAndUser(context.Background(), courseID, userID)
		return err
	})
	return e, found, err
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/enrollment/app/ -run TestCancelPurchased`
Expected: FAIL to compile — `f.svc.CancelPurchased undefined`.

- [ ] **Step 3: Implement**

In `internal/enrollment/app/service.go`, split `Cancel` so the transaction is shared:

```go
// RefundedReason is the cancel reason of an enrollment ended by a refund.
const RefundedReason = "refunded"

// Cancel ends userID's enrollment. The enrolled user may always cancel their own;
// anyone else must manage the course.
func (s *Service) Cancel(ctx context.Context, p auth.Principal, courseID, userID id.ID, reason string) (domain.Enrollment, error) {
	if p.UserID != userID {
		c, err := s.courses.CourseFacts(ctx, courseID)
		if err != nil {
			return domain.Enrollment{}, err
		}
		if err := domain.AuthorizeManage(p, c); err != nil {
			return domain.Enrollment{}, err
		}
	}
	return s.cancel(ctx, courseID, userID, reason)
}

// CancelPurchased ends userID's enrollment after the payment context recorded a refund. It
// applies no principal check. A missing or already canceled enrollment is success.
func (s *Service) CancelPurchased(ctx context.Context, courseID, userID id.ID) error {
	_, err := s.cancel(ctx, courseID, userID, RefundedReason)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

func (s *Service) cancel(ctx context.Context, courseID, userID id.ID, reason string) (domain.Enrollment, error) {
	var e domain.Enrollment
	err := s.tx.RunInTx(ctx, func(r Repos) error {
		// body moved unchanged from the old Cancel
	})
	if err != nil {
		return domain.Enrollment{}, err
	}
	return e, nil
}
```

Move the existing transaction body (from `var e domain.Enrollment` through `return e, nil`) into `cancel` verbatim; do not change it.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/enrollment/...`
Expected: PASS (including existing `TestCancel`).

- [ ] **Step 5: Commit**

```bash
git add internal/enrollment/app
git commit -m "feat(enrollment): cancel an enrollment after a refund"
```

---

### Task 4: Payment app — Refund use case, revocation and reconcile

**Files:**
- Modify: `internal/payment/app/ports.go`
- Modify: `internal/payment/app/errors.go`
- Modify: `internal/payment/app/service.go`
- Modify: `internal/payment/app/fakes_test.go`
- Test: `internal/payment/app/service_test.go`
- Modify: `cmd/api/payment.go` (port adapter)

**Interfaces:**
- Consumes: Task 1 domain API; Task 3 `enrollmentapp.Service.CancelPurchased`.
- Produces:
  - `EnrollmentGranter.RevokePurchased(ctx context.Context, courseID, userID id.ID) error`
  - `type RefundInput struct { Reference, Note string }`
  - `func (s *Service) Refund(ctx context.Context, p auth.Principal, purchaseID id.ID, in RefundInput) (domain.Purchase, error)`
  - `var ErrNotRefundable`

- [ ] **Step 1: Update fakes**

In `internal/payment/app/fakes_test.go`:

`ListUnsettled` — include unrevoked refunds:

```go
		if p.ID > afterID && (stale || p.NeedsGrant() || p.NeedsRevoke()) {
```

`fakeEnrollments` — add revoke tracking:

```go
type fakeEnrollments struct {
	enrolled  map[[2]id.ID]bool // course, user
	grantErr  error
	grants    int
	revokeErr error
	revokes   int
}

func (e *fakeEnrollments) RevokePurchased(_ context.Context, courseID, userID id.ID) error {
	e.revokes++
	if e.revokeErr != nil {
		return e.revokeErr
	}
	delete(e.enrolled, [2]id.ID{courseID, userID})
	return nil
}
```

- [ ] **Step 2: Write the failing tests**

Append to `internal/payment/app/service_test.go`:

```go
var refundIn = app.RefundInput{Reference: "RF-1", Note: "duplicate"}

// paidPurchase checks out as student and confirms through the fake gateway.
func (f fixture) paidPurchase(t *testing.T, txn string) domain.Purchase {
	t.Helper()
	p := f.checkout(t, student)
	f.complete(p, txn)
	p, err := f.svc.Confirm(ctx, student, p.ID)
	if err != nil || p.Status != domain.StatusPaid {
		t.Fatalf("confirm p=%+v err=%v", p, err)
	}
	return p
}

func TestRefundRevokesEnrollment(t *testing.T) {
	f := newFixture(t)
	p := f.paidPurchase(t, "T1")
	f.store.published = nil
	got, err := f.svc.Refund(ctx, admin, p.ID, refundIn)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.StatusRefunded || got.RefundedBy != admin.UserID || got.RefundReference != "RF-1" ||
		got.RevokedAt.IsZero() || f.enroll.revokes != 1 || f.enroll.enrolled[[2]id.ID{paidCourse, student.UserID}] {
		t.Fatalf("p=%+v revokes=%d", got, f.enroll.revokes)
	}
	if stored := f.store.get(p.ID); stored != got {
		t.Fatalf("stored=%+v returned=%+v", stored, got)
	}
	if names := eventNames(f.store.published); len(names) != 1 || names[0] != "payment.purchase.refunded" {
		t.Fatalf("events = %v", names)
	}
}

func TestRefundRejects(t *testing.T) {
	f := newFixture(t)
	paid := f.paidPurchase(t, "T1")
	pending := f.checkout(t, other)
	cases := []struct {
		name string
		who  auth.Principal
		id   id.ID
		in   app.RefundInput
		want error
	}{
		{"buyer", student, paid.ID, refundIn, app.ErrNotFound},
		{"missing", admin, 999, refundIn, app.ErrNotFound},
		{"pending", admin, pending.ID, refundIn, app.ErrNotRefundable},
		{"blank reference", admin, paid.ID, app.RefundInput{Reference: " "}, app.ErrInvalidInput},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := f.svc.Refund(ctx, c.who, c.id, c.in); !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
		})
	}
	if f.store.get(paid.ID).Status != domain.StatusPaid || f.enroll.revokes != 0 {
		t.Fatal("rejected refund changed state")
	}
	if _, err := f.svc.Refund(ctx, admin, paid.ID, refundIn); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Refund(ctx, admin, paid.ID, refundIn); !errors.Is(err, app.ErrNotRefundable) {
		t.Fatalf("repeat err = %v", err)
	}
}

func TestRefundKeepsAccessWhenAnotherPaid(t *testing.T) {
	f := newFixture(t)
	first := f.checkout(t, student)
	second := f.checkout(t, student)
	f.complete(first, "T1")
	f.complete(second, "T2")
	for _, p := range []domain.Purchase{first, second} {
		if _, err := f.svc.Confirm(ctx, student, p.ID); err != nil {
			t.Fatal(err)
		}
	}
	got, err := f.svc.Refund(ctx, admin, second.ID, refundIn)
	if err != nil {
		t.Fatal(err)
	}
	if got.NeedsRevoke() || f.enroll.revokes != 0 || !f.enroll.enrolled[[2]id.ID{paidCourse, student.UserID}] {
		t.Fatalf("p=%+v revokes=%d", got, f.enroll.revokes)
	}
	ev := f.store.published[len(f.store.published)-1].(domain.PurchaseRefunded)
	if ev.AccessRevoked {
		t.Fatalf("event = %+v", ev)
	}
}

func TestRevokeFailureIsReconciled(t *testing.T) {
	f := newFixture(t)
	p := f.paidPurchase(t, "T1")
	f.enroll.revokeErr = errors.New("db down")
	got, err := f.svc.Refund(ctx, admin, p.ID, refundIn)
	if err != nil || !got.NeedsRevoke() || !strings.Contains(f.logs.String(), "enrollment revoke failed") {
		t.Fatalf("p=%+v err=%v logs=%s", got, err, f.logs)
	}
	f.enroll.revokeErr = nil
	statusCalls := f.gw.statusCalls
	if err := f.svc.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if f.store.get(p.ID).NeedsRevoke() || f.enroll.revokes != 2 || f.gw.statusCalls != statusCalls {
		t.Fatalf("p=%+v revokes=%d statusCalls=%d", f.store.get(p.ID), f.enroll.revokes, f.gw.statusCalls)
	}
}

func TestRefundOfUngrantedStopsGrant(t *testing.T) {
	f := newFixture(t)
	f.enroll.grantErr = errors.New("db down")
	p := f.paidPurchase(t, "T1")
	if !f.store.get(p.ID).NeedsGrant() {
		t.Fatal("expected ungranted purchase")
	}
	if _, err := f.svc.Refund(ctx, admin, p.ID, refundIn); err != nil {
		t.Fatal(err)
	}
	f.enroll.grantErr = nil
	grants := f.enroll.grants
	if err := f.svc.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if f.enroll.grants != grants {
		t.Fatalf("grants = %d, want %d", f.enroll.grants, grants)
	}
}

func TestConfirmRefundedSkipsGateway(t *testing.T) {
	f := newFixture(t)
	manual, err := f.svc.RecordManual(ctx, admin, app.ManualInput{UserID: student.UserID, CourseID: paidCourse,
		AmountMinor: 100000, Currency: "NPR", Method: domain.MethodCash, Reference: "R-1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Refund(ctx, admin, manual.ID, refundIn); err != nil {
		t.Fatal(err)
	}
	got, err := f.svc.Confirm(ctx, student, manual.ID)
	if err != nil || got.Status != domain.StatusRefunded || f.gw.statusCalls != 0 {
		t.Fatalf("p=%+v err=%v statusCalls=%d", got, err, f.gw.statusCalls)
	}
}

func TestRefundRetriesStaleVersion(t *testing.T) {
	f := newFixture(t)
	p := f.paidPurchase(t, "T1")
	f.store.conflicts = 1
	if got, err := f.svc.Refund(ctx, admin, p.ID, refundIn); err != nil || got.Status != domain.StatusRefunded {
		t.Fatalf("p=%+v err=%v", got, err)
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/payment/app/`
Expected: FAIL to compile — `app.RefundInput`, `f.svc.Refund`, `app.ErrNotRefundable` undefined.

- [ ] **Step 4: Implement**

`internal/payment/app/errors.go` — add:

```go
	ErrNotRefundable          = errors.New("purchase is not refundable")
```

`internal/payment/app/ports.go` — extend `EnrollmentGranter`:

```go
// EnrollmentGranter is backed by the enrollment context.
type EnrollmentGranter interface {
	IsEnrolled(ctx context.Context, courseID, userID id.ID) (bool, error)
	// GrantPurchased actively enrolls the user. It is idempotent.
	GrantPurchased(ctx context.Context, courseID, userID id.ID) error
	// RevokePurchased cancels the user's enrollment after a refund. A missing or already
	// canceled enrollment is success.
	RevokePurchased(ctx context.Context, courseID, userID id.ID) error
}
```

Also update the `ListUnsettled` doc comment:

```go
	// ListUnsettled returns purchases with an ID above afterID that are pending and created
	// before pendingBefore, paid and not yet granted, or refunded and not yet revoked, in ID order.
```

`internal/payment/app/service.go`:

In `settle`, replace `if p.Status != domain.StatusPaid {` with:

```go
	if p.AwaitsGateway() {
```

In `Reconcile`, inside `for _, p := range page {` after the `ctx.Err()` check, before `settled, err := s.settle(ctx, p)`:

```go
			if p.Status == domain.StatusRefunded {
				if _, err := s.revoke(ctx, p); err != nil {
					s.logger.WarnContext(ctx, "purchase reconcile failed", "purchase_id", p.ID, "error", err)
				}
				continue
			}
```

Update the `Reconcile` doc comment: "…and retries every failed grant and every failed revocation."

Append the use case:

```go
// RefundInput is a full refund a root admin records after paying the buyer back.
type RefundInput struct {
	Reference string
	Note      string
}

// Refund records a full refund of a paid purchase and cancels the buyer's enrollment, unless
// the buyer holds another paid purchase of the course. Only a root admin may refund; anyone
// else gets ErrNotFound.
func (s *Service) Refund(ctx context.Context, p auth.Principal, purchaseID id.ID, in RefundInput) (domain.Purchase, error) {
	if p.Role != auth.RoleRootAdmin {
		return domain.Purchase{}, ErrNotFound
	}
	r := domain.Refund{Reference: in.Reference, Note: in.Note, RefundedBy: p.UserID}
	purchase, err := s.refundOnce(ctx, purchaseID, r)
	if errors.Is(err, ErrConcurrentModification) {
		purchase, err = s.refundOnce(ctx, purchaseID, r)
	}
	if err != nil {
		return domain.Purchase{}, err
	}
	return s.revoke(ctx, purchase)
}

func (s *Service) refundOnce(ctx context.Context, purchaseID id.ID, refund domain.Refund) (domain.Purchase, error) {
	var p domain.Purchase
	err := s.tx.RunInTx(ctx, func(r Repos) error {
		var (
			found bool
			err   error
		)
		p, found, err = r.Purchases.Find(ctx, purchaseID)
		if err != nil {
			return err
		}
		if !found {
			return ErrNotFound
		}
		paid, err := r.Purchases.CountPaid(ctx, p.UserID, p.CourseID)
		if err != nil {
			return err
		}
		// p itself is still paid, so another paid purchase makes the count exceed one.
		ev, err := p.Refund(refund, paid > 1, s.clock.Now())
		switch {
		case errors.Is(err, domain.ErrNotRefundable):
			return fmt.Errorf("%w: %w", ErrNotRefundable, err)
		case errors.Is(err, domain.ErrInvalidPurchase):
			return fmt.Errorf("%w: reference is required (at most 200 characters) and note is at most 1000 characters", ErrInvalidInput)
		case err != nil:
			return err
		}
		if err := r.Purchases.Update(ctx, &p); err != nil {
			return err
		}
		return r.Events.Publish(ctx, ev)
	})
	return p, err
}

// revoke cancels the enrollment of a refunded purchase that still needs it. A failed cancel is
// logged and left for the next reconcile pass.
func (s *Service) revoke(ctx context.Context, p domain.Purchase) (domain.Purchase, error) {
	if !p.NeedsRevoke() {
		return p, nil
	}
	if err := s.enroll.RevokePurchased(ctx, p.CourseID, p.UserID); err != nil {
		s.logger.ErrorContext(ctx, "enrollment revoke failed", "purchase_id", p.ID, "error", err)
		return p, nil
	}
	return s.update(ctx, p.ID, func(cur *domain.Purchase) (domain.Event, error) {
		cur.MarkRevoked(s.clock.Now())
		return nil, nil
	})
}
```

`cmd/api/payment.go` — add to `paymentEnrollments`:

```go
func (e paymentEnrollments) RevokePurchased(ctx context.Context, courseID, userID id.ID) error {
	return e.svc.CancelPurchased(ctx, courseID, userID)
}
```

Update its comment to `// paymentEnrollments lets payment check, grant and revoke enrollment.`

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/payment/... ./cmd/api/ && go vet ./...`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/payment/app cmd/api/payment.go
git commit -m "feat(payment): refund purchases and revoke enrollment"
```

---

### Task 5: Payment HTTP — refund endpoint and purchase fields

**Files:**
- Modify: `internal/payment/adapters/httpapi/httpapi.go`
- Modify: `internal/payment/adapters/httpapi/wire.go`
- Test: `internal/payment/adapters/httpapi/httpapi_test.go`
- Modify: `api/openapi.yaml`

**Interfaces:**
- Consumes: Task 4 `Service.Refund`, `app.RefundInput`, `app.ErrNotRefundable`.
- Produces: `POST /v1/purchases/{purchaseID}/refund`; purchase JSON gains `refunded_at`, `refunded_by`, `refund_reference`, `refund_note`, `revoke_pending`.

- [ ] **Step 1: Write the failing tests**

In `httpapi_test.go`:

Add to `stub`: field `refund app.RefundInput` and method:

```go
func (s *stub) Refund(_ context.Context, p auth.Principal, purchaseID id.ID, in app.RefundInput) (domain.Purchase, error) {
	s.principal, s.purchaseID, s.refund = p, purchaseID, in
	return s.purchase, s.err
}
```

In `TestCheckoutStatusAndShape`, extend `want` with the new keys:

```go
		"course_title": "", "manual_method": nil, "reference": nil, "note": nil, "recorded_by": nil,
		"refunded_at": nil, "refunded_by": nil, "refund_reference": nil, "refund_note": nil, "revoke_pending": false}
```

In `TestErrorMapping` add the case:

```go
		{fmt.Errorf("%w: status paid", app.ErrNotRefundable), http.StatusConflict, "not_refundable"},
```

Append:

```go
func TestRefund(t *testing.T) {
	refunded := domain.Purchase{ID: 9, UserID: 200, CourseID: 11, Price: domain.Money{AmountMinor: 150000, Currency: "NPR"},
		Gateway: "esewa", GatewayRef: "9", GatewayTxn: "T", Status: domain.StatusRefunded, CreatedAt: t0, SettledAt: t0,
		GrantedAt: t0, RefundedAt: t0.Add(time.Hour), RefundedBy: 1, RefundReference: "RF-1"}
	s := &stub{purchase: refunded}
	code, body := call(newServer(s), http.MethodPost, "/v1/purchases/9/refund", `{"reference":"RF-1","note":"dup"}`)
	if code != http.StatusOK || s.purchaseID != 9 || s.refund != (app.RefundInput{Reference: "RF-1", Note: "dup"}) {
		t.Fatalf("code=%d stub=%+v body=%s", code, s, body)
	}
	p := decode[map[string]any](t, body)["purchase"].(map[string]any)
	if p["status"] != "refunded" || p["refunded_at"] != "2026-10-07T01:00:00Z" || p["refunded_by"] != "1" ||
		p["refund_reference"] != "RF-1" || p["refund_note"] != "" || p["revoke_pending"] != true {
		t.Fatalf("purchase = %v", p)
	}
	for _, b := range []string{`{"reference":"r","extra":1}`, `not json`} {
		if code, _ := call(newServer(&stub{}), http.MethodPost, "/v1/purchases/9/refund", b); code != http.StatusBadRequest {
			t.Fatalf("%s: %d", b, code)
		}
	}
	if code, _ := call(newServer(&stub{}), http.MethodPost, "/v1/purchases/x/refund", `{"reference":"r"}`); code != http.StatusNotFound {
		t.Fatalf("malformed id: %d", code)
	}
}
```

If `DecodeJSON` answers 415 rather than 400 for `not json` with a JSON content type, keep only the `extra` case and check `TestCheckoutRequiresJSON` for the established behavior.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/payment/adapters/httpapi/`
Expected: FAIL — route 404/405, missing keys, `not_refundable` maps to 500.

- [ ] **Step 3: Implement**

`httpapi.go`:

Add to the `Service` interface:

```go
	Refund(ctx context.Context, p auth.Principal, purchaseID id.ID, in app.RefundInput) (domain.Purchase, error)
```

Register the route after `recordManual`:

```go
	r.Handle("POST /v1/purchases/{purchaseID}/refund", a(h.refund))
```

Add the handler after `recordManual`:

```go
func (h *Handler) refund(w http.ResponseWriter, r *http.Request) {
	purchaseID, ok := pathID(w, r, "purchaseID")
	if !ok {
		return
	}
	var req refundRequest
	if !httpserver.DecodeJSON(w, r, &req) {
		return
	}
	p, err := h.svc.Refund(r.Context(), principal(r), purchaseID, app.RefundInput{Reference: req.Reference, Note: req.Note})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, purchaseResponse{Purchase: toWire(p)})
}
```

Add to `errorMappings`:

```go
	{app.ErrNotRefundable, http.StatusConflict, "not_refundable", "Not Refundable"},
```

`wire.go`:

Extend `purchaseWire` after `RecordedBy`:

```go
	RefundedAt      *time.Time `json:"refunded_at"`      // null unless refunded
	RefundedBy      *id.ID     `json:"refunded_by"`      // null unless refunded
	RefundReference *string    `json:"refund_reference"` // null unless refunded
	RefundNote      *string    `json:"refund_note"`      // null unless refunded
	RevokePending   bool       `json:"revoke_pending"`   // refunded, enrollment cancel not done yet
```

In `toWire`, set `RevokePending: p.NeedsRevoke()` in the literal and add before `return w`:

```go
	if p.Status == domain.StatusRefunded {
		at, by, ref, note := p.RefundedAt, p.RefundedBy, p.RefundReference, p.RefundNote
		w.RefundedAt, w.RefundedBy, w.RefundReference, w.RefundNote = &at, &by, &ref, &note
	}
```

Add the request type:

```go
type refundRequest struct {
	Reference string `json:"reference"`
	Note      string `json:"note"`
}
```

- [ ] **Step 4: Document in OpenAPI**

In `api/openapi.yaml`, after the `/v1/purchases/{purchaseID}:` path block add:

```yaml
  /v1/purchases/{purchaseID}/refund:
    post:
      summary: Record a full refund
      description: >
        Root admin only; anyone else gets 404. Records that a paid purchase was refunded in full
        outside this system (eSewa merchant portal or bank) and cancels the buyer's enrollment with
        reason `refunded`, unless the buyer holds another paid purchase of the course. Sends the
        refund email. 409 `not_refundable` when the purchase is not `paid`, including one already
        refunded.
      security:
        - bearer: []
      parameters:
        - $ref: "#/components/parameters/PurchaseID"
      requestBody:
        required: true
        content:
          application/json:
            schema: { $ref: "#/components/schemas/RefundRequest" }
      responses:
        "200":
          description: The refunded purchase
          content:
            application/json:
              schema: { $ref: "#/components/schemas/PurchaseResponse" }
        "400": { $ref: "#/components/responses/Problem" }
        "401": { $ref: "#/components/responses/Problem" }
        "404": { $ref: "#/components/responses/Problem" }
        "409": { $ref: "#/components/responses/Problem" }
        "413": { $ref: "#/components/responses/Problem" }
        "415": { $ref: "#/components/responses/Problem" }
        "405": { $ref: "#/components/responses/MethodNotAllowed" }
        "500": { $ref: "#/components/responses/InternalError" }
```

In `components.schemas.Purchase`: append `refunded_at, refunded_by, refund_reference, refund_note, revoke_pending` to `required`, change `status` to `enum: [pending, paid, failed, refunded]`, and add after `recorded_by`:

```yaml
        refunded_at: { type: [string, "null"], format: date-time, description: Null unless refunded }
        refunded_by:
          oneOf: [{ $ref: "#/components/schemas/ID" }, { type: "null" }]
          description: Root admin who recorded the refund
        refund_reference: { type: [string, "null"], description: eSewa portal or bank reference of the refund }
        refund_note: { type: [string, "null"] }
        revoke_pending: { type: boolean, description: True while a refunded purchase's enrollment cancel has not succeeded yet }
```

After `ManualPurchaseRequest` add:

```yaml
    RefundRequest:
      type: object
      additionalProperties: false
      required: [reference]
      properties:
        reference: { type: string, minLength: 1, maxLength: 200 }
        note: { type: string, maxLength: 1000 }
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/payment/...`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/payment/adapters/httpapi api/openapi.yaml
git commit -m "feat(payment): expose purchase refunds over HTTP"
```

---

### Task 6: Notification — refund email

**Files:**
- Create: `internal/notification/app/purchase_refunded.go`
- Create: `internal/notification/app/purchase_refunded_test.go`
- Modify: `internal/notification/app/service.go` (`Renderer`)
- Modify: `internal/notification/app/service_test.go` (`fakeRenderer`)
- Create: `internal/notification/adapters/templates/purchase_refunded.txt.tmpl`
- Create: `internal/notification/adapters/templates/purchase_refunded.html.tmpl`
- Modify: `internal/notification/adapters/templates/templates.go`
- Test: `internal/notification/adapters/templates/templates_test.go`
- Modify: `internal/notification/adapters/events/events.go`
- Test: `internal/notification/adapters/events/events_test.go`
- Modify: `cmd/api/app.go:123`

**Interfaces:**
- Consumes: event JSON of Task 1 `PurchaseRefunded`.
- Produces:
  - `app.PurchaseRefundedInput{EventID, UserID, CourseID, CourseTitle string; AmountMinor int64; Currency, Gateway, ManualMethod, Reference string; AccessRevoked bool; RefundedAt time.Time}`
  - `app.PurchaseRefundedEmail{Name, CourseTitle, Amount, Method, Reference, RefundedOn string; AccessRevoked bool}`
  - `func (s *Service) SendPurchaseRefunded(ctx context.Context, in PurchaseRefundedInput) error`
  - `func PurchaseRefundedKey(eventID string) string`
  - `Renderer.PurchaseRefunded(e PurchaseRefundedEmail) (subject, text, html string, err error)`
  - `events.Handlers.PurchaseRefunded(msg *message.Message) error`

- [ ] **Step 1: Write the failing app test**

In `internal/notification/app/service_test.go`, extend `fakeRenderer`:

```go
type fakeRenderer struct {
	name     string
	err      error
	paid     app.PurchasePaidEmail
	refunded app.PurchaseRefundedEmail
}

func (f *fakeRenderer) PurchaseRefunded(e app.PurchaseRefundedEmail) (string, string, string, error) {
	f.refunded = e
	return "Refunded", "Text", "<p>HTML</p>", f.err
}
```

Create `internal/notification/app/purchase_refunded_test.go`:

```go
package app_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/notification/app"
)

var refundedIn = app.PurchaseRefundedInput{
	EventID: "ev-7", UserID: "200", CourseID: "11", CourseTitle: "Go", AmountMinor: 150000, Currency: "NPR",
	Gateway: "esewa", Reference: "RF-1", AccessRevoked: true, RefundedAt: time.Date(2026, 10, 8, 4, 15, 0, 0, time.UTC),
}

func TestSendPurchaseRefunded(t *testing.T) {
	m, r, u := &fakeMailer{}, &fakeRenderer{}, &fakeUsers{email: "s@example.com", name: " Sita "}
	if err := app.NewService(m, r, u, "").SendPurchaseRefunded(context.Background(), refundedIn); err != nil {
		t.Fatal(err)
	}
	want := app.PurchaseRefundedEmail{Name: "Sita", CourseTitle: "Go", Amount: "NPR 1,500.00", Method: "eSewa",
		Reference: "RF-1", RefundedOn: "8 October 2026, 10:00 NPT", AccessRevoked: true}
	if u.asked != "200" || r.refunded != want {
		t.Fatalf("asked=%q rendered=%+v", u.asked, r.refunded)
	}
	if m.calls != 1 || m.email.To != "s@example.com" || m.email.Subject != "Refunded" ||
		m.key != "ioe:payment.purchase.refunded:ev-7:refunded-v1" {
		t.Fatalf("calls=%d email=%+v key=%q", m.calls, m.email, m.key)
	}
}

func TestSendPurchaseRefundedPermanentFailures(t *testing.T) {
	cases := map[string]struct {
		in    app.PurchaseRefundedInput
		users *fakeUsers
	}{
		"no event id":  {app.PurchaseRefundedInput{UserID: "200"}, &fakeUsers{email: "s@example.com"}},
		"no user id":   {app.PurchaseRefundedInput{EventID: "ev"}, &fakeUsers{email: "s@example.com"}},
		"unknown user": {refundedIn, &fakeUsers{err: app.ErrUnknownUser}},
		"no email":     {refundedIn, &fakeUsers{}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			m := &fakeMailer{}
			err := app.NewService(m, &fakeRenderer{}, c.users, "").SendPurchaseRefunded(context.Background(), c.in)
			if !errors.Is(err, app.ErrPermanent) || m.calls != 0 {
				t.Fatalf("err=%v calls=%d", err, m.calls)
			}
		})
	}
}

func TestSendPurchaseRefundedRetriesContactFailure(t *testing.T) {
	down := errors.New("down")
	err := app.NewService(&fakeMailer{}, &fakeRenderer{}, &fakeUsers{err: down}, "").SendPurchaseRefunded(context.Background(), refundedIn)
	if !errors.Is(err, down) || errors.Is(err, app.ErrPermanent) {
		t.Fatalf("err = %v", err)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/notification/app/`
Expected: FAIL to compile — `app.PurchaseRefundedInput` undefined.

- [ ] **Step 3: Implement the app side**

In `internal/notification/app/service.go` extend `Renderer`:

```go
	PurchaseRefunded(e PurchaseRefundedEmail) (subject, text, html string, err error)
```

Create `internal/notification/app/purchase_refunded.go`:

```go
package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// PurchaseRefundedInput is the data of one payment.purchase.refunded event.
type PurchaseRefundedInput struct {
	EventID       string // outbox message UUID; stable across redeliveries
	UserID        string
	CourseID      string
	CourseTitle   string
	AmountMinor   int64
	Currency      string
	Gateway       string
	ManualMethod  string
	Reference     string
	AccessRevoked bool
	RefundedAt    time.Time
}

// PurchaseRefundedEmail is the display data of the refund-issued email. Every field is already
// formatted; Name may be empty.
type PurchaseRefundedEmail struct {
	Name          string
	CourseTitle   string
	Amount        string
	Method        string
	Reference     string
	RefundedOn    string
	AccessRevoked bool
}

// SendPurchaseRefunded emails the buyer that their payment was refunded, exactly once per event.
func (s *Service) SendPurchaseRefunded(ctx context.Context, in PurchaseRefundedInput) error {
	if in.EventID == "" || in.UserID == "" {
		return fmt.Errorf("%w: purchase refunded requires an event ID and a user ID", ErrPermanent)
	}
	email, name, err := s.users.Contact(ctx, in.UserID)
	if errors.Is(err, ErrUnknownUser) {
		return fmt.Errorf("%w: %w", ErrPermanent, err)
	}
	if err != nil {
		return err
	}
	if email == "" {
		return fmt.Errorf("%w: user %s has no email", ErrPermanent, in.UserID)
	}
	e := PurchaseRefundedEmail{
		Name: strings.TrimSpace(name), CourseTitle: in.CourseTitle, Amount: formatMoney(in.AmountMinor, in.Currency),
		Method: methodLabel(in.Gateway, in.ManualMethod), Reference: in.Reference,
		RefundedOn: in.RefundedAt.In(nepalTime).Format("2 January 2006, 15:04 MST"), AccessRevoked: in.AccessRevoked,
	}
	subject, text, html, err := s.renderer.PurchaseRefunded(e)
	if err != nil {
		return fmt.Errorf("%w: render purchase refunded: %w", ErrPermanent, err)
	}
	return s.mailer.Enqueue(ctx, Email{To: email, Subject: subject, Text: text, HTML: html}, PurchaseRefundedKey(in.EventID))
}

// PurchaseRefundedKey is the idempotency key for the email of one refunded event.
// Changing the email's content requires a new version suffix.
func PurchaseRefundedKey(eventID string) string {
	return "ioe:payment.purchase.refunded:" + eventID + ":refunded-v1"
}
```

Run: `go test ./internal/notification/app/`
Expected: PASS

- [ ] **Step 4: Write the failing template tests**

Append to `internal/notification/adapters/templates/templates_test.go`:

```go
var refundedEmail = app.PurchaseRefundedEmail{Name: "Sita", CourseTitle: "Go", Amount: "NPR 1,500.00", Method: "eSewa",
	Reference: "RF-1", RefundedOn: "8 October 2026, 10:00 NPT", AccessRevoked: true}

func renderRefunded(t *testing.T, e app.PurchaseRefundedEmail) (string, string, string) {
	t.Helper()
	r, err := templates.New()
	if err != nil {
		t.Fatal(err)
	}
	subject, text, html, err := r.PurchaseRefunded(e)
	if err != nil {
		t.Fatal(err)
	}
	return subject, text, html
}

func TestPurchaseRefunded(t *testing.T) {
	subject, text, html := renderRefunded(t, refundedEmail)
	if subject != "Refund issued: Go" {
		t.Fatalf("subject %q", subject)
	}
	for _, want := range []string{"Hello, Sita,", "Go", "NPR 1,500.00", "eSewa", "RF-1", "8 October 2026, 10:00 NPT", "access to this course has ended"} {
		if !strings.Contains(text, want) || !strings.Contains(html, want) {
			t.Fatalf("missing %q\ntext=%s\nhtml=%s", want, text, html)
		}
	}
}

func TestPurchaseRefundedKeptAccessAndBlanks(t *testing.T) {
	e := refundedEmail
	e.AccessRevoked, e.CourseTitle, e.Name = false, "", " "
	subject, text, html := renderRefunded(t, e)
	if subject != "Refund issued" || !strings.HasPrefix(text, "Hello,\n") ||
		strings.Contains(text, "has ended") || strings.Contains(html, "has ended") {
		t.Fatalf("subject=%q text=%s html=%s", subject, text, html)
	}
}

func TestPurchaseRefundedEscapesHTMLOnly(t *testing.T) {
	e := refundedEmail
	e.Reference = "<b>RF</b>"
	_, text, html := renderRefunded(t, e)
	if !strings.Contains(text, "<b>RF</b>") || strings.Contains(html, "<b>") {
		t.Fatalf("text=%s html=%s", text, html)
	}
}
```

Run: `go test ./internal/notification/adapters/templates/`
Expected: FAIL to compile — `r.PurchaseRefunded undefined`.

- [ ] **Step 5: Implement the templates**

Create `internal/notification/adapters/templates/purchase_refunded.txt.tmpl`:

```text
Hello{{with .Name}}, {{.}}{{end}},

Your payment{{with .CourseTitle}} for {{.}}{{end}} was refunded.{{if .AccessRevoked}} Your access to this course has ended.{{end}}

Amount:    {{.Amount}}
Method:    {{.Method}}
Reference: {{.Reference}}
Date:      {{.RefundedOn}}

The IOE team
```

Create `internal/notification/adapters/templates/purchase_refunded.html.tmpl`:

```html
<p>Hello{{with .Name}}, {{.}}{{end}},</p>
<p>Your payment{{with .CourseTitle}} for {{.}}{{end}} was refunded.{{if .AccessRevoked}} Your access to this course has ended.{{end}}</p>
<p>Amount: {{.Amount}}<br>Method: {{.Method}}<br>Reference: {{.Reference}}<br>Date: {{.RefundedOn}}</p>
<p>The IOE team</p>
```

In `templates.go`:

```go
//go:embed welcome.txt.tmpl welcome.html.tmpl purchase_paid.txt.tmpl purchase_paid.html.tmpl purchase_refunded.txt.tmpl purchase_refunded.html.tmpl
var files embed.FS

const (
	welcomeSubject  = "Welcome to IOE"
	paidSubject     = "Payment received"
	refundedSubject = "Refund issued"
)
```

Add `refundedText *texttemplate.Template` and `refundedHTML *htmltemplate.Template` to `Renderer`, parse them in `New` after the paid templates:

```go
	if r.refundedText, err = texttemplate.ParseFS(files, "purchase_refunded.txt.tmpl"); err != nil {
		return nil, err
	}
	if r.refundedHTML, err = htmltemplate.ParseFS(files, "purchase_refunded.html.tmpl"); err != nil {
		return nil, err
	}
```

and add the method:

```go
// PurchaseRefunded renders the refund-issued email. Blank name and title are omitted.
func (r *Renderer) PurchaseRefunded(e app.PurchaseRefundedEmail) (subject, text, html string, err error) {
	e.Name, e.CourseTitle = strings.TrimSpace(e.Name), strings.TrimSpace(e.CourseTitle)
	subject = refundedSubject
	if e.CourseTitle != "" {
		subject += ": " + e.CourseTitle
	}
	text, html, err = execute(r.refundedText, r.refundedHTML, e)
	return subject, text, html, err
}
```

Run: `go test ./internal/notification/adapters/templates/`
Expected: PASS

- [ ] **Step 6: Write the failing handler tests**

In `internal/notification/adapters/events/events_test.go`, add to `fakeSender`:

```go
	refundedCalls int
	refunded      app.PurchaseRefundedInput
```

```go
func (f *fakeSender) SendPurchaseRefunded(_ context.Context, in app.PurchaseRefundedInput) error {
	f.refundedCalls++
	f.refunded = in
	return f.err
}
```

Append:

```go
const refundedPayload = `{"purchase_id":"9","user_id":"200","course_id":"11","course_title":"Go","amount_minor":150000,
"currency":"NPR","gateway":"manual","manual_method":"cash","refund_reference":"RF-1","access_revoked":true,
"occurred_at":"2026-10-08T04:15:00Z"}`

func TestPurchaseRefundedSends(t *testing.T) {
	s := &fakeSender{}
	h, _ := newHandlers(t, s)
	if err := h.PurchaseRefunded(message.NewMessage("ev-7", []byte(refundedPayload))); err != nil {
		t.Fatal(err)
	}
	want := app.PurchaseRefundedInput{EventID: "ev-7", UserID: "200", CourseID: "11", CourseTitle: "Go", AmountMinor: 150000,
		Currency: "NPR", Gateway: "manual", ManualMethod: "cash", Reference: "RF-1", AccessRevoked: true,
		RefundedAt: time.Date(2026, 10, 8, 4, 15, 0, 0, time.UTC)}
	if s.refundedCalls != 1 || s.refunded != want {
		t.Fatalf("calls=%d in=%+v", s.refundedCalls, s.refunded)
	}
}

func TestPurchaseRefundedDropsAndRetries(t *testing.T) {
	s := &fakeSender{}
	h, logs := newHandlers(t, s)
	if err := h.PurchaseRefunded(message.NewMessage("ev-1", []byte("not json"))); err != nil || s.refundedCalls != 0 {
		t.Fatalf("err=%v calls=%d", err, s.refundedCalls)
	}
	s.err = fmt.Errorf("%w: unknown user", app.ErrPermanent)
	if err := h.PurchaseRefunded(message.NewMessage("ev-2", []byte(refundedPayload))); err != nil {
		t.Fatalf("permanent err = %v", err)
	}
	if !strings.Contains(logs.String(), "invalid_payload") || !strings.Contains(logs.String(), "permanent") {
		t.Fatalf("logs = %s", logs)
	}
	errDown := errors.New("down")
	s.err = errDown
	if err := h.PurchaseRefunded(message.NewMessage("ev-3", []byte(refundedPayload))); !errors.Is(err, errDown) {
		t.Fatalf("retryable err = %v", err)
	}
}
```

Run: `go test ./internal/notification/adapters/events/`
Expected: FAIL to compile — `h.PurchaseRefunded undefined`.

- [ ] **Step 7: Implement the handler and wiring**

In `events.go`, extend `Sender`:

```go
	SendPurchaseRefunded(ctx context.Context, in app.PurchaseRefundedInput) error
```

Add after `PurchasePaid`:

```go
// purchaseRefunded mirrors the payment.purchase.refunded payload this context relies on.
type purchaseRefunded struct {
	UserID          string    `json:"user_id"`
	CourseID        string    `json:"course_id"`
	CourseTitle     string    `json:"course_title"`
	AmountMinor     int64     `json:"amount_minor"`
	Currency        string    `json:"currency"`
	Gateway         string    `json:"gateway"`
	ManualMethod    string    `json:"manual_method"`
	RefundReference string    `json:"refund_reference"`
	AccessRevoked   bool      `json:"access_revoked"`
	OccurredAt      time.Time `json:"occurred_at"`
}

// PurchaseRefunded handles payment.purchase.refunded with the same retry and drop rules as Welcome.
func (h *Handlers) PurchaseRefunded(msg *message.Message) error {
	var ev purchaseRefunded
	if err := json.Unmarshal(msg.Payload, &ev); err != nil {
		h.drop(msg, "invalid_payload", err)
		return nil
	}
	err := h.sender.SendPurchaseRefunded(msg.Context(), app.PurchaseRefundedInput{
		EventID: msg.UUID, UserID: ev.UserID, CourseID: ev.CourseID, CourseTitle: ev.CourseTitle,
		AmountMinor: ev.AmountMinor, Currency: ev.Currency, Gateway: ev.Gateway, ManualMethod: ev.ManualMethod,
		Reference: ev.RefundReference, AccessRevoked: ev.AccessRevoked, RefundedAt: ev.OccurredAt,
	})
	if errors.Is(err, app.ErrPermanent) {
		h.drop(msg, "permanent", err)
		return nil
	}
	return err
}
```

In `cmd/api/app.go`, after the `PurchasePaid` line:

```go
	fw.Handle(paymentdomain.PurchaseRefunded{}.EventName(), handlers.PurchaseRefunded)
```

- [ ] **Step 8: Run tests to verify they pass**

Run: `go test ./internal/notification/... ./cmd/api/`
Expected: PASS

- [ ] **Step 9: Commit**

```bash
git add internal/notification cmd/api/app.go
git commit -m "feat(notification): email buyers when a purchase is refunded"
```

---

### Task 7: End-to-end refund and gates

**Files:**
- Modify: `cmd/api/e2e_integration_test.go` (`TestManualPaymentEndToEnd`, from line 1517)

**Interfaces:**
- Consumes: everything above through HTTP.

- [ ] **Step 1: Extend the e2e test**

In `TestManualPaymentEndToEnd`, replace the final email-wait loop (from `deadline := time.After(30 * time.Second)` to the end of the function) with:

```go
	purchaseID := body["items"].([]any)[0].(map[string]any)["id"].(string)
	refund := `{"reference":"RF-77","note":"duplicate"}`
	if resp, body = c.do(http.MethodPost, "/v1/purchases/"+purchaseID+"/refund", refund, student); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("student refund: %d %v", resp.StatusCode, body)
	}
	resp, body = c.do(http.MethodPost, "/v1/purchases/"+purchaseID+"/refund", refund, admin)
	if p := body["purchase"].(map[string]any); resp.StatusCode != http.StatusOK || p["status"] != "refunded" ||
		p["refund_reference"] != "RF-77" || p["revoke_pending"] != false {
		t.Fatalf("refund: %d %v", resp.StatusCode, body)
	}
	if resp, body = c.do(http.MethodPost, "/v1/purchases/"+purchaseID+"/refund", refund, admin); resp.StatusCode != http.StatusConflict || body["type"] != "not_refundable" {
		t.Fatalf("second refund: %d %v", resp.StatusCode, body)
	}
	if resp, body = c.do(http.MethodGet, "/v1/courses/"+courseID+"/lectures/"+lectureID+"/content", "", student); resp.StatusCode != http.StatusForbidden || body["type"] != "enrollment_required" {
		t.Fatalf("read after refund: %d %v", resp.StatusCode, body)
	}
	if resp, body = c.do(http.MethodPost, "/v1/purchases/"+purchaseID+"/confirm", "", student); resp.StatusCode != http.StatusOK {
		t.Fatalf("confirm refunded manual purchase: %d %v", resp.StatusCode, body)
	}

	deadline := time.After(30 * time.Second)
	var paidSeen, refundedSeen bool
	for !paidSeen || !refundedSeen {
		select {
		case got := <-sent:
			switch {
			case strings.HasPrefix(got.key, "ioe:payment.purchase.paid:"):
				if got.recipient != "student@example.com" || got.subject != "Payment received: Go" || !strings.HasSuffix(got.key, ":paid-v1") {
					t.Fatalf("paid email %+v", got)
				}
				paidSeen = true
			case strings.HasPrefix(got.key, "ioe:payment.purchase.refunded:"):
				if got.recipient != "student@example.com" || got.subject != "Refund issued: Go" || !strings.HasSuffix(got.key, ":refunded-v1") {
					t.Fatalf("refund email %+v", got)
				}
				refundedSeen = true
			}
		case <-deadline:
			t.Fatalf("emails not enqueued: paid=%v refunded=%v", paidSeen, refundedSeen)
		}
	}
```

Note `body` at that point holds the admin list response from `GET /v1/users/{studentID}/purchases`; if a later statement reassigns `body` before this block, capture `purchaseID` right after the admin list call instead.

- [ ] **Step 2: Run the e2e test**

Run: `go test -tags integration -run TestManualPaymentEndToEnd ./cmd/api/`
Expected: PASS. Confirm in the output that the test actually ran (not `no tests to run`, not skipped for missing Docker).

- [ ] **Step 3: Run all gates**

Run:

```sh
make check
make test-integration
git diff --check
```

Expected: all pass. `make check` includes the sqlc diff, so a stale `sqlcgen` fails here. Do not report integration tests as passing unless they ran.

- [ ] **Step 4: Commit**

```bash
git add cmd/api/e2e_integration_test.go
git commit -m "test(payment): cover refunds end to end"
```
