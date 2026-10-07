# Payment (eSewa) Design

Date: 2026-10-07

## Status

Approved in conversation on 2026-10-07. Pending written-spec review.

## Context

Payment is the last sub-project named in the enrollment spec (`2026-10-05-enrollment-design.md`),
which listed eSewa, Khalti and connectIPS. This slice delivers eSewa only, against the eSewa
sandbox. Stripe (multiple currencies) will follow, so gateway-specific behavior sits behind a port
and amounts carry their currency.

Today a student cannot buy a course. Self-enrollment in a published paid course returns 402
`payment_required` (`internal/enrollment/domain`), and only a course manager can enroll a student
in a paid course.

Courseauthoring stores prices as `price_amount_minor` (paisa) and `price_currency`, which must be
`NPR` for a non-zero price. `SetPrice` emits no event, so course facts are read synchronously, as
enrollment already does.

`hitox-backend/internal/billing` has a Stripe `Purchase` aggregate (pending, paid, failed,
refunded; price snapshot at checkout). This slice borrows its shape and trims tenancy, refunds and
Stripe.

eSewa ePay v2 works as follows:

- The merchant signs the form fields `total_amount,transaction_uuid,product_code` with
  HMAC-SHA256 and its secret key, base64-encoded.
- The browser posts the form to eSewa. After payment, eSewa redirects the browser to `success_url`
  with a signed base64 `data` query parameter, or to `failure_url`.
- eSewa makes no server-to-server callback. The status API
  (`GET /api/epay/transaction/status/?product_code=&total_amount=&transaction_uuid=`) is
  authoritative and is the only source this design trusts.
- Refunds are made in the eSewa merchant portal; there is no public merchant refund API.

## Goals

- A student buys a published paid course with eSewa and is enrolled as soon as the payment is
  confirmed.
- A payment completed while the student closed the tab is still confirmed and enrolled, without
  the student returning.
- Every purchase state change writes an outbox event in the same transaction.
- Adding Stripe later means adding a gateway adapter (and a webhook endpoint that reuses the
  confirm use case), not changing the domain.

## Non-goals

- Refunds of any kind. A manager cancels the enrollment with the existing endpoint and refunds in
  the eSewa portal; the purchase stays `paid`.
- Stripe, Khalti, connectIPS, and multiple currencies.
- Purchase history lists, receipts, invoices, admin reporting.
- Notification emails on payment, and consumers of the new events.
- The frontend UI.

## Decisions

| Topic | Decision |
|---|---|
| Scope | eSewa only, sandbox only |
| Context | New `payment` context with its own `payment` schema |
| Granting access | After the paid state commits, payment calls an `EnrollmentGranter` port synchronously |
| Why not events | The shared outbox forwarder blocks on a stalled handler, so a notification outage would stop paid students from being enrolled |
| Source of truth | eSewa status API only; the redirect payload is ignored |
| Checkout | Always creates a new pending purchase; earlier pending purchases are settled by the reconciler |
| Late completion | `failed` can still become `paid` when eSewa later reports `COMPLETE` |
| Recovery | A background reconciler settles stale pending purchases and retries failed grants |
| Refunds | None |
| Configuration | Payments are disabled with a warning when eSewa settings are unset |

## Domain (`internal/payment/domain`)

```go
// Money is an amount in the currency's minor unit. Currency is an ISO 4217 code.
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

type Purchase struct {
    ID         id.ID
    UserID     id.ID
    CourseID   id.ID
    Price      Money     // snapshot taken at checkout
    Gateway    string    // "esewa"
    GatewayRef string    // our reference at the gateway; for eSewa, transaction_uuid
    GatewayTxn string    // the gateway's transaction id; set when paid
    Status     Status
    CreatedAt  time.Time
    SettledAt  time.Time // zero while pending
    GrantedAt  time.Time // zero until the enrollment grant succeeds
    Version    int64
}
```

Fields are exported; the repository builds `Purchase` values directly.

Operations:

- `NewPurchase(id, userID, courseID, price, gateway, now)` returns a pending purchase whose
  `GatewayRef` is the purchase ID in decimal, and a `PurchaseInitiated` event. A zero or negative
  amount returns `ErrFreePrice`; an empty currency or gateway returns `ErrInvalidPurchase`.
- `MarkPaid(txn, now)`: on a pending or failed purchase it sets `Status`, `GatewayTxn` and
  `SettledAt` and returns a `PurchasePaid` event. On a paid purchase it changes nothing and returns
  no event. An empty `txn` returns `ErrInvalidPurchase`.
- `MarkFailed(now)`: on a pending purchase it sets `Status` and `SettledAt` and returns a
  `PurchaseFailed` event. On any other status it changes nothing and returns no event.
- `MarkGranted(now)`: on a paid purchase with a zero `GrantedAt` it sets `GrantedAt`. It returns
  no event. On any other purchase it changes nothing.
- `CanView(p auth.Principal)` reports whether the principal is the buyer or a `root_admin`.

Events follow identity's `EventName()` style:

| Name | Payload |
|---|---|
| `payment.purchase.initiated` | `purchase_id`, `user_id`, `course_id`, `amount_minor`, `currency`, `gateway`, `occurred_at` |
| `payment.purchase.paid` | `purchase_id`, `user_id`, `course_id`, `amount_minor`, `currency`, `gateway`, `gateway_txn`, `occurred_at` |
| `payment.purchase.failed` | `purchase_id`, `user_id`, `course_id`, `gateway`, `occurred_at` |

## Application (`internal/payment/app`)

Ports:

```go
// Repository reads and writes purchases in the current transaction.
// Insert sets p.Version to 1. Update returns ErrConcurrentModification when p.Version is stale
// and increments it on success.
type Repository interface {
    Find(ctx context.Context, purchaseID id.ID) (domain.Purchase, bool, error)
    HasPaid(ctx context.Context, userID, courseID id.ID) (bool, error)
    // ListUnsettled returns purchases that are pending and created before pendingBefore,
    // or paid with a zero GrantedAt, oldest first.
    ListUnsettled(ctx context.Context, pendingBefore time.Time, limit int) ([]domain.Purchase, error)
    Insert(ctx context.Context, p *domain.Purchase) error
    Update(ctx context.Context, p *domain.Purchase) error
}

type EventPublisher interface {
    Publish(ctx context.Context, events ...domain.Event) error
}

type Repos struct {
    Purchases Repository
    Events    EventPublisher
}

type TxRunner interface {
    RunInTx(ctx context.Context, fn func(Repos) error) error
}

// CourseCatalog returns ErrNotFound when the course does not exist.
type CourseCatalog interface {
    CourseFacts(ctx context.Context, courseID id.ID) (CourseFacts, error)
}

type CourseFacts struct {
    Published bool
    Price     domain.Money // zero AmountMinor means free
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
    Method string            // "POST"
    URL    string
    Fields map[string]string // form fields, empty for a plain redirect
}

type ResultKind int

const (
    ResultPending ResultKind = iota // not settled yet, or the gateway is unsure
    ResultComplete
    ResultFailed
)

type Result struct {
    Kind ResultKind
    Txn  string // set when Kind is ResultComplete
}
```

`Service` takes the `TxRunner`, `CourseCatalog`, `EnrollmentGranter`, a
`map[string]Gateway` keyed by gateway name, the ID generator and the clock.

### Use cases

**`Checkout(ctx, p, courseID, gateway) (domain.Purchase, Checkout, error)`**

1. Unknown or unconfigured gateway: `ErrGatewayUnavailable`.
2. Read course facts. Missing or unpublished: `ErrNotFound`. Free: `ErrCourseFree`.
3. `IsEnrolled` true: `ErrAlreadyEnrolled`.
4. `HasPaid` true: `ErrAlreadyPurchased`.
5. Create the purchase with the price snapshot and insert it with its event in one transaction.
6. Call `gateway.StartCheckout`. On error, return `ErrGatewayUnavailable`; the pending purchase is
   later failed by the reconciler.

Several pending purchases for the same user and course may exist (for example, two tabs). Each is
settled independently. When a second purchase for the same user and course becomes paid, the
service logs a warning naming both purchases so the duplicate can be refunded by hand.

**`Confirm(ctx, p, purchaseID) (domain.Purchase, error)`**

Loads the purchase. If it does not exist or `CanView` is false: `ErrNotFound`. Then runs `settle`.

**`settle(ctx, purchase)`** (shared by `Confirm` and the reconciler)

1. If the purchase is not paid, call `FetchStatus` outside any transaction:
   - `ResultComplete`: in one transaction, reload, `MarkPaid`, update, publish.
   - `ResultFailed`: in one transaction, reload, `MarkFailed`, update, publish.
   - `ResultPending`: no change.
   - Error: return `ErrGatewayUnavailable` (the reconciler logs and moves on).
2. If the purchase is now paid and `GrantedAt` is zero, call `GrantPurchased`. On success, in one
   transaction, reload, `MarkGranted`, update. On failure, log and return the paid purchase; the
   reconciler retries the grant.

A stale version on update is retried once with a fresh read, as enrollment does for its unique
constraint race.

**`Get(ctx, p, purchaseID)`** returns the purchase when `CanView` is true, otherwise `ErrNotFound`.

**`Reconcile(ctx)`** lists up to 50 unsettled purchases, with pending purchases older than 15
minutes, and runs `settle` on each. A purchase still pending 24 hours after creation is logged at
warn level on each pass. It is safe to run on several replicas at once: updates use optimistic
versions and granting is idempotent.

Errors: `ErrNotFound`, `ErrCourseFree`, `ErrAlreadyEnrolled`, `ErrAlreadyPurchased`,
`ErrGatewayUnavailable`, `ErrConcurrentModification`, `ErrInvalidInput`.

## eSewa adapter (`internal/payment/adapters/esewa`)

Configured with product code, secret key, form URL, status URL, return base URL and an
`*http.Client` with a 10 second timeout.

`StartCheckout` returns `Method: "POST"`, the form URL, and these fields:

| Field | Value |
|---|---|
| `amount` | price as rupees |
| `tax_amount` | `0` |
| `product_service_charge` | `0` |
| `product_delivery_charge` | `0` |
| `total_amount` | price as rupees |
| `transaction_uuid` | `GatewayRef` |
| `product_code` | configured product code |
| `success_url`, `failure_url` | `{return base URL}/payments/{purchaseID}/return` |
| `signed_field_names` | `total_amount,transaction_uuid,product_code` |
| `signature` | base64 HMAC-SHA256 of `total_amount=…,transaction_uuid=…,product_code=…` |

Rupees are formatted from paisa without floating point: `10000` becomes `100`, `12550` becomes
`125.50`, `12505` becomes `125.05`. The same string is used in the signature, the form and the
status query. A currency other than `NPR` is an error.

`FetchStatus` calls the status API with the product code, the formatted total and the
`GatewayRef`. A non-200 response, a malformed body, or a `transaction_uuid` or `total_amount` that
does not match the purchase is an error and changes nothing. Status mapping:

| eSewa status | Result |
|---|---|
| `COMPLETE` | `ResultComplete`, `Txn` = `ref_id` |
| `PENDING`, `AMBIGUOUS` | `ResultPending` |
| `NOT_FOUND`, `CANCELED` | `ResultFailed` |
| `FULL_REFUND`, `PARTIAL_REFUND` | warn log, `ResultPending` |
| anything else | error |

## Enrollment change

Add `Service.EnrollPurchased(ctx, courseID, userID) error` to `internal/enrollment/app`. It takes
no principal, returns `ErrCourseHidden` when the course is not published, and otherwise reuses the
existing `enroll` helper (including its duplicate retry). An already active enrollment is success.

`cmd/api` wires `EnrollmentGranter` with `IsEnrolled` from enrollment's `AccessQuery` and
`GrantPurchased` from `EnrollPurchased`.

## Persistence (`migrations/00009_payment.sql`)

```sql
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
CREATE INDEX purchases_unsettled ON payment.purchases (created_at)
  WHERE status = 'pending' OR (status = 'paid' AND granted_at IS NULL);
```

`failed` to `paid` keeps `purchases_paid_txn` true because `MarkPaid` sets `gateway_txn`.
Queries are generated with sqlc, like the other contexts. The adapter's `TxRunner` writes outbox
messages with `outbox.Publish` in the same transaction.

## HTTP (`internal/payment/adapters/httpapi`)

All routes require authentication.

| Route | Success | Notes |
|---|---|---|
| `POST /v1/courses/{courseID}/purchases` body `{"gateway":"esewa"}` | 201 `{purchase, checkout}` | `checkout` is `{method, url, fields}` |
| `POST /v1/purchases/{purchaseID}/confirm` | 200 `{purchase}` | Called by the frontend return page after success or failure |
| `GET /v1/purchases/{purchaseID}` | 200 `{purchase}` | |

Purchase wire shape: `{id, course_id, user_id, amount_minor, currency, gateway, status,
created_at, settled_at, granted}`. IDs are strings, as elsewhere. `settled_at` is null while
pending.

Error mapping, using the existing problem helper:

| Error | Status | Code |
|---|---|---|
| `ErrInvalidInput` | 400 | `invalid_input` |
| `ErrNotFound` | 404 | `not_found` |
| `ErrCourseFree` | 409 | `course_free` |
| `ErrAlreadyEnrolled` | 409 | `already_enrolled` |
| `ErrAlreadyPurchased` | 409 | `already_purchased` |
| `ErrGatewayUnavailable` | 503 | `payment_unavailable` |

`api/openapi.yaml` documents the routes, the `Purchase` and `Checkout` schemas, and the codes.

## Configuration and wiring

New settings in `internal/platform/config`:

| Variable | Sandbox value in `.env.example` |
|---|---|
| `ESEWA_PRODUCT_CODE` | `EPAYTEST` |
| `ESEWA_SECRET_KEY` | eSewa's published sandbox key |
| `ESEWA_FORM_URL` | `https://rc-epay.esewa.com.np/api/epay/main/v2/form` |
| `ESEWA_STATUS_URL` | `https://rc.esewa.com.np/api/epay/transaction/status/` |
| `PAYMENT_RETURN_URL` | the frontend origin |

When any of these is unset, the eSewa gateway is not registered, `cmd/api` logs a warning, and
checkout returns 503 `payment_unavailable`. Confirm and get still work for existing purchases
whose gateway is registered; otherwise confirm returns 503.

`.gitleaks.toml` gets one allowlist entry for the published sandbox key only.

`cmd/api` builds the payment service, mounts its routes, and runs the reconciler in a goroutine
alongside the outbox forwarder, every 5 minutes, stopping on shutdown.

`.golangci.yml` gets `payment-domain` and `payment-app` depguard rules, and `payment` is added to
`platform-independent-of-contexts` and to every other context's deny rule.

## Testing

- Domain: transition table (pending to paid, pending to failed, failed to paid, paid unchanged,
  failed unchanged by `MarkFailed`), `MarkGranted` only on paid, free price rejected, events and
  payloads, `CanView`.
- App with fakes: checkout rules (hidden, free, already enrolled, already purchased, unknown
  gateway, gateway error), settle for each result kind, grant failure leaves `GrantedAt` zero and a
  later settle grants, duplicate paid purchase logs a warning, stale-version retry, reconcile
  selection and the 24 hour warning.
- eSewa adapter with `httptest`: signature equals eSewa's documented sample, rupee formatting,
  form fields, status mapping, amount or reference mismatch, non-200, malformed body, timeout.
- Postgres integration: round trip, unique `(gateway, gateway_ref)`, check constraints, optimistic
  concurrency, `HasPaid`, `ListUnsettled`.
- HTTP: status codes, error codes and JSON shapes.
- End to end through `cmd/api` with a fake eSewa status server: checkout, confirm, then the student
  reads a non-preview lecture; the reconciler confirms a purchase without a confirm call.
- No automated test calls the real eSewa sandbox. The README describes a manual sandbox check.

## Risks

- Double payment: two tabs can each complete a purchase. Both become paid; a warning is logged and
  the duplicate is refunded by hand.
- A failed grant delays access until the next reconciler pass (up to about 5 minutes) or the next
  confirm call.
- An eSewa status API outage leaves purchases pending; confirm returns 503 and the reconciler keeps
  retrying.
- A refund made in the eSewa portal does not revoke access until a manager cancels the
  enrollment.
- The published sandbox key appears in `.env.example`; the gitleaks allowlist matches that value
  only, and the production key comes from the environment.
