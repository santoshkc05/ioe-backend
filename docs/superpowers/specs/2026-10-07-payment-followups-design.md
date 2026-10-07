# Payment Follow-ups Design

Date: 2026-10-07

## Status

Approved in conversation on 2026-10-07. Pending written-spec review.

## Context

The eSewa payment slice (`2026-10-07-payment-esewa-design.md`) left three things out as
non-goals: purchase history, notification emails on payment, and any way to record a payment made
outside a gateway. This slice adds them.

Current state:

- `payment.purchases` stores one row per checkout attempt. Only `GET /v1/purchases/{purchaseID}`
  reads purchases over HTTP; nothing lists them.
- `payment.purchase.paid` carries IDs, amount, currency, gateway and the gateway transaction id. It
  has no buyer email and no course title, and nothing consumes it.
- The `notification` context sends one email, the welcome email, from an outbox handler on
  `identity.user_registered`. It holds no state and owns no schema.
- `courseauthoring/app.CourseFacts` has no title.
- A course manager can already enroll a student in a paid course without payment. There is no
  record that money changed hands.

## Goals

- A student lists their own purchases, newest first, in every status.
- A root admin lists any user's purchases.
- A root admin records a payment made by bank transfer, cash or another offline method. The
  purchase is created paid, the student is enrolled, and the same paid event is emitted as for
  eSewa.
- Every paid purchase, eSewa or manual, sends the buyer one "payment received" email.
- History and email show the course title as it was when the purchase was made.

## Non-goals

- Emails for failed or initiated purchases.
- Refunds, voiding or editing a manual purchase.
- Receipts or invoices as documents (PDF), admin reporting, totals or exports.
- Instructor access to purchases of their courses.
- Localization; English only.
- The frontend UI.

## Decisions

| Topic | Decision |
|---|---|
| Emails | Paid only |
| History scope | All statuses; own purchases for any signed-in user, any user's for root admin |
| Manual payment | Root admin only; admin enters amount, currency, method, reference and an optional note |
| Manual methods | `bank_transfer`, `cash`, `other` |
| Manual model | Gateway `manual`, created paid in one step; never registered as a `Gateway` |
| Already enrolled | Allowed for manual records; rejected for eSewa checkout as today |
| Course title | Snapshotted on the purchase; carried on `payment.purchase.paid` |
| Buyer email | Looked up by notification through a `UserDirectory` port backed by identity |
| User existence | Payment checks it through its own `UserDirectory` port backed by identity |
| Cursor | Last purchase ID as a decimal string, as in `GET /v1/courses` |

## Domain (`internal/payment/domain`)

`Purchase` gains:

```go
CourseTitle  string // snapshot taken when the purchase is created
ManualMethod string // "bank_transfer", "cash" or "other"; empty unless Gateway is "manual"
RecordedBy   id.ID  // root admin who recorded a manual purchase; zero otherwise
Note         string // admin's free-text remark on a manual purchase; may be empty
```

```go
const GatewayManual = "manual"

const (
    MethodBankTransfer = "bank_transfer"
    MethodCash         = "cash"
    MethodOther        = "other"
)
```

Operations:

- `NewPurchase` gains a `courseTitle` parameter and stores it. An empty title is allowed, so a
  course with no title cannot block checkout.
- `RecordManualPurchase(purchaseID, userID, courseID, courseTitle, price, method, reference, note,
  recordedBy, now)` returns a paid purchase and a `PurchasePaid` event. It does not emit
  `PurchaseInitiated`. The purchase has `Gateway = "manual"`, `GatewayRef` = the purchase ID,
  `GatewayTxn` = `reference`, `Status = paid`, `CreatedAt = SettledAt = now`, and `GrantedAt`
  zero.
  - Amount zero or negative returns `ErrFreePrice`.
  - Empty currency, a method outside the three constants, an empty (after trimming) reference, a
    reference longer than 200 bytes, a note longer than 1000 bytes, or a zero `recordedBy` returns
    `ErrInvalidPurchase`.
- `MarkPaid`, `MarkFailed`, `MarkGranted`, `NeedsGrant` and `CanView` are unchanged.

`PurchasePaid` gains two additive JSON fields:

```go
CourseTitle  string `json:"course_title"`
ManualMethod string `json:"manual_method"` // empty for gateway purchases
```

`MarkPaid` fills both from the purchase.

## Storage

Migration `migrations/00012_payment_history.sql`:

```sql
ALTER TABLE payment.purchases
  ADD COLUMN course_title  text   NOT NULL DEFAULT '',
  ADD COLUMN manual_method text,
  ADD COLUMN recorded_by   bigint,
  ADD COLUMN note          text   NOT NULL DEFAULT '';

-- One-time backfill of existing rows from the course's working copy title.
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
```

The backfill is the one place payment reads another context's schema. It runs once, at deploy,
against sandbox data only, and is acceptable because no runtime code crosses the boundary.
The `Down` migration drops the index, the constraints and the four columns.

Repository additions:

- `ListByUser(ctx, userID, before id.ID, limit int) ([]domain.Purchase, error)`: purchases of
  `userID` with `id < before` (no bound when `before` is zero), ordered by `id DESC`.
- `Insert` and the row mapping carry the four new columns. `Update` is unchanged; the new columns
  never change after insert.

## Application (`internal/payment/app`)

Ports:

- `CourseFacts` gains `Title string`. `cmd/api` fills it from a new `Title` field on
  `courseauthoring/app.CourseFacts`, taken from the same version `Facts` already reads.
- New port:

  ```go
  // UserDirectory is backed by identity.
  type UserDirectory interface {
      // UserExists reports whether a user with this ID exists.
      UserExists(ctx context.Context, userID id.ID) (bool, error)
  }
  ```

- New error `ErrForbidden`.

Use cases:

- `Checkout` passes `CourseFacts.Title` to `NewPurchase`. Nothing else changes.
- `ListByUser(ctx, p, userID, before, limit) (items []domain.Purchase, next id.ID, err error)`:
  - Allowed when `p.UserID == userID` or `p.Role == root_admin`; otherwise `ErrNotFound`.
  - `limit` outside 1..50 returns `ErrInvalidInput`; the HTTP layer applies the default of 20.
  - Reads `limit+1` rows. When more than `limit` come back, returns the first `limit` and `next`
    = the ID of the last returned item; otherwise `next` is zero.
  - An unknown user ID returns an empty page, not an error.
- `RecordManual(ctx, p, in ManualInput) (domain.Purchase, error)` with
  `ManualInput{UserID, CourseID, AmountMinor, Currency, Method, Reference, Note}`:
  1. `p.Role != root_admin` returns `ErrForbidden`.
  2. Course facts: `ErrNotFound` when missing or unpublished; `ErrCourseFree` when the price is
     zero; `ErrInvalidInput` when `in.Currency` differs from the course price's currency.
  3. `UserExists` false returns `ErrNotFound`.
  4. In one transaction: `CountPaid > 0` returns `ErrAlreadyPurchased`; otherwise insert the result
     of `RecordManualPurchase` and publish its `PurchasePaid` event.
  5. After commit, call the existing `settle` with the new purchase. Because it is paid, `settle`
     skips the gateway and only grants: `GrantPurchased`, then `MarkGranted`. A grant failure is
     logged and left for the reconciler, which already retries paid, ungranted purchases.
  6. Domain errors `ErrFreePrice` and `ErrInvalidPurchase` map to `ErrInvalidInput`.
  - The amount may differ from the course price. An existing active enrollment does not block the
    record.

The reconciler and `Confirm` are unchanged. A manual purchase is always paid, and `settle` looks up
a gateway only for unpaid purchases, so the unregistered `manual` gateway is never looked up.

## HTTP (`internal/payment/adapters/httpapi`)

| Route | Who | Success |
|---|---|---|
| `GET /v1/me/purchases?limit=&cursor=` | any signed-in user | 200 `{items, next_cursor}` |
| `GET /v1/users/{userID}/purchases?limit=&cursor=` | root admin, or the user themself | 200 `{items, next_cursor}` |
| `POST /v1/users/{userID}/purchases` | root admin | 201 `{purchase}` |

- `limit` is 1..50, default 20. `cursor` is the previous page's `next_cursor`, an opaque string
  that is in practice the last purchase ID. `next_cursor` is omitted on the last page, as in
  `CatalogPage`.
- Manual record body:

  ```json
  {"course_id": "…", "amount_minor": 150000, "currency": "NPR",
   "method": "bank_transfer", "reference": "Nabil voucher 4471", "note": ""}
  ```

  Unknown fields are rejected, as in the other handlers.

Purchase wire shape gains:

```text
course_title   string
manual_method  string | null   null unless gateway is "manual"
reference      string | null   GatewayTxn for manual purchases; null for gateway purchases
note           string | null   null unless gateway is "manual"
recorded_by    string | null   null unless gateway is "manual"
```

The eSewa transaction id stays out of the wire shape, as today.

Error mapping adds `ErrForbidden` as 403 `forbidden`. `invalid_input`, `not_found`, `course_free`
and `already_purchased` are reused.

`api/openapi.yaml` documents the three routes, a `PurchasePage` schema, a `ManualPurchaseRequest`
schema, and the new `Purchase` fields.

## Notification (`internal/notification`)

Application:

```go
// UserDirectory is backed by identity.
type UserDirectory interface {
    // Contact returns ErrUnknownUser when the user does not exist.
    Contact(ctx context.Context, userID string) (email, name string, err error)
}

type PurchasePaidInput struct {
    EventID      string // outbox message UUID
    UserID       string
    CourseID     string
    CourseTitle  string
    AmountMinor  int64
    Currency     string
    Gateway      string
    ManualMethod string
    PaidAt       time.Time
}
```

- IDs stay strings so `notification/app` keeps importing only the standard library.
- `Service.SendPurchasePaid` looks up the contact, renders and enqueues with key
  `ioe:payment.purchase.paid:<EventID>:paid-v1`.
  - Missing `EventID` or `UserID`, an unknown user, an empty email, or a render failure wrap
    `ErrPermanent`.
  - Any other lookup or enqueue error is returned, so the forwarder retries.
- `Renderer` gains `PurchasePaid(name, courseTitle, amount, method string, paidAt time.Time,
  courseURL string)`. The service formats `amount` as `NPR 1,500.00` from minor units and maps
  the method to "eSewa", "Bank transfer", "Cash" or "Other".
- `NewService` gains the directory and a course URL base. The course link is
  `<PAYMENT_RETURN_URL>/courses/<courseID>`; when `PAYMENT_RETURN_URL` is unset the link is
  omitted.

Templates: `purchase_paid.txt.tmpl` and `purchase_paid.html.tmpl`, embedded next to the welcome
templates. Subject: `Payment received: <course title>`, or `Payment received` when the title is
empty. The body greets by name (omitted when blank), states the course, amount, method and date,
says the student is enrolled, and links to the course when a link is available.

Events adapter: `Handlers.PurchasePaid` decodes its own `purchasePaid` mirror struct (it never
imports payment), calls `SendPurchasePaid`, and drops malformed payloads and permanent failures
with the existing `drop` helper and counter.

Wiring: `registerNotifications` registers the handler on `payment.purchase.paid` next to the
welcome handler, under the same "both or neither" notification-service settings. `cmd/api`
implements `notificationapp.UserDirectory` with `identityapp.Service.GetMe`, mapping identity's
not-found error to `ErrUnknownUser`, and `paymentapp.UserDirectory` with the same call.

## Rollout

- The migration runs before the new binary starts, as for every migration.
- Paid events already in the outbox when the consumer is first registered have no
  `course_title`; their email uses the "Payment received" subject without a title. They are
  sandbox events only.

## Testing

- Payment domain: `RecordManualPurchase` success and each rejection; `NewPurchase` stores the
  title; `PurchasePaid` payload includes `course_title` and `manual_method` from both paths.
- Payment app with fakes:
  - `ListByUser`: self, admin, other user gets `ErrNotFound`, limit bounds, paging and `next`,
    unknown user gets an empty page.
  - `RecordManual`: non-admin `ErrForbidden`, missing, unpublished and free courses, currency
    mismatch, unknown user, already purchased, already enrolled succeeds, event published in the
    transaction, grant failure leaves `GrantedAt` zero and `Reconcile` later grants.
  - `Checkout` stores the course title.
- Payment Postgres integration: new columns round trip, `purchases_manual_consistent` and
  `purchases_manual_method` reject bad rows, `ListByUser` order and paging, migration backfill of
  an existing row.
- Payment HTTP: the three routes, query validation, cursor round trip, 403, 404, 409, JSON shapes.
- Notification: renderer output for paid email with and without title, name and link; amount
  formatting; method labels; `SendPurchasePaid` permanent versus retryable errors; idempotency
  key; handler drops malformed payloads and unknown users and returns transient errors.
- End to end through `cmd/api` with a fake notification service:
  - A root admin records a cash payment; the student lists `/v1/me/purchases`, sees it with its
    title, and reads a non-preview lecture.
  - The outbox holds one `payment.purchase.paid` with `course_title`, and the fake notification
    service receives one email with the paid idempotency key.
  - A student calling `POST /v1/users/{id}/purchases` gets 403.

## Risks

- The outbox forwarder is shared and blocks on a failing handler, so a notification-service
  outage now delays welcome and paid emails together. Enrollment grants stay synchronous and are
  unaffected. This is the existing risk with a second consumer added, not a new kind.
- A mistyped manual purchase cannot be edited or voided in this slice. The admin cancels the
  enrollment with the existing endpoint; the purchase row stays paid.
- Manual amounts are not checked against the course price, so a typo records the wrong amount.
- A user's email change between payment and delivery sends the email to the new address, which
  is the intended behavior.
