# Payment Follow-ups Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Students and root admins can list purchases, root admins can record offline payments that enroll the student, and every paid purchase sends a "payment received" email.

**Architecture:** The `payment` context gains a course-title snapshot, manual-purchase fields, a `ListByUser` query and a `RecordManual` use case; manual purchases are created paid under gateway `manual` and reuse the existing `settle` grant path. `payment.purchase.paid` gains `course_title` and `manual_method`. The stateless `notification` context consumes that event, looks up the buyer through a new `UserDirectory` port wired to identity in `cmd/api`, and enqueues a rendered email through the existing notification-service client.

**Tech Stack:** Go, PostgreSQL 17, sqlc (pgx/v5), goose, Watermill SQL outbox, `html/template` + `text/template`, testcontainers for integration tests.

**Spec:** `docs/superpowers/specs/2026-10-07-payment-followups-design.md`

## Global Constraints

- Read and follow `AGENTS.md`: a context never imports another context; only `cmd/api` wires contexts together; a context reads and writes only its own schema at runtime.
- `internal/notification/app` imports only the standard library (`notification-app` depguard rule).
- Write the outbox message in the same transaction as the state change.
- Manual methods: `bank_transfer`, `cash`, `other`. Gateway name for manual purchases: `manual`.
- Manual reference: required after trimming, at most 200 bytes. Note: at most 1000 bytes.
- History `limit`: 1..50, default 20. `cursor` is the last purchase ID as a decimal string.
- Paid email idempotency key: `ioe:payment.purchase.paid:<outbox message UUID>:paid-v1`.
- Paid email subject: `Payment received: <course title>`, or `Payment received` when the title is empty.
- New error `ErrForbidden` maps to 403 `forbidden`.
- Use Conventional Commits. Before claiming done, run `make check`, `make test-integration` (Docker required; never report integration tests as passing unless they ran), `docker compose config`, `make docker-build`, `git diff --check`.

## Review Focus

1. A manual reference made only of whitespace must be rejected (`invalid_input`), not stored as an empty-looking receipt. Pinned in Task 2 (`TestRecordManualPurchaseRejects`, case "blank reference").
2. A history cursor that is not a number (`?cursor=abc`) or a limit out of range (`?limit=0`, `?limit=51`, `?limit=x`) must return 400 `invalid_input`, never 500 or an empty page. Pinned in Task 5 (`TestListQueryValidation`).
3. A student reading another student's list through `/v1/users/{id}/purchases` must get 404, not the list. Pinned in Task 4 (`TestListByUserAccess`).
4. A manual purchase must never be sent to a gateway by `Confirm` or `Reconcile` (the `manual` gateway is never registered). Pinned in Task 4 (`TestRecordManualGrantFailureIsReconciled` calls `Reconcile` and `Confirm` on a manual purchase with no `manual` gateway present).
5. A course title or buyer name containing HTML must be escaped in the HTML email but shown as-is in the text email. Pinned in Task 6 (`TestPurchasePaidEscapesHTMLOnly`).

---

## File Map

| File | Change |
|---|---|
| `internal/courseauthoring/app/course_service.go` | `CourseFacts.Title` |
| `internal/payment/domain/purchase.go` | new fields, constants, `RecordManualPurchase`, `NewPurchase` title |
| `internal/payment/domain/events.go` | `PurchasePaid` gains `CourseTitle`, `ManualMethod` |
| `migrations/00012_payment_history.sql` | new columns, constraints, backfill, index |
| `sqlc.yaml` | overrides for `manual_method`, `recorded_by` |
| `internal/payment/adapters/postgres/queries.sql`, `purchases.go`, `sqlcgen/*` | new columns, `ListByUser` |
| `internal/payment/app/ports.go`, `errors.go`, `service.go` | `Title`, `UserDirectory`, `ErrForbidden`, `ListByUser`, `RecordManual` |
| `internal/payment/adapters/httpapi/httpapi.go`, `wire.go` | three routes, wire fields |
| `api/openapi.yaml` | routes and schemas |
| `internal/notification/app/service.go`, new `purchase_paid.go` | `SendPurchasePaid`, `UserDirectory`, formatting |
| `internal/notification/adapters/templates/*` | paid templates, `PurchasePaid` render |
| `internal/notification/adapters/events/events.go` | `PurchasePaid` handler |
| `cmd/api/payment.go`, `cmd/api/app.go`, new `cmd/api/users.go` | wiring, identity-backed `UserExists` and `Contact` |
| `cmd/api/e2e_integration_test.go` | manual payment end-to-end test |

---

### Task 1: Course title in course-authoring facts

**Files:**
- Modify: `internal/courseauthoring/app/course_service.go:149-182`
- Test: `internal/courseauthoring/app/course_service_test.go:161-193`

**Interfaces:**
- Produces: `courseauthoringapp.CourseFacts.Title string` — the title of the version `Facts` reads (live version when live, working copy otherwise).

- [ ] **Step 1: Write the failing test**

In `TestFacts`, extend the first assertion and add one after publishing. Change the draft check to also require the title:

```go
	if err != nil || f.Published || !f.Free || !f.Price.IsFree() || f.OwnerID != owner.UserID || f.LectureIDs == nil || len(f.LectureIDs) != 0 || f.Title != "Go" {
		t.Fatalf("draft facts = %+v, %v", f, err)
	}
```

After the existing post-publish assertion block (`f, err = svc.Facts(ctx, c.ID)` and its check), add:

```go
	if f.Title != "Go" {
		t.Fatalf("live title = %q", f.Title)
	}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/courseauthoring/app/ -run TestFacts`
Expected: FAIL — compile error `f.Title undefined`.

- [ ] **Step 3: Implement**

In `CourseFacts` add the field after `Published`:

```go
type CourseFacts struct {
	Published  bool
	Title      string
	Free       bool
	Price      domain.Price
	OwnerID    id.ID
	LectureIDs []id.ID // course order; empty, never nil, when the course has no lectures
}
```

Update the comment on `Facts` to "publication, title, price, ownership and lecture facts" and the literal:

```go
		f = CourseFacts{Published: live, Title: c.Title.String(), Free: c.Price.IsFree(), Price: c.Price, OwnerID: c.OwnerID, LectureIDs: lectureIDs}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/courseauthoring/...`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/courseauthoring/app/course_service.go internal/courseauthoring/app/course_service_test.go
git commit -m "feat(courseauthoring): expose course title in facts"
```

---

### Task 2: Payment domain — title snapshot and manual purchases

**Files:**
- Modify: `internal/payment/domain/purchase.go`, `internal/payment/domain/events.go`
- Modify: `internal/payment/app/ports.go` (`CourseFacts.Title`), `internal/payment/app/service.go` (`Checkout` passes title)
- Modify: `cmd/api/payment.go` (`paymentCourseCatalog` fills `Title`)
- Modify callers of `NewPurchase` in tests: `internal/payment/domain/purchase_test.go`, `internal/payment/adapters/postgres/postgres_integration_test.go` (`fixture.insert`)
- Test: `internal/payment/domain/purchase_test.go`, `internal/payment/app/service_test.go`

**Interfaces:**
- Consumes: `courseauthoringapp.CourseFacts.Title` (Task 1).
- Produces:
  - `domain.Purchase` fields `CourseTitle string`, `ManualMethod string`, `RecordedBy id.ID`, `Note string`.
  - `domain.GatewayManual = "manual"`; `domain.MethodBankTransfer`, `domain.MethodCash`, `domain.MethodOther`.
  - `domain.NewPurchase(purchaseID, userID, courseID id.ID, courseTitle string, price Money, gateway string, now time.Time) (Purchase, Event, error)`.
  - `domain.ManualPayment{Method, Reference, Note string; RecordedBy id.ID}`.
  - `domain.RecordManualPurchase(purchaseID, userID, courseID id.ID, courseTitle string, price Money, m ManualPayment, now time.Time) (Purchase, Event, error)`.
  - `domain.PurchasePaid` fields `CourseTitle string \`json:"course_title"\``, `ManualMethod string \`json:"manual_method"\``.
  - `app.CourseFacts.Title string`.

- [ ] **Step 1: Write the failing domain tests**

In `purchase_test.go`, change `newPending` and `TestNewPurchase` to pass a title, and add the manual tests:

```go
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
	// keep the existing PurchaseInitiated and EventName assertions unchanged
	got, ok := ev.(domain.PurchaseInitiated)
	if !ok || got != (domain.PurchaseInitiated{PurchaseID: 42, UserID: 200, CourseID: 10, AmountMinor: 150000,
		Currency: "NPR", Gateway: "esewa", OccurredAt: t0}) {
		t.Fatalf("event = %#v", ev)
	}
	if ev.EventName() != "payment.purchase.initiated" {
		t.Fatalf("name = %s", ev.EventName())
	}
}
```

In `TestNewPurchaseRejects`, update its call to `domain.NewPurchase(42, 200, 10, "Go", c.price, c.gateway, t0)`.

Find the existing `MarkPaid` test that asserts the `PurchasePaid` event literal and add `CourseTitle: "Go"` to the expected value (the purchase comes from `newPending`). Then add:

```go
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
		with(func(m *domain.ManualPayment) { m.Reference = strings.Repeat("r", 200); m.Note = strings.Repeat("n", 1000) }), t0); err != nil {
		t.Fatalf("limits inclusive: %v", err)
	}
}
```

Add `"strings"` to the test imports.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/payment/domain/`
Expected: FAIL — compile errors (`too many arguments in call to domain.NewPurchase`, `undefined: domain.RecordManualPurchase`).

- [ ] **Step 3: Implement the domain**

In `events.go`, `PurchasePaid` becomes:

```go
// PurchasePaid is emitted when the gateway confirms the payment or a root admin records an offline one.
type PurchasePaid struct {
	PurchaseID   id.ID     `json:"purchase_id"`
	UserID       id.ID     `json:"user_id"`
	CourseID     id.ID     `json:"course_id"`
	CourseTitle  string    `json:"course_title"`
	AmountMinor  int64     `json:"amount_minor"`
	Currency     string    `json:"currency"`
	Gateway      string    `json:"gateway"`
	GatewayTxn   string    `json:"gateway_txn"`
	ManualMethod string    `json:"manual_method"` // empty for gateway purchases
	OccurredAt   time.Time `json:"occurred_at"`
}
```

In `purchase.go` add `"strings"` to imports and:

```go
// GatewayManual names purchases a root admin records for payments made outside any gateway.
// It is never registered as a Gateway, so such purchases are never sent to one.
const GatewayManual = "manual"

// Offline payment methods of a manual purchase.
const (
	MethodBankTransfer = "bank_transfer"
	MethodCash         = "cash"
	MethodOther        = "other"
)

const (
	maxReferenceLen = 200
	maxNoteLen      = 1000
)
```

Add fields to `Purchase` after `CourseID`, and after `GrantedAt`:

```go
	CourseTitle string // snapshot taken when the purchase is created
	...
	ManualMethod string // one of the Method constants; empty unless Gateway is GatewayManual
	RecordedBy   id.ID  // root admin who recorded a manual purchase; zero otherwise
	Note         string // admin's remark on a manual purchase; may be empty
```

`NewPurchase`:

```go
// NewPurchase starts a pending purchase. Its gateway reference is the purchase ID.
func NewPurchase(purchaseID, userID, courseID id.ID, courseTitle string, price Money, gateway string, now time.Time) (Purchase, Event, error) {
	if price.AmountMinor <= 0 {
		return Purchase{}, nil, ErrFreePrice
	}
	if price.Currency == "" || gateway == "" {
		return Purchase{}, nil, ErrInvalidPurchase
	}
	p := Purchase{
		ID: purchaseID, UserID: userID, CourseID: courseID, CourseTitle: courseTitle, Price: price, Gateway: gateway,
		GatewayRef: purchaseID.String(), Status: StatusPending, CreatedAt: now,
	}
	return p, PurchaseInitiated{
		PurchaseID: p.ID, UserID: userID, CourseID: courseID, AmountMinor: price.AmountMinor,
		Currency: price.Currency, Gateway: gateway, OccurredAt: now,
	}, nil
}

// ManualPayment describes an offline payment a root admin records.
type ManualPayment struct {
	Method     string
	Reference  string // bank voucher number, receipt number or similar; stored as GatewayTxn
	Note       string
	RecordedBy id.ID
}

// RecordManualPurchase creates a purchase that is already paid. The amount may differ from the
// course price. It emits PurchasePaid and no PurchaseInitiated.
func RecordManualPurchase(purchaseID, userID, courseID id.ID, courseTitle string, price Money, m ManualPayment, now time.Time) (Purchase, Event, error) {
	if price.AmountMinor <= 0 {
		return Purchase{}, nil, ErrFreePrice
	}
	ref, note := strings.TrimSpace(m.Reference), strings.TrimSpace(m.Note)
	if price.Currency == "" || !validMethod(m.Method) || ref == "" || len(ref) > maxReferenceLen ||
		len(note) > maxNoteLen || m.RecordedBy == 0 {
		return Purchase{}, nil, ErrInvalidPurchase
	}
	p := Purchase{
		ID: purchaseID, UserID: userID, CourseID: courseID, CourseTitle: courseTitle, Price: price,
		Gateway: GatewayManual, GatewayRef: purchaseID.String(), GatewayTxn: ref, Status: StatusPaid,
		CreatedAt: now, SettledAt: now, ManualMethod: m.Method, RecordedBy: m.RecordedBy, Note: note,
	}
	return p, p.paidEvent(now), nil
}

func validMethod(m string) bool {
	return m == MethodBankTransfer || m == MethodCash || m == MethodOther
}

func (p *Purchase) paidEvent(now time.Time) PurchasePaid {
	return PurchasePaid{
		PurchaseID: p.ID, UserID: p.UserID, CourseID: p.CourseID, CourseTitle: p.CourseTitle,
		AmountMinor: p.Price.AmountMinor, Currency: p.Price.Currency, Gateway: p.Gateway,
		GatewayTxn: p.GatewayTxn, ManualMethod: p.ManualMethod, OccurredAt: now,
	}
}
```

`MarkPaid` ends with `p.Status, p.GatewayTxn, p.SettledAt = StatusPaid, txn, now` then `return p.paidEvent(now), nil`.

- [ ] **Step 4: Run domain tests**

Run: `go test ./internal/payment/domain/`
Expected: PASS

- [ ] **Step 5: Write the failing app test for the title snapshot**

In `internal/payment/app/service_test.go`, `newFixture` catalog entries gain titles:

```go
	courses := catalog{
		freeCourse:  {Published: true, Title: "Free"},
		paidCourse:  {Published: true, Title: "Go", Price: price},
		draftCourse: {Published: false, Title: "Draft", Price: price},
	}
```

In `TestCheckoutCreatesPendingPurchase` add after the purchase is returned (variable name as in that test; adapt if it differs):

```go
	if purchase.CourseTitle != "Go" {
		t.Fatalf("title = %q", purchase.CourseTitle)
	}
```

Any test in `service_test.go` that builds `domain.Purchase` literals keeps compiling; no change needed.

- [ ] **Step 6: Run to verify failure**

Run: `go test ./internal/payment/...`
Expected: FAIL — `unknown field Title in struct literal` and `NewPurchase` argument count in `service.go`.

- [ ] **Step 7: Implement app and wiring**

`ports.go`:

```go
// CourseFacts is what payment knows about a course. A zero Price.AmountMinor means free.
type CourseFacts struct {
	Published bool
	Title     string
	Price     domain.Money
}
```

`service.go` `Checkout`: `domain.NewPurchase(s.ids.New(), p.UserID, courseID, c.Title, c.Price, gateway, s.clock.Now())`.

`cmd/api/payment.go` `paymentCourseCatalog.CourseFacts` return literal:

```go
	return paymentapp.CourseFacts{
		Published: f.Published,
		Title:     f.Title,
		Price:     paymentdomain.Money{AmountMinor: f.Price.AmountMinor, Currency: f.Price.Currency},
	}, nil
```

`internal/payment/adapters/postgres/postgres_integration_test.go` `fixture.insert`: `domain.NewPurchase(f.ids.New(), userID, courseID, "Go", npr, "esewa", at)`.

- [ ] **Step 8: Run tests and build**

Run: `go build ./... && go vet -tags integration ./... && go test ./internal/payment/... ./cmd/api/`
Expected: build ok, PASS (integration tests are compiled by vet, not run).

- [ ] **Step 9: Commit**

```bash
git add internal/payment cmd/api/payment.go
git commit -m "feat(payment): snapshot course title and model manual purchases"
```

---

### Task 3: Payment storage — migration, columns, ListByUser

**Files:**
- Create: `migrations/00012_payment_history.sql`
- Modify: `sqlc.yaml` (payment `overrides`), `internal/payment/adapters/postgres/queries.sql`, `internal/payment/adapters/postgres/purchases.go`
- Regenerate: `internal/payment/adapters/postgres/sqlcgen/*` (`make sqlc`)
- Modify: `internal/payment/app/ports.go` (`Repository.ListByUser`), `internal/payment/app/fakes_test.go` (`memTx.ListByUser`)
- Test: `internal/payment/adapters/postgres/postgres_integration_test.go`

**Interfaces:**
- Consumes: Task 2 domain fields.
- Produces: `app.Repository.ListByUser(ctx context.Context, userID, before id.ID, limit int) ([]domain.Purchase, error)` — purchases of `userID` with `id < before` (no bound when `before` is zero), `id DESC`, at most `limit`.

- [ ] **Step 1: Write the failing integration tests**

Append to `postgres_integration_test.go` (add imports `"github.com/jackc/pgx/v5/stdlib"` and `"github.com/santoshkc2200/ioe-backend/internal/platform/migrate"`):

```go
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
		{"UPDATE payment.purchases SET manual_method = NULL WHERE id = $1", manual.ID},                  // manual without method
		{"UPDATE payment.purchases SET recorded_by = NULL WHERE id = $1", manual.ID},                    // manual without recorder
		{"UPDATE payment.purchases SET manual_method = 'cheque' WHERE id = $1", manual.ID},              // unknown method
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
```

- [ ] **Step 2: Verify the tests fail**

Run: `go vet -tags integration ./internal/payment/...`
Expected: FAIL — `r.Purchases.ListByUser undefined`.

- [ ] **Step 3: Write the migration**

`migrations/00012_payment_history.sql`:

```sql
-- +goose Up
ALTER TABLE payment.purchases
  ADD COLUMN course_title  text NOT NULL DEFAULT '',
  ADD COLUMN manual_method text,
  ADD COLUMN recorded_by   bigint,
  ADD COLUMN note          text NOT NULL DEFAULT '';

-- One-time backfill from the course's working copy. No runtime code reads courseauthoring.
UPDATE payment.purchases p
SET course_title = c.title
FROM courseauthoring.courses c
WHERE c.id = p.course_id;

ALTER TABLE payment.purchases
  ALTER COLUMN course_title DROP DEFAULT,
  ALTER COLUMN note DROP DEFAULT,
  ADD CONSTRAINT purchases_manual_consistent CHECK (
    (gateway = 'manual') = (manual_method IS NOT NULL AND recorded_by IS NOT NULL)),
  ADD CONSTRAINT purchases_manual_method CHECK (
    manual_method IS NULL OR manual_method IN ('bank_transfer', 'cash', 'other'));

CREATE INDEX purchases_user_id_desc ON payment.purchases (user_id, id DESC);

-- +goose Down
DROP INDEX payment.purchases_user_id_desc;
ALTER TABLE payment.purchases
  DROP CONSTRAINT purchases_manual_method,
  DROP CONSTRAINT purchases_manual_consistent,
  DROP COLUMN note,
  DROP COLUMN recorded_by,
  DROP COLUMN manual_method,
  DROP COLUMN course_title;
```

If `migrations/embed.go` lists files explicitly rather than with a glob, add the new file there.

- [ ] **Step 4: sqlc overrides and queries**

In `sqlc.yaml`, payment `overrides`, after the two `timestamptz` entries:

```yaml
          - column: payment.purchases.manual_method
            go_type:
              type: string
              pointer: true
          - column: payment.purchases.recorded_by
            go_type:
              type: int64
              pointer: true
```

In `queries.sql`, replace `InsertPurchase` and add the list query:

```sql
-- name: ListPurchasesByUser :many
SELECT * FROM payment.purchases
WHERE user_id = sqlc.arg(user_id)::bigint
  AND (sqlc.arg(before_id)::bigint = 0 OR id < sqlc.arg(before_id)::bigint)
ORDER BY id DESC
LIMIT sqlc.arg(page_limit)::bigint;

-- name: InsertPurchase :exec
INSERT INTO payment.purchases
  (id, user_id, course_id, course_title, amount_minor, currency, gateway, gateway_ref, gateway_txn, status,
   created_at, settled_at, granted_at, manual_method, recorded_by, note, version)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, 1);
```

Run: `make sqlc`
Expected: `sqlcgen/models.go` `PaymentPurchase` gains `CourseTitle string`, `ManualMethod *string`, `RecordedBy *int64`, `Note string`; `ListPurchasesByUserParams{UserID, BeforeID, PageLimit int64}` exists.

- [ ] **Step 5: Repository**

`ports.go` `Repository` gains:

```go
	// ListByUser returns userID's purchases with an ID below before (no bound when before is
	// zero), newest first, at most limit.
	ListByUser(ctx context.Context, userID, before id.ID, limit int) ([]domain.Purchase, error)
```

`purchases.go`:

```go
func (r purchases) ListByUser(ctx context.Context, userID, before id.ID, limit int) ([]domain.Purchase, error) {
	rows, err := r.q.ListPurchasesByUser(ctx, sqlcgen.ListPurchasesByUserParams{
		UserID: int64(userID), BeforeID: int64(before), PageLimit: int64(limit),
	})
	if err != nil {
		return nil, err
	}
	return toDomainAll(rows), nil
}

func toDomainAll(rows []sqlcgen.PaymentPurchase) []domain.Purchase {
	out := make([]domain.Purchase, len(rows))
	for i, row := range rows {
		out[i] = toDomain(row)
	}
	return out
}
```

`ListUnsettled` returns `toDomainAll(rows), nil` instead of its loop. `Insert` params gain:

```go
		CourseTitle: p.CourseTitle, ManualMethod: optionalString(p.ManualMethod),
		RecordedBy: optionalID(p.RecordedBy), Note: p.Note,
```

`toDomain` sets `CourseTitle: r.CourseTitle, Note: r.Note` in the literal and after it:

```go
	if r.ManualMethod != nil {
		p.ManualMethod = *r.ManualMethod
	}
	if r.RecordedBy != nil {
		p.RecordedBy = id.ID(*r.RecordedBy)
	}
```

Helpers next to `optionalTime`:

```go
func optionalString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func optionalID(v id.ID) *int64 {
	if v == 0 {
		return nil
	}
	n := int64(v)
	return &n
}
```

`fakes_test.go` `memTx` gains:

```go
func (t *memTx) ListByUser(_ context.Context, userID, before id.ID, limit int) ([]domain.Purchase, error) {
	var out []domain.Purchase
	for _, p := range t.rows {
		if p.UserID == userID && (before == 0 || p.ID < before) {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
```

- [ ] **Step 6: Run tests**

Run: `make sqlc-check && go test ./internal/payment/... && go test -tags integration ./internal/payment/adapters/postgres/ ./internal/platform/migrate/`
Expected: PASS (requires Docker; if Docker is not running, say so and do not claim the integration tests passed).

- [ ] **Step 7: Commit**

```bash
git add migrations/00012_payment_history.sql sqlc.yaml internal/payment
git commit -m "feat(payment): store titles and manual details, list purchases by user"
```

---

### Task 4: Payment use cases — ListByUser and RecordManual

**Files:**
- Modify: `internal/payment/app/ports.go`, `internal/payment/app/errors.go`, `internal/payment/app/service.go`
- Modify: `internal/payment/app/fakes_test.go`, `internal/payment/app/service_test.go`
- Create: `cmd/api/users.go` (identity-backed directory for payment)
- Modify: `cmd/api/payment.go`, `cmd/api/app.go`

**Interfaces:**
- Consumes: `Repository.ListByUser` (Task 3), `domain.RecordManualPurchase` (Task 2).
- Produces:
  - `app.ErrForbidden`.
  - `app.UserDirectory{ UserExists(ctx, userID id.ID) (bool, error) }`.
  - `app.NewService(tx TxRunner, courses CourseCatalog, users UserDirectory, enroll EnrollmentGranter, gateways map[string]Gateway, ids *id.Generator, c clock.Clock, logger *slog.Logger) *Service`.
  - `(*Service).ListByUser(ctx, p auth.Principal, userID, before id.ID, limit int) ([]domain.Purchase, id.ID, error)` — second result is the next cursor, zero on the last page.
  - `app.ManualInput{UserID, CourseID id.ID; AmountMinor int64; Currency, Method, Reference, Note string}`.
  - `(*Service).RecordManual(ctx, p auth.Principal, in ManualInput) (domain.Purchase, error)`.
  - `cmd/api.identityUsers{svc *identityapp.Service}` with `UserExists`.

- [ ] **Step 1: Write the failing tests**

`fakes_test.go`:

```go
type fakeUsers map[id.ID]bool

func (u fakeUsers) UserExists(_ context.Context, userID id.ID) (bool, error) { return u[userID], nil }
```

`service_test.go` `newFixture`: build the service with users `fakeUsers{student.UserID: true, other.UserID: true, third.UserID: true, admin.UserID: true}`:

```go
	f.svc = app.NewService(f.store, courses, fakeUsers{admin.UserID: true, student.UserID: true, other.UserID: true, third.UserID: true},
		f.enroll, map[string]app.Gateway{"esewa": f.gw}, ids, f.clock, slog.New(slog.NewTextHandler(f.logs, nil)))
```

Append tests:

```go
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
```

Note: `f.checkout` uses the fixture's generator, which produces increasing IDs, so `ids[2]` is newest.

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/payment/app/`
Expected: FAIL — compile errors for `NewService` arity, `app.ManualInput`, `app.ErrForbidden`, `ListByUser`.

- [ ] **Step 3: Implement**

`errors.go` adds `ErrForbidden = errors.New("forbidden")`.

`ports.go` adds:

```go
// UserDirectory is backed by identity.
type UserDirectory interface {
	UserExists(ctx context.Context, userID id.ID) (bool, error)
}
```

`service.go`: add `users UserDirectory` to `Service`, `NewService` signature as in Interfaces (users after courses), and:

```go
// maxPage bounds one ListByUser page.
const maxPage = 50
```

inside the existing `const` block. Then:

```go
// ListByUser returns userID's purchases, newest first, below the before cursor. The caller must
// be that user or a root admin; anyone else gets ErrNotFound. next is zero on the last page.
func (s *Service) ListByUser(ctx context.Context, p auth.Principal, userID, before id.ID, limit int) ([]domain.Purchase, id.ID, error) {
	if p.UserID != userID && p.Role != auth.RoleRootAdmin {
		return nil, 0, ErrNotFound
	}
	if limit < 1 || limit > maxPage {
		return nil, 0, fmt.Errorf("%w: limit must be between 1 and %d", ErrInvalidInput, maxPage)
	}
	var page []domain.Purchase
	err := s.tx.RunInTx(ctx, func(r Repos) error {
		var err error
		page, err = r.Purchases.ListByUser(ctx, userID, before, limit+1)
		return err
	})
	if err != nil {
		return nil, 0, err
	}
	if len(page) <= limit {
		return page, 0, nil
	}
	page = page[:limit]
	return page, page[limit-1].ID, nil
}

// ManualInput is an offline payment a root admin records for a user.
type ManualInput struct {
	UserID      id.ID
	CourseID    id.ID
	AmountMinor int64
	Currency    string
	Method      string
	Reference   string
	Note        string
}

// RecordManual records a payment made outside any gateway as a paid purchase and enrolls the
// buyer. Only a root admin may record one. An existing enrollment does not block it.
func (s *Service) RecordManual(ctx context.Context, p auth.Principal, in ManualInput) (domain.Purchase, error) {
	if p.Role != auth.RoleRootAdmin {
		return domain.Purchase{}, ErrForbidden
	}
	c, err := s.courses.CourseFacts(ctx, in.CourseID)
	if err != nil {
		return domain.Purchase{}, err
	}
	if !c.Published {
		return domain.Purchase{}, ErrNotFound
	}
	if c.Price.AmountMinor == 0 {
		return domain.Purchase{}, ErrCourseFree
	}
	if in.Currency != c.Price.Currency {
		return domain.Purchase{}, fmt.Errorf("%w: currency must be %s", ErrInvalidInput, c.Price.Currency)
	}
	exists, err := s.users.UserExists(ctx, in.UserID)
	if err != nil {
		return domain.Purchase{}, err
	}
	if !exists {
		return domain.Purchase{}, ErrNotFound
	}
	var purchase domain.Purchase
	err = s.tx.RunInTx(ctx, func(r Repos) error {
		paid, err := r.Purchases.CountPaid(ctx, in.UserID, in.CourseID)
		if err != nil {
			return err
		}
		if paid > 0 {
			return ErrAlreadyPurchased
		}
		var ev domain.Event
		purchase, ev, err = domain.RecordManualPurchase(s.ids.New(), in.UserID, in.CourseID, c.Title,
			domain.Money{AmountMinor: in.AmountMinor, Currency: in.Currency},
			domain.ManualPayment{Method: in.Method, Reference: in.Reference, Note: in.Note, RecordedBy: p.UserID},
			s.clock.Now())
		if errors.Is(err, domain.ErrFreePrice) || errors.Is(err, domain.ErrInvalidPurchase) {
			return fmt.Errorf("%w: %w", ErrInvalidInput, err)
		}
		if err != nil {
			return err
		}
		if err := r.Purchases.Insert(ctx, &purchase); err != nil {
			return err
		}
		return r.Events.Publish(ctx, ev)
	})
	if err != nil {
		return domain.Purchase{}, err
	}
	// The purchase is paid, so settle skips the gateway and only grants.
	return s.settle(ctx, purchase)
}
```

`cmd/api/users.go`:

```go
package main

import (
	"context"
	"errors"

	identityapp "github.com/santoshkc2200/ioe-backend/internal/identity/app"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

// identityUsers lets other contexts look users up in identity.
type identityUsers struct{ svc *identityapp.Service }

func (u identityUsers) UserExists(ctx context.Context, userID id.ID) (bool, error) {
	_, err := u.svc.GetMe(ctx, userID)
	if errors.Is(err, identityapp.ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}
```

`cmd/api/payment.go` `registerPayment` gains a `users paymentapp.UserDirectory` parameter after `courses` and passes it to `NewService` after the catalog. In `cmd/api/app.go` call it as `registerPayment(router, pool, courses, identityUsers{svc: identity}, enrollmentAccess, enrollments, ids, clk, cfg, identityHandler.RequireAuth, logger)`.

- [ ] **Step 4: Run tests**

Run: `go build ./... && go test ./internal/payment/... ./cmd/api/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/payment/app cmd/api
git commit -m "feat(payment): list purchases by user and record manual payments"
```

---

### Task 5: Payment HTTP routes and OpenAPI

**Files:**
- Modify: `internal/payment/adapters/httpapi/httpapi.go`, `internal/payment/adapters/httpapi/wire.go`
- Modify: `api/openapi.yaml`
- Test: `internal/payment/adapters/httpapi/httpapi_test.go`

**Interfaces:**
- Consumes: `ListByUser`, `RecordManual`, `ManualInput`, `ErrForbidden` (Task 4).
- Produces: routes `GET /v1/me/purchases`, `GET /v1/users/{userID}/purchases`, `POST /v1/users/{userID}/purchases`; wire fields `course_title`, `manual_method`, `reference`, `note`, `recorded_by`.

- [ ] **Step 1: Write the failing tests**

Extend `stub` in `httpapi_test.go`:

```go
	page      []domain.Purchase
	next      id.ID
	userID    id.ID
	before    id.ID
	limit     int
	manual    app.ManualInput
```

and methods:

```go
func (s *stub) ListByUser(_ context.Context, p auth.Principal, userID, before id.ID, limit int) ([]domain.Purchase, id.ID, error) {
	s.principal, s.userID, s.before, s.limit = p, userID, before, limit
	return s.page, s.next, s.err
}

func (s *stub) RecordManual(_ context.Context, p auth.Principal, in app.ManualInput) (domain.Purchase, error) {
	s.principal, s.manual = p, in
	return s.purchase, s.err
}
```

Tests:

```go
func TestListMine(t *testing.T) {
	manual := domain.Purchase{ID: 9, UserID: 200, CourseID: 11, CourseTitle: "Go", Price: domain.Money{AmountMinor: 100, Currency: "NPR"},
		Gateway: "manual", GatewayRef: "9", GatewayTxn: "R-1", Status: domain.StatusPaid, CreatedAt: t0, SettledAt: t0, GrantedAt: t0,
		ManualMethod: "cash", RecordedBy: 1, Note: "desk"}
	esewa := domain.Purchase{ID: 8, UserID: 200, CourseID: 11, CourseTitle: "Go", Price: domain.Money{AmountMinor: 100, Currency: "NPR"},
		Gateway: "esewa", GatewayRef: "8", Status: domain.StatusPending, CreatedAt: t0}
	s := &stub{page: []domain.Purchase{manual, esewa}, next: 8}
	code, body := call(newServer(s), http.MethodGet, "/v1/me/purchases", "")
	if code != http.StatusOK || s.userID != 200 || s.before != 0 || s.limit != 20 {
		t.Fatalf("code=%d stub=%+v", code, s)
	}
	got := decode[map[string]any](t, body)
	items := got["items"].([]any)
	m, e := items[0].(map[string]any), items[1].(map[string]any)
	if got["next_cursor"] != "8" || m["course_title"] != "Go" || m["manual_method"] != "cash" || m["reference"] != "R-1" ||
		m["note"] != "desk" || m["recorded_by"] != "1" {
		t.Fatalf("body = %s", body)
	}
	for _, k := range []string{"manual_method", "reference", "note", "recorded_by"} {
		if v, ok := e[k]; !ok || v != nil {
			t.Fatalf("esewa %s = %v (present=%v)", k, v, ok)
		}
	}
	s = &stub{}
	code, body = call(newServer(s), http.MethodGet, "/v1/me/purchases?limit=5&cursor=42", "")
	if code != http.StatusOK || s.limit != 5 || s.before != 42 {
		t.Fatalf("code=%d stub=%+v", code, s)
	}
	if got := decode[map[string]any](t, body); got["items"] == nil || len(got["items"].([]any)) != 0 || got["next_cursor"] != nil {
		t.Fatalf("empty page body = %s", body)
	}
}

func TestListQueryValidation(t *testing.T) {
	for _, q := range []string{"?cursor=abc", "?limit=x", "?limit=0", "?limit=51", "?cursor=-1"} {
		code, body := call(newServer(&stub{}), http.MethodGet, "/v1/me/purchases"+q, "")
		if code != http.StatusBadRequest || decode[map[string]any](t, body)["type"] != "invalid_input" {
			t.Fatalf("%s: %d %s", q, code, body)
		}
	}
}

func TestListUser(t *testing.T) {
	s := &stub{}
	if code, _ := call(newServer(s), http.MethodGet, "/v1/users/300/purchases", ""); code != http.StatusOK || s.userID != 300 {
		t.Fatalf("code=%d userID=%v", code, s.userID)
	}
	if code, _ := call(newServer(&stub{}), http.MethodGet, "/v1/users/abc/purchases", ""); code != http.StatusNotFound {
		t.Fatalf("malformed user id: %d", code)
	}
}

func TestRecordManual(t *testing.T) {
	s := &stub{purchase: domain.Purchase{ID: 9, Gateway: "manual", ManualMethod: "cash", GatewayTxn: "R-1", RecordedBy: 1, Status: domain.StatusPaid, CreatedAt: t0, SettledAt: t0}}
	code, body := call(newServer(s), http.MethodPost, "/v1/users/300/purchases",
		`{"course_id":"11","amount_minor":150000,"currency":"NPR","method":"cash","reference":"R-1","note":"desk"}`)
	want := app.ManualInput{UserID: 300, CourseID: 11, AmountMinor: 150000, Currency: "NPR", Method: "cash", Reference: "R-1", Note: "desk"}
	if code != http.StatusCreated || s.manual != want {
		t.Fatalf("code=%d manual=%+v body=%s", code, s.manual, body)
	}
	if p := decode[map[string]any](t, body)["purchase"].(map[string]any); p["manual_method"] != "cash" {
		t.Fatalf("body = %s", body)
	}
	for _, b := range []string{`{"course_id":"x","amount_minor":1,"currency":"NPR","method":"cash","reference":"r"}`,
		`{"course_id":"11","extra":1}`} {
		if code, _ := call(newServer(&stub{}), http.MethodPost, "/v1/users/300/purchases", b); code != http.StatusBadRequest {
			t.Fatalf("%s: %d", b, code)
		}
	}
}
```

In `TestErrorMapping`, add a case `{app.ErrForbidden, http.StatusForbidden, "forbidden"}` using the table shape that test already uses.

Run `go test ./internal/payment/adapters/httpapi/` first and check what a malformed `course_id` in a JSON body yields with `id.ID`'s `UnmarshalJSON` via `httpserver.DecodeJSON`; the test expects 400. If `DecodeJSON` maps it differently, keep the test and parse `course_id` as a `string` field with `id.Parse` in the handler returning `invalid_input`.

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/payment/adapters/httpapi/`
Expected: FAIL — `stub` does not implement `httpapi.Service` until the interface changes; then 404s for unknown routes.

- [ ] **Step 3: Implement**

`httpapi.go`: extend `Service`:

```go
	ListByUser(ctx context.Context, p auth.Principal, userID, before id.ID, limit int) ([]domain.Purchase, id.ID, error)
	RecordManual(ctx context.Context, p auth.Principal, in app.ManualInput) (domain.Purchase, error)
```

Register:

```go
	r.Handle("GET /v1/me/purchases", a(h.listMine))
	r.Handle("GET /v1/users/{userID}/purchases", a(h.listUser))
	r.Handle("POST /v1/users/{userID}/purchases", a(h.recordManual))
```

Handlers (add `"fmt"` and `"strconv"` imports):

```go
const defaultPageLimit = 20

func (h *Handler) listMine(w http.ResponseWriter, r *http.Request) {
	h.list(w, r, principal(r).UserID)
}

func (h *Handler) listUser(w http.ResponseWriter, r *http.Request) {
	userID, ok := pathID(w, r, "userID")
	if !ok {
		return
	}
	h.list(w, r, userID)
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request, userID id.ID) {
	before, limit, err := pageQuery(r)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	items, next, err := h.svc.ListByUser(r.Context(), principal(r), userID, before, limit)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toPageWire(items, next))
}

// pageQuery reads limit (default 20) and cursor. Range checks on limit belong to the service.
func pageQuery(r *http.Request) (id.ID, int, error) {
	v := r.URL.Query()
	limit := defaultPageLimit
	if s := v.Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil {
			return 0, 0, fmt.Errorf("%w: limit must be a number", app.ErrInvalidInput)
		}
		limit = n
	}
	var before id.ID
	if s := v.Get("cursor"); s != "" {
		c, err := id.Parse(s)
		if err != nil || c <= 0 {
			return 0, 0, fmt.Errorf("%w: invalid cursor", app.ErrInvalidInput)
		}
		before = c
	}
	return before, limit, nil
}

func (h *Handler) recordManual(w http.ResponseWriter, r *http.Request) {
	userID, ok := pathID(w, r, "userID")
	if !ok {
		return
	}
	var req manualRequest
	if !httpserver.DecodeJSON(w, r, &req) {
		return
	}
	p, err := h.svc.RecordManual(r.Context(), principal(r), app.ManualInput{
		UserID: userID, CourseID: req.CourseID, AmountMinor: req.AmountMinor, Currency: req.Currency,
		Method: req.Method, Reference: req.Reference, Note: req.Note,
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, purchaseResponse{Purchase: toWire(p)})
}
```

If `id.ID`'s type is unsigned, drop the `c <= 0` check and remove `"?cursor=-1"` from `TestListQueryValidation` only if `id.Parse("-1")` already fails (it then still returns 400).

Add `{app.ErrForbidden, http.StatusForbidden, "forbidden", "Forbidden"}` to `errorMappings` after `ErrNotFound`.

`wire.go`: `purchaseWire` gains

```go
	CourseTitle  string  `json:"course_title"`
	ManualMethod *string `json:"manual_method"` // null unless gateway is manual
	Reference    *string `json:"reference"`     // manual purchases only
	Note         *string `json:"note"`          // manual purchases only
	RecordedBy   *id.ID  `json:"recorded_by"`   // manual purchases only
```

`toWire` sets `CourseTitle: p.CourseTitle` and:

```go
	if p.Gateway == domain.GatewayManual {
		method, ref, note, by := p.ManualMethod, p.GatewayTxn, p.Note, p.RecordedBy
		w.ManualMethod, w.Reference, w.Note, w.RecordedBy = &method, &ref, &note, &by
	}
```

Add:

```go
type manualRequest struct {
	CourseID    id.ID  `json:"course_id"`
	AmountMinor int64  `json:"amount_minor"`
	Currency    string `json:"currency"`
	Method      string `json:"method"`
	Reference   string `json:"reference"`
	Note        string `json:"note"`
}

type pageWire struct {
	Items      []purchaseWire `json:"items"`
	NextCursor string         `json:"next_cursor,omitempty"`
}

func toPageWire(items []domain.Purchase, next id.ID) pageWire {
	out := pageWire{Items: make([]purchaseWire, len(items))}
	for i, p := range items {
		out.Items[i] = toWire(p)
	}
	if next != 0 {
		out.NextCursor = next.String()
	}
	return out
}
```

- [ ] **Step 4: OpenAPI**

In `api/openapi.yaml`:
- `Purchase` schema: add `course_title` to `required` and properties `course_title: { type: string, description: Course title when the purchase was made }`, `manual_method: { type: [string, "null"], enum: [bank_transfer, cash, other, null] }`, `reference: { type: [string, "null"], description: Voucher or receipt number; manual purchases only }`, `note: { type: [string, "null"] }`, `recorded_by: { oneOf: [{ $ref: "#/components/schemas/ID" }, { type: "null" }], description: Root admin who recorded a manual purchase }`; add the four to `required` too (they are always present, possibly null). `gateway` enum becomes `[esewa, manual]`.
- New schemas `PurchasePage` (`required: [items]`, `items: array of Purchase`, `next_cursor: string`) and `ManualPurchaseRequest` (`required: [course_id, amount_minor, currency, method, reference]`, `additionalProperties: false`, `course_id: ID`, `amount_minor: integer int64 minimum 1`, `currency: string`, `method: enum [bank_transfer, cash, other]`, `reference: string minLength 1 maxLength 200`, `note: string maxLength 1000`).
- Paths: `/v1/me/purchases` `get` (limit 1..50 default 20, cursor, 200 `PurchasePage`, 400, 401); `/v1/users/{userID}/purchases` `get` (the user or a root admin; 404 for anyone else) and `post` (root admin; 201 `PurchaseResponse`; 400 `invalid_input`, 403 `forbidden`, 404 `not_found` for unknown user or missing/unpublished course, 409 `course_free` / `already_purchased`). Copy the security, parameter and response `$ref` style used by `/v1/courses/{courseID}/purchases` and the `UserID` path parameter used by `/v1/users/{userID}/enrollments`.

- [ ] **Step 5: Run tests**

Run: `go test ./internal/payment/... && make lint`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/payment/adapters/httpapi api/openapi.yaml
git commit -m "feat(payment): expose purchase history and manual payments over HTTP"
```

---

### Task 6: Notification use case and paid email templates

**Files:**
- Modify: `internal/notification/app/service.go`
- Create: `internal/notification/app/purchase_paid.go`
- Create: `internal/notification/app/purchase_paid_test.go`
- Modify: `internal/notification/app/service_test.go` (fake renderer, `NewService` arity)
- Create: `internal/notification/adapters/templates/purchase_paid.txt.tmpl`, `purchase_paid.html.tmpl`
- Modify: `internal/notification/adapters/templates/templates.go`, `templates_test.go`
- Modify: `cmd/api/users.go` (add `Contact`), `cmd/api/app.go` (`registerNotifications` builds the service with the directory)

**Interfaces:**
- Produces:
  - `app.ErrUnknownUser`.
  - `app.UserDirectory{ Contact(ctx, userID string) (email, name string, err error) }`.
  - `app.PurchasePaidEmail{Name, CourseTitle, Amount, Method, PaidOn, CourseURL string}`.
  - `app.Renderer` gains `PurchasePaid(e PurchasePaidEmail) (subject, text, html string, err error)`.
  - `app.PurchasePaidInput{EventID, UserID, CourseID, CourseTitle string; AmountMinor int64; Currency, Gateway, ManualMethod string; PaidAt time.Time}`.
  - `app.NewService(mailer Mailer, renderer Renderer, users UserDirectory, courseURLBase string) *Service`.
  - `(*Service).SendPurchasePaid(ctx, in PurchasePaidInput) error`.
  - `app.PurchasePaidKey(eventID string) string`.
  - `(*templates.Renderer).PurchasePaid(e app.PurchasePaidEmail) (subject, text, html string, err error)`.
  - `identityUsers.Contact(ctx, userID string) (string, string, error)` in `cmd/api`, implementing `notificationapp.UserDirectory`.

- [ ] **Step 1: Write the failing tests**

In `service_test.go`, add to `fakeRenderer`:

```go
	paid app.PurchasePaidEmail
```

and method:

```go
func (f *fakeRenderer) PurchasePaid(e app.PurchasePaidEmail) (string, string, string, error) {
	f.paid = e
	return "Paid", "Text", "<p>HTML</p>", f.err
}
```

Replace every `app.NewService(m, r)` (and similar two-argument calls) with `app.NewService(m, r, nil, "")`.

`purchase_paid_test.go`:

```go
package app_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/notification/app"
)

type fakeUsers struct {
	email, name string
	err         error
	asked       string
}

func (f *fakeUsers) Contact(_ context.Context, userID string) (string, string, error) {
	f.asked = userID
	return f.email, f.name, f.err
}

var paidIn = app.PurchasePaidInput{
	EventID: "ev-9", UserID: "200", CourseID: "11", CourseTitle: "Go", AmountMinor: 150000, Currency: "NPR",
	Gateway: "esewa", PaidAt: time.Date(2026, 10, 7, 4, 15, 0, 0, time.UTC),
}

func TestSendPurchasePaid(t *testing.T) {
	m, r, u := &fakeMailer{}, &fakeRenderer{}, &fakeUsers{email: "s@example.com", name: "Sita"}
	if err := app.NewService(m, r, u, "https://app.test/").SendPurchasePaid(context.Background(), paidIn); err != nil {
		t.Fatal(err)
	}
	want := app.PurchasePaidEmail{Name: "Sita", CourseTitle: "Go", Amount: "NPR 1,500.00", Method: "eSewa",
		PaidOn: "7 October 2026, 10:00 NPT", CourseURL: "https://app.test/courses/11"}
	if u.asked != "200" || r.paid != want {
		t.Fatalf("asked=%q rendered=%+v", u.asked, r.paid)
	}
	if m.calls != 1 || m.email.To != "s@example.com" || m.email.Subject != "Paid" || m.key != "ioe:payment.purchase.paid:ev-9:paid-v1" {
		t.Fatalf("calls=%d email=%+v key=%q", m.calls, m.email, m.key)
	}
}

func TestSendPurchasePaidFormatting(t *testing.T) {
	cases := []struct {
		amount int64
		method string
		want   app.PurchasePaidEmail
	}{
		{5, "cash", app.PurchasePaidEmail{Amount: "NPR 0.05", Method: "Cash"}},
		{123456789, "bank_transfer", app.PurchasePaidEmail{Amount: "NPR 1,234,567.89", Method: "Bank transfer"}},
		{100, "other", app.PurchasePaidEmail{Amount: "NPR 1.00", Method: "Other"}},
	}
	for _, c := range cases {
		in := paidIn
		in.AmountMinor, in.Gateway, in.ManualMethod = c.amount, "manual", c.method
		r := &fakeRenderer{}
		if err := app.NewService(&fakeMailer{}, r, &fakeUsers{email: "s@example.com"}, "").SendPurchasePaid(context.Background(), in); err != nil {
			t.Fatal(err)
		}
		if r.paid.Amount != c.want.Amount || r.paid.Method != c.want.Method || r.paid.CourseURL != "" {
			t.Fatalf("%d %s: %+v", c.amount, c.method, r.paid)
		}
	}
}

func TestSendPurchasePaidPermanentFailures(t *testing.T) {
	cases := map[string]struct {
		in    app.PurchasePaidInput
		users *fakeUsers
		r     *fakeRenderer
	}{
		"no event id":  {func() app.PurchasePaidInput { in := paidIn; in.EventID = ""; return in }(), &fakeUsers{email: "s@example.com"}, &fakeRenderer{}},
		"no user id":   {func() app.PurchasePaidInput { in := paidIn; in.UserID = ""; return in }(), &fakeUsers{email: "s@example.com"}, &fakeRenderer{}},
		"unknown user": {paidIn, &fakeUsers{err: app.ErrUnknownUser}, &fakeRenderer{}},
		"no email":     {paidIn, &fakeUsers{}, &fakeRenderer{}},
		"render fails": {paidIn, &fakeUsers{email: "s@example.com"}, &fakeRenderer{err: errors.New("bad")}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			m := &fakeMailer{}
			err := app.NewService(m, c.r, c.users, "").SendPurchasePaid(context.Background(), c.in)
			if !errors.Is(err, app.ErrPermanent) || m.calls != 0 {
				t.Fatalf("err=%v calls=%d", err, m.calls)
			}
		})
	}
}

func TestSendPurchasePaidRetryableFailures(t *testing.T) {
	errDown := errors.New("down")
	err := app.NewService(&fakeMailer{}, &fakeRenderer{}, &fakeUsers{err: errDown}, "").SendPurchasePaid(context.Background(), paidIn)
	if !errors.Is(err, errDown) || errors.Is(err, app.ErrPermanent) {
		t.Fatalf("lookup err = %v", err)
	}
	err = app.NewService(&fakeMailer{err: errDown}, &fakeRenderer{}, &fakeUsers{email: "s@example.com"}, "").SendPurchasePaid(context.Background(), paidIn)
	if !errors.Is(err, errDown) || errors.Is(err, app.ErrPermanent) {
		t.Fatalf("enqueue err = %v", err)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/notification/app/`
Expected: FAIL — undefined `app.PurchasePaidEmail`, `NewService` arity.

- [ ] **Step 3: Implement**

`service.go`: `Renderer` gains `PurchasePaid(e PurchasePaidEmail) (subject, text, html string, err error)`. `Service` gains `users UserDirectory` and `courseURLBase string`:

```go
// NewService builds the notification use cases. courseURLBase is the frontend origin used for
// course links; links are omitted when it is empty.
func NewService(mailer Mailer, renderer Renderer, users UserDirectory, courseURLBase string) *Service {
	return &Service{mailer: mailer, renderer: renderer, users: users, courseURLBase: strings.TrimRight(courseURLBase, "/")}
}
```

(add `"strings"` import).

`purchase_paid.go`:

```go
package app

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ErrUnknownUser is returned by a UserDirectory for a user that does not exist.
var ErrUnknownUser = errors.New("unknown user")

// UserDirectory is backed by identity.
type UserDirectory interface {
	Contact(ctx context.Context, userID string) (email, name string, err error)
}

// PurchasePaidInput is the data of one payment.purchase.paid event.
type PurchasePaidInput struct {
	EventID      string // outbox message UUID; stable across redeliveries
	UserID       string
	CourseID     string
	CourseTitle  string
	AmountMinor  int64
	Currency     string
	Gateway      string
	ManualMethod string
	PaidAt       time.Time
}

// PurchasePaidEmail is the display data of the payment-received email. Every field is
// already formatted; CourseURL and Name may be empty.
type PurchasePaidEmail struct {
	Name        string
	CourseTitle string
	Amount      string
	Method      string
	PaidOn      string
	CourseURL   string
}

// nepalTime is Nepal Standard Time (UTC+05:45, no daylight saving), fixed so the binary needs no tzdata.
var nepalTime = time.FixedZone("NPT", 5*3600+45*60)

// SendPurchasePaid emails the buyer that their payment was received, exactly once per event.
func (s *Service) SendPurchasePaid(ctx context.Context, in PurchasePaidInput) error {
	if in.EventID == "" || in.UserID == "" {
		return fmt.Errorf("%w: purchase paid requires an event ID and a user ID", ErrPermanent)
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
	e := PurchasePaidEmail{
		Name: strings.TrimSpace(name), CourseTitle: in.CourseTitle, Amount: formatMoney(in.AmountMinor, in.Currency),
		Method: methodLabel(in.Gateway, in.ManualMethod), PaidOn: in.PaidAt.In(nepalTime).Format("2 January 2006, 15:04 MST"),
	}
	if s.courseURLBase != "" && in.CourseID != "" {
		e.CourseURL = s.courseURLBase + "/courses/" + in.CourseID
	}
	subject, text, html, err := s.renderer.PurchasePaid(e)
	if err != nil {
		return fmt.Errorf("%w: render purchase paid: %w", ErrPermanent, err)
	}
	return s.mailer.Enqueue(ctx, Email{To: email, Subject: subject, Text: text, HTML: html}, PurchasePaidKey(in.EventID))
}

// PurchasePaidKey is the idempotency key for the email of one paid event.
// Changing the email's content requires a new version suffix.
func PurchasePaidKey(eventID string) string {
	return "ioe:payment.purchase.paid:" + eventID + ":paid-v1"
}

// formatMoney renders minor units with two decimals and thousands separators: "NPR 1,500.00".
func formatMoney(minor int64, currency string) string {
	whole := strconv.FormatInt(minor/100, 10)
	var b strings.Builder
	for i, r := range whole {
		if i > 0 && (len(whole)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	return fmt.Sprintf("%s %s.%02d", currency, b.String(), minor%100)
}

func methodLabel(gateway, manual string) string {
	switch {
	case gateway == "esewa":
		return "eSewa"
	case manual == "bank_transfer":
		return "Bank transfer"
	case manual == "cash":
		return "Cash"
	case manual == "other":
		return "Other"
	}
	return gateway
}
```

Amounts are always positive (database check `amount_minor > 0`), so `formatMoney` does not handle negatives.

- [ ] **Step 4: Write the failing template tests**

Append to `templates_test.go`:

```go
var paidEmail = app.PurchasePaidEmail{Name: "Sita", CourseTitle: "Go", Amount: "NPR 1,500.00", Method: "eSewa",
	PaidOn: "7 October 2026, 10:00 NPT", CourseURL: "https://app.test/courses/11"}

func renderPaid(t *testing.T, e app.PurchasePaidEmail) (string, string, string) {
	t.Helper()
	r, err := templates.New()
	if err != nil {
		t.Fatal(err)
	}
	subject, text, html, err := r.PurchasePaid(e)
	if err != nil {
		t.Fatal(err)
	}
	return subject, text, html
}

func TestPurchasePaid(t *testing.T) {
	subject, text, html := renderPaid(t, paidEmail)
	if subject != "Payment received: Go" {
		t.Fatalf("subject %q", subject)
	}
	for _, want := range []string{"Hello, Sita,", "Go", "NPR 1,500.00", "eSewa", "7 October 2026, 10:00 NPT", "enrolled", "https://app.test/courses/11"} {
		if !strings.Contains(text, want) || !strings.Contains(html, want) {
			t.Fatalf("missing %q\ntext=%s\nhtml=%s", want, text, html)
		}
	}
	if !strings.Contains(html, `<a href="https://app.test/courses/11">`) {
		t.Fatalf("html link: %s", html)
	}
}

func TestPurchasePaidWithoutTitleNameOrLink(t *testing.T) {
	e := paidEmail
	e.CourseTitle, e.Name, e.CourseURL = "", " ", ""
	subject, text, html := renderPaid(t, e)
	if subject != "Payment received" {
		t.Fatalf("subject %q", subject)
	}
	if !strings.HasPrefix(text, "Hello,\n") || strings.Contains(text, "http") || strings.Contains(html, "<a ") {
		t.Fatalf("text=%s html=%s", text, html)
	}
}

func TestPurchasePaidEscapesHTMLOnly(t *testing.T) {
	e := paidEmail
	e.CourseTitle, e.Name = "<b>Go</b>", "<i>Sita</i>"
	subject, text, html := renderPaid(t, e)
	if subject != "Payment received: <b>Go</b>" || !strings.Contains(text, "<b>Go</b>") || !strings.Contains(text, "<i>Sita</i>") {
		t.Fatalf("subject=%q text=%s", subject, text)
	}
	if strings.Contains(html, "<b>") || strings.Contains(html, "<i>") || !strings.Contains(html, "&lt;b&gt;Go&lt;/b&gt;") {
		t.Fatalf("html not escaped: %s", html)
	}
}
```

- [ ] **Step 5: Run to verify failure**

Run: `go test ./internal/notification/adapters/templates/`
Expected: FAIL — `r.PurchasePaid undefined`.

- [ ] **Step 6: Templates and renderer**

`purchase_paid.txt.tmpl`:

```text
Hello{{with .Name}}, {{.}}{{end}},

We received your payment{{with .CourseTitle}} for {{.}}{{end}}. You are enrolled.

Amount: {{.Amount}}
Method: {{.Method}}
Date:   {{.PaidOn}}
{{with .CourseURL}}
Start learning: {{.}}
{{end}}
The IOE team
```

`purchase_paid.html.tmpl`:

```html
<p>Hello{{with .Name}}, {{.}}{{end}},</p>
<p>We received your payment{{with .CourseTitle}} for {{.}}{{end}}. You are enrolled.</p>
<p>Amount: {{.Amount}}<br>Method: {{.Method}}<br>Date: {{.PaidOn}}</p>
{{with .CourseURL}}<p><a href="{{.}}">Start learning</a>: {{.}}</p>{{end}}
<p>The IOE team</p>
```

`templates.go`:

```go
//go:embed welcome.txt.tmpl welcome.html.tmpl purchase_paid.txt.tmpl purchase_paid.html.tmpl
var files embed.FS

const (
	welcomeSubject = "Welcome to IOE"
	paidSubject    = "Payment received"
)

// Renderer renders every email. It is safe for concurrent use.
type Renderer struct {
	welcomeText *texttemplate.Template
	welcomeHTML *htmltemplate.Template
	paidText    *texttemplate.Template
	paidHTML    *htmltemplate.Template
}

// New parses the embedded templates.
func New() (*Renderer, error) {
	r := &Renderer{}
	var err error
	if r.welcomeText, err = texttemplate.ParseFS(files, "welcome.txt.tmpl"); err != nil {
		return nil, err
	}
	if r.welcomeHTML, err = htmltemplate.ParseFS(files, "welcome.html.tmpl"); err != nil {
		return nil, err
	}
	if r.paidText, err = texttemplate.ParseFS(files, "purchase_paid.txt.tmpl"); err != nil {
		return nil, err
	}
	if r.paidHTML, err = htmltemplate.ParseFS(files, "purchase_paid.html.tmpl"); err != nil {
		return nil, err
	}
	return r, nil
}

// Welcome renders the welcome email. A blank name is omitted from the greeting.
func (r *Renderer) Welcome(name string) (subject, text, html string, err error) {
	text, html, err = execute(r.welcomeText, r.welcomeHTML, welcomeData{Name: strings.TrimSpace(name)})
	return welcomeSubject, text, html, err
}

// PurchasePaid renders the payment-received email. Blank name, title and link are omitted.
func (r *Renderer) PurchasePaid(e app.PurchasePaidEmail) (subject, text, html string, err error) {
	e.Name, e.CourseTitle = strings.TrimSpace(e.Name), strings.TrimSpace(e.CourseTitle)
	subject = paidSubject
	if e.CourseTitle != "" {
		subject += ": " + e.CourseTitle
	}
	text, html, err = execute(r.paidText, r.paidHTML, e)
	return subject, text, html, err
}

func execute(t *texttemplate.Template, h *htmltemplate.Template, data any) (string, string, error) {
	var textBuf, htmlBuf bytes.Buffer
	if err := t.Execute(&textBuf, data); err != nil {
		return "", "", err
	}
	if err := h.Execute(&htmlBuf, data); err != nil {
		return "", "", err
	}
	return textBuf.String(), htmlBuf.String(), nil
}
```

Import `"github.com/santoshkc2200/ioe-backend/internal/notification/app"` (adapters may import their own context's app). Keep `welcomeData`.

- [ ] **Step 7: Keep `cmd/api` compiling**

`cmd/api/users.go` add (imports `notificationapp "github.com/santoshkc2200/ioe-backend/internal/notification/app"`):

```go
func (u identityUsers) Contact(ctx context.Context, userID string) (string, string, error) {
	uid, err := id.Parse(userID)
	if err != nil {
		return "", "", notificationapp.ErrUnknownUser
	}
	user, err := u.svc.GetMe(ctx, uid)
	if errors.Is(err, identityapp.ErrNotFound) {
		return "", "", notificationapp.ErrUnknownUser
	}
	if err != nil {
		return "", "", err
	}
	return user.Email, user.Name, nil
}
```

`cmd/api/app.go`: `registerNotifications` gains a `users notificationapp.UserDirectory` parameter after `cfg`; call it as `registerNotifications(fw, cfg, identityUsers{svc: identity}, logger)`; build the service with `notificationapp.NewService(mailer, renderer, users, cfg.PaymentReturnURL)`. The paid handler is registered in Task 7.

- [ ] **Step 8: Run tests**

Run: `go build ./... && go test ./internal/notification/... ./cmd/api/ && make lint`
Expected: PASS

- [ ] **Step 9: Commit**

```bash
git add internal/notification cmd/api
git commit -m "feat(notification): render payment received emails"
```

---

### Task 7: Paid event handler and registration

**Files:**
- Modify: `internal/notification/adapters/events/events.go`, `events_test.go`
- Modify: `cmd/api/app.go`

**Interfaces:**
- Consumes: `app.PurchasePaidInput`, `(*Service).SendPurchasePaid` (Task 6).
- Produces:
  - `events.Sender{ SendWelcome; SendPurchasePaid }` replacing `WelcomeSender`; `events.New(sender Sender, logger, meter)`.
  - `(*events.Handlers).PurchasePaid(msg *message.Message) error`, registered on `payment.purchase.paid`.

- [ ] **Step 1: Write the failing handler tests**

In `events_test.go`, `fakeSender` gains:

```go
	paidCalls int
	paid      app.PurchasePaidInput
```

and:

```go
func (f *fakeSender) SendPurchasePaid(_ context.Context, in app.PurchasePaidInput) error {
	f.paidCalls++
	f.paid = in
	return f.err
}
```

Tests:

```go
const paidPayload = `{"purchase_id":"9","user_id":"200","course_id":"11","course_title":"Go","amount_minor":150000,
"currency":"NPR","gateway":"manual","gateway_txn":"R-1","manual_method":"cash","occurred_at":"2026-10-07T04:15:00Z"}`

func TestPurchasePaidSends(t *testing.T) {
	s := &fakeSender{}
	h, _ := newHandlers(t, s)
	if err := h.PurchasePaid(message.NewMessage("ev-9", []byte(paidPayload))); err != nil {
		t.Fatal(err)
	}
	want := app.PurchasePaidInput{EventID: "ev-9", UserID: "200", CourseID: "11", CourseTitle: "Go", AmountMinor: 150000,
		Currency: "NPR", Gateway: "manual", ManualMethod: "cash", PaidAt: time.Date(2026, 10, 7, 4, 15, 0, 0, time.UTC)}
	if s.paidCalls != 1 || s.paid != want {
		t.Fatalf("calls=%d in=%+v", s.paidCalls, s.paid)
	}
}

func TestPurchasePaidDropsInvalidPayloadAndPermanentFailure(t *testing.T) {
	s := &fakeSender{}
	h, logs := newHandlers(t, s)
	if err := h.PurchasePaid(message.NewMessage("ev-1", []byte("not json"))); err != nil || s.paidCalls != 0 {
		t.Fatalf("err=%v calls=%d", err, s.paidCalls)
	}
	s.err = fmt.Errorf("%w: unknown user", app.ErrPermanent)
	if err := h.PurchasePaid(message.NewMessage("ev-2", []byte(paidPayload))); err != nil {
		t.Fatalf("permanent err = %v", err)
	}
	if !strings.Contains(logs.String(), "invalid_payload") || !strings.Contains(logs.String(), "permanent") {
		t.Fatalf("logs = %s", logs)
	}
}

func TestPurchasePaidReturnsRetryableFailure(t *testing.T) {
	errDown := errors.New("down")
	h, _ := newHandlers(t, &fakeSender{err: errDown})
	if err := h.PurchasePaid(message.NewMessage("ev-1", []byte(paidPayload))); !errors.Is(err, errDown) {
		t.Fatalf("err = %v", err)
	}
}
```

Add `"time"` to the imports. Run `go test ./internal/notification/adapters/events/`; expected FAIL — `h.PurchasePaid undefined`.

- [ ] **Step 2: Implement the handler**

In `events.go`, replace `WelcomeSender` with:

```go
// Sender is the notification use-case surface the handlers call.
type Sender interface {
	SendWelcome(ctx context.Context, in app.WelcomeInput) error
	SendPurchasePaid(ctx context.Context, in app.PurchasePaidInput) error
}
```

Rename the `Handlers.welcome` field to `sender Sender`; `New(sender Sender, logger *slog.Logger, meter metric.Meter)`; `Welcome` calls `h.sender.SendWelcome`. Add (`"time"` import):

```go
// purchasePaid mirrors the payment.purchase.paid payload this context relies on.
type purchasePaid struct {
	UserID       string    `json:"user_id"`
	CourseID     string    `json:"course_id"`
	CourseTitle  string    `json:"course_title"`
	AmountMinor  int64     `json:"amount_minor"`
	Currency     string    `json:"currency"`
	Gateway      string    `json:"gateway"`
	ManualMethod string    `json:"manual_method"`
	OccurredAt   time.Time `json:"occurred_at"`
}

// PurchasePaid handles payment.purchase.paid with the same retry and drop rules as Welcome.
func (h *Handlers) PurchasePaid(msg *message.Message) error {
	var ev purchasePaid
	if err := json.Unmarshal(msg.Payload, &ev); err != nil {
		h.drop(msg, "invalid_payload", err)
		return nil
	}
	err := h.sender.SendPurchasePaid(msg.Context(), app.PurchasePaidInput{
		EventID: msg.UUID, UserID: ev.UserID, CourseID: ev.CourseID, CourseTitle: ev.CourseTitle,
		AmountMinor: ev.AmountMinor, Currency: ev.Currency, Gateway: ev.Gateway, ManualMethod: ev.ManualMethod,
		PaidAt: ev.OccurredAt,
	})
	if errors.Is(err, app.ErrPermanent) {
		h.drop(msg, "permanent", err)
		return nil
	}
	return err
}
```

`Welcome` and `PurchasePaid` share the "permanent means drop" tail; leave them as two short functions rather than adding a helper.

- [ ] **Step 3: Run tests**

Run: `go test ./internal/notification/...`
Expected: PASS

- [ ] **Step 4: Register the handler**

In `cmd/api/app.go` `registerNotifications`, after the welcome registration (add the `paymentdomain` import, already used in `cmd/api/payment.go`):

```go
	fw.Handle(paymentdomain.PurchasePaid{}.EventName(), handlers.PurchasePaid)
```

Run: `go build ./... && go test ./cmd/api/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/notification cmd/api/app.go
git commit -m "feat(notification): send payment received emails on paid purchases"
```

---

### Task 8: End-to-end test and gates

**Files:**
- Test: `cmd/api/e2e_integration_test.go` (new `TestManualPaymentEndToEnd`)

**Interfaces:**
- Consumes: everything above.
- Produces: proof that the whole flow works through `cmd/api`.

- [ ] **Step 1: Write the failing end-to-end test**

Append to `cmd/api/e2e_integration_test.go` (reuse `client`, `baseConfig`, `notifyKey`, `appOrigin`, `googletest`):

```go
func TestManualPaymentEndToEnd(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pool := pgtest.New(t)
	google := googletest.NewIssuer(t)

	type sentEmail struct{ key, recipient, subject string }
	sent := make(chan sentEmail, 8)
	notify := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Recipient string `json:"recipient"`
			Subject   string `json:"subject"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		sent <- sentEmail{key: r.Header.Get("Idempotency-Key"), recipient: body.Recipient, subject: body.Subject}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer notify.Close()
	cfg := baseConfig(t, google)
	cfg.NotificationServiceBaseURL = notify.URL
	cfg.NotificationServiceSendAPIKey = notifyKey
	a, err := buildApp(ctx, cfg, slog.New(slog.NewJSONHandler(io.Discard, nil)), pool)
	if err != nil {
		t.Fatal(err)
	}
	defer a.forwarder.Close()
	go func() { _ = a.forwarder.Run(ctx) }()
	srv := httptest.NewServer(a.handler)
	defer srv.Close()
	c := client{t: t, base: srv.URL}
	signIn := func(sub, email string) map[string]string {
		t.Helper()
		tok := google.Sign(t, googletest.Claims(sub, email, "web-client", time.Now()))
		resp, body := c.do(http.MethodPost, "/v1/auth/google", `{"id_token":"`+tok+`"}`, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("sign in %s: %d %v", email, resp.StatusCode, body)
		}
		return map[string]string{"Authorization": "Bearer " + body["access_token"].(string)}
	}
	admin := signIn("sub-admin", "admin@example.com")
	student := signIn("sub-student", "student@example.com")
	_, me := c.do(http.MethodGet, "/v1/me", "", student)
	studentID := me["id"].(string)

	resp, body := c.do(http.MethodPost, "/v1/courses", `{"title":"Go","description":"d"}`, admin)
	courseID := body["id"].(string)
	if resp, body = c.do(http.MethodPost, "/v1/courses/"+courseID+"/price", `{"amount_minor":150000,"currency":"NPR"}`, admin); resp.StatusCode != http.StatusOK {
		t.Fatalf("price: %d %v", resp.StatusCode, body)
	}
	resp, body = c.do(http.MethodPost, "/v1/courses/"+courseID+"/lectures", `{"title":"L1","text_body":"<p>x</p>"}`, admin)
	lectures := body["lectures"].([]any)
	lectureID := lectures[len(lectures)-1].(map[string]any)["id"].(string)
	if resp, _ = c.do(http.MethodPost, "/v1/courses/"+courseID+"/publish", "", admin); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("publish: %d", resp.StatusCode)
	}

	record := `{"course_id":"` + courseID + `","amount_minor":120000,"currency":"NPR","method":"cash","reference":"R-77","note":"desk"}`
	if resp, body = c.do(http.MethodPost, "/v1/users/"+studentID+"/purchases", record, student); resp.StatusCode != http.StatusForbidden || body["type"] != "forbidden" {
		t.Fatalf("student record: %d %v", resp.StatusCode, body)
	}
	resp, body = c.do(http.MethodPost, "/v1/users/"+studentID+"/purchases", record, admin)
	if p := body["purchase"].(map[string]any); resp.StatusCode != http.StatusCreated || p["status"] != "paid" || p["granted"] != true ||
		p["manual_method"] != "cash" || p["reference"] != "R-77" || p["course_title"] != "Go" {
		t.Fatalf("record: %d %v", resp.StatusCode, body)
	}
	if resp, body = c.do(http.MethodPost, "/v1/users/"+studentID+"/purchases", record, admin); resp.StatusCode != http.StatusConflict || body["type"] != "already_purchased" {
		t.Fatalf("second record: %d %v", resp.StatusCode, body)
	}
	if resp, _ = c.do(http.MethodGet, "/v1/courses/"+courseID+"/lectures/"+lectureID+"/content", "", student); resp.StatusCode != http.StatusOK {
		t.Fatalf("read after manual payment: %d", resp.StatusCode)
	}

	resp, body = c.do(http.MethodGet, "/v1/me/purchases", "", student)
	items, _ := body["items"].([]any)
	if resp.StatusCode != http.StatusOK || len(items) != 1 || items[0].(map[string]any)["amount_minor"] != float64(120000) {
		t.Fatalf("my purchases: %d %v", resp.StatusCode, body)
	}
	if resp, body = c.do(http.MethodGet, "/v1/users/"+studentID+"/purchases", "", admin); resp.StatusCode != http.StatusOK || len(body["items"].([]any)) != 1 {
		t.Fatalf("admin list: %d %v", resp.StatusCode, body)
	}
	_, adminMe := c.do(http.MethodGet, "/v1/me", "", admin)
	if resp, _ = c.do(http.MethodGet, "/v1/users/"+adminMe["id"].(string)+"/purchases", "", student); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("student reads admin list: %d", resp.StatusCode)
	}

	deadline := time.After(30 * time.Second)
	for {
		select {
		case got := <-sent:
			if !strings.HasPrefix(got.key, "ioe:payment.purchase.paid:") {
				continue // welcome emails
			}
			if got.recipient != "student@example.com" || got.subject != "Payment received: Go" || !strings.HasSuffix(got.key, ":paid-v1") {
				t.Fatalf("paid email %+v", got)
			}
			return
		case <-deadline:
			t.Fatal("paid email not enqueued")
		}
	}
}
```

Before running, open `internal/notification/adapters/notifysvc/client.go` and confirm the request JSON field names for recipient and subject; adjust the decode struct tags if they differ from `recipient` / `subject`.

- [ ] **Step 2: Run the test**

Run: `go vet -tags integration ./cmd/api/`
Expected: compiles. Then `go test -tags integration ./cmd/api/ -run TestManualPaymentEndToEnd` — Expected: PASS, since Tasks 1–7 implement the flow. If it fails, the failure points at a real gap; fix it in the owning package, not in the test.

- [ ] **Step 3: Run all gates**

Run:

```bash
make check
make test-integration
docker compose config
make docker-build
git diff --check
```

Expected: all pass. `make test-integration` needs Docker; if Docker is unavailable, report that integration tests did not run.

- [ ] **Step 4: Commit**

```bash
git add cmd/api/e2e_integration_test.go
git commit -m "test(api): cover manual payment, history and paid email end to end"
```
