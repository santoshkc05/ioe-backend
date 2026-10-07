# Payment (eSewa) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let a student buy a published paid course through the eSewa ePay v2 sandbox and be enrolled as soon as eSewa confirms the payment, including when the student never returns to the site.

**Architecture:** A new `payment` bounded context owns a `Purchase` aggregate (pending → paid | failed, failed → paid on late completion) in schema `payment`. Its app layer talks to eSewa through a `Gateway` port, reads course price through a `CourseCatalog` port, and grants access through an `EnrollmentGranter` port that `cmd/api` wires to a new `enrollment.Service.EnrollPurchased`. eSewa's status API is the only source of truth; a background reconciler in `cmd/api` settles purchases the buyer never confirmed and retries failed grants.

**Tech Stack:** Go 1.27, `net/http` router (`internal/platform/httpserver`), pgx v5, sqlc 1.31, goose migrations, Watermill SQL outbox (`internal/platform/outbox`), golangci-lint depguard, gitleaks.

**Spec:** `docs/superpowers/specs/2026-10-07-payment-esewa-design.md`

## Global Constraints

- A context never imports another context; cross-context calls go through ports defined by the caller and adapters in `cmd/api`.
- Payment reads and writes only schema `payment`. Enrollment reads and writes only schema `enrollment`.
- Write the outbox message in the same transaction as the state change.
- Money is `{AmountMinor int64, Currency string}`; course prices are NPR paisa today (`150000` = Rs 1500).
- Purchase statuses: `pending`, `paid`, `failed`. Gateway name: `esewa`.
- Events: `payment.purchase.initiated`, `payment.purchase.paid`, `payment.purchase.failed`.
- eSewa signature: base64(HMAC-SHA256(secret, `total_amount=<t>,transaction_uuid=<u>,product_code=<c>`)), `signed_field_names` = `total_amount,transaction_uuid,product_code`.
- eSewa sandbox: product code `EPAYTEST`, secret `8gBm/:&EnhH.1/q`, form URL `https://rc-epay.esewa.com.np/api/epay/main/v2/form`, status URL `https://rc.esewa.com.np/api/epay/transaction/status/`. Documented sample: `total_amount=100,transaction_uuid=11-201-13,product_code=EPAYTEST` signs to `4Ov7pCI1zIOdwtV2BRMUNjz1upIlT/COTxfLhWvVurE=`.
- eSewa status mapping: `COMPLETE` → complete (`ref_id` is the txn); `PENDING`, `AMBIGUOUS` → pending; `NOT_FOUND`, `CANCELED` → failed; `FULL_REFUND`, `PARTIAL_REFUND` → warn log + pending; anything else → error.
- HTTP error codes: 400 `invalid_input`, 404 `not_found`, 409 `course_free` / `already_enrolled` / `already_purchased`, 503 `payment_unavailable`.
- Reconciler: every 5 minutes; pending purchases older than 15 minutes plus paid-but-ungranted purchases; warn on purchases pending ≥ 24 hours.
- No automated test calls the real eSewa sandbox.
- Run `make fmt` before each commit (gofmt aligns comments and struct literals in the code blocks below).
- Use Conventional Commits. Integration tests must not be reported as passing unless they actually ran.
- Required gates: `make check`, `make test-integration`, `docker compose config`, `make docker-build`, `git diff --check`.

## Spec refinements made while planning

- `Repository.HasPaid` becomes `CountPaid(ctx, userID, courseID) (int, error)`; checkout rejects when it is > 0, and settlement uses it to detect a duplicate payment. The duplicate warning names the newly paid purchase and the paid count rather than every purchase ID.
- `ListUnsettled` pages by ID (`afterID`), and `Reconcile` walks every page each pass. With a fixed oldest-first batch of 50, purchases stuck in `PENDING` would starve newer ones forever. The partial index is therefore on `(id)`, not `(created_at)`.
- `EnrollPurchased` requires the course to exist but not to be published. A course unpublished between payment and grant would otherwise be retried forever while the buyer, who paid, is never enrolled. Lecture access still checks publication separately.
- The eSewa adapter compares `transaction_uuid`, `product_code` and `total_amount` only on `COMPLETE`. eSewa's docs do not promise those fields for `NOT_FOUND` or `CANCELED`, and only `COMPLETE` changes access. `total_amount` arrives as a JSON number (`100.0`) and is parsed as a decimal string, never as a float.
- Courseauthoring's `CourseFacts` gains `Price domain.Price` so payment can snapshot the amount and currency.
- Config gains an `allOrNone` helper shared by the media and eSewa settings.

## Review Focus

- The buyer pays, then closes the tab before eSewa redirects: the reconciler must still mark the purchase paid and enroll them — pinned in Task 3 (`TestReconcileSettlesOldPendingAndRetriesGrants`) and Task 8 (`TestPaymentEndToEnd`, reconciler leg).
- Enrollment is down for a moment just after eSewa says `COMPLETE`: the purchase must stay `paid`, the confirm call must still succeed, and a later confirm or reconcile pass must grant exactly once — pinned in Task 3 (`TestGrantFailureIsRetried`).
- The buyer opens two tabs and pays in both: both purchases become paid and an operator-visible warning is logged — pinned in Task 3 (`TestDuplicatePaidPurchaseLogsWarning`).
- eSewa's status response reports `COMPLETE` for a different amount or reference (tampering or a bug): nothing is marked paid — pinned in Task 4 (`TestFetchStatusRejectsMismatch`).
- Many purchases stay `PENDING` at eSewa for days: newer purchases must still be reconciled — pinned in Task 3 (`TestReconcilePagesAndWarnsOnStalePending`).

## File Structure

Create:
- `internal/payment/domain/errors.go` — package doc, domain errors.
- `internal/payment/domain/purchase.go` — `Money`, `Status`, `Purchase`, transitions, `CanView`.
- `internal/payment/domain/events.go` — `Event` and the three purchase events.
- `internal/payment/domain/purchase_test.go`
- `internal/payment/app/errors.go` — app errors.
- `internal/payment/app/ports.go` — `Repository`, `EventPublisher`, `Repos`, `TxRunner`, `CourseCatalog`, `CourseFacts`, `EnrollmentGranter`, `Gateway`, `Checkout`, `ResultKind`, `Result`.
- `internal/payment/app/service.go` — `Service`: `Checkout`, `Confirm`, `Get`, `Reconcile`, `settle`, `update`.
- `internal/payment/app/fakes_test.go`, `internal/payment/app/service_test.go`
- `internal/payment/adapters/esewa/esewa.go`, `internal/payment/adapters/esewa/esewa_test.go`
- `migrations/00009_payment.sql`
- `internal/payment/adapters/postgres/{postgres.go,purchases.go,queries.sql,postgres_integration_test.go}`, `internal/payment/adapters/postgres/sqlcgen/` (generated)
- `internal/payment/adapters/httpapi/{httpapi.go,wire.go,httpapi_test.go}`
- `cmd/api/payment.go`

Modify:
- `internal/courseauthoring/app/course_service.go`, `internal/courseauthoring/app/course_service_test.go` — `CourseFacts.Price`.
- `internal/enrollment/app/service.go`, `internal/enrollment/app/service_test.go` — `EnrollPurchased`.
- `.golangci.yml` — payment depguard rules.
- `sqlc.yaml`, every context's `sqlcgen/models.go` (regenerated).
- `internal/platform/config/config.go`, `internal/platform/config/config_test.go`
- `.env.example`, `.gitleaks.toml`
- `cmd/api/app.go`, `cmd/api/enrollment.go`, `cmd/api/main.go`, `cmd/api/e2e_integration_test.go`
- `api/openapi.yaml`, `README.md`

---

### Task 1: Course price in facts and purchase-driven enrollment

**Files:**
- Modify: `internal/courseauthoring/app/course_service.go:124-149`
- Modify: `internal/courseauthoring/app/course_service_test.go:161-192`
- Modify: `internal/enrollment/app/service.go:30-48`
- Test: `internal/enrollment/app/service_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces: `courseauthoringapp.CourseFacts.Price domain.Price` (courseauthoring `domain.Price{AmountMinor int64, Currency string}`); `func (s *enrollmentapp.Service) EnrollPurchased(ctx context.Context, courseID, userID id.ID) error` — idempotent, no principal, returns `enrollmentapp.ErrNotFound` for a missing course.

- [ ] **Step 1: Extend the facts test**

In `internal/courseauthoring/app/course_service_test.go`, `TestFacts`, change the two fact assertions to also check the price (the file already imports `.../courseauthoring/domain`):

```go
	if err != nil || f.Published || !f.Free || !f.Price.IsFree() || f.OwnerID != owner.UserID || f.LectureIDs == nil || len(f.LectureIDs) != 0 {
		t.Fatalf("draft facts = %+v, %v", f, err)
	}
```

and

```go
	if err != nil || !f.Published || f.Free || f.Price != (domain.Price{AmountMinor: 150000, Currency: "NPR"}) ||
		f.OwnerID != owner.UserID || !slices.Equal(f.LectureIDs, want) {
		t.Fatalf("published facts = %+v, %v; want lecture IDs %v", f, err, want)
	}
```

- [ ] **Step 2: Write the failing enrollment tests**

Append to `internal/enrollment/app/service_test.go`:

```go
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
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/courseauthoring/app/ ./internal/enrollment/app/`
Expected: compile errors `f.Price undefined` and `f.svc.EnrollPurchased undefined`.

- [ ] **Step 4: Add the price to course facts**

In `internal/courseauthoring/app/course_service.go`:

```go
// CourseFacts is what other contexts may know about a course without a principal.
type CourseFacts struct {
	Published  bool
	Free       bool
	Price      domain.Price
	OwnerID    id.ID
	LectureIDs []id.ID // course order; empty, never nil, when the course has no lectures
}
```

and in `Facts`:

```go
		f = CourseFacts{Published: c.Status == domain.StatusPublished, Free: c.Price.IsFree(), Price: c.Price, OwnerID: c.OwnerID, LectureIDs: lectureIDs}
```

- [ ] **Step 5: Add `EnrollPurchased`**

In `internal/enrollment/app/service.go`, replace the tail of `Enroll` and add two methods:

```go
// Enroll makes userID actively enrolled in courseID. activated is false when the user was
// already active.
func (s *Service) Enroll(ctx context.Context, p auth.Principal, courseID, userID id.ID) (domain.Enrollment, bool, error) {
	c, err := s.courses.CourseFacts(ctx, courseID)
	if err != nil {
		return domain.Enrollment{}, false, err
	}
	if err := domain.AuthorizeEnroll(p, c, userID); err != nil {
		return domain.Enrollment{}, false, err
	}
	return s.enrollRetrying(ctx, courseID, userID)
}

// EnrollPurchased actively enrolls userID after the payment context confirmed a purchase.
// It applies no principal check and does not require the course to still be published:
// the buyer has paid. An already active enrollment is success.
func (s *Service) EnrollPurchased(ctx context.Context, courseID, userID id.ID) error {
	if _, err := s.courses.CourseFacts(ctx, courseID); err != nil {
		return err
	}
	_, _, err := s.enrollRetrying(ctx, courseID, userID)
	return err
}

// enrollRetrying runs enroll again when a concurrent first enrollment won the unique
// constraint; that row is then visible.
func (s *Service) enrollRetrying(ctx context.Context, courseID, userID id.ID) (domain.Enrollment, bool, error) {
	e, activated, err := s.enroll(ctx, courseID, userID)
	if errors.Is(err, ErrDuplicate) {
		e, activated, err = s.enroll(ctx, courseID, userID)
	}
	return e, activated, err
}
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test -race ./internal/courseauthoring/... ./internal/enrollment/...`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/courseauthoring/app/course_service.go internal/courseauthoring/app/course_service_test.go internal/enrollment/app/service.go internal/enrollment/app/service_test.go
git commit -m "feat(enrollment): enroll buyers of paid courses"
```

---

### Task 2: Payment domain

**Files:**
- Create: `internal/payment/domain/errors.go`, `internal/payment/domain/purchase.go`, `internal/payment/domain/events.go`
- Test: `internal/payment/domain/purchase_test.go`
- Modify: `.golangci.yml`

**Interfaces:**
- Consumes: `internal/platform/auth`, `internal/platform/id`.
- Produces:
  - `type Money struct{ AmountMinor int64; Currency string }`
  - `type Status string` with `StatusPending`, `StatusPaid`, `StatusFailed`
  - `type Purchase struct{ ID, UserID, CourseID id.ID; Price Money; Gateway, GatewayRef, GatewayTxn string; Status Status; CreatedAt, SettledAt, GrantedAt time.Time; Version int64 }`
  - `func NewPurchase(purchaseID, userID, courseID id.ID, price Money, gateway string, now time.Time) (Purchase, Event, error)`
  - `func (p *Purchase) MarkPaid(txn string, now time.Time) (Event, error)`
  - `func (p *Purchase) MarkFailed(now time.Time) Event`
  - `func (p *Purchase) MarkGranted(now time.Time)`
  - `func (p *Purchase) NeedsGrant() bool`
  - `func (p *Purchase) CanView(pr auth.Principal) bool`
  - `type Event interface{ EventName() string }`; `PurchaseInitiated`, `PurchasePaid`, `PurchaseFailed`
  - `ErrFreePrice`, `ErrInvalidPurchase`

- [ ] **Step 1: Add depguard rules for the new context**

In `.golangci.yml`:

1. Add to `platform-independent-of-contexts.deny` after the assessment entry:

```yaml
            - pkg: github.com/santoshkc2200/ioe-backend/internal/payment
              desc: platform must not depend on bounded contexts
```

2. Add to the `deny` list of each of `identity-independent`, `notification-independent`, `courseauthoring-independent`, `enrollment-independent`, `progress-independent`, `media-independent`, `assessment-independent`:

```yaml
            - pkg: github.com/santoshkc2200/ioe-backend/internal/payment
              desc: bounded contexts must not import each other
```

3. Add after the `assessment-independent` rule:

```yaml
        payment-domain:
          list-mode: strict
          files:
            - "**/internal/payment/domain/**"
            - "!$test"
          allow:
            - $gostd
            - github.com/santoshkc2200/ioe-backend/internal/platform/auth
            - github.com/santoshkc2200/ioe-backend/internal/platform/id
        payment-app:
          list-mode: strict
          files:
            - "**/internal/payment/app/**"
            - "!$test"
          allow:
            - $gostd
            - github.com/santoshkc2200/ioe-backend/internal/payment/domain
            - github.com/santoshkc2200/ioe-backend/internal/platform/auth
            - github.com/santoshkc2200/ioe-backend/internal/platform/clock
            - github.com/santoshkc2200/ioe-backend/internal/platform/id
        payment-independent:
          list-mode: lax
          files:
            - "**/internal/payment/**"
          deny:
            - pkg: github.com/santoshkc2200/ioe-backend/internal/identity
              desc: bounded contexts must not import each other
            - pkg: github.com/santoshkc2200/ioe-backend/internal/notification
              desc: bounded contexts must not import each other
            - pkg: github.com/santoshkc2200/ioe-backend/internal/courseauthoring
              desc: bounded contexts must not import each other
            - pkg: github.com/santoshkc2200/ioe-backend/internal/enrollment
              desc: bounded contexts must not import each other
            - pkg: github.com/santoshkc2200/ioe-backend/internal/progress
              desc: bounded contexts must not import each other
            - pkg: github.com/santoshkc2200/ioe-backend/internal/media
              desc: bounded contexts must not import each other
            - pkg: github.com/santoshkc2200/ioe-backend/internal/assessment
              desc: bounded contexts must not import each other
```

- [ ] **Step 2: Write the failing domain tests**

Create `internal/payment/domain/purchase_test.go`:

```go
package domain_test

import (
	"encoding/json"
	"errors"
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
	p, _, err := domain.NewPurchase(42, 200, 10, npr, "esewa", t0)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestNewPurchase(t *testing.T) {
	p, ev, err := domain.NewPurchase(42, 200, 10, npr, "esewa", t0)
	if err != nil {
		t.Fatal(err)
	}
	want := domain.Purchase{ID: 42, UserID: 200, CourseID: 10, Price: npr, Gateway: "esewa", GatewayRef: "42",
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
			if _, ev, err := domain.NewPurchase(1, 2, 3, c.price, c.gateway, t0); !errors.Is(err, c.want) || ev != nil {
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
	if !ok || paid.GatewayTxn != "0001TS9" || paid.AmountMinor != 150000 || paid.Currency != "NPR" || !paid.OccurredAt.Equal(at) {
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
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/payment/domain/`
Expected: FAIL — package `domain` has no non-test Go files / undefined identifiers.

- [ ] **Step 4: Write the domain**

Create `internal/payment/domain/errors.go`:

```go
// Package domain holds the payment model.
package domain

import "errors"

var (
	ErrFreePrice       = errors.New("price must be positive")
	ErrInvalidPurchase = errors.New("invalid purchase")
)
```

Create `internal/payment/domain/events.go`:

```go
package domain

import (
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

// Event is a domain event written to the outbox under its EventName.
type Event interface {
	EventName() string
}

// PurchaseInitiated is emitted when a buyer starts a checkout.
type PurchaseInitiated struct {
	PurchaseID  id.ID     `json:"purchase_id"`
	UserID      id.ID     `json:"user_id"`
	CourseID    id.ID     `json:"course_id"`
	AmountMinor int64     `json:"amount_minor"`
	Currency    string    `json:"currency"`
	Gateway     string    `json:"gateway"`
	OccurredAt  time.Time `json:"occurred_at"`
}

func (PurchaseInitiated) EventName() string { return "payment.purchase.initiated" }

// PurchasePaid is emitted when the gateway confirms the payment.
type PurchasePaid struct {
	PurchaseID  id.ID     `json:"purchase_id"`
	UserID      id.ID     `json:"user_id"`
	CourseID    id.ID     `json:"course_id"`
	AmountMinor int64     `json:"amount_minor"`
	Currency    string    `json:"currency"`
	Gateway     string    `json:"gateway"`
	GatewayTxn  string    `json:"gateway_txn"`
	OccurredAt  time.Time `json:"occurred_at"`
}

func (PurchasePaid) EventName() string { return "payment.purchase.paid" }

// PurchaseFailed is emitted when the gateway reports that a pending payment will not complete.
type PurchaseFailed struct {
	PurchaseID id.ID     `json:"purchase_id"`
	UserID     id.ID     `json:"user_id"`
	CourseID   id.ID     `json:"course_id"`
	Gateway    string    `json:"gateway"`
	OccurredAt time.Time `json:"occurred_at"`
}

func (PurchaseFailed) EventName() string { return "payment.purchase.failed" }
```

Create `internal/payment/domain/purchase.go`:

```go
package domain

import (
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

// Money is an amount in the currency's minor unit (paisa for NPR). Currency is an ISO 4217 code.
type Money struct {
	AmountMinor int64
	Currency    string
}

type Status string

const (
	StatusPending Status = "pending"
	StatusPaid    Status = "paid"
	StatusFailed  Status = "failed"
)

// Purchase is one buyer's attempt to pay for one course through one gateway. A buyer may have
// several; each is settled independently from the gateway's status.
type Purchase struct {
	ID         id.ID
	UserID     id.ID
	CourseID   id.ID
	Price      Money  // snapshot taken at checkout
	Gateway    string // gateway name, such as "esewa"
	GatewayRef string // our reference at the gateway; for eSewa, transaction_uuid
	GatewayTxn string // the gateway's transaction id; set when paid
	Status     Status
	CreatedAt  time.Time
	SettledAt  time.Time // zero while pending
	GrantedAt  time.Time // zero until the enrollment grant succeeds
	Version    int64
}

// NewPurchase starts a pending purchase. Its gateway reference is the purchase ID.
func NewPurchase(purchaseID, userID, courseID id.ID, price Money, gateway string, now time.Time) (Purchase, Event, error) {
	if price.AmountMinor <= 0 {
		return Purchase{}, nil, ErrFreePrice
	}
	if price.Currency == "" || gateway == "" {
		return Purchase{}, nil, ErrInvalidPurchase
	}
	p := Purchase{
		ID: purchaseID, UserID: userID, CourseID: courseID, Price: price, Gateway: gateway,
		GatewayRef: purchaseID.String(), Status: StatusPending, CreatedAt: now,
	}
	return p, PurchaseInitiated{
		PurchaseID: p.ID, UserID: userID, CourseID: courseID, AmountMinor: price.AmountMinor,
		Currency: price.Currency, Gateway: gateway, OccurredAt: now,
	}, nil
}

// MarkPaid records the gateway's confirmation. A failed purchase can still become paid when the
// gateway completes it late. Marking a paid purchase changes nothing and returns no event.
func (p *Purchase) MarkPaid(txn string, now time.Time) (Event, error) {
	if txn == "" {
		return nil, ErrInvalidPurchase
	}
	if p.Status == StatusPaid {
		return nil, nil
	}
	p.Status, p.GatewayTxn, p.SettledAt = StatusPaid, txn, now
	return PurchasePaid{
		PurchaseID: p.ID, UserID: p.UserID, CourseID: p.CourseID, AmountMinor: p.Price.AmountMinor,
		Currency: p.Price.Currency, Gateway: p.Gateway, GatewayTxn: txn, OccurredAt: now,
	}, nil
}

// MarkFailed records that a pending payment will not complete. On any other status it changes
// nothing and returns no event.
func (p *Purchase) MarkFailed(now time.Time) Event {
	if p.Status != StatusPending {
		return nil
	}
	p.Status, p.SettledAt = StatusFailed, now
	return PurchaseFailed{PurchaseID: p.ID, UserID: p.UserID, CourseID: p.CourseID, Gateway: p.Gateway, OccurredAt: now}
}

// MarkGranted records that the buyer was enrolled. It changes only a paid, ungranted purchase.
func (p *Purchase) MarkGranted(now time.Time) {
	if p.NeedsGrant() {
		p.GrantedAt = now
	}
}

// NeedsGrant reports whether the purchase is paid but the buyer is not yet known to be enrolled.
func (p *Purchase) NeedsGrant() bool { return p.Status == StatusPaid && p.GrantedAt.IsZero() }

// CanView reports whether pr is the buyer or a root admin.
func (p *Purchase) CanView(pr auth.Principal) bool {
	return pr.UserID == p.UserID || pr.Role == auth.RoleRootAdmin
}
```

- [ ] **Step 5: Run tests and lint**

Run: `go test -race ./internal/payment/... && golangci-lint run ./internal/payment/...`
Expected: PASS, no lint findings.

- [ ] **Step 6: Commit**

```bash
git add .golangci.yml internal/payment/domain
git commit -m "feat(payment): add purchase domain"
```

---

### Task 3: Payment use cases

**Files:**
- Create: `internal/payment/app/errors.go`, `internal/payment/app/ports.go`, `internal/payment/app/service.go`
- Test: `internal/payment/app/fakes_test.go`, `internal/payment/app/service_test.go`

**Interfaces:**
- Consumes: Task 2 domain.
- Produces:
  - Errors: `ErrNotFound`, `ErrCourseFree`, `ErrAlreadyEnrolled`, `ErrAlreadyPurchased`, `ErrGatewayUnavailable`, `ErrConcurrentModification`, `ErrInvalidInput`
  - `Repository{ Find(ctx, id.ID) (domain.Purchase, bool, error); CountPaid(ctx, userID, courseID id.ID) (int, error); ListUnsettled(ctx, pendingBefore time.Time, afterID id.ID, limit int) ([]domain.Purchase, error); Insert(ctx, *domain.Purchase) error; Update(ctx, *domain.Purchase) error }`
  - `EventPublisher{ Publish(ctx, ...domain.Event) error }`, `Repos{Purchases Repository; Events EventPublisher}`, `TxRunner{ RunInTx(ctx, func(Repos) error) error }`
  - `CourseCatalog{ CourseFacts(ctx, id.ID) (CourseFacts, error) }`, `CourseFacts{Published bool; Price domain.Money}`
  - `EnrollmentGranter{ IsEnrolled(ctx, courseID, userID id.ID) (bool, error); GrantPurchased(ctx, courseID, userID id.ID) error }`
  - `Gateway{ StartCheckout(ctx, domain.Purchase) (Checkout, error); FetchStatus(ctx, domain.Purchase) (Result, error) }`
  - `Checkout{Method, URL string; Fields map[string]string}`, `ResultKind` (`ResultPending`=0, `ResultComplete`, `ResultFailed`), `Result{Kind ResultKind; Txn string}`
  - `func NewService(tx TxRunner, courses CourseCatalog, enroll EnrollmentGranter, gateways map[string]Gateway, ids *id.Generator, c clock.Clock, logger *slog.Logger) *Service`
  - `(*Service).Checkout(ctx, auth.Principal, courseID id.ID, gateway string) (domain.Purchase, Checkout, error)`
  - `(*Service).Confirm(ctx, auth.Principal, purchaseID id.ID) (domain.Purchase, error)`
  - `(*Service).Get(ctx, auth.Principal, purchaseID id.ID) (domain.Purchase, error)`
  - `(*Service).Reconcile(ctx) error`

- [ ] **Step 1: Write the fakes**

Create `internal/payment/app/fakes_test.go`:

```go
package app_test

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/payment/app"
	"github.com/santoshkc2200/ioe-backend/internal/payment/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

type fixedClock struct{ now time.Time }

func (c *fixedClock) Now() time.Time { return c.now }

// memStore is a single-goroutine fake; RunInTx copies state and commits only on success.
type memStore struct {
	mu        sync.Mutex
	rows      map[id.ID]domain.Purchase
	published []domain.Event
	// conflicts makes the next Update calls fail with ErrConcurrentModification.
	conflicts int
}

func newMemStore() *memStore { return &memStore{rows: map[id.ID]domain.Purchase{}} }

func (m *memStore) RunInTx(_ context.Context, fn func(app.Repos) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	tx := &memTx{store: m, rows: make(map[id.ID]domain.Purchase, len(m.rows))}
	for k, v := range m.rows {
		tx.rows[k] = v
	}
	if err := fn(app.Repos{Purchases: tx, Events: tx}); err != nil {
		return err
	}
	m.rows = tx.rows
	m.published = append(m.published, tx.events...)
	return nil
}

func (m *memStore) get(purchaseID id.ID) domain.Purchase {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.rows[purchaseID]
}

type memTx struct {
	store  *memStore
	rows   map[id.ID]domain.Purchase
	events []domain.Event
}

func (t *memTx) Find(_ context.Context, purchaseID id.ID) (domain.Purchase, bool, error) {
	p, ok := t.rows[purchaseID]
	return p, ok, nil
}

func (t *memTx) CountPaid(_ context.Context, userID, courseID id.ID) (int, error) {
	n := 0
	for _, p := range t.rows {
		if p.UserID == userID && p.CourseID == courseID && p.Status == domain.StatusPaid {
			n++
		}
	}
	return n, nil
}

func (t *memTx) ListUnsettled(_ context.Context, pendingBefore time.Time, afterID id.ID, limit int) ([]domain.Purchase, error) {
	var out []domain.Purchase
	for _, p := range t.rows {
		stale := p.Status == domain.StatusPending && p.CreatedAt.Before(pendingBefore)
		if p.ID > afterID && (stale || p.NeedsGrant()) {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (t *memTx) Insert(_ context.Context, p *domain.Purchase) error {
	p.Version = 1
	t.rows[p.ID] = *p
	return nil
}

func (t *memTx) Update(_ context.Context, p *domain.Purchase) error {
	if t.store.conflicts > 0 {
		t.store.conflicts--
		return app.ErrConcurrentModification
	}
	cur, ok := t.rows[p.ID]
	if !ok || cur.Version != p.Version {
		return app.ErrConcurrentModification
	}
	p.Version++
	t.rows[p.ID] = *p
	return nil
}

func (t *memTx) Publish(_ context.Context, evs ...domain.Event) error {
	t.events = append(t.events, evs...)
	return nil
}

type catalog map[id.ID]app.CourseFacts

func (c catalog) CourseFacts(_ context.Context, courseID id.ID) (app.CourseFacts, error) {
	f, ok := c[courseID]
	if !ok {
		return app.CourseFacts{}, app.ErrNotFound
	}
	return f, nil
}

type fakeEnrollments struct {
	enrolled map[[2]id.ID]bool // course, user
	grantErr error
	grants   int
}

func (e *fakeEnrollments) IsEnrolled(_ context.Context, courseID, userID id.ID) (bool, error) {
	return e.enrolled[[2]id.ID{courseID, userID}], nil
}

func (e *fakeEnrollments) GrantPurchased(_ context.Context, courseID, userID id.ID) error {
	e.grants++
	if e.grantErr != nil {
		return e.grantErr
	}
	e.enrolled[[2]id.ID{courseID, userID}] = true
	return nil
}

// fakeGateway reports results by gateway reference; a missing result is pending.
type fakeGateway struct {
	checkoutErr error
	results     map[string]app.Result
	statusErr   error
	statusCalls int
}

func (g *fakeGateway) StartCheckout(_ context.Context, p domain.Purchase) (app.Checkout, error) {
	if g.checkoutErr != nil {
		return app.Checkout{}, g.checkoutErr
	}
	return app.Checkout{Method: "POST", URL: "https://pay.test/form", Fields: map[string]string{"ref": p.GatewayRef}}, nil
}

func (g *fakeGateway) FetchStatus(_ context.Context, p domain.Purchase) (app.Result, error) {
	g.statusCalls++
	if g.statusErr != nil {
		return app.Result{}, g.statusErr
	}
	return g.results[p.GatewayRef], nil
}
```

- [ ] **Step 2: Write the failing service tests**

Create `internal/payment/app/service_test.go`:

```go
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
		freeCourse:  {Published: true},
		paidCourse:  {Published: true, Price: price},
		draftCourse: {Published: false, Price: price},
	}
	f.svc = app.NewService(f.store, courses, f.enroll, map[string]app.Gateway{"esewa": f.gw}, ids, f.clock,
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
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/payment/app/`
Expected: FAIL — undefined `app.Service`, `app.Result`, and the other app identifiers.

- [ ] **Step 4: Write errors and ports**

Create `internal/payment/app/errors.go`:

```go
package app

import "errors"

var (
	ErrNotFound               = errors.New("not found")
	ErrCourseFree             = errors.New("course is free")
	ErrAlreadyEnrolled        = errors.New("already enrolled")
	ErrAlreadyPurchased       = errors.New("already purchased")
	ErrGatewayUnavailable     = errors.New("payment gateway unavailable")
	ErrConcurrentModification = errors.New("concurrent modification")
	ErrInvalidInput           = errors.New("invalid input")
)
```

Create `internal/payment/app/ports.go`:

```go
// Package app contains the payment use cases and the ports they depend on.
package app

import (
	"context"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/payment/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

// Repository reads and writes purchases in the current transaction. Insert sets p.Version to 1.
// Update returns ErrConcurrentModification when p.Version is stale and increments it on success.
type Repository interface {
	Find(ctx context.Context, purchaseID id.ID) (domain.Purchase, bool, error)
	CountPaid(ctx context.Context, userID, courseID id.ID) (int, error)
	// ListUnsettled returns purchases with an ID above afterID that are pending and created
	// before pendingBefore, or paid and not yet granted, in ID order.
	ListUnsettled(ctx context.Context, pendingBefore time.Time, afterID id.ID, limit int) ([]domain.Purchase, error)
	Insert(ctx context.Context, p *domain.Purchase) error
	Update(ctx context.Context, p *domain.Purchase) error
}

// EventPublisher records events in the current transaction.
type EventPublisher interface {
	Publish(ctx context.Context, events ...domain.Event) error
}

// Repos are bound to one transaction.
type Repos struct {
	Purchases Repository
	Events    EventPublisher
}

// TxRunner commits when fn returns nil and rolls back otherwise.
type TxRunner interface {
	RunInTx(ctx context.Context, fn func(Repos) error) error
}

// CourseFacts is what payment knows about a course. A zero Price.AmountMinor means free.
type CourseFacts struct {
	Published bool
	Price     domain.Money
}

// CourseCatalog returns ErrNotFound when the course does not exist.
type CourseCatalog interface {
	CourseFacts(ctx context.Context, courseID id.ID) (CourseFacts, error)
}

// EnrollmentGranter is backed by the enrollment context.
type EnrollmentGranter interface {
	IsEnrolled(ctx context.Context, courseID, userID id.ID) (bool, error)
	// GrantPurchased actively enrolls the user. It is idempotent.
	GrantPurchased(ctx context.Context, courseID, userID id.ID) error
}

// Gateway is one payment provider.
type Gateway interface {
	StartCheckout(ctx context.Context, p domain.Purchase) (Checkout, error)
	FetchStatus(ctx context.Context, p domain.Purchase) (Result, error)
}

// Checkout tells the client how to send the buyer to the gateway.
type Checkout struct {
	Method string
	URL    string
	Fields map[string]string // form fields; empty for a plain redirect
}

type ResultKind int

const (
	ResultPending  ResultKind = iota // not settled yet, or the gateway is unsure
	ResultComplete                   // paid; Result.Txn is the gateway's transaction id
	ResultFailed                     // will not complete
)

// Result is the gateway's authoritative view of one purchase.
type Result struct {
	Kind ResultKind
	Txn  string
}
```

- [ ] **Step 5: Write the service**

Create `internal/payment/app/service.go`:

```go
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/payment/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/clock"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

const (
	// pendingGrace gives the buyer time to finish paying before the reconciler asks the gateway.
	pendingGrace = 15 * time.Minute
	// stalePending is how long a purchase may stay pending before each pass warns about it.
	stalePending = 24 * time.Hour
	// reconcilePage bounds one ListUnsettled read.
	reconcilePage = 50
)

// Service implements the payment use cases.
type Service struct {
	tx       TxRunner
	courses  CourseCatalog
	enroll   EnrollmentGranter
	gateways map[string]Gateway
	ids      *id.Generator
	clock    clock.Clock
	logger   *slog.Logger
}

// NewService keys gateways by name; an unconfigured gateway is simply absent.
func NewService(tx TxRunner, courses CourseCatalog, enroll EnrollmentGranter, gateways map[string]Gateway, ids *id.Generator, c clock.Clock, logger *slog.Logger) *Service {
	return &Service{tx: tx, courses: courses, enroll: enroll, gateways: gateways, ids: ids, clock: c, logger: logger}
}

// Checkout starts a new purchase of a published paid course and returns how to reach the
// gateway. Every call creates a new pending purchase; earlier ones are settled independently.
func (s *Service) Checkout(ctx context.Context, p auth.Principal, courseID id.ID, gateway string) (domain.Purchase, Checkout, error) {
	if gateway == "" {
		return domain.Purchase{}, Checkout{}, fmt.Errorf("%w: gateway is required", ErrInvalidInput)
	}
	gw, ok := s.gateways[gateway]
	if !ok {
		return domain.Purchase{}, Checkout{}, ErrGatewayUnavailable
	}
	c, err := s.courses.CourseFacts(ctx, courseID)
	if err != nil {
		return domain.Purchase{}, Checkout{}, err
	}
	if !c.Published {
		return domain.Purchase{}, Checkout{}, ErrNotFound
	}
	if c.Price.AmountMinor == 0 {
		return domain.Purchase{}, Checkout{}, ErrCourseFree
	}
	enrolled, err := s.enroll.IsEnrolled(ctx, courseID, p.UserID)
	if err != nil {
		return domain.Purchase{}, Checkout{}, err
	}
	if enrolled {
		return domain.Purchase{}, Checkout{}, ErrAlreadyEnrolled
	}
	var purchase domain.Purchase
	err = s.tx.RunInTx(ctx, func(r Repos) error {
		paid, err := r.Purchases.CountPaid(ctx, p.UserID, courseID)
		if err != nil {
			return err
		}
		if paid > 0 {
			return ErrAlreadyPurchased
		}
		var ev domain.Event
		purchase, ev, err = domain.NewPurchase(s.ids.New(), p.UserID, courseID, c.Price, gateway, s.clock.Now())
		if err != nil {
			return err
		}
		if err := r.Purchases.Insert(ctx, &purchase); err != nil {
			return err
		}
		return r.Events.Publish(ctx, ev)
	})
	if err != nil {
		return domain.Purchase{}, Checkout{}, err
	}
	co, err := gw.StartCheckout(ctx, purchase)
	if err != nil {
		// The pending purchase stays; the reconciler fails it once the gateway reports it unknown.
		return domain.Purchase{}, Checkout{}, fmt.Errorf("%w: %w", ErrGatewayUnavailable, err)
	}
	return purchase, co, nil
}

// Confirm asks the gateway about the caller's purchase and records the outcome.
func (s *Service) Confirm(ctx context.Context, p auth.Principal, purchaseID id.ID) (domain.Purchase, error) {
	purchase, err := s.Get(ctx, p, purchaseID)
	if err != nil {
		return domain.Purchase{}, err
	}
	return s.settle(ctx, purchase)
}

// Get returns a purchase to its buyer or a root admin; anyone else gets ErrNotFound.
func (s *Service) Get(ctx context.Context, p auth.Principal, purchaseID id.ID) (domain.Purchase, error) {
	var purchase domain.Purchase
	err := s.tx.RunInTx(ctx, func(r Repos) error {
		var (
			found bool
			err   error
		)
		purchase, found, err = r.Purchases.Find(ctx, purchaseID)
		if err != nil {
			return err
		}
		if !found || !purchase.CanView(p) {
			return ErrNotFound
		}
		return nil
	})
	return purchase, err
}

// Reconcile settles every pending purchase older than pendingGrace and retries every failed
// grant. Errors on single purchases are logged so one bad purchase never blocks the rest.
func (s *Service) Reconcile(ctx context.Context) error {
	now := s.clock.Now()
	var after id.ID
	for {
		var page []domain.Purchase
		err := s.tx.RunInTx(ctx, func(r Repos) error {
			var err error
			page, err = r.Purchases.ListUnsettled(ctx, now.Add(-pendingGrace), after, reconcilePage)
			return err
		})
		if err != nil {
			return err
		}
		for _, p := range page {
			if err := ctx.Err(); err != nil {
				return err
			}
			settled, err := s.settle(ctx, p)
			if err != nil {
				s.logger.WarnContext(ctx, "purchase reconcile failed", "purchase_id", p.ID, "error", err)
				continue
			}
			if settled.Status == domain.StatusPending && now.Sub(settled.CreatedAt) >= stalePending {
				s.logger.WarnContext(ctx, "purchase pending for over 24 hours", "purchase_id", p.ID, "gateway", p.Gateway)
			}
		}
		if len(page) < reconcilePage {
			return nil
		}
		after = page[len(page)-1].ID
	}
}

// settle asks the gateway about an unpaid purchase, records the outcome, and grants the
// enrollment of a paid purchase that was not granted yet. It is idempotent.
func (s *Service) settle(ctx context.Context, p domain.Purchase) (domain.Purchase, error) {
	if p.Status != domain.StatusPaid {
		gw, ok := s.gateways[p.Gateway]
		if !ok {
			return p, ErrGatewayUnavailable
		}
		res, err := gw.FetchStatus(ctx, p)
		if err != nil {
			return p, fmt.Errorf("%w: %w", ErrGatewayUnavailable, err)
		}
		switch res.Kind {
		case ResultComplete:
			p, err = s.update(ctx, p.ID, func(cur *domain.Purchase) (domain.Event, error) {
				return cur.MarkPaid(res.Txn, s.clock.Now())
			})
			if err != nil {
				return p, err
			}
			s.warnIfDuplicate(ctx, p)
		case ResultFailed:
			p, err = s.update(ctx, p.ID, func(cur *domain.Purchase) (domain.Event, error) {
				return cur.MarkFailed(s.clock.Now()), nil
			})
			if err != nil {
				return p, err
			}
		case ResultPending:
		}
	}
	if !p.NeedsGrant() {
		return p, nil
	}
	if err := s.enroll.GrantPurchased(ctx, p.CourseID, p.UserID); err != nil {
		// The purchase stays paid and ungranted; the next confirm or reconcile pass retries.
		s.logger.ErrorContext(ctx, "enrollment grant failed", "purchase_id", p.ID, "error", err)
		return p, nil
	}
	return s.update(ctx, p.ID, func(cur *domain.Purchase) (domain.Event, error) {
		cur.MarkGranted(s.clock.Now())
		return nil, nil
	})
}

// update reloads the purchase, applies change, and writes it with its event in one
// transaction. A stale version from a concurrent writer is retried once on a fresh read.
func (s *Service) update(ctx context.Context, purchaseID id.ID, change func(*domain.Purchase) (domain.Event, error)) (domain.Purchase, error) {
	p, err := s.updateOnce(ctx, purchaseID, change)
	if errors.Is(err, ErrConcurrentModification) {
		p, err = s.updateOnce(ctx, purchaseID, change)
	}
	return p, err
}

func (s *Service) updateOnce(ctx context.Context, purchaseID id.ID, change func(*domain.Purchase) (domain.Event, error)) (domain.Purchase, error) {
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
		before := p
		ev, err := change(&p)
		if err != nil {
			return err
		}
		if p == before {
			return nil
		}
		if err := r.Purchases.Update(ctx, &p); err != nil {
			return err
		}
		if ev == nil {
			return nil
		}
		return r.Events.Publish(ctx, ev)
	})
	return p, err
}

// warnIfDuplicate logs when the buyer now holds more than one paid purchase of the course,
// which happens when two checkouts were both paid. The extra payment is refunded by hand.
func (s *Service) warnIfDuplicate(ctx context.Context, p domain.Purchase) {
	var n int
	err := s.tx.RunInTx(ctx, func(r Repos) error {
		var err error
		n, err = r.Purchases.CountPaid(ctx, p.UserID, p.CourseID)
		return err
	})
	if err != nil {
		s.logger.ErrorContext(ctx, "duplicate payment check failed", "purchase_id", p.ID, "error", err)
		return
	}
	if n > 1 {
		s.logger.WarnContext(ctx, "duplicate paid purchase needs a manual refund",
			"purchase_id", p.ID, "user_id", p.UserID, "course_id", p.CourseID, "paid_count", n)
	}
}
```

- [ ] **Step 6: Run tests and lint**

Run: `go test -race ./internal/payment/... && golangci-lint run ./internal/payment/...`
Expected: PASS, no lint findings.

- [ ] **Step 7: Commit**

```bash
git add internal/payment/app
git commit -m "feat(payment): add checkout, confirm and reconcile use cases"
```

---

### Task 4: eSewa gateway adapter

**Files:**
- Create: `internal/payment/adapters/esewa/esewa.go`
- Test: `internal/payment/adapters/esewa/esewa_test.go`

**Interfaces:**
- Consumes: `app.Gateway`, `app.Checkout`, `app.Result`, `app.ResultKind`, `domain.Purchase`, `domain.Money`.
- Produces: `const esewa.Name = "esewa"`; `type esewa.Config{ProductCode, SecretKey, FormURL, StatusURL, ReturnURL string}`; `func esewa.New(cfg Config, logger *slog.Logger) *Gateway` implementing `app.Gateway`.

- [ ] **Step 1: Write the failing adapter tests**

Create `internal/payment/adapters/esewa/esewa_test.go`:

```go
package esewa_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/santoshkc2200/ioe-backend/internal/payment/adapters/esewa"
	"github.com/santoshkc2200/ioe-backend/internal/payment/app"
	"github.com/santoshkc2200/ioe-backend/internal/payment/domain"
)

// sandboxKey is eSewa's published ePay sandbox secret for product code EPAYTEST.
const sandboxKey = "8gBm/:&EnhH.1/q"

var ctx = context.Background()

func gateway(statusURL string, logs io.Writer) *esewa.Gateway {
	return esewa.New(esewa.Config{
		ProductCode: "EPAYTEST",
		SecretKey:   sandboxKey,
		FormURL:     "https://rc-epay.esewa.com.np/api/epay/main/v2/form",
		StatusURL:   statusURL,
		ReturnURL:   "https://app.test/",
	}, slog.New(slog.NewTextHandler(logs, nil)))
}

func purchase(amountMinor int64, ref string) domain.Purchase {
	return domain.Purchase{ID: 42, UserID: 200, CourseID: 10, Price: domain.Money{AmountMinor: amountMinor, Currency: "NPR"},
		Gateway: "esewa", GatewayRef: ref, Status: domain.StatusPending}
}

func TestStartCheckoutMatchesDocumentedSignature(t *testing.T) {
	co, err := gateway("https://unused.test/", io.Discard).StartCheckout(ctx, purchase(10000, "11-201-13"))
	if err != nil {
		t.Fatal(err)
	}
	if co.Method != http.MethodPost || co.URL != "https://rc-epay.esewa.com.np/api/epay/main/v2/form" {
		t.Fatalf("checkout = %+v", co)
	}
	want := map[string]string{
		"amount":                  "100",
		"tax_amount":              "0",
		"product_service_charge":  "0",
		"product_delivery_charge": "0",
		"total_amount":            "100",
		"transaction_uuid":        "11-201-13",
		"product_code":            "EPAYTEST",
		"success_url":             "https://app.test/payments/42/return",
		"failure_url":             "https://app.test/payments/42/return",
		"signed_field_names":      "total_amount,transaction_uuid,product_code",
		"signature":               "4Ov7pCI1zIOdwtV2BRMUNjz1upIlT/COTxfLhWvVurE=",
	}
	if len(co.Fields) != len(want) {
		t.Fatalf("fields = %v", co.Fields)
	}
	for k, v := range want {
		if co.Fields[k] != v {
			t.Fatalf("%s = %q, want %q", k, co.Fields[k], v)
		}
	}
}

func TestStartCheckoutFormatsRupees(t *testing.T) {
	cases := map[int64]string{1: "0.01", 12505: "125.05", 12550: "125.50", 150000: "1500"}
	for paisa, want := range cases {
		co, err := gateway("https://unused.test/", io.Discard).StartCheckout(ctx, purchase(paisa, "1"))
		if err != nil || co.Fields["total_amount"] != want || co.Fields["amount"] != want {
			t.Fatalf("%d paisa: fields=%v err=%v", paisa, co.Fields, err)
		}
	}
}

func TestStartCheckoutRejectsOtherCurrencies(t *testing.T) {
	p := purchase(100, "1")
	p.Price.Currency = "USD"
	if _, err := gateway("https://unused.test/", io.Discard).StartCheckout(ctx, p); err == nil {
		t.Fatal("USD accepted")
	}
}

// statusServer answers every status request with body and records the query.
func statusServer(t *testing.T, code int, body string, query *url.Values) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if query != nil {
			*query = r.URL.Query()
		}
		if r.Method != http.MethodGet || r.URL.Path != "/api/epay/transaction/status/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/api/epay/transaction/status/"
}

func TestFetchStatusComplete(t *testing.T) {
	var q url.Values
	u := statusServer(t, http.StatusOK,
		`{"product_code":"EPAYTEST","transaction_uuid":"42","total_amount":125.5,"status":"COMPLETE","ref_id":"0001TS9"}`, &q)
	res, err := gateway(u, io.Discard).FetchStatus(ctx, purchase(12550, "42"))
	if err != nil || res != (app.Result{Kind: app.ResultComplete, Txn: "0001TS9"}) {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if q.Get("product_code") != "EPAYTEST" || q.Get("total_amount") != "125.50" || q.Get("transaction_uuid") != "42" {
		t.Fatalf("query = %v", q)
	}
}

func TestFetchStatusMapping(t *testing.T) {
	cases := map[string]app.ResultKind{
		"PENDING":   app.ResultPending,
		"AMBIGUOUS": app.ResultPending,
		"NOT_FOUND": app.ResultFailed,
		"CANCELED":  app.ResultFailed,
	}
	for status, want := range cases {
		// eSewa may omit or null the echo fields for unsettled transactions.
		u := statusServer(t, http.StatusOK, `{"product_code":null,"transaction_uuid":null,"total_amount":null,"status":"`+status+`","ref_id":null}`, nil)
		res, err := gateway(u, io.Discard).FetchStatus(ctx, purchase(10000, "42"))
		if err != nil || res.Kind != want {
			t.Fatalf("%s: res=%+v err=%v", status, res, err)
		}
	}
}

func TestFetchStatusRefundIsLoggedAndPending(t *testing.T) {
	var logs bytes.Buffer
	u := statusServer(t, http.StatusOK, `{"product_code":"EPAYTEST","transaction_uuid":"42","total_amount":100.0,"status":"FULL_REFUND","ref_id":"R"}`, nil)
	res, err := gateway(u, &logs).FetchStatus(ctx, purchase(10000, "42"))
	if err != nil || res.Kind != app.ResultPending {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if !strings.Contains(logs.String(), "FULL_REFUND") {
		t.Fatalf("logs = %s", logs.String())
	}
}

func TestFetchStatusRejectsMismatch(t *testing.T) {
	bodies := map[string]string{
		"amount":       `{"product_code":"EPAYTEST","transaction_uuid":"42","total_amount":1.0,"status":"COMPLETE","ref_id":"T"}`,
		"reference":    `{"product_code":"EPAYTEST","transaction_uuid":"43","total_amount":100.0,"status":"COMPLETE","ref_id":"T"}`,
		"product":      `{"product_code":"OTHER","transaction_uuid":"42","total_amount":100.0,"status":"COMPLETE","ref_id":"T"}`,
		"missing txn":  `{"product_code":"EPAYTEST","transaction_uuid":"42","total_amount":100.0,"status":"COMPLETE","ref_id":""}`,
		"float amount": `{"product_code":"EPAYTEST","transaction_uuid":"42","total_amount":100.001,"status":"COMPLETE","ref_id":"T"}`,
	}
	for name, body := range bodies {
		u := statusServer(t, http.StatusOK, body, nil)
		if res, err := gateway(u, io.Discard).FetchStatus(ctx, purchase(10000, "42")); err == nil {
			t.Fatalf("%s: accepted %+v", name, res)
		}
	}
}

func TestFetchStatusErrors(t *testing.T) {
	cases := map[string]string{
		"non-200":   statusServer(t, http.StatusBadGateway, `{}`, nil),
		"malformed": statusServer(t, http.StatusOK, `not json`, nil),
		"unknown":   statusServer(t, http.StatusOK, `{"status":"SOMETHING_NEW"}`, nil),
	}
	for name, u := range cases {
		if res, err := gateway(u, io.Discard).FetchStatus(ctx, purchase(10000, "42")); err == nil {
			t.Fatalf("%s: res=%+v", name, res)
		}
	}
	if _, err := gateway("http://127.0.0.1:1/status/", io.Discard).FetchStatus(ctx, purchase(10000, "42")); err == nil {
		t.Fatal("unreachable server accepted")
	}
}

func TestFetchStatusErrorsNeverContainTheSecret(t *testing.T) {
	u := statusServer(t, http.StatusInternalServerError, `{}`, nil)
	_, err := gateway(u, io.Discard).FetchStatus(ctx, purchase(10000, "42"))
	if err == nil || strings.Contains(err.Error(), sandboxKey) {
		t.Fatalf("err = %v", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/payment/adapters/esewa/`
Expected: FAIL — package `esewa` has no non-test Go files.

- [ ] **Step 3: Write the adapter**

Create `internal/payment/adapters/esewa/esewa.go`:

```go
// Package esewa implements the eSewa ePay v2 gateway. eSewa never calls the server, so the
// status API is the only source of truth; the browser redirect is ignored.
package esewa

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/santoshkc2200/ioe-backend/internal/payment/app"
	"github.com/santoshkc2200/ioe-backend/internal/payment/domain"
)

// Name is the gateway name stored on purchases and sent by clients.
const Name = "esewa"

const (
	requestTimeout = 10 * time.Second
	maxBodyBytes   = 1 << 16
	signedFields   = "total_amount,transaction_uuid,product_code"
	currency       = "NPR"
)

// Config holds the merchant settings. ReturnURL is the frontend origin: eSewa sends the buyer
// back to {ReturnURL}/payments/{purchaseID}/return after success or failure.
type Config struct {
	ProductCode string
	SecretKey   string
	FormURL     string
	StatusURL   string
	ReturnURL   string
}

// Gateway implements app.Gateway. Errors never include the secret key.
type Gateway struct {
	cfg    Config
	http   *http.Client
	logger *slog.Logger
}

var _ app.Gateway = (*Gateway)(nil)

func New(cfg Config, logger *slog.Logger) *Gateway {
	cfg.ReturnURL = strings.TrimRight(cfg.ReturnURL, "/")
	return &Gateway{
		cfg:    cfg,
		http:   &http.Client{Timeout: requestTimeout, Transport: otelhttp.NewTransport(http.DefaultTransport)},
		logger: logger,
	}
}

// StartCheckout returns the signed form the browser posts to eSewa.
func (g *Gateway) StartCheckout(_ context.Context, p domain.Purchase) (app.Checkout, error) {
	total, err := rupees(p.Price)
	if err != nil {
		return app.Checkout{}, err
	}
	ret := g.cfg.ReturnURL + "/payments/" + p.ID.String() + "/return"
	return app.Checkout{Method: http.MethodPost, URL: g.cfg.FormURL, Fields: map[string]string{
		"amount":                  total,
		"tax_amount":              "0",
		"product_service_charge":  "0",
		"product_delivery_charge": "0",
		"total_amount":            total,
		"transaction_uuid":        p.GatewayRef,
		"product_code":            g.cfg.ProductCode,
		"success_url":             ret,
		"failure_url":             ret,
		"signed_field_names":      signedFields,
		"signature":               g.sign(total, p.GatewayRef),
	}}, nil
}

func (g *Gateway) sign(total, transactionUUID string) string {
	mac := hmac.New(sha256.New, []byte(g.cfg.SecretKey))
	_, _ = mac.Write([]byte("total_amount=" + total + ",transaction_uuid=" + transactionUUID + ",product_code=" + g.cfg.ProductCode))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

type statusResponse struct {
	ProductCode     string      `json:"product_code"`
	TransactionUUID string      `json:"transaction_uuid"`
	TotalAmount     json.Number `json:"total_amount"`
	Status          string      `json:"status"`
	RefID           string      `json:"ref_id"`
}

// FetchStatus asks eSewa's status API about the purchase.
func (g *Gateway) FetchStatus(ctx context.Context, p domain.Purchase) (app.Result, error) {
	total, err := rupees(p.Price)
	if err != nil {
		return app.Result{}, err
	}
	u, err := url.Parse(g.cfg.StatusURL)
	if err != nil {
		return app.Result{}, fmt.Errorf("esewa status: parse url: %w", err)
	}
	q := u.Query()
	q.Set("product_code", g.cfg.ProductCode)
	q.Set("total_amount", total)
	q.Set("transaction_uuid", p.GatewayRef)
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return app.Result{}, fmt.Errorf("esewa status: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := g.http.Do(req)
	if err != nil {
		return app.Result{}, fmt.Errorf("esewa status: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return app.Result{}, fmt.Errorf("esewa status: HTTP %d", resp.StatusCode)
	}
	var body statusResponse
	dec := json.NewDecoder(io.LimitReader(resp.Body, maxBodyBytes))
	dec.UseNumber()
	if err := dec.Decode(&body); err != nil {
		return app.Result{}, fmt.Errorf("esewa status: decode: %w", err)
	}
	switch body.Status {
	case "COMPLETE":
		if err := g.matches(body, p); err != nil {
			return app.Result{}, err
		}
		return app.Result{Kind: app.ResultComplete, Txn: body.RefID}, nil
	case "PENDING", "AMBIGUOUS":
		return app.Result{Kind: app.ResultPending}, nil
	case "NOT_FOUND", "CANCELED":
		return app.Result{Kind: app.ResultFailed}, nil
	case "FULL_REFUND", "PARTIAL_REFUND":
		g.logger.WarnContext(ctx, "esewa reports a refund; access is unchanged", "purchase_id", p.ID, "status", body.Status)
		return app.Result{Kind: app.ResultPending}, nil
	default:
		return app.Result{}, fmt.Errorf("esewa status: unknown status %q", body.Status)
	}
}

// matches checks that a COMPLETE response is about this purchase and its exact amount.
func (g *Gateway) matches(body statusResponse, p domain.Purchase) error {
	amount, err := paisa(body.TotalAmount)
	if err != nil || amount != p.Price.AmountMinor || body.TransactionUUID != p.GatewayRef ||
		body.ProductCode != g.cfg.ProductCode || body.RefID == "" {
		return fmt.Errorf("esewa status: response does not match purchase %s", p.ID)
	}
	return nil
}

// rupees formats NPR paisa as eSewa expects: 10000 → "100", 12550 → "125.50".
func rupees(m domain.Money) (string, error) {
	if m.Currency != currency || m.AmountMinor <= 0 {
		return "", fmt.Errorf("esewa: unsupported amount %d %q", m.AmountMinor, m.Currency)
	}
	whole, frac := m.AmountMinor/100, m.AmountMinor%100
	if frac == 0 {
		return strconv.FormatInt(whole, 10), nil
	}
	return fmt.Sprintf("%d.%02d", whole, frac), nil
}

// paisa parses a decimal rupee amount such as "100", "100.0" or "125.5" without floating point.
func paisa(n json.Number) (int64, error) {
	whole, frac, _ := strings.Cut(n.String(), ".")
	frac = strings.TrimRight(frac, "0")
	if len(frac) > 2 {
		return 0, fmt.Errorf("esewa: amount %q has more than two decimals", n)
	}
	frac += strings.Repeat("0", 2-len(frac))
	w, err := strconv.ParseInt(whole, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("esewa: amount %q: %w", n, err)
	}
	f, err := strconv.ParseInt(frac, 10, 64)
	if err != nil || w < 0 {
		return 0, fmt.Errorf("esewa: amount %q is invalid", n)
	}
	return w*100 + f, nil
}
```

- [ ] **Step 4: Run tests and lint**

Run: `go test -race ./internal/payment/adapters/esewa/ && golangci-lint run ./internal/payment/...`
Expected: PASS, no lint findings.

- [ ] **Step 5: Commit**

```bash
git add internal/payment/adapters/esewa
git commit -m "feat(payment): add eSewa ePay gateway"
```

---

### Task 5: Purchase persistence

**Files:**
- Create: `migrations/00009_payment.sql`
- Create: `internal/payment/adapters/postgres/queries.sql`, `postgres.go`, `purchases.go`
- Create (generated): `internal/payment/adapters/postgres/sqlcgen/`
- Modify: `sqlc.yaml`, every `internal/*/adapters/postgres/sqlcgen/models.go` (regenerated)
- Test: `internal/payment/adapters/postgres/postgres_integration_test.go`

**Interfaces:**
- Consumes: `app.Repository`, `app.EventPublisher`, `app.Repos`, `app.TxRunner`, `app.ErrConcurrentModification`, domain types.
- Produces: `func postgres.NewTxRunner(pool *pgxpool.Pool) *TxRunner` implementing `app.TxRunner`.

- [ ] **Step 1: Write the migration**

Create `migrations/00009_payment.sql`:

```sql
-- +goose Up
CREATE SCHEMA payment;

CREATE TABLE payment.purchases (
  id           bigint PRIMARY KEY,
  user_id      bigint NOT NULL,
  course_id    bigint NOT NULL,
  amount_minor bigint NOT NULL CHECK (amount_minor > 0),
  currency     text NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  gateway      text NOT NULL CHECK (gateway <> ''),
  gateway_ref  text NOT NULL,
  gateway_txn  text NOT NULL DEFAULT '',
  status       text NOT NULL CHECK (status IN ('pending', 'paid', 'failed')),
  created_at   timestamptz NOT NULL,
  settled_at   timestamptz,
  granted_at   timestamptz,
  version      bigint NOT NULL,
  CONSTRAINT purchases_gateway_ref_unique UNIQUE (gateway, gateway_ref),
  CONSTRAINT purchases_settled_consistent CHECK ((status = 'pending') = (settled_at IS NULL)),
  CONSTRAINT purchases_paid_txn CHECK ((status = 'paid') = (gateway_txn <> '')),
  CONSTRAINT purchases_granted_paid CHECK (granted_at IS NULL OR status = 'paid')
);

CREATE INDEX purchases_user_course ON payment.purchases (user_id, course_id);
CREATE INDEX purchases_unsettled ON payment.purchases (id)
  WHERE status = 'pending' OR (status = 'paid' AND granted_at IS NULL);

-- +goose Down
DROP SCHEMA payment CASCADE;
```

Note: `purchases_paid_txn` means a failed purchase must have an empty `gateway_txn`; `MarkFailed` never sets one, and failed → paid sets it.

- [ ] **Step 2: Write the queries and sqlc entry**

Create `internal/payment/adapters/postgres/queries.sql`:

```sql
-- name: GetPurchase :one
SELECT * FROM payment.purchases WHERE id = $1;

-- name: CountPaidPurchases :one
SELECT count(*) FROM payment.purchases WHERE user_id = $1 AND course_id = $2 AND status = 'paid';

-- name: ListUnsettledPurchases :many
SELECT * FROM payment.purchases
WHERE id > sqlc.arg(after_id)::bigint
  AND ((status = 'pending' AND created_at < sqlc.arg(pending_before)::timestamptz)
       OR (status = 'paid' AND granted_at IS NULL))
ORDER BY id
LIMIT sqlc.arg(page_limit)::bigint;

-- name: InsertPurchase :exec
INSERT INTO payment.purchases
  (id, user_id, course_id, amount_minor, currency, gateway, gateway_ref, gateway_txn, status,
   created_at, settled_at, granted_at, version)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, 1);

-- name: UpdatePurchase :execrows
UPDATE payment.purchases
SET gateway_txn = $3, status = $4, settled_at = $5, granted_at = $6, version = version + 1
WHERE id = $1 AND version = $2;
```

Append to `sqlc.yaml` under `sql:`:

```yaml
  - engine: postgresql
    schema: migrations
    queries: internal/payment/adapters/postgres/queries.sql
    gen:
      go:
        package: sqlcgen
        out: internal/payment/adapters/postgres/sqlcgen
        sql_package: pgx/v5
        overrides:
          - db_type: timestamptz
            go_type: time.Time
          - db_type: timestamptz
            nullable: true
            go_type:
              import: time
              type: Time
              pointer: true
```

- [ ] **Step 3: Generate**

Run: `make sqlc`
Expected: creates `internal/payment/adapters/postgres/sqlcgen/{db.go,models.go,queries.sql.go}` with `PaymentPurchase`, `InsertPurchaseParams`, `UpdatePurchaseParams`, `CountPaidPurchasesParams`, `ListUnsettledPurchasesParams{AfterID int64, PendingBefore time.Time, PageLimit int64}`; every other context's `sqlcgen/models.go` gains `PaymentPurchase`. If a generated field name differs, use the generated name in Step 5.

- [ ] **Step 4: Write the failing integration tests**

Create `internal/payment/adapters/postgres/postgres_integration_test.go`:

```go
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
	p, ev, err := domain.NewPurchase(f.ids.New(), userID, courseID, npr, "esewa", at)
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
		"UPDATE payment.purchases SET status = 'paid', settled_at = now() WHERE id = $1",                // paid without txn
		"UPDATE payment.purchases SET status = 'failed' WHERE id = $1",                                   // settled without settled_at
		"UPDATE payment.purchases SET granted_at = now() WHERE id = $1",                                  // granted while pending
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
```

- [ ] **Step 5: Write the adapter**

Create `internal/payment/adapters/postgres/postgres.go`:

```go
// Package postgres implements payment persistence on the payment schema.
package postgres

import (
	"context"
	"encoding/json"

	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/santoshkc2200/ioe-backend/internal/payment/adapters/postgres/sqlcgen"
	"github.com/santoshkc2200/ioe-backend/internal/payment/app"
	"github.com/santoshkc2200/ioe-backend/internal/payment/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/outbox"
)

// TxRunner runs payment use cases in one PostgreSQL transaction.
type TxRunner struct{ pool *pgxpool.Pool }

func NewTxRunner(pool *pgxpool.Pool) *TxRunner { return &TxRunner{pool: pool} }

func (r *TxRunner) RunInTx(ctx context.Context, fn func(app.Repos) error) error {
	return pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		return fn(app.Repos{Purchases: purchases{q: sqlcgen.New(tx)}, Events: events{tx: tx}})
	})
}

type events struct{ tx pgx.Tx }

// Publish writes each event to the outbox in the current transaction, topic = event name.
func (e events) Publish(ctx context.Context, evs ...domain.Event) error {
	for _, ev := range evs {
		payload, err := json.Marshal(ev)
		if err != nil {
			return err
		}
		msg := message.NewMessage(uuid.NewString(), payload)
		msg.Metadata.Set("event_name", ev.EventName())
		if err := outbox.Publish(ctx, e.tx, ev.EventName(), msg); err != nil {
			return err
		}
	}
	return nil
}
```

Create `internal/payment/adapters/postgres/purchases.go`:

```go
package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/santoshkc2200/ioe-backend/internal/payment/adapters/postgres/sqlcgen"
	"github.com/santoshkc2200/ioe-backend/internal/payment/app"
	"github.com/santoshkc2200/ioe-backend/internal/payment/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

type purchases struct{ q *sqlcgen.Queries }

func (r purchases) Find(ctx context.Context, purchaseID id.ID) (domain.Purchase, bool, error) {
	row, err := r.q.GetPurchase(ctx, int64(purchaseID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Purchase{}, false, nil
	}
	if err != nil {
		return domain.Purchase{}, false, err
	}
	return toDomain(row), true, nil
}

func (r purchases) CountPaid(ctx context.Context, userID, courseID id.ID) (int, error) {
	n, err := r.q.CountPaidPurchases(ctx, sqlcgen.CountPaidPurchasesParams{UserID: int64(userID), CourseID: int64(courseID)})
	return int(n), err
}

func (r purchases) ListUnsettled(ctx context.Context, pendingBefore time.Time, afterID id.ID, limit int) ([]domain.Purchase, error) {
	rows, err := r.q.ListUnsettledPurchases(ctx, sqlcgen.ListUnsettledPurchasesParams{
		AfterID: int64(afterID), PendingBefore: pendingBefore, PageLimit: int64(limit),
	})
	if err != nil {
		return nil, err
	}
	out := make([]domain.Purchase, len(rows))
	for i, row := range rows {
		out[i] = toDomain(row)
	}
	return out, nil
}

func (r purchases) Insert(ctx context.Context, p *domain.Purchase) error {
	err := r.q.InsertPurchase(ctx, sqlcgen.InsertPurchaseParams{
		ID: int64(p.ID), UserID: int64(p.UserID), CourseID: int64(p.CourseID),
		AmountMinor: p.Price.AmountMinor, Currency: p.Price.Currency, Gateway: p.Gateway,
		GatewayRef: p.GatewayRef, GatewayTxn: p.GatewayTxn, Status: string(p.Status), CreatedAt: p.CreatedAt,
		SettledAt: optionalTime(p.SettledAt), GrantedAt: optionalTime(p.GrantedAt),
	})
	if err != nil {
		return err
	}
	p.Version = 1
	return nil
}

func (r purchases) Update(ctx context.Context, p *domain.Purchase) error {
	n, err := r.q.UpdatePurchase(ctx, sqlcgen.UpdatePurchaseParams{
		ID: int64(p.ID), Version: p.Version, GatewayTxn: p.GatewayTxn, Status: string(p.Status),
		SettledAt: optionalTime(p.SettledAt), GrantedAt: optionalTime(p.GrantedAt),
	})
	if err != nil {
		return err
	}
	if n == 0 {
		return app.ErrConcurrentModification
	}
	p.Version++
	return nil
}

func toDomain(r sqlcgen.PaymentPurchase) domain.Purchase {
	p := domain.Purchase{
		ID: id.ID(r.ID), UserID: id.ID(r.UserID), CourseID: id.ID(r.CourseID),
		Price:   domain.Money{AmountMinor: r.AmountMinor, Currency: r.Currency},
		Gateway: r.Gateway, GatewayRef: r.GatewayRef, GatewayTxn: r.GatewayTxn,
		Status: domain.Status(r.Status), CreatedAt: r.CreatedAt.UTC(), Version: r.Version,
	}
	if r.SettledAt != nil {
		p.SettledAt = r.SettledAt.UTC()
	}
	if r.GrantedAt != nil {
		p.GrantedAt = r.GrantedAt.UTC()
	}
	return p
}

func optionalTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}
```

- [ ] **Step 6: Run the integration tests**

Run: `go test -race -tags integration ./internal/payment/adapters/postgres/`
Expected: PASS (requires a running Docker daemon; if Docker is not running, report that the tests did not run).

- [ ] **Step 7: Verify generated code and lint**

Run: `make sqlc-check && golangci-lint run ./...`
Expected: no diff, no findings.

- [ ] **Step 8: Commit**

```bash
git add migrations/00009_payment.sql sqlc.yaml internal/payment/adapters/postgres internal/*/adapters/postgres/sqlcgen
git commit -m "feat(payment): persist purchases in postgres"
```

---

### Task 6: Payment HTTP API

**Files:**
- Create: `internal/payment/adapters/httpapi/httpapi.go`, `internal/payment/adapters/httpapi/wire.go`
- Test: `internal/payment/adapters/httpapi/httpapi_test.go`

**Interfaces:**
- Consumes: `app.Checkout`, app errors, `domain.Purchase`.
- Produces: `type httpapi.Service interface{ Checkout(...); Confirm(...); Get(...) }` matching `*app.Service`; `type httpapi.Config{RequireAuth httpserver.Middleware; Logger *slog.Logger}`; `func httpapi.New(svc Service, cfg Config) *Handler`; `(*Handler).Register(r *httpserver.Router)`.

- [ ] **Step 1: Write the failing handler tests**

Create `internal/payment/adapters/httpapi/httpapi_test.go`:

```go
package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/payment/adapters/httpapi"
	"github.com/santoshkc2200/ioe-backend/internal/payment/app"
	"github.com/santoshkc2200/ioe-backend/internal/payment/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/httpserver"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
	"github.com/santoshkc2200/ioe-backend/internal/platform/problem"
)

var t0 = time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC)

type stub struct {
	purchase domain.Purchase
	checkout app.Checkout
	err      error

	principal  auth.Principal
	courseID   id.ID
	purchaseID id.ID
	gateway    string
}

func (s *stub) Checkout(_ context.Context, p auth.Principal, courseID id.ID, gateway string) (domain.Purchase, app.Checkout, error) {
	s.principal, s.courseID, s.gateway = p, courseID, gateway
	return s.purchase, s.checkout, s.err
}

func (s *stub) Confirm(_ context.Context, p auth.Principal, purchaseID id.ID) (domain.Purchase, error) {
	s.principal, s.purchaseID = p, purchaseID
	return s.purchase, s.err
}

func (s *stub) Get(_ context.Context, p auth.Principal, purchaseID id.ID) (domain.Purchase, error) {
	s.principal, s.purchaseID = p, purchaseID
	return s.purchase, s.err
}

func fakeAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{UserID: 200, Role: auth.RoleStudent})))
	})
}

func newServer(s *stub) http.Handler {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	r, h := httpserver.NewRouter(httpserver.Options{Logger: logger, AllowedOrigins: []string{"https://app.test"}, ServiceName: "test"})
	httpapi.New(s, httpapi.Config{RequireAuth: fakeAuth, Logger: logger}).Register(r)
	return h
}

func call(h http.Handler, method, path, body string) (int, []byte) {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w.Code, w.Body.Bytes()
}

func decode[T any](t *testing.T, b []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("decode %s: %v", b, err)
	}
	return v
}

var pending = domain.Purchase{ID: 1, UserID: 200, CourseID: 10, Price: domain.Money{AmountMinor: 150000, Currency: "NPR"},
	Gateway: "esewa", GatewayRef: "1", Status: domain.StatusPending, CreatedAt: t0, Version: 1}

func TestCheckoutStatusAndShape(t *testing.T) {
	s := &stub{purchase: pending, checkout: app.Checkout{Method: "POST", URL: "https://pay.test/form", Fields: map[string]string{"signature": "sig"}}}
	code, body := call(newServer(s), "POST", "/v1/courses/10/purchases", `{"gateway":"esewa"}`)
	if code != http.StatusCreated {
		t.Fatalf("code = %d %s", code, body)
	}
	if s.courseID != 10 || s.gateway != "esewa" || s.principal.UserID != 200 {
		t.Fatalf("call = %+v", s)
	}
	got := decode[struct {
		Purchase map[string]any `json:"purchase"`
		Checkout struct {
			Method string            `json:"method"`
			URL    string            `json:"url"`
			Fields map[string]string `json:"fields"`
		} `json:"checkout"`
	}](t, body)
	want := map[string]any{"id": "1", "course_id": "10", "user_id": "200", "amount_minor": float64(150000), "currency": "NPR",
		"gateway": "esewa", "status": "pending", "created_at": "2026-10-07T00:00:00Z", "settled_at": nil, "granted": false}
	if len(got.Purchase) != len(want) {
		t.Fatalf("purchase keys = %v", got.Purchase)
	}
	for k, v := range want {
		if got.Purchase[k] != v {
			t.Fatalf("%s = %v, want %v (body %s)", k, got.Purchase[k], v, body)
		}
	}
	if got.Checkout.Method != "POST" || got.Checkout.URL != "https://pay.test/form" || got.Checkout.Fields["signature"] != "sig" {
		t.Fatalf("checkout = %+v", got.Checkout)
	}
}

func TestConfirmAndGet(t *testing.T) {
	paid := pending
	paid.Status, paid.GatewayTxn, paid.SettledAt, paid.GrantedAt = domain.StatusPaid, "T", t0.Add(time.Minute), t0.Add(time.Minute)
	s := &stub{purchase: paid}
	for _, req := range []struct{ method, path string }{{"POST", "/v1/purchases/1/confirm"}, {"GET", "/v1/purchases/1"}} {
		code, body := call(newServer(s), req.method, req.path, "")
		if code != http.StatusOK || s.purchaseID != 1 {
			t.Fatalf("%s %s: %d %s", req.method, req.path, code, body)
		}
		got := decode[struct {
			Purchase map[string]any `json:"purchase"`
		}](t, body)
		if got.Purchase["status"] != "paid" || got.Purchase["settled_at"] != "2026-10-07T00:01:00Z" || got.Purchase["granted"] != true {
			t.Fatalf("%s: %s", req.path, body)
		}
		if _, ok := got.Purchase["gateway_txn"]; ok {
			t.Fatalf("gateway_txn leaked: %s", body)
		}
	}
}

func TestMalformedIDsAre404(t *testing.T) {
	for _, req := range []struct{ method, path, body string }{
		{"POST", "/v1/courses/abc/purchases", `{"gateway":"esewa"}`},
		{"POST", "/v1/purchases/abc/confirm", ""},
		{"GET", "/v1/purchases/abc", ""},
	} {
		if code, _ := call(newServer(&stub{}), req.method, req.path, req.body); code != http.StatusNotFound {
			t.Fatalf("%s %s: %d", req.method, req.path, code)
		}
	}
}

func TestCheckoutRequiresJSON(t *testing.T) {
	r := httptest.NewRequest("POST", "/v1/courses/10/purchases", strings.NewReader(`gateway=esewa`))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	newServer(&stub{}).ServeHTTP(w, r)
	if w.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("code = %d", w.Code)
	}
	if code, _ := call(newServer(&stub{}), "POST", "/v1/courses/10/purchases", `{"gateway":"esewa","extra":1}`); code != http.StatusBadRequest {
		t.Fatalf("unknown field code = %d", code)
	}
}

func TestErrorMapping(t *testing.T) {
	cases := []struct {
		err    error
		status int
		typ    string
	}{
		{fmt.Errorf("%w: gateway is required", app.ErrInvalidInput), http.StatusBadRequest, "invalid_input"},
		{app.ErrNotFound, http.StatusNotFound, "not_found"},
		{app.ErrCourseFree, http.StatusConflict, "course_free"},
		{app.ErrAlreadyEnrolled, http.StatusConflict, "already_enrolled"},
		{app.ErrAlreadyPurchased, http.StatusConflict, "already_purchased"},
		{fmt.Errorf("%w: timeout", app.ErrGatewayUnavailable), http.StatusServiceUnavailable, "payment_unavailable"},
		{app.ErrConcurrentModification, http.StatusConflict, "concurrent_modification"},
		{errors.New("boom"), http.StatusInternalServerError, problem.TypeInternal},
	}
	for _, c := range cases {
		code, body := call(newServer(&stub{err: c.err}), "POST", "/v1/courses/10/purchases", `{"gateway":"esewa"}`)
		p := decode[problem.Problem](t, body)
		if code != c.status || p.Type != c.typ {
			t.Fatalf("%v: %d %s", c.err, code, body)
		}
		if c.status == http.StatusServiceUnavailable && strings.Contains(string(body), "timeout") {
			t.Fatalf("gateway detail leaked: %s", body)
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/payment/adapters/httpapi/`
Expected: FAIL — package `httpapi` has no non-test Go files.

- [ ] **Step 3: Write the handlers**

Create `internal/payment/adapters/httpapi/wire.go`:

```go
package httpapi

import (
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/payment/app"
	"github.com/santoshkc2200/ioe-backend/internal/payment/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

type purchaseWire struct {
	ID          id.ID      `json:"id"`
	CourseID    id.ID      `json:"course_id"`
	UserID      id.ID      `json:"user_id"`
	AmountMinor int64      `json:"amount_minor"`
	Currency    string     `json:"currency"`
	Gateway     string     `json:"gateway"`
	Status      string     `json:"status"`
	CreatedAt   time.Time  `json:"created_at"`
	SettledAt   *time.Time `json:"settled_at"` // null while pending
	Granted     bool       `json:"granted"`
}

type checkoutWire struct {
	Method string            `json:"method"`
	URL    string            `json:"url"`
	Fields map[string]string `json:"fields"`
}

type checkoutRequest struct {
	Gateway string `json:"gateway"`
}

type checkoutResponse struct {
	Purchase purchaseWire `json:"purchase"`
	Checkout checkoutWire `json:"checkout"`
}

type purchaseResponse struct {
	Purchase purchaseWire `json:"purchase"`
}

func toWire(p domain.Purchase) purchaseWire {
	w := purchaseWire{
		ID: p.ID, CourseID: p.CourseID, UserID: p.UserID, AmountMinor: p.Price.AmountMinor, Currency: p.Price.Currency,
		Gateway: p.Gateway, Status: string(p.Status), CreatedAt: p.CreatedAt, Granted: !p.GrantedAt.IsZero(),
	}
	if !p.SettledAt.IsZero() {
		at := p.SettledAt
		w.SettledAt = &at
	}
	return w
}

func toCheckoutWire(c app.Checkout) checkoutWire {
	fields := c.Fields
	if fields == nil {
		fields = map[string]string{}
	}
	return checkoutWire{Method: c.Method, URL: c.URL, Fields: fields}
}
```

Create `internal/payment/adapters/httpapi/httpapi.go`:

```go
// Package httpapi exposes payment over HTTP.
package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/santoshkc2200/ioe-backend/internal/payment/app"
	"github.com/santoshkc2200/ioe-backend/internal/payment/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/httpserver"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
	"github.com/santoshkc2200/ioe-backend/internal/platform/problem"
)

// Service is the payment use-case surface the handlers call.
type Service interface {
	Checkout(ctx context.Context, p auth.Principal, courseID id.ID, gateway string) (domain.Purchase, app.Checkout, error)
	Confirm(ctx context.Context, p auth.Principal, purchaseID id.ID) (domain.Purchase, error)
	Get(ctx context.Context, p auth.Principal, purchaseID id.ID) (domain.Purchase, error)
}

type Config struct {
	RequireAuth httpserver.Middleware
	Logger      *slog.Logger
}

type Handler struct {
	svc Service
	cfg Config
}

func New(svc Service, cfg Config) *Handler { return &Handler{svc: svc, cfg: cfg} }

// Register mounts the payment routes. Every route requires authentication.
func (h *Handler) Register(r *httpserver.Router) {
	a := func(f http.HandlerFunc) http.Handler { return h.cfg.RequireAuth(f) }
	r.Handle("POST /v1/courses/{courseID}/purchases", a(h.checkout))
	r.Handle("POST /v1/purchases/{purchaseID}/confirm", a(h.confirm))
	r.Handle("GET /v1/purchases/{purchaseID}", a(h.get))
}

func (h *Handler) checkout(w http.ResponseWriter, r *http.Request) {
	courseID, ok := pathID(w, r, "courseID")
	if !ok {
		return
	}
	var req checkoutRequest
	if !httpserver.DecodeJSON(w, r, &req) {
		return
	}
	p, co, err := h.svc.Checkout(r.Context(), principal(r), courseID, req.Gateway)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, checkoutResponse{Purchase: toWire(p), Checkout: toCheckoutWire(co)})
}

func (h *Handler) confirm(w http.ResponseWriter, r *http.Request) {
	purchaseID, ok := pathID(w, r, "purchaseID")
	if !ok {
		return
	}
	p, err := h.svc.Confirm(r.Context(), principal(r), purchaseID)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, purchaseResponse{Purchase: toWire(p)})
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	purchaseID, ok := pathID(w, r, "purchaseID")
	if !ok {
		return
	}
	p, err := h.svc.Get(r.Context(), principal(r), purchaseID)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, purchaseResponse{Purchase: toWire(p)})
}

// pathID parses a path value. A malformed ID names no resource, so it is a 404.
func pathID(w http.ResponseWriter, r *http.Request, name string) (id.ID, bool) {
	v, err := id.Parse(r.PathValue(name))
	if err != nil {
		problem.Write(w, r, http.StatusNotFound, problem.TypeNotFound, "Not Found", "")
		return 0, false
	}
	return v, true
}

func principal(r *http.Request) auth.Principal {
	p, _ := auth.PrincipalFrom(r.Context()) // RequireAuth guarantees presence
	return p
}

type errorMapping struct {
	err    error
	status int
	typ    string
	title  string
}

var errorMappings = []errorMapping{
	{app.ErrInvalidInput, http.StatusBadRequest, "invalid_input", "Invalid Input"},
	{app.ErrNotFound, http.StatusNotFound, "not_found", "Not Found"},
	{app.ErrCourseFree, http.StatusConflict, "course_free", "Course Is Free"},
	{app.ErrAlreadyEnrolled, http.StatusConflict, "already_enrolled", "Already Enrolled"},
	{app.ErrAlreadyPurchased, http.StatusConflict, "already_purchased", "Already Purchased"},
	{app.ErrConcurrentModification, http.StatusConflict, "concurrent_modification", "Concurrent Modification"},
	{app.ErrGatewayUnavailable, http.StatusServiceUnavailable, "payment_unavailable", "Payment Unavailable"},
}

func (h *Handler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	for _, m := range errorMappings {
		if errors.Is(err, m.err) {
			detail := ""
			if m.status == http.StatusBadRequest {
				detail = err.Error()
			}
			if m.status >= http.StatusInternalServerError {
				h.cfg.Logger.WarnContext(r.Context(), "payment gateway unavailable", "error", err)
			}
			problem.Write(w, r, m.status, m.typ, m.title, detail)
			return
		}
	}
	h.cfg.Logger.ErrorContext(r.Context(), "payment request failed", "error", err)
	problem.Write(w, r, http.StatusInternalServerError, problem.TypeInternal, "Internal Server Error", "")
}
```

- [ ] **Step 4: Run tests and lint**

Run: `go test -race ./internal/payment/... && golangci-lint run ./internal/payment/...`
Expected: PASS, no lint findings.

- [ ] **Step 5: Commit**

```bash
git add internal/payment/adapters/httpapi
git commit -m "feat(payment): expose purchases over HTTP"
```

---

### Task 7: eSewa configuration and secret scanning

**Files:**
- Modify: `internal/platform/config/config.go`
- Test: `internal/platform/config/config_test.go`
- Modify: `.env.example`, `.gitleaks.toml`

**Interfaces:**
- Produces: `Config.EsewaProductCode`, `Config.EsewaSecretKey`, `Config.EsewaFormURL`, `Config.EsewaStatusURL`, `Config.PaymentReturnURL` (all `string`); `func (c Config) EsewaEnabled() bool`.

- [ ] **Step 1: Write the failing config tests**

Append to `internal/platform/config/config_test.go`:

```go
// esewaSandboxKey is eSewa's published ePay sandbox secret for product code EPAYTEST.
const esewaSandboxKey = "8gBm/:&EnhH.1/q"

func esewaEnv() map[string]string {
	env := validEnv()
	env["ESEWA_PRODUCT_CODE"] = "EPAYTEST"
	env["ESEWA_SECRET_KEY"] = esewaSandboxKey
	env["ESEWA_FORM_URL"] = "https://rc-epay.esewa.com.np/api/epay/main/v2/form"
	env["ESEWA_STATUS_URL"] = "https://rc.esewa.com.np/api/epay/transaction/status/"
	env["PAYMENT_RETURN_URL"] = "http://localhost:5173"
	return env
}

func TestEsewaDisabledByDefault(t *testing.T) {
	cfg, err := config.LoadFrom(validEnv())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.EsewaEnabled() {
		t.Fatal("eSewa enabled without configuration")
	}
}

func TestEsewaEnabled(t *testing.T) {
	cfg, err := config.LoadFrom(esewaEnv())
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.EsewaEnabled() || cfg.EsewaProductCode != "EPAYTEST" || cfg.EsewaSecretKey != esewaSandboxKey ||
		cfg.EsewaFormURL != "https://rc-epay.esewa.com.np/api/epay/main/v2/form" ||
		cfg.EsewaStatusURL != "https://rc.esewa.com.np/api/epay/transaction/status/" || cfg.PaymentReturnURL != "http://localhost:5173" {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestEsewaConfigRejectsInvalidValues(t *testing.T) {
	cases := []struct {
		name, key, value, want string
	}{
		{"missing secret", "ESEWA_SECRET_KEY", "", "ESEWA_SECRET_KEY: required"},
		{"missing return url", "PAYMENT_RETURN_URL", "", "PAYMENT_RETURN_URL: required"},
		{"bad form url", "ESEWA_FORM_URL", "ftp://esewa.test/form", "ESEWA_FORM_URL"},
		{"status url with query", "ESEWA_STATUS_URL", "https://esewa.test/status/?x=1", "ESEWA_STATUS_URL"},
		{"relative return url", "PAYMENT_RETURN_URL", "/payments", "PAYMENT_RETURN_URL"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			env := esewaEnv()
			env[c.key] = c.value
			_, err := config.LoadFrom(env)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
			if strings.Contains(err.Error(), esewaSandboxKey) {
				t.Fatalf("error leaks the secret: %v", err)
			}
		})
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/platform/config/`
Expected: FAIL — `cfg.EsewaEnabled undefined`.

- [ ] **Step 3: Add the settings and a shared all-or-none check**

In `internal/platform/config/config.go`, add to `Config` after `MediaServiceAPIKey`:

```go
	EsewaProductCode              string   `env:"ESEWA_PRODUCT_CODE"`
	EsewaSecretKey                string   `env:"ESEWA_SECRET_KEY"`
	EsewaFormURL                  string   `env:"ESEWA_FORM_URL"`
	EsewaStatusURL                string   `env:"ESEWA_STATUS_URL"`
	PaymentReturnURL              string   `env:"PAYMENT_RETURN_URL"`
```

Add after `MediaEnabled`:

```go
// EsewaEnabled reports whether the eSewa gateway is configured.
// LoadFrom guarantees that all five eSewa variables are set or none is.
func (c Config) EsewaEnabled() bool { return c.EsewaProductCode != "" }
```

In `validate`, after `errs = append(errs, c.validateMedia()...)`:

```go
	errs = append(errs, c.validateEsewa()...)
```

Replace `validateMedia` with this version, and add `envVar`, `allOrNone` and `validateEsewa`:

```go
type envVar struct{ name, value string }

// allOrNone reports whether any of vars is set and, when some but not all are, one error per
// missing variable.
func allOrNone(vars []envVar) (bool, []error) {
	var set, missing []string
	for _, v := range vars {
		if v.value == "" {
			missing = append(missing, v.name)
		} else {
			set = append(set, v.name)
		}
	}
	if len(set) == 0 {
		return false, nil
	}
	var errs []error
	for _, name := range missing {
		errs = append(errs, fmt.Errorf("%s: required when %s is set", name, strings.Join(set, " and ")))
	}
	return true, errs
}

const minMediaAPIKeyLen = 32

func (c *Config) validateMedia() []error {
	set, errs := allOrNone([]envVar{
		{"MEDIA_SERVICE_BASE_URL", c.MediaServiceBaseURL},
		{"MEDIA_SERVICE_PUBLIC_URL", c.MediaServicePublicURL},
		{"MEDIA_SERVICE_API_KEY", c.MediaServiceAPIKey},
	})
	if !set || len(errs) > 0 {
		return errs
	}
	if !isBaseURL(c.MediaServiceBaseURL) {
		errs = append(errs, fmt.Errorf("MEDIA_SERVICE_BASE_URL: %q is not an absolute http(s) URL without query or fragment", c.MediaServiceBaseURL))
	}
	if !isBaseURL(c.MediaServicePublicURL) {
		errs = append(errs, fmt.Errorf("MEDIA_SERVICE_PUBLIC_URL: %q is not an absolute http(s) URL without query or fragment", c.MediaServicePublicURL))
	}
	if len(c.MediaServiceAPIKey) < minMediaAPIKeyLen {
		errs = append(errs, fmt.Errorf("MEDIA_SERVICE_API_KEY: must be at least %d characters", minMediaAPIKeyLen))
	}
	return errs
}

// validateEsewa never echoes ESEWA_SECRET_KEY.
func (c *Config) validateEsewa() []error {
	set, errs := allOrNone([]envVar{
		{"ESEWA_PRODUCT_CODE", c.EsewaProductCode},
		{"ESEWA_SECRET_KEY", c.EsewaSecretKey},
		{"ESEWA_FORM_URL", c.EsewaFormURL},
		{"ESEWA_STATUS_URL", c.EsewaStatusURL},
		{"PAYMENT_RETURN_URL", c.PaymentReturnURL},
	})
	if !set || len(errs) > 0 {
		return errs
	}
	for _, v := range []envVar{
		{"ESEWA_FORM_URL", c.EsewaFormURL},
		{"ESEWA_STATUS_URL", c.EsewaStatusURL},
		{"PAYMENT_RETURN_URL", c.PaymentReturnURL},
	} {
		if !isBaseURL(v.value) {
			errs = append(errs, fmt.Errorf("%s: %q is not an absolute http(s) URL without query or fragment", v.name, v.value))
		}
	}
	return errs
}
```

- [ ] **Step 4: Document the sandbox settings and allowlist the published key**

Append to `.env.example`:

```sh

# eSewa ePay. Leave all five empty to disable payments (startup logs a warning).
# These are eSewa's published sandbox values for product code EPAYTEST, not real credentials.
# PAYMENT_RETURN_URL is the frontend origin: eSewa sends the buyer back to
# {PAYMENT_RETURN_URL}/payments/{purchaseID}/return, and that page calls
# POST /v1/purchases/{purchaseID}/confirm.
ESEWA_PRODUCT_CODE=EPAYTEST
ESEWA_SECRET_KEY=8gBm/:&EnhH.1/q
ESEWA_FORM_URL=https://rc-epay.esewa.com.np/api/epay/main/v2/form
ESEWA_STATUS_URL=https://rc.esewa.com.np/api/epay/transaction/status/
PAYMENT_RETURN_URL=http://localhost:5173
```

Append to `.gitleaks.toml`:

```toml

[[allowlists]]
description = "eSewa's publicly documented ePay sandbox secret (product code EPAYTEST), used by .env.example, tests and docs"
regexTarget = "secret"
regexes = ['''8gBm/:&EnhH\.1/q''']
```

- [ ] **Step 5: Run tests and the secret scan**

Run: `go test -race ./internal/platform/config/ && make secrets`
Expected: PASS; gitleaks reports no leaks.

- [ ] **Step 6: Commit**

```bash
git add internal/platform/config .env.example .gitleaks.toml
git commit -m "feat(config): add eSewa sandbox settings"
```

---

### Task 8: Wire payments, run the reconciler, and document the API

**Files:**
- Create: `cmd/api/payment.go`
- Modify: `cmd/api/enrollment.go`, `cmd/api/app.go`, `cmd/api/main.go`
- Test: `cmd/api/e2e_integration_test.go`
- Modify: `api/openapi.yaml`, `README.md`

**Interfaces:**
- Consumes: `courseauthoringapp.CourseService.Facts` (with `Price`), `enrollmentapp.AccessQuery.IsActivelyEnrolled`, `enrollmentapp.Service.EnrollPurchased`, `paymentapp.NewService`, `paymentapp.Service.Reconcile`, `esewa.New`, `esewa.Name`, `paymentpg.NewTxRunner`, `paymenthttp.New`, `config.Config.EsewaEnabled`.
- Produces: `application.payments *paymentapp.Service`; `func runReconciler(ctx context.Context, svc *paymentapp.Service, logger *slog.Logger)`.

- [ ] **Step 1: Write the failing end-to-end tests**

Append to `cmd/api/e2e_integration_test.go`:

```go
// fakeEsewa answers eSewa status requests from a status map keyed by transaction_uuid.
type fakeEsewa struct {
	mu     sync.Mutex
	status map[string]string
}

func (f *fakeEsewa) set(ref, status string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status[ref] = status
}

func (f *fakeEsewa) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	ref := q.Get("transaction_uuid")
	f.mu.Lock()
	status, ok := f.status[ref]
	f.mu.Unlock()
	if !ok {
		status = "NOT_FOUND"
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = fmt.Fprintf(w, `{"product_code":%q,"transaction_uuid":%q,"total_amount":%s,"status":%q,"ref_id":"REF-%s"}`,
		q.Get("product_code"), ref, q.Get("total_amount"), status, ref)
}

func esewaConfig(cfg config.Config, statusURL string) config.Config {
	cfg.EsewaProductCode = "EPAYTEST"
	cfg.EsewaSecretKey = "8gBm/:&EnhH.1/q"
	cfg.EsewaFormURL = "https://rc-epay.esewa.com.np/api/epay/main/v2/form"
	cfg.EsewaStatusURL = statusURL
	cfg.PaymentReturnURL = appOrigin
	return cfg
}

func TestPaymentEndToEnd(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pool := pgtest.New(t)
	google := googletest.NewIssuer(t)
	fake := &fakeEsewa{status: map[string]string{}}
	esewaSrv := httptest.NewServer(fake)
	defer esewaSrv.Close()
	cfg := esewaConfig(baseConfig(t, google), esewaSrv.URL+"/api/epay/transaction/status/")
	a, err := buildApp(ctx, cfg, slog.New(slog.NewJSONHandler(io.Discard, nil)), pool)
	if err != nil {
		t.Fatal(err)
	}
	defer a.forwarder.Close()
	srv := httptest.NewServer(a.handler)
	defer srv.Close()
	c := client{t: t, base: srv.URL}
	bearer := func(tok string) map[string]string { return map[string]string{"Authorization": "Bearer " + tok} }
	signIn := func(sub, email string) string {
		t.Helper()
		tok := google.Sign(t, googletest.Claims(sub, email, "web-client", time.Now()))
		resp, body := c.do(http.MethodPost, "/v1/auth/google", `{"id_token":"`+tok+`"}`, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("sign in %s: %d %v", email, resp.StatusCode, body)
		}
		return body["access_token"].(string)
	}
	admin := bearer(signIn("sub-admin", "admin@example.com"))
	student := bearer(signIn("sub-student", "student@example.com"))
	stranger := bearer(signIn("sub-stranger", "stranger@example.com"))

	publish := func(priced bool) (string, string) {
		t.Helper()
		resp, body := c.do(http.MethodPost, "/v1/courses", `{"title":"Go","description":"d"}`, admin)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("create: %d %v", resp.StatusCode, body)
		}
		courseID := body["id"].(string)
		if priced {
			if resp, body = c.do(http.MethodPost, "/v1/courses/"+courseID+"/price", `{"amount_minor":150000,"currency":"NPR"}`, admin); resp.StatusCode != http.StatusOK {
				t.Fatalf("price: %d %v", resp.StatusCode, body)
			}
		}
		resp, body = c.do(http.MethodPost, "/v1/courses/"+courseID+"/lectures", `{"title":"L1","text_body":"<p>x</p>"}`, admin)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("lecture: %d %v", resp.StatusCode, body)
		}
		lectures := body["lectures"].([]any)
		lectureID := lectures[len(lectures)-1].(map[string]any)["id"].(string)
		if resp, _ = c.do(http.MethodPost, "/v1/courses/"+courseID+"/publish", "", admin); resp.StatusCode != http.StatusNoContent {
			t.Fatalf("publish: %d", resp.StatusCode)
		}
		return courseID, lectureID
	}
	read := func(who map[string]string, courseID, lectureID string) int {
		resp, _ := c.do(http.MethodGet, "/v1/courses/"+courseID+"/lectures/"+lectureID+"/content", "", who)
		return resp.StatusCode
	}
	checkout := func(who map[string]string, courseID string) (*http.Response, map[string]any) {
		return c.do(http.MethodPost, "/v1/courses/"+courseID+"/purchases", `{"gateway":"esewa"}`, who)
	}

	freeID, _ := publish(false)
	if resp, body := checkout(student, freeID); resp.StatusCode != http.StatusConflict || body["type"] != "course_free" {
		t.Fatalf("free checkout: %d %v", resp.StatusCode, body)
	}

	courseID, lectureID := publish(true)
	resp, body := checkout(student, courseID)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("checkout: %d %v", resp.StatusCode, body)
	}
	purchase := body["purchase"].(map[string]any)
	form := body["checkout"].(map[string]any)
	fields := form["fields"].(map[string]any)
	purchaseID := purchase["id"].(string)
	if purchase["status"] != "pending" || purchase["amount_minor"] != float64(150000) || form["method"] != "POST" ||
		form["url"] != "https://rc-epay.esewa.com.np/api/epay/main/v2/form" || fields["total_amount"] != "1500" ||
		fields["transaction_uuid"] != purchaseID || fields["success_url"] != appOrigin+"/payments/"+purchaseID+"/return" ||
		fields["signature"] == "" {
		t.Fatalf("checkout body = %v", body)
	}
	if code := read(student, courseID, lectureID); code != http.StatusForbidden {
		t.Fatalf("read before paying: %d", code)
	}

	confirm := func(who map[string]string, id string) (*http.Response, map[string]any) {
		return c.do(http.MethodPost, "/v1/purchases/"+id+"/confirm", "", who)
	}
	fake.set(purchaseID, "PENDING")
	if resp, body = confirm(student, purchaseID); resp.StatusCode != http.StatusOK || body["purchase"].(map[string]any)["status"] != "pending" {
		t.Fatalf("pending confirm: %d %v", resp.StatusCode, body)
	}
	fake.set(purchaseID, "COMPLETE")
	resp, body = confirm(student, purchaseID)
	if p := body["purchase"].(map[string]any); resp.StatusCode != http.StatusOK || p["status"] != "paid" || p["granted"] != true {
		t.Fatalf("complete confirm: %d %v", resp.StatusCode, body)
	}
	if code := read(student, courseID, lectureID); code != http.StatusOK {
		t.Fatalf("read after paying: %d", code)
	}
	if resp, body = checkout(student, courseID); resp.StatusCode != http.StatusConflict || body["type"] != "already_enrolled" {
		t.Fatalf("second checkout: %d %v", resp.StatusCode, body)
	}
	if resp, _ = c.do(http.MethodGet, "/v1/purchases/"+purchaseID, "", stranger); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("stranger get: %d", resp.StatusCode)
	}
	if resp, _ = c.do(http.MethodGet, "/v1/purchases/"+purchaseID, "", admin); resp.StatusCode != http.StatusOK {
		t.Fatalf("admin get: %d", resp.StatusCode)
	}

	// The stranger pays but never comes back; the reconciler enrolls them.
	resp, body = checkout(stranger, courseID)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("stranger checkout: %d %v", resp.StatusCode, body)
	}
	strangerPurchase := body["purchase"].(map[string]any)["id"].(string)
	fake.set(strangerPurchase, "COMPLETE")
	strangerPurchaseID, err := strconv.ParseInt(strangerPurchase, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "UPDATE payment.purchases SET created_at = created_at - interval '1 hour' WHERE id = $1", strangerPurchaseID); err != nil {
		t.Fatal(err)
	}
	if err := a.payments.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if code := read(stranger, courseID, lectureID); code != http.StatusOK {
		t.Fatalf("stranger read after reconcile: %d", code)
	}

	var initiated, paid int
	if err := pool.QueryRow(ctx, `SELECT
		count(*) FILTER (WHERE payload->>'destination_topic' = 'payment.purchase.initiated'),
		count(*) FILTER (WHERE payload->>'destination_topic' = 'payment.purchase.paid')
		FROM platform.outbox_messages`).Scan(&initiated, &paid); err != nil {
		t.Fatal(err)
	}
	if initiated != 2 || paid != 2 {
		t.Fatalf("initiated=%d paid=%d", initiated, paid)
	}
}

func TestPaymentsDisabledReturn503(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pool := pgtest.New(t)
	google := googletest.NewIssuer(t)
	a, err := buildApp(ctx, baseConfig(t, google), slog.New(slog.NewJSONHandler(io.Discard, nil)), pool)
	if err != nil {
		t.Fatal(err)
	}
	defer a.forwarder.Close()
	srv := httptest.NewServer(a.handler)
	defer srv.Close()
	c := client{t: t, base: srv.URL}
	tok := google.Sign(t, googletest.Claims("sub-student", "student@example.com", "web-client", time.Now()))
	_, body := c.do(http.MethodPost, "/v1/auth/google", `{"id_token":"`+tok+`"}`, nil)
	headers := map[string]string{"Authorization": "Bearer " + body["access_token"].(string)}
	resp, body := c.do(http.MethodPost, "/v1/courses/1/purchases", `{"gateway":"esewa"}`, headers)
	if resp.StatusCode != http.StatusServiceUnavailable || body["type"] != "payment_unavailable" {
		t.Fatalf("disabled checkout: %d %v", resp.StatusCode, body)
	}
}
```

`fmt`, `strconv` and `sync` are already imported by this file; `config` is imported for `baseConfig`.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -race -tags integration -run 'TestPayment' ./cmd/api/`
Expected: compile error `a.payments undefined`.

- [ ] **Step 3: Return the enrollment service from its registration**

In `cmd/api/enrollment.go`, replace `registerEnrollment`:

```go
// registerEnrollment mounts enrollment and returns its service for contexts that grant access.
func registerEnrollment(r *httpserver.Router, tx enrollmentapp.TxRunner, courses *courseauthoringapp.CourseService, ids *id.Generator, clk clock.Clock, requireAuth httpserver.Middleware, logger *slog.Logger) *enrollmentapp.Service {
	svc := enrollmentapp.NewService(tx, courseCatalog{courses: courses}, ids, clk)
	enrollmenthttp.New(svc, enrollmenthttp.Config{RequireAuth: requireAuth, Logger: logger}).Register(r)
	return svc
}
```

- [ ] **Step 4: Write the payment wiring**

Create `cmd/api/payment.go`:

```go
package main

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	courseauthoringapp "github.com/santoshkc2200/ioe-backend/internal/courseauthoring/app"
	enrollmentapp "github.com/santoshkc2200/ioe-backend/internal/enrollment/app"
	"github.com/santoshkc2200/ioe-backend/internal/payment/adapters/esewa"
	paymenthttp "github.com/santoshkc2200/ioe-backend/internal/payment/adapters/httpapi"
	paymentpg "github.com/santoshkc2200/ioe-backend/internal/payment/adapters/postgres"
	paymentapp "github.com/santoshkc2200/ioe-backend/internal/payment/app"
	paymentdomain "github.com/santoshkc2200/ioe-backend/internal/payment/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/clock"
	"github.com/santoshkc2200/ioe-backend/internal/platform/config"
	"github.com/santoshkc2200/ioe-backend/internal/platform/httpserver"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

// reconcileInterval is how often unconfirmed purchases are settled with their gateway.
const reconcileInterval = 5 * time.Minute

// paymentCourseCatalog lets payment read course publication and price from course authoring.
type paymentCourseCatalog struct{ courses *courseauthoringapp.CourseService }

func (c paymentCourseCatalog) CourseFacts(ctx context.Context, courseID id.ID) (paymentapp.CourseFacts, error) {
	f, err := c.courses.Facts(ctx, courseID)
	if errors.Is(err, courseauthoringapp.ErrNotFound) {
		return paymentapp.CourseFacts{}, paymentapp.ErrNotFound
	}
	if err != nil {
		return paymentapp.CourseFacts{}, err
	}
	return paymentapp.CourseFacts{
		Published: f.Published,
		Price:     paymentdomain.Money{AmountMinor: f.Price.AmountMinor, Currency: f.Price.Currency},
	}, nil
}

// paymentEnrollments lets payment check and grant enrollment.
type paymentEnrollments struct {
	access *enrollmentapp.AccessQuery
	svc    *enrollmentapp.Service
}

func (e paymentEnrollments) IsEnrolled(ctx context.Context, courseID, userID id.ID) (bool, error) {
	return e.access.IsActivelyEnrolled(ctx, courseID, userID)
}

func (e paymentEnrollments) GrantPurchased(ctx context.Context, courseID, userID id.ID) error {
	return e.svc.EnrollPurchased(ctx, courseID, userID)
}

// registerPayment mounts payment routes and returns the service the reconciler runs. Without
// eSewa settings the routes still exist and checkout answers 503 payment_unavailable.
func registerPayment(r *httpserver.Router, pool *pgxpool.Pool, courses *courseauthoringapp.CourseService, access *enrollmentapp.AccessQuery, enrollments *enrollmentapp.Service, ids *id.Generator, clk clock.Clock, cfg config.Config, requireAuth httpserver.Middleware, logger *slog.Logger) *paymentapp.Service {
	gateways := map[string]paymentapp.Gateway{}
	if cfg.EsewaEnabled() {
		gateways[esewa.Name] = esewa.New(esewa.Config{
			ProductCode: cfg.EsewaProductCode,
			SecretKey:   cfg.EsewaSecretKey,
			FormURL:     cfg.EsewaFormURL,
			StatusURL:   cfg.EsewaStatusURL,
			ReturnURL:   cfg.PaymentReturnURL,
		}, logger)
	} else {
		logger.Warn("payments disabled: ESEWA_PRODUCT_CODE, ESEWA_SECRET_KEY, ESEWA_FORM_URL, ESEWA_STATUS_URL and PAYMENT_RETURN_URL are not set")
	}
	svc := paymentapp.NewService(paymentpg.NewTxRunner(pool), paymentCourseCatalog{courses: courses},
		paymentEnrollments{access: access, svc: enrollments}, gateways, ids, clk, logger)
	paymenthttp.New(svc, paymenthttp.Config{RequireAuth: requireAuth, Logger: logger}).Register(r)
	return svc
}

// runReconciler settles unconfirmed purchases now and every reconcileInterval until ctx ends.
func runReconciler(ctx context.Context, svc *paymentapp.Service, logger *slog.Logger) {
	t := time.NewTicker(reconcileInterval)
	defer t.Stop()
	for {
		if err := svc.Reconcile(ctx); err != nil && ctx.Err() == nil {
			logger.ErrorContext(ctx, "purchase reconcile failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
```

- [ ] **Step 5: Wire it into the application**

In `cmd/api/app.go`:

1. Add the import `paymentapp "github.com/santoshkc2200/ioe-backend/internal/payment/app"`.
2. Change the struct:

```go
type application struct {
	handler   http.Handler
	forwarder *outbox.Forwarder
	payments  *paymentapp.Service
}
```

3. Replace the `registerEnrollment(...)` call line with:

```go
	enrollments := registerEnrollment(router, enrollmentTx, courses, ids, clk, identityHandler.RequireAuth, logger)
	payments := registerPayment(router, pool, courses, enrollmentAccess, enrollments, ids, clk, cfg, identityHandler.RequireAuth, logger)
```

4. Change the return to `return &application{handler: handler, forwarder: fw, payments: payments}, nil`.

In `cmd/api/main.go`, add after the forwarder goroutine:

```go
	g.Go(func() error {
		runReconciler(gctx, a.payments, logger)
		return nil
	})
```

- [ ] **Step 6: Run the end-to-end tests**

Run: `go test -race -tags integration -run 'TestPayment|TestEnrollmentEndToEnd' ./cmd/api/`
Expected: PASS (requires Docker; if Docker is not running, report that the tests did not run).

- [ ] **Step 7: Document the API**

In `api/openapi.yaml`:

1. Insert these path items immediately before the line `components:`:

```yaml
  /v1/courses/{courseID}/purchases:
    post:
      summary: Start buying a course
      description: >
        Creates a pending purchase of a published paid course at its current price and returns
        the form the browser posts to the payment gateway. Every call creates a new purchase.
        409 `course_free` for a free course, `already_enrolled` when the caller is actively
        enrolled, `already_purchased` when the caller already paid for it. 503
        `payment_unavailable` when the gateway is not configured or cannot be reached.
        Non-published courses are 404.
      security:
        - bearer: []
      parameters:
        - $ref: "#/components/parameters/CourseID"
      requestBody:
        required: true
        content:
          application/json:
            schema: { $ref: "#/components/schemas/CheckoutRequest" }
      responses:
        "201":
          description: The pending purchase and the gateway form
          content:
            application/json:
              schema: { $ref: "#/components/schemas/CheckoutResponse" }
        "400": { $ref: "#/components/responses/Problem" }
        "401": { $ref: "#/components/responses/Problem" }
        "404": { $ref: "#/components/responses/Problem" }
        "409": { $ref: "#/components/responses/Problem" }
        "413": { $ref: "#/components/responses/Problem" }
        "415": { $ref: "#/components/responses/Problem" }
        "503": { $ref: "#/components/responses/Problem" }
        "405": { $ref: "#/components/responses/MethodNotAllowed" }
        "500": { $ref: "#/components/responses/InternalError" }
  /v1/purchases/{purchaseID}/confirm:
    post:
      summary: Settle a purchase with its gateway
      description: >
        The buyer or a root admin. Asks the gateway for the purchase's status, records it, and
        enrolls the buyer once paid. The frontend calls this from
        `/payments/{purchaseID}/return` after the gateway redirects back, on success or failure.
        Safe to repeat. 503 `payment_unavailable` when the gateway cannot be reached; the
        purchase is then settled later by the server.
      security:
        - bearer: []
      parameters:
        - $ref: "#/components/parameters/PurchaseID"
      responses:
        "200":
          description: The purchase after settlement
          content:
            application/json:
              schema: { $ref: "#/components/schemas/PurchaseResponse" }
        "401": { $ref: "#/components/responses/Problem" }
        "404": { $ref: "#/components/responses/Problem" }
        "409": { $ref: "#/components/responses/Problem" }
        "503": { $ref: "#/components/responses/Problem" }
        "405": { $ref: "#/components/responses/MethodNotAllowed" }
        "500": { $ref: "#/components/responses/InternalError" }
  /v1/purchases/{purchaseID}:
    get:
      summary: Read a purchase
      description: The buyer or a root admin; anyone else gets 404.
      security:
        - bearer: []
      parameters:
        - $ref: "#/components/parameters/PurchaseID"
      responses:
        "200":
          description: The purchase
          content:
            application/json:
              schema: { $ref: "#/components/schemas/PurchaseResponse" }
        "401": { $ref: "#/components/responses/Problem" }
        "404": { $ref: "#/components/responses/Problem" }
        "405": { $ref: "#/components/responses/MethodNotAllowed" }
        "500": { $ref: "#/components/responses/InternalError" }
```

2. Under `components.parameters`, after `AttemptID`:

```yaml
    PurchaseID:
      name: purchaseID
      in: path
      required: true
      schema: { $ref: "#/components/schemas/ID" }
```

3. Under `components.schemas`, after `CancelEnrollmentRequest`:

```yaml
    CheckoutRequest:
      type: object
      required: [gateway]
      properties:
        gateway: { type: string, enum: [esewa] }
    Purchase:
      type: object
      required: [id, course_id, user_id, amount_minor, currency, gateway, status, created_at, settled_at, granted]
      properties:
        id: { $ref: "#/components/schemas/ID" }
        course_id: { $ref: "#/components/schemas/ID" }
        user_id: { $ref: "#/components/schemas/ID" }
        amount_minor: { type: integer, format: int64, minimum: 1, description: Price snapshot in the currency's minor unit }
        currency: { type: string, description: ISO 4217 code }
        gateway: { type: string, enum: [esewa] }
        status: { type: string, enum: [pending, paid, failed] }
        created_at: { type: string, format: date-time }
        settled_at: { type: [string, "null"], format: date-time, description: Null while pending }
        granted: { type: boolean, description: True once the buyer is enrolled }
    Checkout:
      type: object
      required: [method, url, fields]
      properties:
        method: { type: string, enum: [POST] }
        url: { type: string, format: uri, description: Gateway form action }
        fields:
          type: object
          additionalProperties: { type: string }
          description: Form fields to post unchanged, including the signature
    CheckoutResponse:
      type: object
      required: [purchase, checkout]
      properties:
        purchase: { $ref: "#/components/schemas/Purchase" }
        checkout: { $ref: "#/components/schemas/Checkout" }
    PurchaseResponse:
      type: object
      required: [purchase]
      properties:
        purchase: { $ref: "#/components/schemas/Purchase" }
```

4. In `components.schemas.Problem.properties.type.enum`, append:

```yaml
            - payment_required
            - course_free
            - already_enrolled
            - already_purchased
            - payment_unavailable
```

In `README.md`, insert after the `## Enrollment` section:

```markdown
## Payments

Students buy paid courses through eSewa ePay (sandbox only for now).
`POST /v1/courses/{courseID}/purchases` with `{"gateway":"esewa"}` creates a pending purchase
and returns `checkout: {method, url, fields}`; the frontend builds a form from it and submits
it. eSewa sends the buyer back to `{PAYMENT_RETURN_URL}/payments/{purchaseID}/return`, and that
page calls `POST /v1/purchases/{purchaseID}/confirm`. The backend never trusts the redirect: it
asks eSewa's status API and enrolls the buyer once the payment is `COMPLETE`. A background
reconciler settles purchases older than 15 minutes every 5 minutes, so a buyer who closes the
tab after paying is still enrolled. Refunds are made by hand in the eSewa merchant portal; a
manager then cancels the enrollment.

Configure `ESEWA_PRODUCT_CODE`, `ESEWA_SECRET_KEY`, `ESEWA_FORM_URL`, `ESEWA_STATUS_URL` and
`PAYMENT_RETURN_URL`, or none of them to disable payments. `.env.example` carries eSewa's
published sandbox values. To try the sandbox by hand, run the backend and frontend, buy a paid
course, and pay with one of the eSewa test accounts listed at
https://developer.esewa.com.np/pages/Epay (password and OTP are on that page).
```

- [ ] **Step 8: Run all required gates**

Run each and confirm it passes:

```bash
make check
make test-integration
docker compose config
make docker-build
git diff --check
```

Expected: all succeed. `make test-integration` requires Docker; if it cannot run, say so rather than reporting it as passing.

- [ ] **Step 9: Commit**

```bash
git add cmd/api api/openapi.yaml README.md
git commit -m "feat(api): wire eSewa payments and reconcile purchases"
```
