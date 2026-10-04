# Notification Service Integration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Send one welcome email per `identity.user_registered` outbox event through the standalone notification service, with no lost and no duplicate emails.

**Architecture:** The outbox forwarder stops writing to an in-memory `gochannel` and instead calls registered handlers synchronously, so a SQL outbox message is acknowledged only after its handlers succeed (Watermill `Retry` middleware plus SQL resend on failure). A new `internal/notification` bounded context renders the welcome email and enqueues it over HTTP with an idempotency key derived from the outbox message UUID. The notification service runs as a separate process against the same `ioe` database, in its own `notification` schema and role.

**Tech Stack:** Go 1.27.1, Watermill v1.5.3 and watermill-sql v4.1.5, pgx v5, OpenTelemetry (otel v1.47.0, otelhttp v0.71.0), `text/template` and `html/template`, Docker Compose, testcontainers PostgreSQL 17.

**Spec:** `docs/superpowers/specs/2026-10-04-notification-integration-design.md`

## Before you start

The main checkout (`fix/code-review-findings`) has unrelated uncommitted changes in `internal/identity`, `internal/platform/httpserver`, and `api/openapi.yaml`. Do not work in it and never stage those files. Create an isolated worktree from the current `HEAD` with superpowers:using-git-worktrees and run every step there. Line numbers in this plan are not given for files with uncommitted local edits; locate code by the quoted snippet instead.

Integration tests need a running Docker daemon. Never report them as passing unless they ran.

## Global Constraints

- Go `1.27.1`; module `github.com/santoshkc2200/ioe-backend`; no new modules except promoting `go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp` (already `v0.71.0` indirect) to a direct dependency.
- No bounded context imports another. `internal/notification` never imports `internal/identity` and vice versa; only `cmd/api` wires both.
- `internal/notification/app` imports only the Go standard library.
- `internal/platform` never imports a bounded context.
- Event topic: `identity.user_registered`. Payload JSON fields: `user_id`, `email`, `occurred_at`, and new `name`.
- Idempotency key: `ioe:identity.user_registered:<outbox message UUID>:welcome-v1`.
- Retry middleware: `MaxRetries: 5, InitialInterval: 1s, MaxInterval: 1m, Multiplier: 2`; SQL subscriber `ResendInterval: 5s`.
- Notification client: `POST {base}/v1/notifications`, `Authorization: Bearer <key>`, `Idempotency-Key`, JSON `{recipient, subject, text_body, html_body}`, no `tenant_id`, 10s timeout, `otelhttp` transport.
- Status mapping: `202` success; network error, timeout, `429`, `5xx` retryable; `409` and every other `4xx` wrap `app.ErrPermanent`; any other status retryable.
- Subject: `Welcome to IOE`. Greeting `Hello, <name>,` or `Hello,` when the trimmed name is empty.
- Dropped events: OpenTelemetry counter `notification.events.dropped`, attribute `reason` = `invalid_payload` or `permanent`.
- Config: `NOTIFICATION_SERVICE_BASE_URL` and `NOTIFICATION_SERVICE_SEND_API_KEY`, both or neither. URL absolute `http`/`https`. Key unpadded base64url decoding to at least 32 bytes.
- Logs and errors never contain the API key, recipient email, rendered bodies, or event payload.
- Shared database `ioe`; role `notification`; the `ioe` role never gets privileges on schema `notification`; `ioe-backend` migrations never touch it.
- Conventional Commits. Required gates (`AGENTS.md`): `make check`, `make test-integration`, `docker compose config`, `make docker-build`, `git diff --check`.

## Review Focus

1. A reverse proxy in front of the notification service returns `502` with an HTML body: the email must be retried, not dropped, and the code must not fail on the non-JSON body. Pinned in Task 5.
2. `NOTIFICATION_SERVICE_BASE_URL` set with a trailing slash or a path prefix (`http://host/notify/`): the request must go to `/notify/v1/notifications`, not `//v1/...`. Pinned in Task 5.
3. Outbox events written before this change have no `name` field, and Google may return a whitespace-only name: the email must still go out with `Hello,`. Pinned in Tasks 4 and 6.
4. A handler panics (for example a nil dereference in a future handler): the forwarder must recover, retry, and eventually deliver instead of crashing the process. Pinned in Task 1.
5. An operator pastes a padded (`=`) or standard-alphabet (`+`, `/`) base64 key: startup must fail with a message naming the variable and never echo the key. Pinned in Task 7.

---

### Task 1: Synchronous outbox dispatcher with retry

**Files:**
- Modify: `internal/platform/outbox/outbox.go`
- Create: `internal/platform/outbox/dispatcher_test.go`
- Modify: `internal/platform/outbox/outbox_integration_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces:
  - `type outbox.Handler func(*message.Message) error`
  - `func (f *outbox.Forwarder) Handle(topic string, h outbox.Handler)` (call before `Run`)
  - `Forwarder.Subscriber()` is removed. `NewForwarder`, `Run`, `Close`, `Publish` keep their signatures.

- [ ] **Step 1: Write the failing dispatcher unit tests**

Create `internal/platform/outbox/dispatcher_test.go`:

```go
package outbox

import (
	"errors"
	"testing"

	"github.com/ThreeDotsLabs/watermill/message"
)

func TestDispatcherCallsHandlersInOrderAndStopsAtFirstError(t *testing.T) {
	var calls []string
	errFail := errors.New("fail")
	d := &dispatcher{handlers: map[string][]Handler{}}
	d.handlers["t"] = []Handler{
		func(m *message.Message) error { calls = append(calls, "a:"+m.UUID); return nil },
		func(m *message.Message) error { calls = append(calls, "b:"+m.UUID); return errFail },
		func(m *message.Message) error { calls = append(calls, "c:"+m.UUID); return nil },
	}
	err := d.Publish("t", message.NewMessage("m1", nil))
	if !errors.Is(err, errFail) {
		t.Fatalf("err = %v, want %v", err, errFail)
	}
	if len(calls) != 2 || calls[0] != "a:m1" || calls[1] != "b:m1" {
		t.Fatalf("calls = %v", calls)
	}
}

func TestDispatcherWithoutHandlersAcknowledges(t *testing.T) {
	d := &dispatcher{handlers: map[string][]Handler{}}
	if err := d.Publish("unknown", message.NewMessage("m1", nil)); err != nil {
		t.Fatalf("err = %v", err)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/platform/outbox/ -run Dispatcher -v`
Expected: FAIL to compile with `undefined: dispatcher` and `undefined: Handler`.

- [ ] **Step 3: Replace the gochannel with the dispatcher**

Replace `internal/platform/outbox/outbox.go` with:

```go
// Package outbox implements the transactional outbox: messages are written in the caller's
// PostgreSQL transaction and delivered to in-process handlers after commit.
package outbox

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/ThreeDotsLabs/watermill"
	wsql "github.com/ThreeDotsLabs/watermill-sql/v4/pkg/sql"
	"github.com/ThreeDotsLabs/watermill/components/forwarder"
	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/ThreeDotsLabs/watermill/message/router/middleware"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

const (
	forwarderTopic = "outbox"
	consumerGroup  = "forwarder"
	// resendInterval is the pause before a nacked message starts a new retry cycle.
	resendInterval = 5 * time.Second
)

func schemaAdapter() wsql.DefaultPostgreSQLSchema {
	return wsql.DefaultPostgreSQLSchema{
		GenerateMessagesTableName: func(string) string { return "platform.outbox_messages" },
	}
}

func offsetsAdapter() wsql.DefaultPostgreSQLOffsetsAdapter {
	return wsql.DefaultPostgreSQLOffsetsAdapter{
		GenerateMessagesOffsetsTableName: func(string) string { return "platform.outbox_offsets" },
	}
}

// Publish stores msgs for topic inside tx. They are delivered only if tx commits.
// The trace context of ctx is stored in each message's metadata.
func Publish(ctx context.Context, tx pgx.Tx, topic string, msgs ...*message.Message) error {
	pub, err := wsql.NewPublisher(wsql.TxFromPgx(tx), wsql.PublisherConfig{SchemaAdapter: schemaAdapter()}, watermill.NopLogger{})
	if err != nil {
		return err
	}
	prop := otel.GetTextMapPropagator()
	for _, m := range msgs {
		m.SetContext(ctx)
		prop.Inject(ctx, propagation.MapCarrier(m.Metadata))
	}
	return forwarder.NewPublisher(pub, forwarder.PublisherConfig{ForwarderTopic: forwarderTopic}).Publish(topic, msgs...)
}

// Handler processes one forwarded message. Returning an error makes the forwarder retry the
// message with backoff; returning nil acknowledges it. Delivery is at least once, and a
// failure in one handler of a topic re-runs every handler of that topic, so handlers must be
// idempotent. The message carries a background context: handlers must bound their own work.
type Handler func(*message.Message) error

// dispatcher is the forwarder's output. It calls handlers synchronously, so the forwarder
// acknowledges a SQL message only after every handler for its topic has succeeded.
type dispatcher struct{ handlers map[string][]Handler }

func (d *dispatcher) Publish(topic string, msgs ...*message.Message) error {
	for _, m := range msgs {
		for _, h := range d.handlers[topic] {
			if err := h(m); err != nil {
				return err
			}
		}
	}
	return nil
}

func (*dispatcher) Close() error { return nil }

// Forwarder moves committed outbox messages to the registered handlers.
type Forwarder struct {
	fw *forwarder.Forwarder
	d  *dispatcher
}

func NewForwarder(pool *pgxpool.Pool, logger *slog.Logger) (*Forwarder, error) {
	wl := watermill.NewSlogLogger(logger)
	sub, err := wsql.NewSubscriber(wsql.BeginnerFromPgx(pool), wsql.SubscriberConfig{
		ConsumerGroup:  consumerGroup,
		SchemaAdapter:  schemaAdapter(),
		OffsetsAdapter: offsetsAdapter(),
		ResendInterval: resendInterval,
	}, wl)
	if err != nil {
		return nil, err
	}
	d := &dispatcher{handlers: map[string][]Handler{}}
	fw, err := forwarder.NewForwarder(sub, d, wl, forwarder.Config{
		ForwarderTopic: forwarderTopic,
		// Retry wraps Recoverer so that a handler panic becomes an error that is retried.
		Middlewares: []message.HandlerMiddleware{
			middleware.Retry{
				MaxRetries:      5,
				InitialInterval: time.Second,
				MaxInterval:     time.Minute,
				Multiplier:      2,
				Logger:          wl,
			}.Middleware,
			middleware.Recoverer,
		},
	})
	if err != nil {
		return nil, errors.Join(err, sub.Close())
	}
	return &Forwarder{fw: fw, d: d}, nil
}

// Handle registers h for messages published to topic. Call it before Run.
func (f *Forwarder) Handle(topic string, h Handler) {
	f.d.handlers[topic] = append(f.d.handlers[topic], h)
}

// Run forwards messages until ctx is cancelled or Close is called.
func (f *Forwarder) Run(ctx context.Context) error { return f.fw.Run(ctx) }

func (f *Forwarder) Close() error { return f.fw.Close() }
```

- [ ] **Step 4: Run the unit tests to verify they pass**

Run: `go test ./internal/platform/outbox/ -run Dispatcher -v`
Expected: PASS (2 tests).

- [ ] **Step 5: Rewrite the integration tests around `Handle`**

Replace `internal/platform/outbox/outbox_integration_test.go` with:

```go
//go:build integration

package outbox_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/santoshkc2200/ioe-backend/internal/platform/outbox"
	"github.com/santoshkc2200/ioe-backend/internal/platform/postgres/pgtest"
)

func publish(ctx context.Context, t *testing.T, pool *pgxpool.Pool, topic, id, payload string) {
	t.Helper()
	if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		return outbox.Publish(ctx, tx, topic, message.NewMessage(id, []byte(payload)))
	}); err != nil {
		t.Fatal(err)
	}
}

// startForwarder runs a forwarder with handlers until the returned stop function is called
// (or the test ends). stop returns only after Run has returned and the forwarder is closed.
func startForwarder(ctx context.Context, t *testing.T, pool *pgxpool.Pool, handlers map[string]outbox.Handler) (stop func()) {
	t.Helper()
	fw, err := outbox.NewForwarder(pool, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	for topic, h := range handlers {
		fw.Handle(topic, h)
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = fw.Run(runCtx)
	}()
	var once sync.Once
	stop = func() {
		once.Do(func() {
			cancel()
			<-done
			_ = fw.Close()
		})
	}
	t.Cleanup(stop)
	return stop
}

func receive(ctx context.Context, t *testing.T, ch <-chan string) string {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-ctx.Done():
		t.Fatal("timed out waiting for a forwarded message")
		return ""
	}
}

func TestRolledBackPublishWritesNothing(t *testing.T) {
	ctx := context.Background()
	pool := pgtest.New(t)
	errAbort := errors.New("abort")
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if err := outbox.Publish(ctx, tx, "test.topic", message.NewMessage("m1", []byte(`{}`))); err != nil {
			return err
		}
		return errAbort
	})
	if !errors.Is(err, errAbort) {
		t.Fatalf("err = %v", err)
	}
	var n int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM platform.outbox_messages").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("outbox rows = %d after rollback", n)
	}
}

func TestCommittedPublishIsForwardedWithTraceContext(t *testing.T) {
	otel.SetTextMapPropagator(propagation.TraceContext{})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := pgtest.New(t)

	type forwarded struct{ payload, traceparent string }
	got := make(chan forwarded, 1)
	startForwarder(ctx, t, pool, map[string]outbox.Handler{"test.topic": func(m *message.Message) error {
		got <- forwarded{payload: string(m.Payload), traceparent: m.Metadata.Get("traceparent")}
		return nil
	}})

	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    trace.TraceID{0xaa, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15},
		SpanID:     trace.SpanID{1, 2, 3, 4, 5, 6, 7, 8},
		TraceFlags: trace.FlagsSampled,
	})
	publish(trace.ContextWithSpanContext(ctx, sc), t, pool, "test.topic", "m2", `{"ok":true}`)

	select {
	case m := <-got:
		if m.payload != `{"ok":true}` {
			t.Fatalf("payload = %s", m.payload)
		}
		if !strings.Contains(m.traceparent, sc.TraceID().String()) {
			t.Fatalf("traceparent = %q", m.traceparent)
		}
	case <-ctx.Done():
		t.Fatal("message not forwarded")
	}
}

func TestFailingAndPanickingHandlerIsRetriedUntilItSucceeds(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool := pgtest.New(t)

	var calls atomic.Int32
	done := make(chan string, 1)
	startForwarder(ctx, t, pool, map[string]outbox.Handler{"test.topic": func(m *message.Message) error {
		switch calls.Add(1) {
		case 1:
			panic("handler bug")
		case 2:
			return errors.New("transient")
		default:
			done <- m.UUID
			return nil
		}
	}})
	publish(ctx, t, pool, "test.topic", "m1", `{}`)

	if got := receive(ctx, t, done); got != "m1" {
		t.Fatalf("delivered %q, want m1", got)
	}
	if n := calls.Load(); n != 3 {
		t.Fatalf("handler calls = %d, want 3", n)
	}
}

func TestClosedForwarderRedeliversUnacknowledgedMessage(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool := pgtest.New(t)

	attempted := make(chan string, 1)
	stop := startForwarder(ctx, t, pool, map[string]outbox.Handler{"test.topic": func(m *message.Message) error {
		select {
		case attempted <- m.UUID:
		default:
		}
		return errors.New("notification service down")
	}})
	publish(ctx, t, pool, "test.topic", "m1", `{}`)
	if got := receive(ctx, t, attempted); got != "m1" {
		t.Fatalf("attempted %q, want m1", got)
	}
	stop()

	delivered := make(chan string, 1)
	startForwarder(ctx, t, pool, map[string]outbox.Handler{"test.topic": func(m *message.Message) error {
		delivered <- m.UUID
		return nil
	}})
	if got := receive(ctx, t, delivered); got != "m1" {
		t.Fatalf("redelivered %q, want m1", got)
	}
}

func TestMessageWithoutHandlerIsAcknowledged(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool := pgtest.New(t)

	got := make(chan string, 2)
	stop := startForwarder(ctx, t, pool, map[string]outbox.Handler{"test.handled": func(m *message.Message) error {
		got <- m.UUID
		return nil
	}})
	publish(ctx, t, pool, "test.unhandled", "m1", `{}`)
	publish(ctx, t, pool, "test.handled", "m2", `{}`)
	if v := receive(ctx, t, got); v != "m2" {
		t.Fatalf("got %q, want m2", v)
	}
	stop()

	// Messages are forwarded in offset order, so a redelivered m1 would arrive before m3.
	startForwarder(ctx, t, pool, map[string]outbox.Handler{
		"test.unhandled": func(m *message.Message) error { got <- "unhandled:" + m.UUID; return nil },
		"test.handled":   func(m *message.Message) error { got <- m.UUID; return nil },
	})
	publish(ctx, t, pool, "test.handled", "m3", `{}`)
	if v := receive(ctx, t, got); v != "m3" {
		t.Fatalf("got %q, want m3 (m1 must not be redelivered)", v)
	}
}
```

- [ ] **Step 6: Run the outbox integration tests**

Run: `go test -race -tags integration ./internal/platform/outbox/ -v`
Expected: PASS for all five tests. `TestFailingAndPanickingHandlerIsRetriedUntilItSucceeds` takes about 3s (1s + 2s backoff). Docker must be running; if it is not, stop and report that the integration tests did not run.

- [ ] **Step 7: Confirm nothing else used `Subscriber()` and the package builds**

Run: `grep -rn 'Subscriber()' --include='*.go' cmd internal; go build ./... && go vet ./...`
Expected: no `grep` matches; build and vet succeed.

- [ ] **Step 8: Commit**

```bash
git add internal/platform/outbox/outbox.go internal/platform/outbox/dispatcher_test.go internal/platform/outbox/outbox_integration_test.go
git commit -m "feat(outbox): deliver to handlers synchronously with retry"
```

---

### Task 2: Carry the user's name in `UserRegistered`

**Files:**
- Modify: `internal/identity/domain/events.go`
- Modify: `internal/identity/app/service.go` (the `r.Events.Publish(ctx, domain.UserRegistered{...})` call in `signIn`)
- Test: `internal/identity/app/service_test.go` (`TestSignInCreatesStudentAndPublishesEvent`)

**Interfaces:**
- Consumes: nothing.
- Produces: event JSON `{"user_id": "...", "email": "...", "name": "...", "occurred_at": "..."}` on topic `identity.user_registered`; `domain.UserRegistered{}.EventName()` remains `"identity.user_registered"`.

- [ ] **Step 1: Extend the failing assertion**

In `TestSignInCreatesStudentAndPublishesEvent`, change the event assertion so it also requires the name (the fixture's `alice` identity has `Name: "Alice"`):

```go
	if ev, ok := st.events[0].(domain.UserRegistered); !ok || ev.UserID != s.User.ID || ev.Name != "Alice" || !ev.OccurredAt.Equal(t0) {
		t.Fatalf("event %+v", st.events[0])
	}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/identity/app/ -run TestSignInCreatesStudentAndPublishesEvent -v`
Expected: FAIL to compile with `ev.Name undefined`.

- [ ] **Step 3: Add the field and set it**

In `internal/identity/domain/events.go`, replace the `UserRegistered` struct with:

```go
type UserRegistered struct {
	UserID     uuid.UUID `json:"user_id"`
	Email      string    `json:"email"`
	Name       string    `json:"name"`
	OccurredAt time.Time `json:"occurred_at"`
}
```

In `internal/identity/app/service.go`, change the publish call in `signIn` to:

```go
		if err := r.Events.Publish(ctx, domain.UserRegistered{UserID: user.ID, Email: user.Email, Name: user.Name, OccurredAt: now}); err != nil {
```

- [ ] **Step 4: Run the identity tests**

Run: `go test -race ./internal/identity/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/identity/domain/events.go internal/identity/app/service.go internal/identity/app/service_test.go
git commit -m "feat(identity): include name in UserRegistered event"
```

---

### Task 3: Notification application service

**Files:**
- Create: `internal/notification/app/service.go`
- Test: `internal/notification/app/service_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces (package `github.com/santoshkc2200/ioe-backend/internal/notification/app`):
  - `var ErrPermanent error`
  - `type Email struct{ To, Subject, Text, HTML string }`
  - `type Mailer interface { Enqueue(ctx context.Context, email Email, idempotencyKey string) error }`
  - `type Renderer interface { Welcome(name string) (subject, text, html string, err error) }`
  - `type WelcomeInput struct{ EventID, Email, Name string }`
  - `func NewService(mailer Mailer, renderer Renderer) *Service`
  - `func (s *Service) SendWelcome(ctx context.Context, in WelcomeInput) error`
  - `func WelcomeKey(eventID string) string`

- [ ] **Step 1: Write the failing tests**

Create `internal/notification/app/service_test.go`:

```go
package app_test

import (
	"context"
	"errors"
	"testing"

	"github.com/santoshkc2200/ioe-backend/internal/notification/app"
)

type fakeMailer struct {
	calls int
	email app.Email
	key   string
	err   error
}

func (f *fakeMailer) Enqueue(_ context.Context, email app.Email, key string) error {
	f.calls++
	f.email, f.key = email, key
	return f.err
}

type fakeRenderer struct {
	name string
	err  error
}

func (f *fakeRenderer) Welcome(name string) (string, string, string, error) {
	f.name = name
	return "Subject", "Text", "<p>HTML</p>", f.err
}

func TestSendWelcomeRendersAndEnqueues(t *testing.T) {
	m, r := &fakeMailer{}, &fakeRenderer{}
	err := app.NewService(m, r).SendWelcome(context.Background(), app.WelcomeInput{EventID: "ev-1", Email: "a@example.com", Name: "Alice"})
	if err != nil {
		t.Fatal(err)
	}
	if r.name != "Alice" {
		t.Fatalf("rendered name %q", r.name)
	}
	want := app.Email{To: "a@example.com", Subject: "Subject", Text: "Text", HTML: "<p>HTML</p>"}
	if m.calls != 1 || m.email != want {
		t.Fatalf("calls=%d email=%+v", m.calls, m.email)
	}
	if m.key != "ioe:identity.user_registered:ev-1:welcome-v1" {
		t.Fatalf("key %q", m.key)
	}
}

func TestSendWelcomeRejectsMissingFieldsAsPermanent(t *testing.T) {
	for name, in := range map[string]app.WelcomeInput{
		"no event id": {Email: "a@example.com"},
		"no email":    {EventID: "ev-1"},
	} {
		t.Run(name, func(t *testing.T) {
			m := &fakeMailer{}
			err := app.NewService(m, &fakeRenderer{}).SendWelcome(context.Background(), in)
			if !errors.Is(err, app.ErrPermanent) {
				t.Fatalf("err = %v, want ErrPermanent", err)
			}
			if m.calls != 0 {
				t.Fatal("mailer called")
			}
		})
	}
}

func TestSendWelcomeRenderFailureIsPermanent(t *testing.T) {
	m := &fakeMailer{}
	err := app.NewService(m, &fakeRenderer{err: errors.New("bad template")}).
		SendWelcome(context.Background(), app.WelcomeInput{EventID: "ev-1", Email: "a@example.com"})
	if !errors.Is(err, app.ErrPermanent) || m.calls != 0 {
		t.Fatalf("err = %v, calls = %d", err, m.calls)
	}
}

func TestSendWelcomePropagatesMailerError(t *testing.T) {
	errDown := errors.New("down")
	err := app.NewService(&fakeMailer{err: errDown}, &fakeRenderer{}).
		SendWelcome(context.Background(), app.WelcomeInput{EventID: "ev-1", Email: "a@example.com"})
	if !errors.Is(err, errDown) || errors.Is(err, app.ErrPermanent) {
		t.Fatalf("err = %v", err)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/notification/app/ -v`
Expected: FAIL to compile (`no non-test Go files` or undefined `app.NewService`).

- [ ] **Step 3: Implement the service**

Create `internal/notification/app/service.go`:

```go
// Package app holds the notification use cases and the ports they depend on.
package app

import (
	"context"
	"errors"
	"fmt"
)

// ErrPermanent marks a failure that retrying the same request cannot fix.
var ErrPermanent = errors.New("permanent notification failure")

// Email is one fully rendered message.
type Email struct {
	To      string
	Subject string
	Text    string
	HTML    string
}

// Mailer enqueues one rendered email. It returns an error wrapping ErrPermanent when
// retrying the same request cannot succeed.
type Mailer interface {
	Enqueue(ctx context.Context, email Email, idempotencyKey string) error
}

// Renderer produces the subject and bodies of each email.
type Renderer interface {
	Welcome(name string) (subject, text, html string, err error)
}

// WelcomeInput is the data needed to welcome a newly registered user.
type WelcomeInput struct {
	EventID string // outbox message UUID; stable across redeliveries
	Email   string
	Name    string
}

type Service struct {
	mailer   Mailer
	renderer Renderer
}

func NewService(mailer Mailer, renderer Renderer) *Service {
	return &Service{mailer: mailer, renderer: renderer}
}

// SendWelcome renders the welcome email and enqueues it exactly once per event.
func (s *Service) SendWelcome(ctx context.Context, in WelcomeInput) error {
	if in.EventID == "" || in.Email == "" {
		return fmt.Errorf("%w: welcome requires an event ID and an email", ErrPermanent)
	}
	subject, text, html, err := s.renderer.Welcome(in.Name)
	if err != nil {
		return fmt.Errorf("%w: render welcome: %w", ErrPermanent, err)
	}
	return s.mailer.Enqueue(ctx, Email{To: in.Email, Subject: subject, Text: text, HTML: html}, WelcomeKey(in.EventID))
}

// WelcomeKey is the idempotency key for the welcome email of one registration event.
// Changing the email's content requires a new version suffix.
func WelcomeKey(eventID string) string {
	return "ioe:identity.user_registered:" + eventID + ":welcome-v1"
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test -race ./internal/notification/app/ -v`
Expected: PASS (4 tests, 2 subtests).

- [ ] **Step 5: Commit**

```bash
git add internal/notification/app
git commit -m "feat(notification): add welcome email use case"
```

---

### Task 4: Welcome email templates

**Files:**
- Create: `internal/notification/adapters/templates/templates.go`
- Create: `internal/notification/adapters/templates/welcome.txt.tmpl`
- Create: `internal/notification/adapters/templates/welcome.html.tmpl`
- Test: `internal/notification/adapters/templates/templates_test.go`

**Interfaces:**
- Consumes: satisfies `app.Renderer` from Task 3.
- Produces: `func templates.New() (*templates.Renderer, error)`; `func (r *Renderer) Welcome(name string) (subject, text, html string, err error)`.

- [ ] **Step 1: Write the failing tests**

Create `internal/notification/adapters/templates/templates_test.go`:

```go
package templates_test

import (
	"strings"
	"testing"

	"github.com/santoshkc2200/ioe-backend/internal/notification/adapters/templates"
	"github.com/santoshkc2200/ioe-backend/internal/notification/app"
)

var _ app.Renderer = (*templates.Renderer)(nil)

func render(t *testing.T, name string) (string, string, string) {
	t.Helper()
	r, err := templates.New()
	if err != nil {
		t.Fatal(err)
	}
	subject, text, html, err := r.Welcome(name)
	if err != nil {
		t.Fatal(err)
	}
	return subject, text, html
}

func TestWelcomeWithName(t *testing.T) {
	subject, text, html := render(t, "Alice")
	if subject != "Welcome to IOE" {
		t.Fatalf("subject %q", subject)
	}
	if !strings.HasPrefix(text, "Hello, Alice,\n") {
		t.Fatalf("text %q", text)
	}
	if !strings.Contains(html, "<p>Hello, Alice,</p>") {
		t.Fatalf("html %q", html)
	}
}

func TestWelcomeWithoutNameOmitsIt(t *testing.T) {
	for _, name := range []string{"", "   ", "\t\n"} {
		_, text, html := render(t, name)
		if !strings.HasPrefix(text, "Hello,\n") {
			t.Fatalf("name %q: text %q", name, text)
		}
		if !strings.Contains(html, "<p>Hello,</p>") {
			t.Fatalf("name %q: html %q", name, html)
		}
	}
}

func TestWelcomeEscapesHTMLOnly(t *testing.T) {
	_, text, html := render(t, "<b>Bob</b>")
	if !strings.Contains(html, "Hello, &lt;b&gt;Bob&lt;/b&gt;,") || strings.Contains(html, "<b>") {
		t.Fatalf("html not escaped: %q", html)
	}
	if !strings.Contains(text, "Hello, <b>Bob</b>,") {
		t.Fatalf("text %q", text)
	}
}

func TestWelcomeDoesNotEvaluateNameAsTemplate(t *testing.T) {
	_, text, _ := render(t, "{{.Name}}")
	if !strings.Contains(text, "Hello, {{.Name}},") {
		t.Fatalf("text %q", text)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/notification/adapters/templates/ -v`
Expected: FAIL to compile (`undefined: templates.New`).

- [ ] **Step 3: Add the templates and renderer**

Create `internal/notification/adapters/templates/welcome.txt.tmpl`:

```text
Hello{{with .Name}}, {{.}}{{end}},

Welcome to IOE. Your account is ready.

The IOE team
```

Create `internal/notification/adapters/templates/welcome.html.tmpl`:

```html
<p>Hello{{with .Name}}, {{.}}{{end}},</p>
<p>Welcome to IOE. Your account is ready.</p>
<p>The IOE team</p>
```

Create `internal/notification/adapters/templates/templates.go`:

```go
// Package templates renders notification emails from embedded Go templates.
package templates

import (
	"bytes"
	"embed"
	htmltemplate "html/template"
	"strings"
	texttemplate "text/template"
)

//go:embed welcome.txt.tmpl welcome.html.tmpl
var files embed.FS

const welcomeSubject = "Welcome to IOE"

// Renderer renders every email. It is safe for concurrent use.
type Renderer struct {
	welcomeText *texttemplate.Template
	welcomeHTML *htmltemplate.Template
}

// New parses the embedded templates.
func New() (*Renderer, error) {
	text, err := texttemplate.ParseFS(files, "welcome.txt.tmpl")
	if err != nil {
		return nil, err
	}
	html, err := htmltemplate.ParseFS(files, "welcome.html.tmpl")
	if err != nil {
		return nil, err
	}
	return &Renderer{welcomeText: text, welcomeHTML: html}, nil
}

type welcomeData struct{ Name string }

// Welcome renders the welcome email. A blank name is omitted from the greeting.
func (r *Renderer) Welcome(name string) (subject, text, html string, err error) {
	data := welcomeData{Name: strings.TrimSpace(name)}
	var textBuf, htmlBuf bytes.Buffer
	if err = r.welcomeText.Execute(&textBuf, data); err != nil {
		return "", "", "", err
	}
	if err = r.welcomeHTML.Execute(&htmlBuf, data); err != nil {
		return "", "", "", err
	}
	return welcomeSubject, textBuf.String(), htmlBuf.String(), nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test -race ./internal/notification/adapters/templates/ -v`
Expected: PASS (4 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/notification/adapters/templates
git commit -m "feat(notification): add welcome email templates"
```

---

### Task 5: Notification service HTTP client

**Files:**
- Create: `internal/notification/adapters/notifysvc/client.go`
- Test: `internal/notification/adapters/notifysvc/client_test.go`
- Modify: `go.mod`, `go.sum` (otelhttp becomes direct)

**Interfaces:**
- Consumes: `app.Email`, `app.ErrPermanent`, satisfies `app.Mailer` (Task 3).
- Produces: `func notifysvc.New(baseURL, apiKey string) *notifysvc.Client`; `func (c *Client) Enqueue(ctx context.Context, email app.Email, idempotencyKey string) error`.

- [ ] **Step 1: Write the failing tests**

Create `internal/notification/adapters/notifysvc/client_test.go`:

```go
package notifysvc_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/santoshkc2200/ioe-backend/internal/notification/adapters/notifysvc"
	"github.com/santoshkc2200/ioe-backend/internal/notification/app"
)

const apiKey = "test-send-key"

var _ app.Mailer = (*notifysvc.Client)(nil)

type captured struct {
	method, path, auth, key, contentType string
	body                                 map[string]any
}

func newServer(t *testing.T, status int, contentType, body string) (*httptest.Server, *captured) {
	t.Helper()
	c := &captured{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.method, c.path = r.Method, r.URL.Path
		c.auth, c.key, c.contentType = r.Header.Get("Authorization"), r.Header.Get("Idempotency-Key"), r.Header.Get("Content-Type")
		_ = json.NewDecoder(r.Body).Decode(&c.body)
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("X-Request-ID", "req-1")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, c
}

var email = app.Email{To: "a@example.com", Subject: "Welcome to IOE", Text: "Hello,", HTML: "<p>Hello,</p>"}

func TestEnqueueSendsContractRequest(t *testing.T) {
	srv, c := newServer(t, http.StatusAccepted, "application/json", `{"notification":{"id":"x"},"idempotent_replay":false}`)
	if err := notifysvc.New(srv.URL, apiKey).Enqueue(context.Background(), email, "key-1"); err != nil {
		t.Fatal(err)
	}
	if c.method != http.MethodPost || c.path != "/v1/notifications" {
		t.Fatalf("%s %s", c.method, c.path)
	}
	if c.auth != "Bearer "+apiKey || c.key != "key-1" || c.contentType != "application/json" {
		t.Fatalf("headers auth=%q key=%q type=%q", c.auth, c.key, c.contentType)
	}
	want := map[string]any{"recipient": "a@example.com", "subject": "Welcome to IOE", "text_body": "Hello,", "html_body": "<p>Hello,</p>"}
	if len(c.body) != len(want) {
		t.Fatalf("body %v", c.body)
	}
	for k, v := range want {
		if c.body[k] != v {
			t.Fatalf("body[%s] = %v, want %v", k, c.body[k], v)
		}
	}
}

func TestEnqueueOmitsEmptyHTML(t *testing.T) {
	srv, c := newServer(t, http.StatusAccepted, "application/json", `{}`)
	plain := email
	plain.HTML = ""
	if err := notifysvc.New(srv.URL, apiKey).Enqueue(context.Background(), plain, "key-1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.body["html_body"]; ok {
		t.Fatalf("html_body sent: %v", c.body)
	}
}

func TestEnqueueJoinsBaseURLWithPathPrefix(t *testing.T) {
	srv, c := newServer(t, http.StatusAccepted, "application/json", `{}`)
	if err := notifysvc.New(srv.URL+"/notify/", apiKey).Enqueue(context.Background(), email, "key-1"); err != nil {
		t.Fatal(err)
	}
	if c.path != "/notify/v1/notifications" {
		t.Fatalf("path %q", c.path)
	}
}

func TestEnqueueMapsStatuses(t *testing.T) {
	cases := []struct {
		status          int
		contentType     string
		body            string
		wantPermanent   bool
		wantErrContains string
	}{
		{http.StatusConflict, "application/problem+json", `{"type":"idempotency-conflict"}`, true, "idempotency-conflict"},
		{http.StatusBadRequest, "application/problem+json", `{"type":"validation"}`, true, "validation"},
		{http.StatusUnauthorized, "application/problem+json", `{"type":"unauthorized"}`, true, "unauthorized"},
		{http.StatusTooManyRequests, "application/problem+json", `{"type":"rate-limited"}`, false, "429"},
		{http.StatusInternalServerError, "application/problem+json", `{"type":"internal"}`, false, "500"},
		{http.StatusBadGateway, "text/html", `<html><body>Bad Gateway</body></html>`, false, "502"},
	}
	for _, tc := range cases {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			srv, _ := newServer(t, tc.status, tc.contentType, tc.body)
			err := notifysvc.New(srv.URL, apiKey).Enqueue(context.Background(), email, "key-1")
			if err == nil {
				t.Fatal("expected error")
			}
			if errors.Is(err, app.ErrPermanent) != tc.wantPermanent {
				t.Fatalf("permanent = %v, want %v (%v)", !tc.wantPermanent, tc.wantPermanent, err)
			}
			msg := err.Error()
			if !strings.Contains(msg, tc.wantErrContains) || !strings.Contains(msg, "req-1") {
				t.Fatalf("error %q lacks %q or request id", msg, tc.wantErrContains)
			}
			if strings.Contains(msg, apiKey) || strings.Contains(msg, email.To) || strings.Contains(msg, "Bad Gateway") {
				t.Fatalf("error leaks secret, recipient, or body: %q", msg)
			}
		})
	}
}

func TestEnqueueNetworkErrorIsRetryable(t *testing.T) {
	srv, _ := newServer(t, http.StatusAccepted, "application/json", `{}`)
	url := srv.URL
	srv.Close()
	err := notifysvc.New(url, apiKey).Enqueue(context.Background(), email, "key-1")
	if err == nil || errors.Is(err, app.ErrPermanent) {
		t.Fatalf("err = %v, want retryable error", err)
	}
	if strings.Contains(err.Error(), apiKey) {
		t.Fatalf("error leaks key: %v", err)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/notification/adapters/notifysvc/ -v`
Expected: FAIL to compile (`undefined: notifysvc.New`).

- [ ] **Step 3: Implement the client**

Create `internal/notification/adapters/notifysvc/client.go`:

```go
// Package notifysvc enqueues email through the standalone notification service.
package notifysvc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/santoshkc2200/ioe-backend/internal/notification/app"
)

const (
	requestTimeout = 10 * time.Second
	maxBodyBytes   = 64 << 10
)

// Client calls POST /v1/notifications with a send key.
type Client struct {
	endpoint string
	apiKey   string
	http     *http.Client
}

// New returns a client for the service at baseURL, which may include a path prefix.
func New(baseURL, apiKey string) *Client {
	return &Client{
		endpoint: strings.TrimRight(baseURL, "/") + "/v1/notifications",
		apiKey:   apiKey,
		http:     &http.Client{Timeout: requestTimeout, Transport: otelhttp.NewTransport(http.DefaultTransport)},
	}
}

type enqueueRequest struct {
	Recipient string `json:"recipient"`
	Subject   string `json:"subject"`
	TextBody  string `json:"text_body"`
	HTMLBody  string `json:"html_body,omitempty"`
}

// Enqueue submits email under idempotencyKey. Errors never include the key, recipient,
// or bodies; permanent failures wrap app.ErrPermanent.
func (c *Client) Enqueue(ctx context.Context, email app.Email, idempotencyKey string) error {
	body, err := json.Marshal(enqueueRequest{Recipient: email.To, Subject: email.Subject, TextBody: email.Text, HTMLBody: email.HTML})
	if err != nil {
		return fmt.Errorf("%w: encode request: %w", app.ErrPermanent, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("%w: build request: %w", app.ErrPermanent, err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", idempotencyKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("notification service request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusAccepted {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxBodyBytes))
		return nil
	}
	failure := fmt.Errorf("notification service responded %d (type %q, request %q)",
		resp.StatusCode, problemType(resp.Body), resp.Header.Get("X-Request-ID"))
	switch {
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= http.StatusInternalServerError:
		return failure
	case resp.StatusCode >= http.StatusBadRequest:
		return fmt.Errorf("%w: %w", app.ErrPermanent, failure)
	default:
		return failure
	}
}

// problemType returns the problem+json "type" field, or "" for any other body.
func problemType(body io.Reader) string {
	var problem struct {
		Type string `json:"type"`
	}
	if err := json.NewDecoder(io.LimitReader(body, maxBodyBytes)).Decode(&problem); err != nil {
		return ""
	}
	return problem.Type
}
```

- [ ] **Step 4: Promote otelhttp and run the tests**

Run: `go mod tidy && go test -race ./internal/notification/adapters/notifysvc/ -v`
Expected: `go.mod` lists `go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp v0.71.0` in the direct `require` block; all tests PASS.

- [ ] **Step 5: Commit**

```bash
git add go.mod go.sum internal/notification/adapters/notifysvc
git commit -m "feat(notification): add notification service client"
```

---

### Task 6: `identity.user_registered` event handler

**Files:**
- Create: `internal/notification/adapters/events/events.go`
- Test: `internal/notification/adapters/events/events_test.go`

**Interfaces:**
- Consumes: `app.WelcomeInput`, `app.ErrPermanent` (Task 3); signature matches `outbox.Handler` from Task 1 without importing it.
- Produces:
  - `type events.WelcomeSender interface { SendWelcome(ctx context.Context, in app.WelcomeInput) error }`
  - `func events.New(welcome WelcomeSender, logger *slog.Logger, meter metric.Meter) (*events.Handlers, error)`
  - `func (h *Handlers) Welcome(msg *message.Message) error`

- [ ] **Step 1: Write the failing tests**

Create `internal/notification/adapters/events/events_test.go`:

```go
package events_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/ThreeDotsLabs/watermill/message"
	"go.opentelemetry.io/otel/metric/noop"

	"github.com/santoshkc2200/ioe-backend/internal/notification/adapters/events"
	"github.com/santoshkc2200/ioe-backend/internal/notification/app"
)

type fakeSender struct {
	calls int
	in    app.WelcomeInput
	err   error
}

func (f *fakeSender) SendWelcome(_ context.Context, in app.WelcomeInput) error {
	f.calls++
	f.in = in
	return f.err
}

func newHandlers(t *testing.T, sender *fakeSender) (*events.Handlers, *bytes.Buffer) {
	t.Helper()
	var logs bytes.Buffer
	h, err := events.New(sender, slog.New(slog.NewJSONHandler(&logs, nil)), noop.NewMeterProvider().Meter("test"))
	if err != nil {
		t.Fatal(err)
	}
	return h, &logs
}

func TestWelcomeSendsForRegisteredUser(t *testing.T) {
	s := &fakeSender{}
	h, _ := newHandlers(t, s)
	msg := message.NewMessage("ev-1", []byte(`{"user_id":"u1","email":"a@example.com","name":"Alice","occurred_at":"2026-10-04T00:00:00Z"}`))
	if err := h.Welcome(msg); err != nil {
		t.Fatal(err)
	}
	want := app.WelcomeInput{EventID: "ev-1", Email: "a@example.com", Name: "Alice"}
	if s.calls != 1 || s.in != want {
		t.Fatalf("calls=%d in=%+v", s.calls, s.in)
	}
}

func TestWelcomeAcceptsEventWithoutName(t *testing.T) {
	s := &fakeSender{}
	h, _ := newHandlers(t, s)
	if err := h.Welcome(message.NewMessage("ev-1", []byte(`{"user_id":"u1","email":"a@example.com"}`))); err != nil {
		t.Fatal(err)
	}
	if s.in.Name != "" || s.in.Email != "a@example.com" {
		t.Fatalf("in=%+v", s.in)
	}
}

func TestWelcomeDropsInvalidPayload(t *testing.T) {
	s := &fakeSender{}
	h, logs := newHandlers(t, s)
	if err := h.Welcome(message.NewMessage("ev-1", []byte(`{"email":"a@example.com"`))); err != nil {
		t.Fatalf("err = %v, want nil (acknowledged)", err)
	}
	if s.calls != 0 {
		t.Fatal("sender called")
	}
	if out := logs.String(); !strings.Contains(out, "invalid_payload") || !strings.Contains(out, "ev-1") || strings.Contains(out, "a@example.com") {
		t.Fatalf("log %q", out)
	}
}

func TestWelcomeDropsPermanentFailure(t *testing.T) {
	s := &fakeSender{err: fmt.Errorf("%w: notification service responded 409", app.ErrPermanent)}
	h, logs := newHandlers(t, s)
	if err := h.Welcome(message.NewMessage("ev-1", []byte(`{"email":"a@example.com","name":"Alice"}`))); err != nil {
		t.Fatalf("err = %v, want nil (acknowledged)", err)
	}
	if out := logs.String(); !strings.Contains(out, `"reason":"permanent"`) || strings.Contains(out, "a@example.com") || strings.Contains(out, "Alice") {
		t.Fatalf("log %q", out)
	}
}

func TestWelcomeReturnsRetryableFailure(t *testing.T) {
	errDown := errors.New("notification service request: connection refused")
	h, _ := newHandlers(t, &fakeSender{err: errDown})
	if err := h.Welcome(message.NewMessage("ev-1", []byte(`{"email":"a@example.com"}`))); !errors.Is(err, errDown) {
		t.Fatalf("err = %v, want %v", err, errDown)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/notification/adapters/events/ -v`
Expected: FAIL to compile (`undefined: events.New`).

- [ ] **Step 3: Implement the handler**

Create `internal/notification/adapters/events/events.go`:

```go
// Package events turns outbox events into notification use-case calls.
package events

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"

	"github.com/ThreeDotsLabs/watermill/message"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/santoshkc2200/ioe-backend/internal/notification/app"
)

// WelcomeSender is the use case behind the welcome handler.
type WelcomeSender interface {
	SendWelcome(ctx context.Context, in app.WelcomeInput) error
}

// Handlers holds one outbox handler per consumed event.
type Handlers struct {
	welcome WelcomeSender
	logger  *slog.Logger
	dropped metric.Int64Counter
}

func New(welcome WelcomeSender, logger *slog.Logger, meter metric.Meter) (*Handlers, error) {
	dropped, err := meter.Int64Counter("notification.events.dropped",
		metric.WithDescription("Events acknowledged without sending a notification."))
	if err != nil {
		return nil, err
	}
	return &Handlers{welcome: welcome, logger: logger, dropped: dropped}, nil
}

// userRegistered mirrors the identity.user_registered payload this context relies on.
type userRegistered struct {
	Email string `json:"email"`
	Name  string `json:"name"`
}

// Welcome handles identity.user_registered. It returns an error only when the message
// should be retried; malformed events and permanent failures are logged and acknowledged.
func (h *Handlers) Welcome(msg *message.Message) error {
	var ev userRegistered
	if err := json.Unmarshal(msg.Payload, &ev); err != nil {
		h.drop(msg, "invalid_payload", err)
		return nil
	}
	err := h.welcome.SendWelcome(msg.Context(), app.WelcomeInput{EventID: msg.UUID, Email: ev.Email, Name: ev.Name})
	if errors.Is(err, app.ErrPermanent) {
		h.drop(msg, "permanent", err)
		return nil
	}
	return err
}

func (h *Handlers) drop(msg *message.Message, reason string, cause error) {
	ctx := msg.Context()
	h.logger.ErrorContext(ctx, "notification event dropped", "event_id", msg.UUID, "reason", reason, "error", cause.Error())
	h.dropped.Add(ctx, 1, metric.WithAttributes(attribute.String("reason", reason)))
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go mod tidy && go test -race ./internal/notification/... -v`
Expected: `go mod tidy` leaves `go.mod` unchanged except, if needed, moving `go.opentelemetry.io/otel/metric` into the direct block; all notification tests PASS.

- [ ] **Step 5: Commit**

```bash
git add go.mod go.sum internal/notification/adapters/events
git commit -m "feat(notification): handle user registration events"
```

---

### Task 7: Notification service configuration

**Files:**
- Modify: `internal/platform/config/config.go`
- Test: `internal/platform/config/config_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `config.Config.NotificationServiceBaseURL string`, `config.Config.NotificationServiceSendAPIKey string`, `func (c Config) NotificationsEnabled() bool`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/platform/config/config_test.go`:

```go
const localNotificationKey = "c3Nzc3Nzc3Nzc3Nzc3Nzc3Nzc3Nzc3Nzc3Nzc3Nzc3M"

func TestNotificationsDisabledByDefault(t *testing.T) {
	cfg, err := config.LoadFrom(validEnv())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.NotificationsEnabled() {
		t.Fatal("notifications enabled without configuration")
	}
}

func TestNotificationsEnabled(t *testing.T) {
	env := validEnv()
	env["NOTIFICATION_SERVICE_BASE_URL"] = "http://notification:8080"
	env["NOTIFICATION_SERVICE_SEND_API_KEY"] = localNotificationKey
	cfg, err := config.LoadFrom(env)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.NotificationsEnabled() || cfg.NotificationServiceBaseURL != "http://notification:8080" || cfg.NotificationServiceSendAPIKey != localNotificationKey {
		t.Fatalf("%+v", cfg)
	}
}

func TestNotificationConfigRejectsInvalidValues(t *testing.T) {
	cases := map[string]struct{ url, key, wantVar string }{
		"url without key":       {"http://notification:8080", "", "NOTIFICATION_SERVICE_SEND_API_KEY"},
		"key without url":       {"", localNotificationKey, "NOTIFICATION_SERVICE_BASE_URL"},
		"url without scheme":    {"notification:8080", localNotificationKey, "NOTIFICATION_SERVICE_BASE_URL"},
		"url with ftp scheme":   {"ftp://notification", localNotificationKey, "NOTIFICATION_SERVICE_BASE_URL"},
		"url with query":        {"http://notification:8080?x=1", localNotificationKey, "NOTIFICATION_SERVICE_BASE_URL"},
		"short key":             {"http://notification:8080", "c2hvcnQ", "NOTIFICATION_SERVICE_SEND_API_KEY"},
		"padded key":            {"http://notification:8080", localNotificationKey + "=", "NOTIFICATION_SERVICE_SEND_API_KEY"},
		"standard alphabet key": {"http://notification:8080", "+" + localNotificationKey[1:], "NOTIFICATION_SERVICE_SEND_API_KEY"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			env := validEnv()
			if tc.url != "" {
				env["NOTIFICATION_SERVICE_BASE_URL"] = tc.url
			}
			if tc.key != "" {
				env["NOTIFICATION_SERVICE_SEND_API_KEY"] = tc.key
			}
			_, err := config.LoadFrom(env)
			if err == nil || !strings.Contains(err.Error(), tc.wantVar) {
				t.Fatalf("err = %v, want mention of %s", err, tc.wantVar)
			}
			if tc.key != "" && strings.Contains(err.Error(), tc.key) {
				t.Fatalf("error echoes the key: %v", err)
			}
		})
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/platform/config/ -run Notification -v`
Expected: FAIL to compile (`cfg.NotificationsEnabled undefined`).

- [ ] **Step 3: Add the fields, accessor, and validation**

In `internal/platform/config/config.go`:

Add `"encoding/base64"` to the imports.

Add these fields to `Config`, after `LogLevel`:

```go
	NotificationServiceBaseURL    string `env:"NOTIFICATION_SERVICE_BASE_URL"`
	NotificationServiceSendAPIKey string `env:"NOTIFICATION_SERVICE_SEND_API_KEY"`
```

Add after `TrustedProxies`:

```go
// NotificationsEnabled reports whether the notification service is configured.
// LoadFrom guarantees that both variables are set or neither is.
func (c Config) NotificationsEnabled() bool { return c.NotificationServiceBaseURL != "" }
```

In `validate`, before `return errors.Join(errs...)`, add:

```go
	errs = append(errs, c.validateNotifications()...)
```

Add after `validate`:

```go
func (c *Config) validateNotifications() []error {
	hasURL, hasKey := c.NotificationServiceBaseURL != "", c.NotificationServiceSendAPIKey != ""
	switch {
	case !hasURL && !hasKey:
		return nil
	case !hasKey:
		return []error{errors.New("NOTIFICATION_SERVICE_SEND_API_KEY: required when NOTIFICATION_SERVICE_BASE_URL is set")}
	case !hasURL:
		return []error{errors.New("NOTIFICATION_SERVICE_BASE_URL: required when NOTIFICATION_SERVICE_SEND_API_KEY is set")}
	}
	var errs []error
	if !isBaseURL(c.NotificationServiceBaseURL) {
		errs = append(errs, fmt.Errorf("NOTIFICATION_SERVICE_BASE_URL: %q is not an absolute http(s) URL without query or fragment", c.NotificationServiceBaseURL))
	}
	if key, err := base64.RawURLEncoding.DecodeString(c.NotificationServiceSendAPIKey); err != nil || len(key) < 32 {
		errs = append(errs, errors.New("NOTIFICATION_SERVICE_SEND_API_KEY: must be unpadded base64url encoding at least 32 bytes"))
	}
	return errs
}

func isBaseURL(s string) bool {
	u, err := url.Parse(s)
	if err != nil {
		return false
	}
	return (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" &&
		u.RawQuery == "" && u.Fragment == "" && u.User == nil
}
```

- [ ] **Step 4: Run the config tests**

Run: `go test -race ./internal/platform/config/ -v`
Expected: PASS, including all 8 subtests of `TestNotificationConfigRejectsInvalidValues`.

- [ ] **Step 5: Commit**

```bash
git add internal/platform/config
git commit -m "feat(config): add notification service settings"
```

---

### Task 8: Wire notifications into the API and enforce boundaries

**Files:**
- Modify: `cmd/api/app.go`
- Modify: `cmd/api/e2e_integration_test.go`
- Modify: `.golangci.yml`

**Interfaces:**
- Consumes: `outbox.Forwarder.Handle` (Task 1), `identitydomain.UserRegistered{}.EventName()` (Task 2), `notificationapp.NewService` (Task 3), `templates.New` (Task 4), `notifysvc.New` (Task 5), `notificationevents.New` and `(*Handlers).Welcome` (Task 6), `cfg.NotificationsEnabled`, `cfg.NotificationServiceBaseURL`, `cfg.NotificationServiceSendAPIKey` (Task 7).
- Produces: running welcome-email pipeline.

- [ ] **Step 1: Extend the end-to-end test (failing)**

In `cmd/api/e2e_integration_test.go`:

Add a constant next to `appOrigin`:

```go
const notifyKey = "c3Nzc3Nzc3Nzc3Nzc3Nzc3Nzc3Nzc3Nzc3Nzc3Nzc3M"
```

At the start of `TestEndToEnd`, right after `google := googletest.NewIssuer(t)`, add a fake notification service:

```go
	type sentEmail struct{ key, recipient, auth string }
	sent := make(chan sentEmail, 4)
	notify := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Recipient string `json:"recipient"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		sent <- sentEmail{key: r.Header.Get("Idempotency-Key"), recipient: body.Recipient, auth: r.Header.Get("Authorization")}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer notify.Close()
```

Add to the `config.Config` literal:

```go
		NotificationServiceBaseURL:    notify.URL,
		NotificationServiceSendAPIKey: notifyKey,
```

Immediately after `defer a.forwarder.Close()`, start the forwarder:

```go
	go func() { _ = a.forwarder.Run(ctx) }()
```

At the end of `TestEndToEnd`, after the outbox count check, add:

```go
	select {
	case got := <-sent:
		if got.recipient != "e2e@example.com" || got.auth != "Bearer "+notifyKey ||
			!strings.HasPrefix(got.key, "ioe:identity.user_registered:") || !strings.HasSuffix(got.key, ":welcome-v1") {
			t.Fatalf("welcome email %+v", got)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("welcome email not enqueued")
	}
	select {
	case got := <-sent:
		t.Fatalf("unexpected second email %+v", got)
	case <-time.After(2 * time.Second):
	}
```

- [ ] **Step 2: Run the end-to-end test to verify it fails**

Run: `go test -race -tags integration ./cmd/api/ -run TestEndToEnd -v`
Expected: FAIL with `welcome email not enqueued` (no handler registered yet). Docker must be running.

- [ ] **Step 3: Register the notification handler in `buildApp`**

In `cmd/api/app.go`, add imports:

```go
	"errors"

	"go.opentelemetry.io/otel"

	identitydomain "github.com/santoshkc2200/ioe-backend/internal/identity/domain"
	notificationevents "github.com/santoshkc2200/ioe-backend/internal/notification/adapters/events"
	"github.com/santoshkc2200/ioe-backend/internal/notification/adapters/notifysvc"
	"github.com/santoshkc2200/ioe-backend/internal/notification/adapters/templates"
	notificationapp "github.com/santoshkc2200/ioe-backend/internal/notification/app"
```

Replace the end of `buildApp`:

```go
	fw, err := outbox.NewForwarder(pool, logger)
	if err != nil {
		return nil, err
	}
	return &application{handler: handler, forwarder: fw}, nil
}
```

with:

```go
	fw, err := outbox.NewForwarder(pool, logger)
	if err != nil {
		return nil, err
	}
	if err := registerNotifications(fw, cfg, logger); err != nil {
		return nil, errors.Join(err, fw.Close())
	}
	return &application{handler: handler, forwarder: fw}, nil
}

// registerNotifications subscribes the notification context to the events it consumes.
func registerNotifications(fw *outbox.Forwarder, cfg config.Config, logger *slog.Logger) error {
	if !cfg.NotificationsEnabled() {
		logger.Warn("notifications disabled: NOTIFICATION_SERVICE_BASE_URL and NOTIFICATION_SERVICE_SEND_API_KEY are not set")
		return nil
	}
	renderer, err := templates.New()
	if err != nil {
		return err
	}
	mailer := notifysvc.New(cfg.NotificationServiceBaseURL, cfg.NotificationServiceSendAPIKey)
	handlers, err := notificationevents.New(notificationapp.NewService(mailer, renderer), logger,
		otel.Meter("github.com/santoshkc2200/ioe-backend/internal/notification"))
	if err != nil {
		return err
	}
	fw.Handle(identitydomain.UserRegistered{}.EventName(), handlers.Welcome)
	return nil
}
```

- [ ] **Step 4: Run the end-to-end test to verify it passes**

Run: `go test -race -tags integration ./cmd/api/ -run TestEndToEnd -v`
Expected: PASS.

- [ ] **Step 5: Add depguard rules for the new context**

In `.golangci.yml`, under `linters.settings.depguard.rules`:

Extend `platform-independent-of-contexts.deny` with:

```yaml
            - pkg: github.com/santoshkc2200/ioe-backend/internal/notification
              desc: platform must not depend on bounded contexts
```

Add these rules after `identity-app`:

```yaml
        identity-independent-of-notification:
          list-mode: lax
          files:
            - "**/internal/identity/**"
          deny:
            - pkg: github.com/santoshkc2200/ioe-backend/internal/notification
              desc: bounded contexts must not import each other
        notification-independent-of-identity:
          list-mode: lax
          files:
            - "**/internal/notification/**"
          deny:
            - pkg: github.com/santoshkc2200/ioe-backend/internal/identity
              desc: bounded contexts must not import each other
        notification-app:
          list-mode: strict
          files:
            - "**/internal/notification/app/**"
            - "!$test"
          allow:
            - $gostd
```

- [ ] **Step 6: Lint and run all unit tests**

Run: `make lint && make test`
Expected: no lint findings; all tests PASS. If `goimports` reports grouping in `cmd/api/app.go`, run `make fmt` and re-run `make lint`.

- [ ] **Step 7: Commit**

```bash
git add cmd/api/app.go cmd/api/e2e_integration_test.go .golangci.yml
git commit -m "feat(api): send welcome email on user registration"
```

---

### Task 9: Shared-database local stack, docs, and final gates

**Files:**
- Create: `compose/notification-db-setup.sql`
- Modify: `docker-compose.yml`
- Modify: `.env.example`
- Modify: `README.md`
- Modify (only if gitleaks flags the local credentials): `.gitleaks.toml`

**Interfaces:**
- Consumes: `NOTIFICATION_SERVICE_BASE_URL`, `NOTIFICATION_SERVICE_SEND_API_KEY` (Task 7); the notification service's `migrate` and `serve` commands and its environment variables (`../notification/.env.example`).
- Produces: `docker compose --profile app up` runs ioe, the notification service in schema `notification` of database `ioe`, and Mailpit.

- [ ] **Step 1: Add the idempotent role setup script**

Create `compose/notification-db-setup.sql`:

```sql
-- Local development only. Creates the notification service's login role in the shared
-- ioe database. The service creates and owns the `notification` schema when it migrates.
-- Safe to run on every `docker compose up`.
DO $$
BEGIN
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'notification') THEN
        CREATE ROLE notification LOGIN PASSWORD 'notification-local-dev';
    END IF;
END
$$;

GRANT CONNECT, CREATE ON DATABASE ioe TO notification;
```

- [ ] **Step 2: Add the notification services to compose**

In `docker-compose.yml`, add under `services:` (after `openobserve`):

```yaml
  mailpit:
    profiles: [app]
    image: axllent/mailpit:v1.27.8
    ports:
      - "127.0.0.1:8025:8025"
    healthcheck:
      test: ["CMD", "/mailpit", "readyz"]
      interval: 2s
      timeout: 3s
      retries: 20

  notification-db-setup:
    profiles: [app]
    image: postgres:17-alpine@sha256:b0f9560a2de083e2cc7382e75f808c7381a32852a7ec49117deedb300e552b24
    environment:
      PGPASSWORD: ioe
    command: ["psql", "-h", "postgres", "-U", "ioe", "-d", "ioe", "-v", "ON_ERROR_STOP=1", "-f", "/setup/notification-db-setup.sql"]
    volumes:
      - ./compose/notification-db-setup.sql:/setup/notification-db-setup.sql:ro
    depends_on:
      postgres:
        condition: service_healthy

  notification-migrate:
    profiles: [app]
    build: ../notification
    command: ["migrate"]
    environment:
      DATABASE_URL: postgres://notification:notification-local-dev@postgres:5432/ioe?sslmode=disable
    depends_on:
      notification-db-setup:
        condition: service_completed_successfully

  notification:
    profiles: [app]
    build: ../notification
    command: ["serve"]
    environment:
      HTTP_ADDRESS: ":8080"
      DATABASE_URL: postgres://notification:notification-local-dev@postgres:5432/ioe?sslmode=disable
      # Local-only keys, documented in ../notification/README.md.
      NOTIFICATION_SEND_API_KEYS: c3Nzc3Nzc3Nzc3Nzc3Nzc3Nzc3Nzc3Nzc3Nzc3Nzc3M
      NOTIFICATION_READ_API_KEYS: cnJycnJycnJycnJycnJycnJycnJycnJycnJycnJycnI
      PUSH_ENABLED: "false"
      SMTP_HOST: mailpit
      SMTP_PORT: "1025"
      SMTP_FROM_ADDRESS: notifications@ioe.test
      SMTP_FROM_NAME: IOE
      SMTP_MESSAGE_ID_DOMAIN: mail.ioe.test
      SMTP_TLS_MODE: insecure
      ALLOW_INSECURE_SMTP: "true"
      LOG_LEVEL: info
    ports:
      - "127.0.0.1:8081:8080"
    depends_on:
      notification-migrate:
        condition: service_completed_successfully
      mailpit:
        condition: service_healthy
```

In the existing `api` service, add to `environment`:

```yaml
      NOTIFICATION_SERVICE_BASE_URL: http://notification:8080
      NOTIFICATION_SERVICE_SEND_API_KEY: c3Nzc3Nzc3Nzc3Nzc3Nzc3Nzc3Nzc3Nzc3Nzc3Nzc3M
```

and to its `depends_on`:

```yaml
      notification:
        condition: service_healthy
```

- [ ] **Step 3: Validate the compose file**

Run: `docker compose config --quiet && docker compose --profile app config --services`
Expected: no error; the service list includes `mailpit`, `notification-db-setup`, `notification-migrate`, `notification`, `migrate`, `api`, `postgres`, `openobserve`.

- [ ] **Step 4: Document the variables and the shared database**

Append to `.env.example`:

```sh

# Notification service. Leave both empty to disable notifications (startup logs a warning).
# Local service: docker compose --profile app up -d --wait notification
# then set NOTIFICATION_SERVICE_BASE_URL=http://localhost:8081 and the local-only key
# NOTIFICATION_SERVICE_SEND_API_KEY=c3Nzc3Nzc3Nzc3Nzc3Nzc3Nzc3Nzc3Nzc3Nzc3Nzc3M
NOTIFICATION_SERVICE_BASE_URL=
NOTIFICATION_SERVICE_SEND_API_KEY=
```

Append to `README.md`:

````markdown
## Notifications

Welcome emails are sent through the standalone notification service in `../notification`.
`docker compose --profile app up --build` creates its database role, runs its migrations into
the `notification` schema of the `ioe` database, and starts it with Mailpit
(http://localhost:8025). To use it with `make run`, start only the service and point `.env` at it:

```sh
docker compose --profile app up -d --wait notification
# .env: NOTIFICATION_SERVICE_BASE_URL=http://localhost:8081 and the local key from .env.example
```

In production the notification service shares the application database but owns only the
`notification` schema. Before its first `notification-service migrate`, a database
administrator creates a dedicated login role for it and runs
`GRANT CONNECT, CREATE ON DATABASE <database> TO <role>;`. Never grant the application role
access to that schema, and never manage it from `ioe-backend` migrations. Deploy
`notification-service migrate` as its own job before `notification-service serve`.
````

- [ ] **Step 5: Commit**

```bash
git add compose/notification-db-setup.sql docker-compose.yml .env.example README.md
git commit -m "build: run notification service on the shared database locally"
```

- [ ] **Step 6: Scan for secrets and allowlist the local-only credentials if flagged**

Run: `make secrets`
Expected: no leaks. If gitleaks reports `c3Nzc3Nzc3Nzc3Nzc3Nzc3Nzc3Nzc3Nzc3Nzc3Nzc3M`, `cnJycnJycnJycnJycnJycnJycnJycnJycnJycnJycnI`, or `notification-local-dev`, append to `.gitleaks.toml`:

```toml

[[allowlists]]
description = "Notification service local-dev credentials (documented in ../notification/README.md, compose only)"
regexTarget = "secret"
regexes = [
  '''c3Nzc3Nzc3Nzc3Nzc3Nzc3Nzc3Nzc3Nzc3Nzc3Nzc3M''',
  '''cnJycnJycnJycnJycnJycnJycnJycnJycnJycnJycnI''',
  '''notification-local-dev''',
]
```

then run `make secrets` again (expected: no leaks) and commit:

```bash
git add .gitleaks.toml
git commit -m "chore: allowlist notification service local-dev credentials"
```

If gitleaks reports anything else, stop and report it.

- [ ] **Step 7: Run every required gate**

Run each and record the result:

```sh
make check
make test-integration
docker compose config --quiet
make docker-build
git diff --check
```

Expected: all succeed. `make test-integration` requires Docker; if it cannot run, report that integration tests did not run instead of marking them passed.

- [ ] **Step 8: Manual check of the shared database on PostgreSQL 17**

This verifies what the automated tests cannot: the notification migrations on PostgreSQL 17 in the shared `ioe` database, and Mailpit delivery. It requires a `.env` with valid JWT settings (see the Quick start in `README.md`).

```sh
docker compose --profile app up --build -d --wait
docker compose exec postgres psql -U ioe -d ioe -c "\dn"
curl -si http://localhost:8081/v1/notifications \
  -H 'Authorization: Bearer c3Nzc3Nzc3Nzc3Nzc3Nzc3Nzc3Nzc3Nzc3Nzc3Nzc3M' \
  -H 'Content-Type: application/json' -H 'Idempotency-Key: manual-check-1' \
  --data '{"recipient":"check@example.com","subject":"Check","text_body":"Shared database check."}'
sleep 5
curl -s http://localhost:8025/api/v1/messages | grep -c 'check@example.com'
docker compose --profile app down
```

Expected: `\dn` lists `identity`, `notification`, `platform`; the `curl` prints `HTTP/1.1 202 Accepted`; the Mailpit query prints a count of at least `1`. Report the outcome; if any step fails, stop and report the failing output.
