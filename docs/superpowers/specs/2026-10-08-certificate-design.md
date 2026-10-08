# Certificate Design

Date: 2026-10-08

## Status

Approved in conversation on 2026-10-08. Pending written-spec review.

## Context

The progress and exams specs (`2026-10-06-progress-design.md`, `2026-10-06-exams-design.md`) both
list certificates as a non-goal. Students can now finish lectures, pass exams and, since the
refund slice, lose access when a purchase is refunded. Nothing records that a student completed a
course.

Current state:

- `progress` records per-user lecture completion for a course. It emits no events.
- `assessment` records exam attempts with `Passed`. It emits no events. A course can have several
  exams.
- `enrollment` owns active or canceled access. `payment` emits `payment.purchase.refunded` with an
  `access_revoked` flag, false when another paid purchase keeps access.
- Contexts never import each other. Synchronous needs go through a port defined in the consumer's
  `app` and wired in `cmd/api`. Asynchronous needs go through `platform/outbox`.

## Goals

- An instructor sets, per course, whether certificates are offered and what earns one.
- A student who meets the course's bar claims a certificate on demand.
- Anyone with a certificate code can verify it publicly and see whether it is valid or revoked.
- A refund that revokes access also revokes the student's certificate for that course.

## Non-goals

- PDF or image rendering. The backend stores the record; the frontend renders it.
- Automatic issuing. The student claims; no `progress` or `assessment` events are added.
- Manual revoke by a root admin.
- Emailing the student when a certificate is issued.
- Certificate templates, signatures, or expiry.

## Decisions

- **Mode per course.** `off`, `completion`, or `completion_and_exam`. The default for a course with
  no policy is `off`.
- **Exam rule.** `completion_and_exam` names one exam, `exam_id`, that the student must have passed.
  The exam must belong to the course.
- **Claim on demand.** `POST` checks eligibility through ports and issues. No event consumers are
  needed for issuing.
- **Revoke on refund.** The context consumes `enrollment.enrollment.canceled` and revokes when
  `reason` is `refunded`. It listens to enrollment, not to `payment.purchase.refunded`, because
  payment announces a refund before the enrollment is canceled: a claim in between would keep
  its certificate.
- **Snapshots.** The record stores the student's name and the course title at issue time.
- **Reissue.** A revoked certificate stays revoked. If the student later qualifies again and
  claims, a new certificate with a new code is issued.

## Architecture

New context `internal/certificate/` with its own `certificate` schema.

```text
internal/certificate/
  domain/     Certificate, Policy, Mode, Code, errors
  app/        Service, ports, eligibility
  adapters/
    postgres/ repository, sqlc queries
    httpapi/  handlers, wire types
    events/   enrollment.enrollment.canceled consumer
```

### Domain

```go
type Mode string // off | completion | completion_and_exam

type Policy struct {
    CourseID id.ID
    Mode     Mode
    ExamID   id.ID // zero unless Mode is completion_and_exam
}

type Certificate struct {
    ID          id.ID
    Code        string
    UserID      id.ID
    CourseID    id.ID
    StudentName string
    CourseTitle string
    IssuedAt    time.Time
    RevokedAt   time.Time // zero while valid
}
```

- `NewPolicy` rejects an unknown mode, a missing `ExamID` under `completion_and_exam`, and an
  `ExamID` under any other mode.
- `Code` is generated from a cryptographic random source: 16 random bytes, base32 without padding,
  so it is URL-safe and unguessable.
- Revocation is a single SQL update on the valid certificate, so it is a no-op on one already
  revoked.

### App ports

Defined in `certificate/app`, wired in `cmd/api`:

| Port | Method | Backed by |
|---|---|---|
| `CourseManagement` | `CanManage(p, courseID)` | courseauthoring |
| `Enrollments` | `IsActivelyEnrolled(courseID, userID)` | enrollment |
| `Progress` | `IsComplete(courseID, userID)` | progress |
| `Exams` | `ExamInCourse(courseID, examID)`, `HasPassed(courseID, userID, examID)` | assessment |
| `Directory` | `StudentName(userID)`, `CourseTitle(courseID)` | identity, courseauthoring |

`progress` and `assessment` gain the small query methods those adapters need. They emit no events
and change no behavior.

### Service

- `SetPolicy(p, courseID, mode, examID)`: requires `CanManage`. For `completion_and_exam`, requires
  `Exams.ExamInCourse`. Upserts the policy.
- `GetPolicy(p, courseID)`: requires `CanManage`.
- `Claim(p, courseID)`:
  1. Load the policy. `off` or absent returns `ErrCertificatesDisabled`.
  2. Require `Enrollments.IsActivelyEnrolled`, otherwise `ErrNotEnrolled`.
  3. If the user already has a valid certificate for the course, return it with `created=false`.
  4. Require `Progress.IsComplete`, otherwise `ErrProgressIncomplete`.
  5. For `completion_and_exam`, require `Exams.ExamInCourse`, otherwise `ErrCertificatesDisabled`
     (the exam was deleted after the policy was set, so the policy can no longer be met), then
     `Exams.HasPassed`, otherwise `ErrExamNotPassed`.
  6. Snapshot names through `Directory`, generate a code, insert. A unique-violation on the
     (course, user) valid index returns the existing certificate, so concurrent claims converge.
- `ListMine(p)`, `GetMine(p, courseID)`: the caller's certificates.
- `Verify(code)`: public; returns the certificate or `ErrNotFound`.
- `RevokeForRefund(userID, courseID, refundedAt)`: used by the event consumer; idempotent. It
  revokes only a certificate issued at or before `refundedAt`, so a late or redelivered event
  never revokes a certificate the student earned again after the refund.

## HTTP

| Route | Auth | Result |
|---|---|---|
| `GET /courses/{id}/certificate-policy` | course manager | `{mode, exam_id?}` |
| `PUT /courses/{id}/certificate-policy` | course manager | `{mode, exam_id?}` |
| `POST /courses/{id}/certificate` | signed-in student | 201 created, 200 existing |
| `GET /courses/{id}/certificate` | signed-in student | the caller's valid certificate, 404 if none |
| `GET /me/certificates` | signed-in student | the caller's certificates, newest first |
| `GET /certificates/{code}` | none | `{code, student_name, course_title, issued_at, status}` |

- `status` is `valid` or `revoked`. The public response carries no user id, email or course id.
- `POST` failures are `409` problem details with a `reason` of `certificates_disabled`,
  `not_enrolled`, `progress_incomplete` or `exam_not_passed`.
- Invalid policy input (unknown mode, missing or foreign exam) uses the existing invalid-input
  problem mapping.
- All routes are added to `api/openapi.yaml`.

## Data

Migration under `migrations/`, schema `certificate`:

```sql
CREATE TABLE certificate.policies (
  course_id  bigint PRIMARY KEY,
  mode       text NOT NULL CHECK (mode IN ('off','completion','completion_and_exam')),
  exam_id    bigint,
  updated_at timestamptz NOT NULL,
  CHECK ((mode = 'completion_and_exam') = (exam_id IS NOT NULL))
);

CREATE TABLE certificate.certificates (
  id           bigint PRIMARY KEY,
  code         text NOT NULL UNIQUE,
  user_id      bigint NOT NULL,
  course_id    bigint NOT NULL,
  student_name text NOT NULL,
  course_title text NOT NULL,
  issued_at    timestamptz NOT NULL,
  revoked_at   timestamptz
);

CREATE UNIQUE INDEX certificates_one_valid
  ON certificate.certificates (course_id, user_id) WHERE revoked_at IS NULL;
CREATE INDEX certificates_user ON certificate.certificates (user_id, issued_at DESC);
```

No foreign keys cross schemas. sqlc config gains the new schema.

## Events

The `events` adapter subscribes to `enrollment.enrollment.canceled`. When `reason` is `refunded`
it calls `RevokeForRefund(user_id, course_id, occurred_at)`. Any other reason does nothing.
Handling is idempotent and bounded by `occurred_at`, so outbox redelivery is safe.

## Repository rules

- Add `certificate-domain` and `certificate-app` depguard rules in `.golangci.yml`.
- Add the context's import path to `platform-independent-of-contexts` and a deny rule in every
  other context.
- `cmd/api` registers the context, its adapters, and the port wiring.

## Testing

- **Domain:** policy validation (all mode and exam combinations), code format
  and uniqueness.
- **App, with fakes:** the eligibility matrix across the three modes (not enrolled, progress
  incomplete, exam not passed, success), existing certificate returned on repeat claim, reissue
  after revoke, revoke idempotence and its `refundedAt` bound, a policy exam removed from the
  course, policy validation including a foreign exam, authorization on policy routes.
- **HTTP:** status codes and problem reasons, the public verify body has no private fields, unknown
  code is 404.
- **Integration (Docker):** the valid-certificate unique index under concurrent claims, revocation
  sparing certificates issued after the refund, and a cmd/api e2e flow from policy to claim to
  verify to refund.
- Integration tests are reported as passing only if they actually ran.

## Risks

- `Progress.IsComplete` depends on how the progress context defines "complete" against the course's
  current lecture list. The plan must read that definition before wiring the adapter, and must not
  invent a second one.
- Because claiming is manual, a student who completes a course sees no certificate until they ask
  for one. The frontend should offer the claim action when the course is complete.
