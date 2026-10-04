# Notification Service Integration Design

Date: 2026-10-04

## Status

Approved in conversation on 2026-10-04. Pending written-spec review.

## Context

The standalone notification service (`../notification`, module `github.com/santoshkc2200/notification-service`) durably enqueues caller-rendered email over HTTP (`POST /v1/notifications`), delivers it at least once through SMTP, and deduplicates requests by `Idempotency-Key`. It owns the PostgreSQL `notification` schema and runs its own goose migrations (`notification-service migrate`), tracked in `notification.goose_db_version`.

`ioe-backend` publishes domain events through a Watermill SQL outbox (`platform.outbox_messages`). A forwarder moves committed messages to an in-process `gochannel`. Nothing consumes them yet. The forwarder acknowledges a SQL message as soon as the `gochannel` accepts it, so a failing or absent subscriber loses the message. That is acceptable only while no consumer exists.

This spec adds the first consumer: a welcome email sent when a user registers.

## Goals

- Send one welcome email per `identity.user_registered` event through the notification service.
- Never lose an event between the outbox and a successful `202` from the notification service, including across process crashes.
- Never send a duplicate email because of a redelivered event.
- Run the notification service against the same PostgreSQL database as `ioe-backend`, in its own schema and under its own role.
- Keep bounded-context isolation: no context imports another.

## Non-goals

- Browser push notifications.
- Any email other than the welcome email.
- Localization; English only.
- Reading delivery status from the notification service.
- A dead-letter table. Permanent failures are logged and counted.
- Per-consumer outbox subscriptions. Revisit when a second consumer exists (see Risks).

## Decisions

| Topic | Decision |
|---|---|
| Consumer location | New bounded context `internal/notification` in `ioe-backend`; it owns no PostgreSQL schema |
| Delivery guarantee | Keep the forwarder; replace its `gochannel` output with a synchronous in-process dispatcher; `Retry` middleware on the forwarder |
| Event payload | `identity.UserRegistered` gains `Name` (additive JSON field `name`) |
| Rendering | `ioe-backend` renders subject, text, and HTML from embedded Go templates |
| Idempotency key | `ioe:identity.user_registered:<outbox message UUID>:welcome-v1` |
| Database | Same database (`ioe`), schema `notification`, dedicated role `notification` |
| Configuration | `NOTIFICATION_SERVICE_BASE_URL` and `NOTIFICATION_SERVICE_SEND_API_KEY`: both or neither |
| Tracing | Outbound client wrapped with `otelhttp`; the service currently ignores `traceparent` |

## Architecture

### Repository layout additions

```text
internal/notification/
  app/                         SendWelcome use case; Mailer port; ErrPermanent
  adapters/
    events/                    Watermill handler for identity.user_registered
    notifysvc/                 HTTP client for the notification service
    templates/                 embedded welcome.txt.tmpl, welcome.html.tmpl
```

There is no `domain` package: the context holds no state and enforces no invariants beyond rendering. `notification` here names the `ioe-backend` context; the `notification` PostgreSQL schema belongs to the external service, and `ioe-backend` code never reads or writes it.

### Dependency rules

- `internal/notification/app` imports only the standard library.
- `internal/notification/adapters/events` decodes the event JSON into its own struct. It does not import `internal/identity`.
- `.golangci.yml` gains `notification-app` depguard rules, adds `internal/notification` to `platform-independent-of-contexts`, adds a deny rule so `identity` cannot import `notification`, and a deny rule so `notification` cannot import `identity`.
- `cmd/api` is the only package that wires the context.

### Outbox delivery (`internal/platform/outbox`)

The `gochannel` is removed. Even with `BlockPublishUntilSubscriberAck`, a closing `gochannel` or subscriber makes `Publish` return `nil`, so the forwarder would acknowledge a message that was never handled and a graceful shutdown during retries would lose it.

- New unexported `dispatcher` type implements `message.Publisher`; only `Handler` and `Forwarder.Handle` are exported:

  ```go
  // Handler processes one forwarded message. Returning an error makes the
  // forwarder retry the message; returning nil acknowledges it.
  type Handler func(*message.Message) error

  func (f *Forwarder) Handle(topic string, h Handler)
  func (d *dispatcher) Publish(topic string, msgs ...*message.Message) error
  func (d *dispatcher) Close() error
  ```

  `Publish` calls every handler registered for the topic, in registration order, and returns the first error. A topic with no handlers returns `nil`, so the message is acknowledged exactly as today. `Handle` must be called before `Forwarder.Run`; the handler map is read-only afterwards.
- `NewForwarder` passes the dispatcher as the forwarder's publisher and sets `forwarder.Config.Middlewares` to, in order:
  - `middleware.Retry{InitialInterval: 1s, MaxInterval: 1m, Multiplier: 2, MaxRetries: 5}` (outer)
  - `middleware.Recoverer` (inner, so a handler panic becomes an error that `Retry` retries)
- The SQL subscriber is configured with `ResendInterval: 5s`. When a retry cycle is exhausted, the forwarder nacks, the SQL subscriber resends after 5s, and a new cycle begins. A transient failure delays delivery but never drops it.
- The forwarder acknowledges the SQL message only after `Publish` returns `nil`. On shutdown or crash the SQL subscriber stops without acknowledging, so the message is redelivered on the next start. The `Retry` middleware sees the SQL message's context and stops retrying when the subscriber closes. The message a handler receives is rebuilt by the forwarder with a background context, so handlers must bound their own work; the notification client's 10s timeout does this.
- `Forwarder.Subscriber()` is replaced by `Forwarder.Handle(topic string, h Handler)`, which delegates to the dispatcher.

### Identity change

`internal/identity/domain.UserRegistered` gains `Name string` with JSON tag `name`, set from the verified Google profile when the user is created. Consumers treat a missing or empty name as absent.

## Notification Context

### Application (`internal/notification/app`)

```go
type Email struct {
    To      string
    Subject string
    Text    string
    HTML    string
}

// Mailer enqueues one rendered email. It returns an error wrapping ErrPermanent
// when retrying the same request cannot succeed.
type Mailer interface {
    Enqueue(ctx context.Context, email Email, idempotencyKey string) error
}

type Renderer interface {
    Welcome(name string) (subject, text, html string, err error)
}

type WelcomeInput struct {
    EventID string // outbox message UUID
    Email   string
    Name    string
}

func (s *Service) SendWelcome(ctx context.Context, in WelcomeInput) error
```

`SendWelcome` validates that `EventID` and `Email` are non-empty (otherwise `ErrPermanent`), renders the welcome email, and calls `Mailer.Enqueue` with key `ioe:identity.user_registered:<EventID>:welcome-v1`. The key fits the service's limit of 128 printable ASCII bytes. Changing the email's content requires a new key suffix (`welcome-v2`), because the service rejects a reused key with different content.

### Templates (`internal/notification/adapters/templates`)

- `welcome.txt.tmpl` uses `text/template`; `welcome.html.tmpl` uses `html/template` so the name is escaped.
- Subject: `Welcome to IOE`.
- Greeting: `Hello, <name>,` when a name is present, `Hello,` otherwise.
- Templates are embedded with `embed` and parsed once at construction; a parse failure stops startup.

### Notification service client (`internal/notification/adapters/notifysvc`)

- `POST {base_url}/v1/notifications` with `Authorization: Bearer <send key>`, `Idempotency-Key`, and JSON `{recipient, subject, text_body, html_body}`. `tenant_id` is omitted (default scope).
- `http.Client` with a 10s timeout and `otelhttp.NewTransport`.
- Response mapping:

| Response | Result |
|---|---|
| `202` (including `idempotent_replay: true`) | success |
| network error, timeout, `429`, `5xx` | retryable error |
| `409` | `ErrPermanent` (key reused with different content) |
| any other `4xx` | `ErrPermanent` |

- Logs and errors never include the API key, recipient, or rendered bodies. Problem responses are reduced to status, `type`, and the response `X-Request-ID`.

### Event handler (`internal/notification/adapters/events`)

- Exposes `Welcome(msg *message.Message) error`, registered with `Forwarder.Handle("identity.user_registered", ...)`.
- Decodes `{user_id, email, name}` into its own struct and calls `SendWelcome` with `EventID = msg.UUID`. The forwarder preserves the original message UUID, so it is stable across redeliveries.
- Decision:

| Outcome | Action |
|---|---|
| success | return `nil` (acknowledged) |
| invalid JSON, or error wrapping `ErrPermanent` | log at error level (event ID and cause only), increment OpenTelemetry counter `notification.events.dropped` with attribute `reason` (`invalid_payload` or `permanent`), return `nil` |
| any other error | return it, so the message is retried |

Acknowledging permanent failures prevents a single poison message from blocking the outbox indefinitely.

## Composition (`cmd/api`)

- When notification configuration is present, `buildApp` constructs the template renderer, the `notifysvc` client, the `notification/app` service, and the event handler, and registers it with `forwarder.Handle` before returning.
- When it is absent, no handler is registered and startup logs a warning that notifications are disabled. Events are then acknowledged without processing, exactly as today.
- Startup and shutdown are unchanged: the forwarder runs in the errgroup and is closed on return.

## Configuration

| Variable | Required | Purpose |
|---|---|---|
| `NOTIFICATION_SERVICE_BASE_URL` | with the key | absolute `http` or `https` URL of the notification service |
| `NOTIFICATION_SERVICE_SEND_API_KEY` | with the URL | send bearer key (unpadded base64url, at least 32 decoded bytes) |

Setting exactly one of the two stops the process with a message naming the missing variable. The key is never logged.

## Database

The notification service connects to the `ioe` database with its own role and owns only the `notification` schema. `ioe-backend` migrations never create, alter, or read that schema, and no `ioe-backend` context may be named so that its schema would be `notification`.

Required privileges for the `notification` role: `CONNECT` and `CREATE` on database `ioe` (so `notification-service migrate` can create its schema), after which it owns the schema and its objects. The `ioe` role receives no privileges on the `notification` schema.

Production: the database administrator creates the role and grants these privileges before the first `notification-service migrate`. This is documented in `README.md`, not automated.

PostgreSQL version: `ioe-backend` uses PostgreSQL 17; the notification service's own compose uses 18. Its migrations use no 18-only features found in review; compatibility is confirmed by the manual compose check below.

## Local Development

`docker-compose.yml` gains, under the `app` profile:

- `notification-db-setup`: one-shot `postgres` image running `psql` as the `ioe` superuser. It creates role `notification` with a local-only password if missing and grants `CONNECT, CREATE ON DATABASE ioe`. It is idempotent so existing `pgdata` volumes work without a reset.
- `notification-migrate`: built from `../notification`, `command: ["migrate"]`, depends on `notification-db-setup`.
- `notification`: built from `../notification`, `command: ["serve"]`, depends on `notification-migrate`, configured with local-only send and read keys and Mailpit SMTP (`SMTP_TLS_MODE=insecure`, `ALLOW_INSECURE_SMTP=true`).
- `mailpit`: SMTP for the notification service, UI on `127.0.0.1:8025`.
- `api` gains `NOTIFICATION_SERVICE_BASE_URL=http://notification:8080` and the local send key, and depends on `notification` being healthy.

`.env.example` documents both variables. Local keys are marked local-only and allowlisted in `.gitleaks.toml` if gitleaks flags them.

## Testing

Unit tests:

- Templates: with and without a name; HTML output escapes a name containing markup; text output does not.
- `SendWelcome`: key format, rejection of empty event ID or email as `ErrPermanent`, propagation of mailer errors.
- `notifysvc` with `httptest.Server`: request method, path, headers, and body; mapping of `202`, `409`, `400`, `429`, `500`, and a closed connection.
- Event handler: ack on success, ack on invalid JSON and `ErrPermanent`, error returned on retryable failure.
- Configuration: both set, neither set, and each single-variable case.

Integration tests (`make test-integration`, real PostgreSQL):

- Outbox: with a handler that fails twice then succeeds, the handler sees the message three times and succeeds once, and the forwarder's offset advances only after the success.
- Outbox: when the forwarder is closed while its handler is still failing, a new forwarder redelivers the same message UUID.
- Outbox: a message for a topic with no handler is acknowledged and not redelivered.
- End to end: Google sign-in for a new user results in exactly one `POST /v1/notifications` to an `httptest` fake of the notification service, with the expected idempotency key and recipient; a second sign-in by the same user sends nothing.

Not covered by `make check` or `make test-integration`: the real notification service, Mailpit delivery, and notification migrations on PostgreSQL 17. These are verified manually with `docker compose --profile app up --build` and a sign-in that produces a message in Mailpit.

## Risks

- Head-of-line blocking: while the notification service is unavailable, the forwarder blocks on the welcome event and no later outbox message is forwarded to any consumer. Acceptable with a single consumer. When a second consumer is added, move to per-consumer durable subscriptions.
- Open transaction during retries: the Watermill SQL subscriber holds a transaction (and the offset row lock) while a message is being handled, including across retries. During a long notification-service outage this is a long-running transaction, which holds back vacuum. If PostgreSQL ends it (for example through `idle_in_transaction_session_timeout`), the message is not acknowledged and is redelivered, so correctness holds.
- Idempotency window: the service reserves a key only while its row is retained (`RETENTION_PERIOD`, default 30 days). An outbox message redelivered after that window would send again. Outbox messages are forwarded within seconds to minutes, so this requires an outage longer than the retention period.
- Compose couples the two repositories by relative path (`../notification`). Production deploys the service independently.
