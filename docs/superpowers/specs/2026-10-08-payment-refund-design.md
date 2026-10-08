# Payment Refund Design

Date: 2026-10-08

## Status

Approved in conversation on 2026-10-08. Pending written-spec review.

## Context

The eSewa slice (`2026-10-07-payment-esewa-design.md`) and the follow-ups slice
(`2026-10-07-payment-followups-design.md`) left refunds out. Today a refund is made by hand in the
eSewa merchant portal (eSewa has no public merchant refund API) or paid back offline, the purchase
stays `paid`, and a manager cancels the enrollment with the existing endpoint.

Current state:

- `domain.Purchase` has statuses `pending`, `paid`, `failed`. `GrantedAt` records the enrollment
  grant; `NeedsGrant` is `paid` and ungranted.
- `app.Service.settle` grants the enrollment after the purchase commits. A failed grant is retried by
  `Reconcile`, which reads `Repository.ListUnsettled` (pending past grace, or paid and ungranted).
- `EnrollmentGranter` (backed by enrollment) has `IsEnrolled` and `GrantPurchased`.
- `enrollment/app.Service.Cancel` cancels with a reason and emits `EnrollmentCanceled`.
- `notification` emails the buyer on `payment.purchase.paid`.
- `warnIfDuplicate` logs when two purchases of the same course are both paid so one can be refunded
  by hand.

## Goals

- A root admin records that a paid purchase was refunded in full, with the refund reference from the
  eSewa portal or bank.
- Recording the refund ends the student's access to the course, unless they hold another paid
  purchase of it.
- The student receives one "refund issued" email.
- The student and the admin see the refund on the purchase.

## Non-goals

- Issuing refunds through a gateway API. The money moves outside this system.
- Partial refunds, goodwill credits, or refunding without revoking access.
- Student-initiated refund requests or an approval workflow.
- Refund windows or eligibility rules.
- Acting on eSewa's `FULL_REFUND` / `PARTIAL_REFUND` status; it stays a warning.
- Distinguishing a purchased enrollment from a manager-granted one.
- Admin reporting, exports, the frontend UI.

## Decisions

| Topic | Decision |
|---|---|
| Who | Root admin only; anyone else gets 404 |
| Refundable | Only `paid` purchases, eSewa or manual |
| Amount | Always the full purchase price |
| Effect | Status becomes `refunded`; enrollment is canceled with reason `refunded` |
| Other paid purchase | Access kept; revocation skipped |
| Cross-context write | After commit through `EnrollmentGranter.RevokePurchased`; retried by `Reconcile` |
| Repeat refund | 409 `not_refundable`, not idempotent success |
| Visibility | Refund fields visible to the buyer and root admin |
| Email | Yes, one per refund event |

## Domain (`internal/payment/domain`)

```go
const StatusRefunded Status = "refunded"

// Refund describes a refund a root admin records after paying the buyer back outside this system.
type Refund struct {
	Reference  string // eSewa portal or bank reference; required, at most 200 characters after trimming
	Note       string // optional, at most 1000 characters after trimming
	RefundedBy id.ID  // required
}
```

New `Purchase` fields: `RefundedAt time.Time`, `RefundedBy id.ID`, `RefundReference string`,
`RefundNote string`, `RevokedAt time.Time`. All are zero unless the purchase is refunded.

**`(*Purchase).Refund(r Refund, otherPaid bool, now time.Time) (Event, error)`**

- Status other than `paid`: `ErrNotRefundable`.
- Invalid reference, note or `RefundedBy`: `ErrInvalidPurchase`.
- Sets status `refunded`, `RefundedAt`, `RefundedBy`, `RefundReference`, `RefundNote`.
- When `otherPaid` is true, sets `RevokedAt = now` so no revocation is attempted.
- Returns `PurchaseRefunded`.

**`NeedsRevoke() bool`**: status `refunded` and `RevokedAt` zero.

**`MarkRevoked(now)`**: sets `RevokedAt` when `NeedsRevoke`; otherwise no change.

`NeedsGrant` already requires `paid`, so refunding a paid but ungranted purchase stops its grant.
`MarkPaid` on a refunded purchase must change nothing and return no event, so a late gateway report
cannot undo a refund; `MarkFailed` already ignores non-pending purchases.

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
	ManualMethod    string    `json:"manual_method"`
	RefundReference string    `json:"refund_reference"`
	AccessRevoked   bool      `json:"access_revoked"` // false when another paid purchase keeps access
	OccurredAt      time.Time `json:"occurred_at"`
}

func (PurchaseRefunded) EventName() string { return "payment.purchase.refunded" }
```

## Application (`internal/payment/app`)

`EnrollmentGranter` gains:

```go
// RevokePurchased cancels the user's enrollment after a refund. A missing or already canceled
// enrollment is success.
RevokePurchased(ctx context.Context, courseID, userID id.ID) error
```

**`Service.Refund(ctx, p auth.Principal, purchaseID id.ID, in RefundInput) (domain.Purchase, error)`**

1. `p.Role != RoleRootAdmin`: `ErrNotFound`.
2. In one transaction: load the purchase (`ErrNotFound` if missing), `CountPaid(userID, courseID)`,
   and call `Refund` with `otherPaid = count > 1` (the purchase itself is still `paid` when
   counted). Write the update and `PurchaseRefunded`. A stale version is retried once, as `update`
   does.
3. After commit, run `revoke`.
4. Domain `ErrNotRefundable` maps to app `ErrNotRefundable`; `ErrInvalidPurchase` to
   `ErrInvalidInput`.

**`revoke(ctx, p) (domain.Purchase, error)`**: when `NeedsRevoke`, call `RevokePurchased`; on error
log it and return `p` unchanged (the next reconcile retries); on success `update` with
`MarkRevoked`.

**`Reconcile`**: `ListUnsettled` also returns refunded, unrevoked purchases. For those, `Reconcile`
calls `revoke` instead of `settle`.

## Enrollment (`internal/enrollment/app`)

**`Service.CancelPurchased(ctx, courseID, userID id.ID) error`**: no principal check, like
`EnrollPurchased`. Loads the enrollment; a missing one is success. Cancels it through the existing
domain `Cancel` with reason `"refunded"`; an already canceled enrollment returns no event and is
success. Writes `EnrollmentCanceled` as `Cancel` does.

`cmd/api` wires the payment `EnrollmentGranter` adapter's `RevokePurchased` to it.

## Persistence

Migration `00015_payment_refund.sql`:

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
```

The Down migration restores the previous constraints and index and drops the new columns. It fails
while `refunded` rows exist, so refund records are never silently lost.

The implementer confirms the generated name of the inline status check (`purchases_status_check`)
against the database before relying on it.

`ListUnsettled` gains the refunded branch. `CountPaid` already counts only `paid`. The row mapping
and `Update` carry the new columns. Regenerate sqlc.

## HTTP (`internal/payment/adapters/httpapi`)

`POST /v1/purchases/{purchaseID}/refund`

```json
{ "reference": "ESEWA-RF-123", "note": "Duplicate payment" }
```

- `reference` required, 1-200 characters; `note` optional, at most 1000.
- `200` with `PurchaseResponse`.
- `400` invalid input, `404` missing purchase or caller not a root admin, `409 not_refundable` when
  the purchase is not `paid` (including already refunded).

`Purchase` schema changes:

- `status` enum adds `refunded`.
- New required, nullable fields: `refunded_at` (date-time), `refunded_by` (ID), `refund_reference`,
  `refund_note`.
- New required boolean `access_revoked`: true once `RevokedAt` is set.

Document the endpoint and fields in `api/openapi.yaml`.

## Notification (`internal/notification`)

- `app.PurchaseRefundedInput` mirrors the event plus `EventID`.
- `Service.SendPurchaseRefunded` follows `SendPurchasePaid`: contact lookup, permanent errors for a
  missing event ID, user or email, render, enqueue.
- `PurchaseRefundedEmail{Name, CourseTitle, Amount, Method, Reference, RefundedOn, AccessRevoked}`,
  formatted with `formatMoney`, `methodLabel` and Nepal time.
- Idempotency key `ioe:payment.purchase.refunded:<eventID>:refunded-v1`.
- The renderer gains `PurchaseRefunded` with text and HTML templates. The email says the refund was
  issued and, when `AccessRevoked`, that course access has ended.
- `adapters/events` adds a `PurchaseRefunded` handler with the same retry and drop rules, subscribed
  to `payment.purchase.refunded` in `cmd/api`.

## Error handling

- Revocation failure after commit leaves the purchase refunded and unrevoked; it is logged and
  retried every reconcile pass (about 5 minutes).
- A concurrent grant and refund: refund wins on the version check; the grant step's `update` finds a
  non-paid purchase and `MarkGranted` changes nothing. A grant call that reached enrollment first is
  then canceled by the revoke.
- A refund of a purchase whose course was since deleted: `CancelPurchased` finds no enrollment and
  succeeds.

## Testing

- Domain: `Refund` from each status; reference and note limits; `otherPaid` sets `RevokedAt`;
  `MarkPaid` on refunded is a no-op; `NeedsGrant` false after refund; `NeedsRevoke` / `MarkRevoked`.
- Payment app with fakes: non-admin gets `ErrNotFound`; refund revokes; failed revoke leaves
  `RevokedAt` zero and `Reconcile` revokes it; second paid purchase keeps access and skips
  `RevokePurchased`; event written in the refund transaction.
- Enrollment app: `CancelPurchased` cancels an active enrollment with reason `refunded`; missing and
  already canceled are success.
- Payment postgres integration: new constraints accept a refunded row and reject inconsistent ones;
  `ListUnsettled` returns refunded unrevoked rows only.
- HTTP: request validation, 404 for non-admin, 409 mapping, new response fields.
- Notification: email fields, revoked/kept wording, idempotency key, handler decoding.
- e2e: record a manual purchase, refund it, the enrollment is canceled and the refund email is
  enqueued.

## Known limitations

- An enrollment does not record why it exists; a refund cancels it even if a manager had also
  enrolled the student by hand.
- If the student pays again through another checkout after the refund, they are enrolled again.
- The eSewa status API reporting `FULL_REFUND` is still only logged.
