# Certificate Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `certificate` bounded context: instructors set a per-course certificate policy, students claim a certificate on demand, anyone verifies a code publicly, and a refund that revokes access revokes the certificate.

**Architecture:** New hexagonal context `internal/certificate/{domain,app,adapters}` with its own `certificate` PostgreSQL schema. It never imports another context. Eligibility comes from ports that `cmd/api` backs with `progress`, `assessment`, `enrollment`, `courseauthoring` and `identity`. Revocation consumes `payment.purchase.refunded` from the outbox.

**Tech Stack:** Go 1.27, `net/http` router from `internal/platform/httpserver`, pgx/v5 + sqlc, goose migrations, watermill outbox handlers, testcontainers Postgres for integration tests.

**Spec:** `docs/superpowers/specs/2026-10-08-certificate-design.md`

## Global Constraints

- Module path `github.com/santoshkc2200/ioe-backend`. Conventional Commits.
- A context never imports another context. Only `cmd/api` wires them.
- `certificate/domain` imports stdlib and `platform/id` only. `certificate/app` imports stdlib, `certificate/domain`, `platform/{auth,clock,id}` only.
- A context reads and writes only its own schema (`certificate`). No foreign keys across schemas.
- IDs are `id.ID` (snowflake, JSON strings). Paths use the `/v1` prefix like every other route.
- Certificate `Code`: `crypto/rand.Text()`, 26 characters of RFC 4648 base32 (`A-Z2-7`).
- Mode values are exactly `off`, `completion`, `completion_and_exam`. A course with no policy row behaves as `off`.
- Public verify response contains `code`, `student_name`, `course_title`, `issued_at`, `status` and nothing else.
- Integration tests must not be reported as passing unless they actually ran (`make test-integration`, needs Docker).
- Gates before the branch is done: `make check`, `make test-integration`, `docker compose config`, `make docker-build`, `git diff --check`.

## Review Focus

Failure modes the spec implies but a straight reading of the tasks would not test. Each has a test in the task named in brackets.

1. A course with zero lectures must never count as complete, otherwise anyone enrolled in an empty course claims a certificate. [Task 1]
2. Two concurrent claims by the same student must produce one valid certificate and both calls must return it. [Task 4 integration, Task 5]
3. A refund event with `access_revoked: false` must not revoke. A refund event for a student with no certificate, or a redelivered event, must succeed silently. [Task 5, Task 7]
4. A malformed refund payload (bad JSON, zero or missing ids) must be logged and acknowledged, not returned as an error, or the outbox retries it forever. [Task 7]
5. `GET /v1/certificates/{code}` with a wrong-length, lowercase or odd-character code must return 404, never 500, and must not reach the database. [Task 3, Task 5, Task 6]

---

## File Structure

**Create**

- `migrations/00016_certificate.sql`: schema, tables, indexes.
- `internal/certificate/domain/certificate.go`: `Mode`, `Policy`, `Certificate`, `NewCode`, `ValidCode`.
- `internal/certificate/domain/errors.go`: domain errors.
- `internal/certificate/domain/certificate_test.go`
- `internal/certificate/app/ports.go`: `Repository`, `TxRunner`, `CourseManagement`, `Enrollments`, `Progress`, `Exams`, `Directory`.
- `internal/certificate/app/errors.go`
- `internal/certificate/app/service.go`: `Service` use cases.
- `internal/certificate/app/fakes_test.go`, `internal/certificate/app/service_test.go`
- `internal/certificate/adapters/postgres/{postgres.go,certificate.go,queries.sql}` and generated `sqlcgen/`
- `internal/certificate/adapters/postgres/postgres_integration_test.go`
- `internal/certificate/adapters/httpapi/{httpapi.go,wire.go,httpapi_test.go}`
- `internal/certificate/adapters/events/{events.go,events_test.go}`
- `cmd/api/certificate.go`: port adapters and `registerCertificate`.

**Modify**

- `internal/progress/domain/progress.go`, `internal/progress/app/service.go` (+ their tests): completion check.
- `internal/assessment/app/query.go`, `internal/assessment/app/quiz_service_test.go` or a new `query_test.go`: exam queries.
- `.golangci.yml`, `sqlc.yaml`: new context rules and sqlc target.
- `cmd/api/progress.go`, `cmd/api/app.go`: wiring.
- `cmd/api/e2e_integration_test.go`: end-to-end test.
- `api/openapi.yaml`: new paths and schemas.

---

### Task 1: Progress completion check

**Files:**
- Modify: `internal/progress/domain/progress.go`
- Modify: `internal/progress/app/service.go`
- Test: `internal/progress/domain/progress_test.go`, `internal/progress/app/service_test.go`

**Interfaces:**
- Produces: `func (p CourseProgress) CompletedAll(lectureIDs []id.ID) bool` and `func (s *Service) IsComplete(ctx context.Context, courseID, userID id.ID) (bool, error)`. Task 8 passes `*progressapp.Service` directly as certificate's `Progress` port.

- [ ] **Step 1: Write the failing domain test**

Append to `internal/progress/domain/progress_test.go` (package `domain_test`; the file already imports `testing`, `time` and `id`; add any import it lacks):

```go
func TestCompletedAll(t *testing.T) {
	now := time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC)
	done := func(lecture id.ID) domain.LectureProgress {
		return domain.LectureProgress{LectureID: lecture, State: domain.LectureStateCompleted, UpdatedAt: now}
	}
	doing := func(lecture id.ID) domain.LectureProgress {
		return domain.LectureProgress{LectureID: lecture, State: domain.LectureStateInProgress, UpdatedAt: now}
	}
	cases := []struct {
		name     string
		progress domain.CourseProgress
		lectures []id.ID
		want     bool
	}{
		{"all completed", domain.CourseProgress{Lectures: []domain.LectureProgress{done(50), done(51)}}, []id.ID{50, 51}, true},
		{"extra completed lecture is fine", domain.CourseProgress{Lectures: []domain.LectureProgress{done(50), done(51), done(52)}}, []id.ID{50, 51}, true},
		{"one in progress", domain.CourseProgress{Lectures: []domain.LectureProgress{done(50), doing(51)}}, []id.ID{50, 51}, false},
		{"one missing", domain.CourseProgress{Lectures: []domain.LectureProgress{done(50)}}, []id.ID{50, 51}, false},
		{"no lectures is never complete", domain.CourseProgress{Lectures: []domain.LectureProgress{done(50)}}, nil, false},
		{"empty progress", domain.CourseProgress{}, []id.ID{50}, false},
	}
	for _, c := range cases {
		if got := c.progress.CompletedAll(c.lectures); got != c.want {
			t.Errorf("%s: CompletedAll = %v, want %v", c.name, got, c.want)
		}
	}
}
```

- [ ] **Step 2: Run it to see it fail**

Run: `go test ./internal/progress/domain/ -run TestCompletedAll`
Expected: FAIL, `CompletedAll undefined`.

- [ ] **Step 3: Implement `CompletedAll`**

In `internal/progress/domain/progress.go` add `"slices"` to the imports and add after `CompletedLectureIDs`:

```go
// CompletedAll reports whether every lecture in lectureIDs is completed. A course without
// lectures is never complete.
func (p CourseProgress) CompletedAll(lectureIDs []id.ID) bool {
	if len(lectureIDs) == 0 {
		return false
	}
	done := p.CompletedLectureIDs()
	for _, l := range lectureIDs {
		if !slices.Contains(done, l) {
			return false
		}
	}
	return true
}
```

- [ ] **Step 4: Run it to see it pass**

Run: `go test ./internal/progress/domain/`
Expected: PASS.

- [ ] **Step 5: Write the failing app test**

Append to `internal/progress/app/service_test.go` (package `app_test`; fixture has course `10` with lectures 50 and 51, and `student` enrolled):

```go
func TestIsComplete(t *testing.T) {
	f := newFixture(t)
	complete := func(who id.ID) bool {
		t.Helper()
		ok, err := f.svc.IsComplete(ctx, course, who)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if complete(student.UserID) {
		t.Fatal("complete before any progress")
	}
	if err := f.svc.RecordLecture(ctx, student, course, 50, student.UserID, domain.LectureStateCompleted, 0); err != nil {
		t.Fatal(err)
	}
	if complete(student.UserID) {
		t.Fatal("complete with one of two lectures done")
	}
	if err := f.svc.RecordLecture(ctx, student, course, 51, student.UserID, domain.LectureStateCompleted, 0); err != nil {
		t.Fatal(err)
	}
	if !complete(student.UserID) {
		t.Fatal("not complete with both lectures done")
	}
	if complete(other.UserID) {
		t.Fatal("another user's progress counted")
	}
	if _, err := f.svc.IsComplete(ctx, missing, student.UserID); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("missing course error = %v", err)
	}
}
```

- [ ] **Step 6: Run it to see it fail**

Run: `go test ./internal/progress/app/ -run TestIsComplete`
Expected: FAIL, `f.svc.IsComplete undefined`.

- [ ] **Step 7: Implement `IsComplete`**

In `internal/progress/app/service.go` add after `CourseProgress`:

```go
// IsComplete reports whether userID completed every lecture of the course. It applies no
// authorization: callers decide who may ask. ErrNotFound when the course does not exist.
func (s *Service) IsComplete(ctx context.Context, courseID, userID id.ID) (bool, error) {
	c, err := s.courses.CourseFacts(ctx, courseID)
	if err != nil {
		return false, err
	}
	var (
		cp    domain.CourseProgress
		found bool
	)
	err = s.tx.RunInTx(ctx, func(r Repository) error {
		var err error
		cp, found, err = r.FindCourse(ctx, courseID, userID)
		return err
	})
	if err != nil || !found {
		return false, err
	}
	return cp.CompletedAll(c.LectureIDs), nil
}
```

- [ ] **Step 8: Run progress tests and commit**

Run: `go test -race ./internal/progress/...`
Expected: PASS.

```bash
git add internal/progress
git commit -m "feat(progress): report whether a user completed a course"
```

---

### Task 2: Assessment exam queries

**Files:**
- Modify: `internal/assessment/app/query.go`
- Create: `internal/assessment/app/query_test.go`

**Interfaces:**
- Produces: `func (q *AssessmentQuery) ExamInCourse(ctx context.Context, courseID, examID id.ID) (bool, error)` and `func (q *AssessmentQuery) HasPassed(ctx context.Context, courseID, userID, examID id.ID) (bool, error)`. Task 8 passes `*assessmentapp.AssessmentQuery` directly as certificate's `Exams` port.

- [ ] **Step 1: Write the failing test**

Create `internal/assessment/app/query_test.go` (package `app_test`; `newFixture`, `course`, `student`, `examInput`, `f.createExam` and `f.query` already exist in this package's tests):

```go
package app_test

import (
	"testing"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/assessment/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

func TestExamInCourse(t *testing.T) {
	f := newFixture(t)
	e := f.createExam(t, examInput())
	for _, c := range []struct {
		name         string
		courseID     id.ID
		examID       id.ID
		want         bool
	}{
		{"exam in its course", course, e.ID, true},
		{"exam in another course", course + 1, e.ID, false},
		{"unknown exam", course, e.ID + 999, false},
	} {
		got, err := f.query.ExamInCourse(ctx, c.courseID, c.examID)
		if err != nil || got != c.want {
			t.Errorf("%s: got %v, %v; want %v", c.name, got, err, c.want)
		}
	}
}

func TestHasPassed(t *testing.T) {
	f := newFixture(t)
	e := f.createExam(t, examInput())
	submitted := time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC)
	yes, no := true, false
	put := func(attemptID id.ID, userID id.ID, passed *bool) {
		f.store.examAttempts[attemptID] = domain.ExamAttempt{
			ID: attemptID, ExamID: e.ID, CourseID: course, UserID: userID,
			StartedAt: submitted, SubmittedAt: &submitted, Passed: passed,
		}
	}
	has := func(userID id.ID) bool {
		t.Helper()
		got, err := f.query.HasPassed(ctx, course, userID, e.ID)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if has(student.UserID) {
		t.Fatal("passed with no attempts")
	}
	put(900, student.UserID, &no)
	if has(student.UserID) {
		t.Fatal("passed with only a failed attempt")
	}
	open := domain.ExamAttempt{ID: 901, ExamID: e.ID, CourseID: course, UserID: student.UserID, StartedAt: submitted}
	f.store.examAttempts[901] = open
	if has(student.UserID) {
		t.Fatal("passed with an open attempt")
	}
	put(902, student.UserID, &yes)
	if !has(student.UserID) {
		t.Fatal("not passed after a passing attempt")
	}
	if has(student.UserID + 1) {
		t.Fatal("another user's attempt counted")
	}
	if got, err := f.query.HasPassed(ctx, course, student.UserID, e.ID+999); err != nil || got {
		t.Fatalf("other exam: %v, %v", got, err)
	}
}
```

- [ ] **Step 2: Run it to see it fail**

Run: `go test ./internal/assessment/app/ -run 'TestExamInCourse|TestHasPassed'`
Expected: FAIL, `ExamInCourse undefined`.

- [ ] **Step 3: Implement the queries**

Append to `internal/assessment/app/query.go`:

```go
// ExamInCourse reports whether the exam exists, is not deleted and belongs to courseID.
func (q *AssessmentQuery) ExamInCourse(ctx context.Context, courseID, examID id.ID) (bool, error) {
	var ok bool
	err := q.tx.RunInTx(ctx, func(r Repos) error {
		e, err := r.Exams.Find(ctx, examID, LockNone)
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		ok = e.CourseID == courseID
		return nil
	})
	return ok, err
}

// HasPassed reports whether userID has a submitted, passing attempt on the exam. Attempts
// are graded against the revision they were taken on, so later edits never change the answer.
func (q *AssessmentQuery) HasPassed(ctx context.Context, courseID, userID, examID id.ID) (bool, error) {
	var passed bool
	err := q.tx.RunInTx(ctx, func(r Repos) error {
		attempts, err := r.Exams.ListUserAttempts(ctx, courseID, userID)
		if err != nil {
			return err
		}
		for _, a := range attempts {
			if a.ExamID == examID && a.SubmittedAt != nil && a.Passed != nil && *a.Passed {
				passed = true
				return nil
			}
		}
		return nil
	})
	return passed, err
}
```

Add `"errors"` to the imports of `query.go`.

- [ ] **Step 4: Run assessment tests and commit**

Run: `gofmt -l internal/assessment && go test -race ./internal/assessment/...`
Expected: no gofmt output (fix alignment in the test's struct literal if listed), tests PASS.

```bash
git add internal/assessment
git commit -m "feat(assessment): answer whether a user passed an exam"
```

---

### Task 3: Certificate domain, lint rules, sqlc target

**Files:**
- Create: `internal/certificate/domain/certificate.go`, `internal/certificate/domain/errors.go`
- Test: `internal/certificate/domain/certificate_test.go`
- Modify: `.golangci.yml`, `sqlc.yaml`

**Interfaces:**
- Produces (used by every later task):

```go
package domain

type Mode string
const (ModeOff Mode = "off"; ModeCompletion Mode = "completion"; ModeCompletionAndExam Mode = "completion_and_exam")
func (m Mode) Valid() bool

type Policy struct{ CourseID id.ID; Mode Mode; ExamID id.ID }
func NewPolicy(courseID id.ID, mode Mode, examID id.ID) (Policy, error)

type Certificate struct {
	ID id.ID; Code string; UserID id.ID; CourseID id.ID
	StudentName, CourseTitle string
	IssuedAt, RevokedAt time.Time // RevokedAt zero while valid
}
func NewCertificate(certID id.ID, code string, userID, courseID id.ID, studentName, courseTitle string, now time.Time) Certificate
func (c Certificate) Revoked() bool

const CodeLen = 26
func NewCode() string
func ValidCode(s string) bool

var ErrInvalidMode, ErrExamRequired, ErrExamNotAllowed, ErrExamNotInCourse error
```

- [ ] **Step 1: Write the failing tests**

Create `internal/certificate/domain/certificate_test.go`:

```go
package domain_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/certificate/domain"
)

func TestNewPolicy(t *testing.T) {
	cases := []struct {
		name string
		mode domain.Mode
		exam int64
		want error
	}{
		{"off", domain.ModeOff, 0, nil},
		{"completion", domain.ModeCompletion, 0, nil},
		{"completion and exam", domain.ModeCompletionAndExam, 700, nil},
		{"unknown mode", domain.Mode("always"), 0, domain.ErrInvalidMode},
		{"empty mode", domain.Mode(""), 0, domain.ErrInvalidMode},
		{"exam mode without exam", domain.ModeCompletionAndExam, 0, domain.ErrExamRequired},
		{"completion with exam", domain.ModeCompletion, 700, domain.ErrExamNotAllowed},
		{"off with exam", domain.ModeOff, 700, domain.ErrExamNotAllowed},
	}
	for _, c := range cases {
		p, err := domain.NewPolicy(10, c.mode, idOf(c.exam))
		if !errors.Is(err, c.want) {
			t.Errorf("%s: error = %v, want %v", c.name, err, c.want)
			continue
		}
		if err == nil && (p.CourseID != 10 || p.Mode != c.mode || p.ExamID != idOf(c.exam)) {
			t.Errorf("%s: policy = %+v", c.name, p)
		}
	}
}

func TestCodes(t *testing.T) {
	a, b := domain.NewCode(), domain.NewCode()
	if a == b {
		t.Fatal("two codes are equal")
	}
	if len(a) != domain.CodeLen || !domain.ValidCode(a) {
		t.Fatalf("generated code %q is not valid", a)
	}
	for _, bad := range []string{"", a[:25], a + "A", strings.ToLower(a), a[:25] + "1", a[:25] + "=", a[:25] + " ", "../../etc/passwd"} {
		if domain.ValidCode(bad) {
			t.Errorf("ValidCode(%q) = true", bad)
		}
	}
}

func TestNewCertificateIsValidUntilRevoked(t *testing.T) {
	now := time.Date(2026, 10, 8, 9, 0, 0, 0, time.FixedZone("NPT", 5*3600+45*60))
	c := domain.NewCertificate(1, domain.NewCode(), 200, 10, "Asha Rai", "Go", now)
	if c.Revoked() || !c.RevokedAt.IsZero() {
		t.Fatalf("new certificate is revoked: %+v", c)
	}
	if !c.IssuedAt.Equal(now) || c.IssuedAt.Location() != time.UTC {
		t.Fatalf("IssuedAt = %v, want %v in UTC", c.IssuedAt, now)
	}
	c.RevokedAt = now
	if !c.Revoked() {
		t.Fatal("certificate with RevokedAt is not revoked")
	}
}
```

Add a small helper in the same file:

```go
func idOf(n int64) id.ID { return id.ID(n) }
```

and add `"github.com/santoshkc2200/ioe-backend/internal/platform/id"` to the imports.

- [ ] **Step 2: Run it to see it fail**

Run: `go test ./internal/certificate/domain/`
Expected: FAIL, package has no non-test files / undefined symbols.

- [ ] **Step 3: Implement the domain**

Create `internal/certificate/domain/errors.go`:

```go
// Package domain holds the certificate model.
package domain

import "errors"

var (
	ErrInvalidMode    = errors.New("mode must be off, completion or completion_and_exam")
	ErrExamRequired   = errors.New("completion_and_exam requires exam_id")
	ErrExamNotAllowed = errors.New("exam_id is only allowed with completion_and_exam")
	ErrExamNotInCourse = errors.New("exam does not belong to the course")
)
```

Create `internal/certificate/domain/certificate.go`:

```go
package domain

import (
	"crypto/rand"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

// Mode is what earns a student a certificate in a course.
type Mode string

const (
	ModeOff               Mode = "off"
	ModeCompletion        Mode = "completion"
	ModeCompletionAndExam Mode = "completion_and_exam"
)

func (m Mode) Valid() bool {
	return m == ModeOff || m == ModeCompletion || m == ModeCompletionAndExam
}

// Policy is one course's certificate rule. ExamID is the zero ID unless Mode is
// ModeCompletionAndExam.
type Policy struct {
	CourseID id.ID
	Mode     Mode
	ExamID   id.ID
}

func NewPolicy(courseID id.ID, mode Mode, examID id.ID) (Policy, error) {
	if !mode.Valid() {
		return Policy{}, ErrInvalidMode
	}
	if mode == ModeCompletionAndExam && examID.IsZero() {
		return Policy{}, ErrExamRequired
	}
	if mode != ModeCompletionAndExam && !examID.IsZero() {
		return Policy{}, ErrExamNotAllowed
	}
	return Policy{CourseID: courseID, Mode: mode, ExamID: examID}, nil
}

// Certificate is one user's certificate for one course. StudentName and CourseTitle are
// snapshots taken at issue time. RevokedAt is the zero time while the certificate is valid.
type Certificate struct {
	ID          id.ID
	Code        string
	UserID      id.ID
	CourseID    id.ID
	StudentName string
	CourseTitle string
	IssuedAt    time.Time
	RevokedAt   time.Time
}

func NewCertificate(certID id.ID, code string, userID, courseID id.ID, studentName, courseTitle string, now time.Time) Certificate {
	return Certificate{
		ID: certID, Code: code, UserID: userID, CourseID: courseID,
		StudentName: studentName, CourseTitle: courseTitle, IssuedAt: now.UTC(),
	}
}

func (c Certificate) Revoked() bool { return !c.RevokedAt.IsZero() }

// CodeLen is the length of a certificate code: 26 base32 characters, 130 bits.
const CodeLen = 26

// NewCode returns an unguessable URL-safe code.
func NewCode() string { return rand.Text() }

// ValidCode reports whether s has the shape NewCode produces. Callers use it to reject
// malformed lookups before touching storage.
func ValidCode(s string) bool {
	if len(s) != CodeLen {
		return false
	}
	for i := range len(s) {
		c := s[i]
		if (c < 'A' || c > 'Z') && (c < '2' || c > '7') {
			return false
		}
	}
	return true
}
```

- [ ] **Step 4: Run it to see it pass**

Run: `gofmt -l internal/certificate; go test -race ./internal/certificate/domain/`
Expected: no gofmt output (run `gofmt -w` on any listed file, the `errors.go` var block needs alignment), tests PASS.

- [ ] **Step 5: Add lint rules**

Insert a `certificate` deny entry after every existing `internal/blog` deny entry, copying that entry's own `desc` line:

```bash
awk '
/- pkg: github.com\/santoshkc2200\/ioe-backend\/internal\/blog$/ {
  print; getline desc; print desc
  print "            - pkg: github.com/santoshkc2200/ioe-backend/internal/certificate"
  print desc; next
}
{ print }' .golangci.yml > .golangci.yml.new && mv .golangci.yml.new .golangci.yml
grep -c 'internal/certificate$' .golangci.yml
```

Expected: `9` (platform plus the identity, notification, courseauthoring, enrollment, progress, media, assessment and payment independence rules).

Then replace the end of the `blog-independent` rule and add the three certificate rules. Use Edit on this unique anchor (only `blog-independent` is followed directly by `exclusions:`):

old:

```yaml
            - pkg: github.com/santoshkc2200/ioe-backend/internal/payment
              desc: bounded contexts must not import each other
  exclusions:
```

new:

```yaml
            - pkg: github.com/santoshkc2200/ioe-backend/internal/payment
              desc: bounded contexts must not import each other
            - pkg: github.com/santoshkc2200/ioe-backend/internal/certificate
              desc: bounded contexts must not import each other
        certificate-domain:
          list-mode: strict
          files:
            - "**/internal/certificate/domain/**"
            - "!$test"
          allow:
            - $gostd
            - github.com/santoshkc2200/ioe-backend/internal/platform/id
        certificate-app:
          list-mode: strict
          files:
            - "**/internal/certificate/app/**"
            - "!$test"
          allow:
            - $gostd
            - github.com/santoshkc2200/ioe-backend/internal/certificate/domain
            - github.com/santoshkc2200/ioe-backend/internal/platform/auth
            - github.com/santoshkc2200/ioe-backend/internal/platform/clock
            - github.com/santoshkc2200/ioe-backend/internal/platform/id
        certificate-independent:
          list-mode: lax
          files:
            - "**/internal/certificate/**"
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
            - pkg: github.com/santoshkc2200/ioe-backend/internal/payment
              desc: bounded contexts must not import each other
            - pkg: github.com/santoshkc2200/ioe-backend/internal/blog
              desc: bounded contexts must not import each other
  exclusions:
```

- [ ] **Step 6: Add the sqlc target**

In `sqlc.yaml`, append after the last `sql:` entry (copy the shape of the `progress` entry, same indentation):

```yaml
  - engine: postgresql
    schema: migrations
    queries: internal/certificate/adapters/postgres/queries.sql
    gen:
      go:
        package: sqlcgen
        out: internal/certificate/adapters/postgres/sqlcgen
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

Do not run `make sqlc` yet; the queries file arrives in Task 4.

- [ ] **Step 7: Lint and commit**

Run: `golangci-lint run ./internal/certificate/... ./internal/progress/... ./internal/assessment/...`
Expected: no issues.

```bash
git add internal/certificate .golangci.yml sqlc.yaml
git commit -m "feat(certificate): add domain model and architecture rules"
```

---

### Task 4: Migration and Postgres adapter

**Files:**
- Create: `migrations/00016_certificate.sql`
- Create: `internal/certificate/adapters/postgres/{postgres.go,certificate.go,queries.sql}` and generated `sqlcgen/`
- Create: `internal/certificate/app/ports.go` (the `Repository` and `TxRunner` part; Task 5 adds the rest)
- Test: `internal/certificate/adapters/postgres/postgres_integration_test.go`

**Interfaces:**
- Consumes: Task 3 domain types.
- Produces: in package `app` (file `ports.go`):

```go
type Repository interface {
	FindPolicy(ctx context.Context, courseID id.ID) (domain.Policy, bool, error)
	UpsertPolicy(ctx context.Context, p domain.Policy, now time.Time) error
	// FindValid returns the user's unrevoked certificate for the course.
	FindValid(ctx context.Context, courseID, userID id.ID) (domain.Certificate, bool, error)
	// Insert returns false, and stores nothing, when the user already holds a valid certificate for the course.
	Insert(ctx context.Context, c domain.Certificate) (bool, error)
	FindByCode(ctx context.Context, code string) (domain.Certificate, bool, error)
	// ListByUser returns every certificate of the user, newest first.
	ListByUser(ctx context.Context, userID id.ID) ([]domain.Certificate, error)
	// RevokeValid revokes the user's valid certificate for the course; a no-op when there is none.
	RevokeValid(ctx context.Context, courseID, userID id.ID, now time.Time) error
}

type TxRunner interface {
	RunInTx(ctx context.Context, fn func(Repository) error) error
}
```

and `postgres.NewTxRunner(pool *pgxpool.Pool) *TxRunner` implementing `app.TxRunner`.

- [ ] **Step 1: Write the migration**

Create `migrations/00016_certificate.sql`:

```sql
-- +goose Up
CREATE SCHEMA certificate;

CREATE TABLE certificate.policies (
  course_id  bigint PRIMARY KEY,
  mode       text NOT NULL CHECK (mode IN ('off', 'completion', 'completion_and_exam')),
  exam_id    bigint,
  updated_at timestamptz NOT NULL,
  CONSTRAINT policies_exam_matches_mode CHECK ((mode = 'completion_and_exam') = (exam_id IS NOT NULL))
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
CREATE INDEX certificates_user_issued ON certificate.certificates (user_id, issued_at DESC, id DESC);

-- +goose Down
DROP SCHEMA certificate CASCADE;
```

- [ ] **Step 2: Write the queries**

Create `internal/certificate/adapters/postgres/queries.sql`:

```sql
-- name: UpsertPolicy :exec
INSERT INTO certificate.policies (course_id, mode, exam_id, updated_at)
VALUES ($1, $2, $3, $4)
ON CONFLICT (course_id) DO UPDATE SET
  mode = EXCLUDED.mode,
  exam_id = EXCLUDED.exam_id,
  updated_at = EXCLUDED.updated_at;

-- name: GetPolicy :one
SELECT * FROM certificate.policies WHERE course_id = $1;

-- name: InsertCertificate :one
-- Returns no row when the user already holds a valid certificate for the course.
INSERT INTO certificate.certificates (id, code, user_id, course_id, student_name, course_title, issued_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT DO NOTHING
RETURNING id;

-- name: GetValidCertificate :one
SELECT * FROM certificate.certificates
WHERE course_id = $1 AND user_id = $2 AND revoked_at IS NULL;

-- name: GetCertificateByCode :one
SELECT * FROM certificate.certificates WHERE code = $1;

-- name: ListCertificatesByUser :many
SELECT * FROM certificate.certificates
WHERE user_id = $1
ORDER BY issued_at DESC, id DESC;

-- name: RevokeValidCertificate :exec
UPDATE certificate.certificates SET revoked_at = $3
WHERE course_id = $1 AND user_id = $2 AND revoked_at IS NULL;
```

- [ ] **Step 3: Generate sqlc code**

Run: `make sqlc && ls internal/certificate/adapters/postgres/sqlcgen`
Expected: `db.go models.go queries.sql.go`. Open `models.go` and note the generated names. Expected: `CertificateCertificate` (fields `ID, Code, UserID, CourseID int64`, `StudentName, CourseTitle string`, `IssuedAt time.Time`, `RevokedAt *time.Time`) and `CertificatePolicy` (`CourseID int64`, `Mode string`, `ExamID pgtype.Int8`, `UpdatedAt time.Time`). If a name differs, use the generated one in Step 5.

- [ ] **Step 4: Write the repository port**

Create `internal/certificate/app/ports.go` with the `Repository` and `TxRunner` shown under Interfaces (package `app`, imports `context`, `time`, `certificate/domain`, `platform/id`). Task 5 appends the remaining ports to this file.

```go
// Package app contains the certificate use cases and the ports they depend on.
package app

import (
	"context"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/certificate/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

// Repository reads and writes certificates and policies in the current transaction.
type Repository interface {
	FindPolicy(ctx context.Context, courseID id.ID) (domain.Policy, bool, error)
	UpsertPolicy(ctx context.Context, p domain.Policy, now time.Time) error
	// FindValid returns the user's unrevoked certificate for the course.
	FindValid(ctx context.Context, courseID, userID id.ID) (domain.Certificate, bool, error)
	// Insert returns false, and stores nothing, when the user already holds a valid
	// certificate for the course.
	Insert(ctx context.Context, c domain.Certificate) (bool, error)
	FindByCode(ctx context.Context, code string) (domain.Certificate, bool, error)
	// ListByUser returns every certificate of the user, newest first.
	ListByUser(ctx context.Context, userID id.ID) ([]domain.Certificate, error)
	// RevokeValid revokes the user's valid certificate for the course; a no-op when there is none.
	RevokeValid(ctx context.Context, courseID, userID id.ID, now time.Time) error
}

// TxRunner commits when fn returns nil and rolls back otherwise.
type TxRunner interface {
	RunInTx(ctx context.Context, fn func(Repository) error) error
}
```

- [ ] **Step 5: Write the adapter**

Create `internal/certificate/adapters/postgres/postgres.go`:

```go
// Package postgres implements certificate persistence on the certificate schema.
package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/santoshkc2200/ioe-backend/internal/certificate/adapters/postgres/sqlcgen"
	"github.com/santoshkc2200/ioe-backend/internal/certificate/app"
)

// TxRunner runs certificate use cases in one PostgreSQL transaction.
type TxRunner struct{ pool *pgxpool.Pool }

func NewTxRunner(pool *pgxpool.Pool) *TxRunner { return &TxRunner{pool: pool} }

func (r *TxRunner) RunInTx(ctx context.Context, fn func(app.Repository) error) error {
	return pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		return fn(repo{q: sqlcgen.New(tx)})
	})
}
```

Create `internal/certificate/adapters/postgres/certificate.go`:

```go
package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/santoshkc2200/ioe-backend/internal/certificate/adapters/postgres/sqlcgen"
	"github.com/santoshkc2200/ioe-backend/internal/certificate/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

type repo struct{ q *sqlcgen.Queries }

func (r repo) FindPolicy(ctx context.Context, courseID id.ID) (domain.Policy, bool, error) {
	row, err := r.q.GetPolicy(ctx, int64(courseID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Policy{}, false, nil
	}
	if err != nil {
		return domain.Policy{}, false, err
	}
	p := domain.Policy{CourseID: id.ID(row.CourseID), Mode: domain.Mode(row.Mode)}
	if row.ExamID.Valid {
		p.ExamID = id.ID(row.ExamID.Int64)
	}
	return p, true, nil
}

func (r repo) UpsertPolicy(ctx context.Context, p domain.Policy, now time.Time) error {
	return r.q.UpsertPolicy(ctx, sqlcgen.UpsertPolicyParams{
		CourseID:  int64(p.CourseID),
		Mode:      string(p.Mode),
		ExamID:    pgtype.Int8{Int64: int64(p.ExamID), Valid: !p.ExamID.IsZero()},
		UpdatedAt: now.UTC(),
	})
}

func (r repo) FindValid(ctx context.Context, courseID, userID id.ID) (domain.Certificate, bool, error) {
	row, err := r.q.GetValidCertificate(ctx, sqlcgen.GetValidCertificateParams{CourseID: int64(courseID), UserID: int64(userID)})
	return one(row, err)
}

func (r repo) Insert(ctx context.Context, c domain.Certificate) (bool, error) {
	_, err := r.q.InsertCertificate(ctx, sqlcgen.InsertCertificateParams{
		ID: int64(c.ID), Code: c.Code, UserID: int64(c.UserID), CourseID: int64(c.CourseID),
		StudentName: c.StudentName, CourseTitle: c.CourseTitle, IssuedAt: c.IssuedAt.UTC(),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (r repo) FindByCode(ctx context.Context, code string) (domain.Certificate, bool, error) {
	row, err := r.q.GetCertificateByCode(ctx, code)
	return one(row, err)
}

func (r repo) ListByUser(ctx context.Context, userID id.ID) ([]domain.Certificate, error) {
	rows, err := r.q.ListCertificatesByUser(ctx, int64(userID))
	if err != nil {
		return nil, err
	}
	out := make([]domain.Certificate, len(rows))
	for i, row := range rows {
		out[i] = toCertificate(row)
	}
	return out, nil
}

func (r repo) RevokeValid(ctx context.Context, courseID, userID id.ID, now time.Time) error {
	return r.q.RevokeValidCertificate(ctx, sqlcgen.RevokeValidCertificateParams{
		CourseID: int64(courseID), UserID: int64(userID), RevokedAt: &now,
	})
}

func one(row sqlcgen.CertificateCertificate, err error) (domain.Certificate, bool, error) {
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Certificate{}, false, nil
	}
	if err != nil {
		return domain.Certificate{}, false, err
	}
	return toCertificate(row), true, nil
}

func toCertificate(row sqlcgen.CertificateCertificate) domain.Certificate {
	c := domain.Certificate{
		ID: id.ID(row.ID), Code: row.Code, UserID: id.ID(row.UserID), CourseID: id.ID(row.CourseID),
		StudentName: row.StudentName, CourseTitle: row.CourseTitle, IssuedAt: row.IssuedAt.UTC(),
	}
	if row.RevokedAt != nil {
		c.RevokedAt = row.RevokedAt.UTC()
	}
	return c
}
```

`RevokeValid` passes `&now` where `now` is the function parameter; convert with `now = now.UTC()` first if `gofmt`/lint ask. The `RevokeValidCertificateParams` field names follow the query's `$1,$2,$3` order (`CourseID`, `UserID`, `RevokedAt`); confirm against the generated file.

- [ ] **Step 6: Write the integration test**

Create `internal/certificate/adapters/postgres/postgres_integration_test.go`:

```go
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
		if err := tx.RunInTx(ctx, func(r app.Repository) error { return r.RevokeValid(ctx, 10, 200, revokedAt) }); err != nil {
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
```

- [ ] **Step 7: Run integration tests**

Run: `go test -race -tags integration ./internal/certificate/adapters/postgres/ -v`
Expected: all four tests PASS. If Docker is not running, stop and say so; do not report this as passing.

- [ ] **Step 8: Check generated code and commit**

Run: `make sqlc-check && go vet ./internal/certificate/... && golangci-lint run ./internal/certificate/...`
Expected: no diff, no issues.

```bash
git add migrations internal/certificate sqlc.yaml
git commit -m "feat(certificate): persist policies and certificates"
```

---

### Task 5: Certificate use cases

**Files:**
- Modify: `internal/certificate/app/ports.go`
- Create: `internal/certificate/app/errors.go`, `internal/certificate/app/service.go`
- Test: `internal/certificate/app/fakes_test.go`, `internal/certificate/app/service_test.go`

**Interfaces:**
- Consumes: Task 3 domain, Task 4 `Repository` and `TxRunner`.
- Produces:

```go
// ports added to ports.go
type CourseManagement interface {
	// CanManage returns nil when p manages the course; otherwise ErrNotFound or ErrForbidden.
	CanManage(ctx context.Context, p auth.Principal, courseID id.ID) error
}
type Enrollments interface {
	IsActivelyEnrolled(ctx context.Context, courseID, userID id.ID) (bool, error)
}
type Progress interface {
	IsComplete(ctx context.Context, courseID, userID id.ID) (bool, error)
}
type Exams interface {
	ExamInCourse(ctx context.Context, courseID, examID id.ID) (bool, error)
	HasPassed(ctx context.Context, courseID, userID, examID id.ID) (bool, error)
}
type Directory interface {
	StudentName(ctx context.Context, userID id.ID) (string, error)  // ErrNotFound when the user is unknown
	CourseTitle(ctx context.Context, courseID id.ID) (string, error) // ErrNotFound when the course is unknown
}

// errors.go
var ErrNotFound, ErrForbidden, ErrInvalidInput, ErrCertificatesDisabled,
	ErrNotEnrolled, ErrProgressIncomplete, ErrExamNotPassed error

// service.go
func NewService(tx TxRunner, courses CourseManagement, enrollments Enrollments, progress Progress,
	exams Exams, directory Directory, ids *id.Generator, c clock.Clock) *Service
func (s *Service) GetPolicy(ctx context.Context, p auth.Principal, courseID id.ID) (domain.Policy, error)
func (s *Service) SetPolicy(ctx context.Context, p auth.Principal, courseID id.ID, mode domain.Mode, examID id.ID) (domain.Policy, error)
func (s *Service) Claim(ctx context.Context, p auth.Principal, courseID id.ID) (cert domain.Certificate, created bool, err error)
func (s *Service) GetMine(ctx context.Context, p auth.Principal, courseID id.ID) (domain.Certificate, error)
func (s *Service) ListMine(ctx context.Context, p auth.Principal) ([]domain.Certificate, error)
func (s *Service) Verify(ctx context.Context, code string) (domain.Certificate, error)
func (s *Service) RevokeForRefund(ctx context.Context, userID, courseID id.ID) error
```

- [ ] **Step 1: Write the fakes**

Create `internal/certificate/app/fakes_test.go`:

```go
package app_test

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/certificate/app"
	"github.com/santoshkc2200/ioe-backend/internal/certificate/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

type fixedClock struct{ now time.Time }

func (c *fixedClock) Now() time.Time { return c.now }

// memStore mimics the postgres repository, including the one-valid-certificate rule.
type memStore struct {
	mu       sync.Mutex
	policies map[id.ID]domain.Policy
	certs    []domain.Certificate
}

func newMemStore() *memStore { return &memStore{policies: map[id.ID]domain.Policy{}} }

func (m *memStore) RunInTx(_ context.Context, fn func(app.Repository) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return fn(m)
}

func (m *memStore) FindPolicy(_ context.Context, courseID id.ID) (domain.Policy, bool, error) {
	p, ok := m.policies[courseID]
	return p, ok, nil
}

func (m *memStore) UpsertPolicy(_ context.Context, p domain.Policy, _ time.Time) error {
	m.policies[p.CourseID] = p
	return nil
}

func (m *memStore) FindValid(_ context.Context, courseID, userID id.ID) (domain.Certificate, bool, error) {
	for _, c := range m.certs {
		if c.CourseID == courseID && c.UserID == userID && !c.Revoked() {
			return c, true, nil
		}
	}
	return domain.Certificate{}, false, nil
}

func (m *memStore) Insert(ctx context.Context, c domain.Certificate) (bool, error) {
	if _, ok, _ := m.FindValid(ctx, c.CourseID, c.UserID); ok {
		return false, nil
	}
	m.certs = append(m.certs, c)
	return true, nil
}

func (m *memStore) FindByCode(_ context.Context, code string) (domain.Certificate, bool, error) {
	for _, c := range m.certs {
		if c.Code == code {
			return c, true, nil
		}
	}
	return domain.Certificate{}, false, nil
}

func (m *memStore) ListByUser(_ context.Context, userID id.ID) ([]domain.Certificate, error) {
	var out []domain.Certificate
	for _, c := range m.certs {
		if c.UserID == userID {
			out = append(out, c)
		}
	}
	slices.Reverse(out)
	return out, nil
}

func (m *memStore) RevokeValid(_ context.Context, courseID, userID id.ID, now time.Time) error {
	for i, c := range m.certs {
		if c.CourseID == courseID && c.UserID == userID && !c.Revoked() {
			m.certs[i].RevokedAt = now
		}
	}
	return nil
}

// manager allows owner to manage the one course it knows.
type manager struct {
	course id.ID
	owner  id.ID
}

func (m manager) CanManage(_ context.Context, p auth.Principal, courseID id.ID) error {
	switch {
	case courseID != m.course:
		return app.ErrNotFound
	case p.UserID != m.owner:
		return app.ErrForbidden
	}
	return nil
}

type pair [2]id.ID

type enrollments map[pair]bool

func (e enrollments) IsActivelyEnrolled(_ context.Context, courseID, userID id.ID) (bool, error) {
	return e[pair{courseID, userID}], nil
}

// progress reports (course, user) pairs as complete.
type progress map[pair]bool

func (p progress) IsComplete(_ context.Context, courseID, userID id.ID) (bool, error) {
	return p[pair{courseID, userID}], nil
}

// exams knows which exams belong to which course and who passed which exam.
type exams struct {
	inCourse map[pair]bool // {course, exam}
	passed   map[[3]id.ID]bool
}

func (e exams) ExamInCourse(_ context.Context, courseID, examID id.ID) (bool, error) {
	return e.inCourse[pair{courseID, examID}], nil
}

func (e exams) HasPassed(_ context.Context, courseID, userID, examID id.ID) (bool, error) {
	return e.passed[[3]id.ID{courseID, userID, examID}], nil
}

type directory struct{}

func (directory) StudentName(_ context.Context, userID id.ID) (string, error) {
	if userID == unknownUser {
		return "", app.ErrNotFound
	}
	return "Asha Rai", nil
}

func (directory) CourseTitle(context.Context, id.ID) (string, error) { return "Go", nil }
```

- [ ] **Step 2: Write the failing tests**

Create `internal/certificate/app/service_test.go`:

```go
package app_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/certificate/app"
	"github.com/santoshkc2200/ioe-backend/internal/certificate/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

var (
	ctx     = context.Background()
	t0      = time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC)
	owner   = auth.Principal{UserID: 100, Role: auth.RoleInstructor}
	student = auth.Principal{UserID: 200, Role: auth.RoleStudent}
	other   = auth.Principal{UserID: 300, Role: auth.RoleStudent}
)

const (
	course      id.ID = 10
	exam        id.ID = 700
	foreignExam id.ID = 800 // belongs to another course
	unknownUser id.ID = 999
)

type fixture struct {
	svc      *app.Service
	store    *memStore
	enrolled enrollments
	done     progress
	exams    exams
	clock    *fixedClock
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	gen, err := id.NewGenerator(1)
	if err != nil {
		t.Fatal(err)
	}
	f := fixture{
		store:    newMemStore(),
		enrolled: enrollments{{course, student.UserID}: true},
		done:     progress{},
		exams:    exams{inCourse: map[pair]bool{{course, exam}: true}, passed: map[[3]id.ID]bool{}},
		clock:    &fixedClock{now: t0},
	}
	f.svc = app.NewService(f.store, manager{course: course, owner: owner.UserID}, f.enrolled, f.done, f.exams, directory{}, gen, f.clock)
	return f
}

func (f fixture) setPolicy(t *testing.T, mode domain.Mode, examID id.ID) {
	t.Helper()
	if _, err := f.svc.SetPolicy(ctx, owner, course, mode, examID); err != nil {
		t.Fatal(err)
	}
}

func TestSetPolicy(t *testing.T) {
	f := newFixture(t)
	cases := []struct {
		name     string
		who      auth.Principal
		courseID id.ID
		mode     domain.Mode
		exam     id.ID
		want     error
	}{
		{"owner sets completion", owner, course, domain.ModeCompletion, 0, nil},
		{"owner sets exam mode", owner, course, domain.ModeCompletionAndExam, exam, nil},
		{"owner turns it off", owner, course, domain.ModeOff, 0, nil},
		{"student cannot manage", student, course, domain.ModeCompletion, 0, app.ErrForbidden},
		{"unknown course", owner, 11, domain.ModeCompletion, 0, app.ErrNotFound},
		{"unknown mode", owner, course, domain.Mode("always"), 0, app.ErrInvalidInput},
		{"exam mode without exam", owner, course, domain.ModeCompletionAndExam, 0, app.ErrInvalidInput},
		{"exam from another course", owner, course, domain.ModeCompletionAndExam, foreignExam, app.ErrInvalidInput},
		{"exam on completion mode", owner, course, domain.ModeCompletion, exam, app.ErrInvalidInput},
	}
	for _, c := range cases {
		p, err := f.svc.SetPolicy(ctx, c.who, c.courseID, c.mode, c.exam)
		if !errors.Is(err, c.want) {
			t.Errorf("%s: error = %v, want %v", c.name, err, c.want)
			continue
		}
		if err == nil && (p.Mode != c.mode || p.ExamID != c.exam || p.CourseID != c.courseID) {
			t.Errorf("%s: policy = %+v", c.name, p)
		}
	}
	// A rejected write leaves the last good policy in place.
	f.setPolicy(t, domain.ModeCompletion, 0)
	_, _ = f.svc.SetPolicy(ctx, owner, course, domain.ModeCompletionAndExam, foreignExam)
	if p, err := f.svc.GetPolicy(ctx, owner, course); err != nil || p.Mode != domain.ModeCompletion {
		t.Fatalf("policy after rejected write = %+v, %v", p, err)
	}
}

func TestGetPolicy(t *testing.T) {
	f := newFixture(t)
	p, err := f.svc.GetPolicy(ctx, owner, course)
	if err != nil || p.Mode != domain.ModeOff || p.CourseID != course || !p.ExamID.IsZero() {
		t.Fatalf("default policy = %+v, %v", p, err)
	}
	if _, err := f.svc.GetPolicy(ctx, student, course); !errors.Is(err, app.ErrForbidden) {
		t.Fatalf("student read = %v", err)
	}
}

func TestClaimEligibility(t *testing.T) {
	cases := []struct {
		name  string
		setup func(f fixture, t *testing.T)
		want  error
	}{
		{"no policy", func(fixture, *testing.T) {}, app.ErrCertificatesDisabled},
		{"policy off", func(f fixture, t *testing.T) { f.setPolicy(t, domain.ModeOff, 0) }, app.ErrCertificatesDisabled},
		{"not enrolled", func(f fixture, t *testing.T) {
			f.setPolicy(t, domain.ModeCompletion, 0)
			delete(f.enrolled, pair{course, student.UserID})
		}, app.ErrNotEnrolled},
		{"progress incomplete", func(f fixture, t *testing.T) { f.setPolicy(t, domain.ModeCompletion, 0) }, app.ErrProgressIncomplete},
		{"completion met", func(f fixture, t *testing.T) {
			f.setPolicy(t, domain.ModeCompletion, 0)
			f.done[pair{course, student.UserID}] = true
		}, nil},
		{"exam mode, exam not passed", func(f fixture, t *testing.T) {
			f.setPolicy(t, domain.ModeCompletionAndExam, exam)
			f.done[pair{course, student.UserID}] = true
		}, app.ErrExamNotPassed},
		{"exam mode, exam passed but course incomplete", func(f fixture, t *testing.T) {
			f.setPolicy(t, domain.ModeCompletionAndExam, exam)
			f.exams.passed[[3]id.ID{course, student.UserID, exam}] = true
		}, app.ErrProgressIncomplete},
		{"exam mode, both met", func(f fixture, t *testing.T) {
			f.setPolicy(t, domain.ModeCompletionAndExam, exam)
			f.done[pair{course, student.UserID}] = true
			f.exams.passed[[3]id.ID{course, student.UserID, exam}] = true
		}, nil},
	}
	for _, c := range cases {
		f := newFixture(t)
		c.setup(f, t)
		cert, created, err := f.svc.Claim(ctx, student, course)
		if !errors.Is(err, c.want) {
			t.Errorf("%s: error = %v, want %v", c.name, err, c.want)
			continue
		}
		if c.want != nil {
			continue
		}
		if !created || !domain.ValidCode(cert.Code) || cert.UserID != student.UserID || cert.CourseID != course ||
			cert.StudentName != "Asha Rai" || cert.CourseTitle != "Go" || !cert.IssuedAt.Equal(t0) || cert.Revoked() {
			t.Errorf("%s: certificate = %+v created=%v", c.name, cert, created)
		}
	}
}

func TestClaimTwiceReturnsTheSameCertificate(t *testing.T) {
	f := newFixture(t)
	f.setPolicy(t, domain.ModeCompletion, 0)
	f.done[pair{course, student.UserID}] = true
	first, created, err := f.svc.Claim(ctx, student, course)
	if err != nil || !created {
		t.Fatalf("first claim: %v created=%v", err, created)
	}
	second, created, err := f.svc.Claim(ctx, student, course)
	if err != nil || created || second.Code != first.Code || second.ID != first.ID {
		t.Fatalf("second claim: %+v created=%v err=%v", second, created, err)
	}
	if len(f.store.certs) != 1 {
		t.Fatalf("%d certificates stored", len(f.store.certs))
	}
}

func TestClaimStillWorksWhenTheCertificateOutlivesAPolicyChange(t *testing.T) {
	f := newFixture(t)
	f.setPolicy(t, domain.ModeCompletion, 0)
	f.done[pair{course, student.UserID}] = true
	first, _, err := f.svc.Claim(ctx, student, course)
	if err != nil {
		t.Fatal(err)
	}
	f.setPolicy(t, domain.ModeOff, 0)
	if _, _, err := f.svc.Claim(ctx, student, course); !errors.Is(err, app.ErrCertificatesDisabled) {
		t.Fatalf("claim with policy off = %v", err)
	}
	got, err := f.svc.GetMine(ctx, student, course)
	if err != nil || got.Code != first.Code {
		t.Fatalf("GetMine after policy off = %+v, %v", got, err)
	}
}

func TestClaimUnknownStudentIsNotFound(t *testing.T) {
	f := newFixture(t)
	f.setPolicy(t, domain.ModeCompletion, 0)
	ghost := auth.Principal{UserID: unknownUser, Role: auth.RoleStudent}
	f.enrolled[pair{course, ghost.UserID}] = true
	f.done[pair{course, ghost.UserID}] = true
	if _, _, err := f.svc.Claim(ctx, ghost, course); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("claim = %v", err)
	}
}

func TestRevokeForRefund(t *testing.T) {
	f := newFixture(t)
	f.setPolicy(t, domain.ModeCompletion, 0)
	f.done[pair{course, student.UserID}] = true
	first, _, err := f.svc.Claim(ctx, student, course)
	if err != nil {
		t.Fatal(err)
	}

	f.clock.now = t0.Add(time.Hour)
	if err := f.svc.RevokeForRefund(ctx, other.UserID, course); err != nil {
		t.Fatalf("revoke for a user without a certificate: %v", err)
	}
	if err := f.svc.RevokeForRefund(ctx, student.UserID, course+1); err != nil {
		t.Fatalf("revoke for another course: %v", err)
	}
	if _, ok, _ := f.store.FindValid(ctx, course, student.UserID); !ok {
		t.Fatal("unrelated revokes touched the certificate")
	}
	for range 2 { // redelivery is safe
		if err := f.svc.RevokeForRefund(ctx, student.UserID, course); err != nil {
			t.Fatal(err)
		}
	}
	revoked, err := f.svc.Verify(ctx, first.Code)
	if err != nil || !revoked.Revoked() || !revoked.RevokedAt.Equal(t0.Add(time.Hour)) {
		t.Fatalf("verify after revoke = %+v, %v", revoked, err)
	}
	if _, err := f.svc.GetMine(ctx, student, course); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("GetMine after revoke = %v", err)
	}

	// Enrollment returns; the student qualifies again and gets a new certificate.
	again, created, err := f.svc.Claim(ctx, student, course)
	if err != nil || !created || again.Code == first.Code || again.ID == first.ID {
		t.Fatalf("reissue = %+v created=%v err=%v", again, created, err)
	}
	if old, err := f.svc.Verify(ctx, first.Code); err != nil || !old.Revoked() {
		t.Fatalf("old certificate after reissue = %+v, %v", old, err)
	}
	list, err := f.svc.ListMine(ctx, student)
	if err != nil || len(list) != 2 || list[0].Code != again.Code {
		t.Fatalf("ListMine = %+v, %v", list, err)
	}
}

func TestVerify(t *testing.T) {
	f := newFixture(t)
	f.setPolicy(t, domain.ModeCompletion, 0)
	f.done[pair{course, student.UserID}] = true
	cert, _, err := f.svc.Claim(ctx, student, course)
	if err != nil {
		t.Fatal(err)
	}
	got, err := f.svc.Verify(ctx, cert.Code)
	if err != nil || got.Code != cert.Code || got.Revoked() {
		t.Fatalf("verify = %+v, %v", got, err)
	}
	for _, bad := range []string{"", "short", cert.Code + "A", domain.NewCode(), "a" + cert.Code[1:]} {
		if _, err := f.svc.Verify(ctx, bad); !errors.Is(err, app.ErrNotFound) {
			t.Errorf("verify(%q) = %v, want ErrNotFound", bad, err)
		}
	}
}
```

- [ ] **Step 3: Run it to see it fail**

Run: `go test ./internal/certificate/app/`
Expected: FAIL, `undefined: app.NewService` and the error values.

- [ ] **Step 4: Add the ports and errors**

Append to `internal/certificate/app/ports.go` (add imports `platform/auth` and keep the existing ones):

```go
// CourseManagement is backed by courseauthoring.
type CourseManagement interface {
	// CanManage returns nil when p manages the course; otherwise ErrNotFound or ErrForbidden.
	CanManage(ctx context.Context, p auth.Principal, courseID id.ID) error
}

// Enrollments is backed by enrollment.
type Enrollments interface {
	IsActivelyEnrolled(ctx context.Context, courseID, userID id.ID) (bool, error)
}

// Progress is backed by progress.
type Progress interface {
	// IsComplete reports whether the user completed every lecture of the course.
	IsComplete(ctx context.Context, courseID, userID id.ID) (bool, error)
}

// Exams is backed by assessment.
type Exams interface {
	ExamInCourse(ctx context.Context, courseID, examID id.ID) (bool, error)
	HasPassed(ctx context.Context, courseID, userID, examID id.ID) (bool, error)
}

// Directory supplies the names a certificate snapshots, backed by identity and courseauthoring.
type Directory interface {
	// StudentName returns ErrNotFound when the user is unknown.
	StudentName(ctx context.Context, userID id.ID) (string, error)
	// CourseTitle returns ErrNotFound when the course is unknown.
	CourseTitle(ctx context.Context, courseID id.ID) (string, error)
}
```

Create `internal/certificate/app/errors.go`:

```go
package app

import "errors"

var (
	ErrNotFound             = errors.New("not found")
	ErrForbidden            = errors.New("forbidden")
	ErrInvalidInput         = errors.New("invalid input")
	ErrCertificatesDisabled = errors.New("certificates are not offered for this course")
	ErrNotEnrolled          = errors.New("an active enrollment is required")
	ErrProgressIncomplete   = errors.New("the course is not complete")
	ErrExamNotPassed        = errors.New("the certificate exam has not been passed")
)
```

- [ ] **Step 5: Implement the service**

Create `internal/certificate/app/service.go`:

```go
package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/santoshkc2200/ioe-backend/internal/certificate/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/clock"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

// Service implements the certificate use cases.
type Service struct {
	tx          TxRunner
	courses     CourseManagement
	enrollments Enrollments
	progress    Progress
	exams       Exams
	directory   Directory
	ids         *id.Generator
	clock       clock.Clock
}

func NewService(tx TxRunner, courses CourseManagement, enrollments Enrollments, progress Progress, exams Exams, directory Directory, ids *id.Generator, c clock.Clock) *Service {
	return &Service{tx: tx, courses: courses, enrollments: enrollments, progress: progress, exams: exams, directory: directory, ids: ids, clock: c}
}

// GetPolicy returns the course's policy to a manager. A course without a policy is off.
func (s *Service) GetPolicy(ctx context.Context, p auth.Principal, courseID id.ID) (domain.Policy, error) {
	if err := s.courses.CanManage(ctx, p, courseID); err != nil {
		return domain.Policy{}, err
	}
	policy := domain.Policy{CourseID: courseID, Mode: domain.ModeOff}
	err := s.tx.RunInTx(ctx, func(r Repository) error {
		stored, found, err := r.FindPolicy(ctx, courseID)
		if found {
			policy = stored
		}
		return err
	})
	return policy, err
}

// SetPolicy replaces the course's policy. The caller is authorized before any input is
// examined, so a stranger learns nothing about the course.
func (s *Service) SetPolicy(ctx context.Context, p auth.Principal, courseID id.ID, mode domain.Mode, examID id.ID) (domain.Policy, error) {
	if err := s.courses.CanManage(ctx, p, courseID); err != nil {
		return domain.Policy{}, err
	}
	policy, err := domain.NewPolicy(courseID, mode, examID)
	if err != nil {
		return domain.Policy{}, fmt.Errorf("%w: %w", ErrInvalidInput, err)
	}
	if policy.Mode == domain.ModeCompletionAndExam {
		ok, err := s.exams.ExamInCourse(ctx, courseID, policy.ExamID)
		if err != nil {
			return domain.Policy{}, err
		}
		if !ok {
			return domain.Policy{}, fmt.Errorf("%w: %w", ErrInvalidInput, domain.ErrExamNotInCourse)
		}
	}
	err = s.tx.RunInTx(ctx, func(r Repository) error {
		return r.UpsertPolicy(ctx, policy, s.clock.Now())
	})
	return policy, err
}

// Claim issues the caller's certificate for the course when they meet the course's policy.
// created is false when the caller already held a valid certificate. The eligibility
// lookups run outside any transaction: each takes its own pool connection.
func (s *Service) Claim(ctx context.Context, p auth.Principal, courseID id.ID) (cert domain.Certificate, created bool, err error) {
	var (
		policy   domain.Policy
		found    bool
		existing domain.Certificate
		hasValid bool
	)
	err = s.tx.RunInTx(ctx, func(r Repository) error {
		var err error
		if policy, found, err = r.FindPolicy(ctx, courseID); err != nil {
			return err
		}
		existing, hasValid, err = r.FindValid(ctx, courseID, p.UserID)
		return err
	})
	if err != nil {
		return domain.Certificate{}, false, err
	}
	if !found || policy.Mode == domain.ModeOff {
		return domain.Certificate{}, false, ErrCertificatesDisabled
	}
	active, err := s.enrollments.IsActivelyEnrolled(ctx, courseID, p.UserID)
	if err != nil {
		return domain.Certificate{}, false, err
	}
	if !active {
		return domain.Certificate{}, false, ErrNotEnrolled
	}
	if hasValid {
		return existing, false, nil
	}
	complete, err := s.progress.IsComplete(ctx, courseID, p.UserID)
	if err != nil {
		return domain.Certificate{}, false, err
	}
	if !complete {
		return domain.Certificate{}, false, ErrProgressIncomplete
	}
	if policy.Mode == domain.ModeCompletionAndExam {
		passed, err := s.exams.HasPassed(ctx, courseID, p.UserID, policy.ExamID)
		if err != nil {
			return domain.Certificate{}, false, err
		}
		if !passed {
			return domain.Certificate{}, false, ErrExamNotPassed
		}
	}
	name, err := s.directory.StudentName(ctx, p.UserID)
	if err != nil {
		return domain.Certificate{}, false, err
	}
	title, err := s.directory.CourseTitle(ctx, courseID)
	if err != nil {
		return domain.Certificate{}, false, err
	}
	issued := domain.NewCertificate(s.ids.New(), domain.NewCode(), p.UserID, courseID, name, title, s.clock.Now())
	err = s.tx.RunInTx(ctx, func(r Repository) error {
		inserted, err := r.Insert(ctx, issued)
		if err != nil {
			return err
		}
		if inserted {
			cert, created = issued, true
			return nil
		}
		// A concurrent claim won; return its certificate.
		var ok bool
		cert, ok, err = r.FindValid(ctx, courseID, p.UserID)
		if err == nil && !ok {
			err = errors.New("certificate insert conflicted but no valid certificate exists")
		}
		return err
	})
	return cert, created, err
}

// GetMine returns the caller's valid certificate for the course, or ErrNotFound.
func (s *Service) GetMine(ctx context.Context, p auth.Principal, courseID id.ID) (domain.Certificate, error) {
	var (
		cert  domain.Certificate
		found bool
	)
	err := s.tx.RunInTx(ctx, func(r Repository) error {
		var err error
		cert, found, err = r.FindValid(ctx, courseID, p.UserID)
		return err
	})
	if err == nil && !found {
		err = ErrNotFound
	}
	return cert, err
}

// ListMine returns every certificate of the caller, revoked ones included, newest first.
func (s *Service) ListMine(ctx context.Context, p auth.Principal) ([]domain.Certificate, error) {
	var out []domain.Certificate
	err := s.tx.RunInTx(ctx, func(r Repository) error {
		var err error
		out, err = r.ListByUser(ctx, p.UserID)
		return err
	})
	return out, err
}

// Verify returns the certificate with the given code, revoked or not. A malformed code is
// ErrNotFound without a lookup.
func (s *Service) Verify(ctx context.Context, code string) (domain.Certificate, error) {
	if !domain.ValidCode(code) {
		return domain.Certificate{}, ErrNotFound
	}
	var (
		cert  domain.Certificate
		found bool
	)
	err := s.tx.RunInTx(ctx, func(r Repository) error {
		var err error
		cert, found, err = r.FindByCode(ctx, code)
		return err
	})
	if err == nil && !found {
		err = ErrNotFound
	}
	return cert, err
}

// RevokeForRefund revokes the user's valid certificate for the course. It is idempotent and
// does nothing when there is none.
func (s *Service) RevokeForRefund(ctx context.Context, userID, courseID id.ID) error {
	return s.tx.RunInTx(ctx, func(r Repository) error {
		return r.RevokeValid(ctx, courseID, userID, s.clock.Now())
	})
}
```

- [ ] **Step 6: Run the tests**

Run: `gofmt -l internal/certificate; go test -race ./internal/certificate/app/ && golangci-lint run ./internal/certificate/...`
Expected: PASS and no lint issues. In `Claim`, the named results shadow with `err :=` inside closures; keep the closure-local declarations as written.

- [ ] **Step 7: Commit**

```bash
git add internal/certificate
git commit -m "feat(certificate): set policies, claim, verify and revoke"
```

---

### Task 6: HTTP adapter

**Files:**
- Create: `internal/certificate/adapters/httpapi/{httpapi.go,wire.go}`
- Test: `internal/certificate/adapters/httpapi/httpapi_test.go`

**Interfaces:**
- Consumes: Task 5 `Service` method set and app errors.
- Produces: `httpapi.New(svc Service, cfg Config) *Handler`, `(*Handler).Register(r *httpserver.Router)`, and

```go
type Config struct {
	RequireAuth   httpserver.Middleware
	VerifyLimiter *httpserver.RateLimiter
	IPs           httpserver.IPResolver
	Logger        *slog.Logger
}
```

Routes (all `/v1`): `GET|PUT /courses/{courseID}/certificate-policy`, `POST|GET /courses/{courseID}/certificate`, `GET /me/certificates` (auth required), `GET /certificates/{code}` (public, rate limited).

- [ ] **Step 1: Write the failing tests**

Create `internal/certificate/adapters/httpapi/httpapi_test.go`:

```go
package httpapi_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/certificate/adapters/httpapi"
	"github.com/santoshkc2200/ioe-backend/internal/certificate/app"
	"github.com/santoshkc2200/ioe-backend/internal/certificate/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/httpserver"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

var t0 = time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC)

type stub struct {
	policy  domain.Policy
	cert    domain.Certificate
	created bool
	list    []domain.Certificate
	err     error

	principal auth.Principal
	courseID  id.ID
	mode      domain.Mode
	examID    id.ID
	code      string
}

func (s *stub) GetPolicy(_ context.Context, p auth.Principal, courseID id.ID) (domain.Policy, error) {
	s.principal, s.courseID = p, courseID
	return s.policy, s.err
}

func (s *stub) SetPolicy(_ context.Context, p auth.Principal, courseID id.ID, mode domain.Mode, examID id.ID) (domain.Policy, error) {
	s.principal, s.courseID, s.mode, s.examID = p, courseID, mode, examID
	return s.policy, s.err
}

func (s *stub) Claim(_ context.Context, p auth.Principal, courseID id.ID) (domain.Certificate, bool, error) {
	s.principal, s.courseID = p, courseID
	return s.cert, s.created, s.err
}

func (s *stub) GetMine(_ context.Context, p auth.Principal, courseID id.ID) (domain.Certificate, error) {
	s.principal, s.courseID = p, courseID
	return s.cert, s.err
}

func (s *stub) ListMine(_ context.Context, p auth.Principal) ([]domain.Certificate, error) {
	s.principal = p
	return s.list, s.err
}

func (s *stub) Verify(_ context.Context, code string) (domain.Certificate, error) {
	s.code = code
	return s.cert, s.err
}

func fakeAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{UserID: 200, Role: auth.RoleStudent})))
	})
}

// denyAuth rejects every request, proving a route is mounted behind authentication.
func denyAuth(http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) })
}

func newServer(s *stub, requireAuth httpserver.Middleware) http.Handler {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	r, h := httpserver.NewRouter(httpserver.Options{Logger: logger, AllowedOrigins: []string{"https://app.test"}, ServiceName: "test"})
	httpapi.New(s, httpapi.Config{
		RequireAuth: requireAuth, VerifyLimiter: httpserver.NewRateLimiter(1000), IPs: httpserver.NewIPResolver(nil), Logger: logger,
	}).Register(r)
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

func problemType(t *testing.T, b []byte) string {
	t.Helper()
	var p struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatalf("decode %s: %v", b, err)
	}
	return p.Type
}

func validCert() domain.Certificate {
	return domain.Certificate{
		ID: 1, Code: strings.Repeat("A", domain.CodeLen), UserID: 200, CourseID: 10,
		StudentName: "Asha Rai", CourseTitle: "Go", IssuedAt: t0,
	}
}

func TestClaim(t *testing.T) {
	s := &stub{cert: validCert(), created: true}
	code, body := call(newServer(s, fakeAuth), "POST", "/v1/courses/10/certificate", "")
	if code != http.StatusCreated {
		t.Fatalf("created: %d %s", code, body)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"id": "1", "code": strings.Repeat("A", 26), "course_id": "10", "course_title": "Go",
		"student_name": "Asha Rai", "issued_at": "2026-10-08T09:00:00Z", "status": "valid",
	}
	if len(got) != len(want) {
		t.Fatalf("fields = %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %v, want %v", k, got[k], v)
		}
	}
	if s.principal.UserID != 200 || s.courseID != 10 {
		t.Fatalf("stub = %+v", s)
	}

	s.created = false
	if code, _ := call(newServer(s, fakeAuth), "POST", "/v1/courses/10/certificate", ""); code != http.StatusOK {
		t.Fatalf("existing: %d", code)
	}
}

func TestClaimRefusals(t *testing.T) {
	for err, want := range map[error]struct {
		status int
		typ    string
	}{
		app.ErrCertificatesDisabled: {http.StatusConflict, "certificates_disabled"},
		app.ErrNotEnrolled:          {http.StatusConflict, "not_enrolled"},
		app.ErrProgressIncomplete:   {http.StatusConflict, "progress_incomplete"},
		app.ErrExamNotPassed:        {http.StatusConflict, "exam_not_passed"},
		app.ErrNotFound:             {http.StatusNotFound, "not_found"},
	} {
		code, body := call(newServer(&stub{err: err}, fakeAuth), "POST", "/v1/courses/10/certificate", "")
		if code != want.status || problemType(t, body) != want.typ {
			t.Errorf("%v: %d %s", err, code, body)
		}
	}
	code, body := call(newServer(&stub{err: io.ErrUnexpectedEOF}, fakeAuth), "POST", "/v1/courses/10/certificate", "")
	if code != http.StatusInternalServerError || strings.Contains(string(body), "unexpected EOF") {
		t.Fatalf("internal error leaked: %d %s", code, body)
	}
}

func TestRevokedCertificateStatus(t *testing.T) {
	c := validCert()
	c.RevokedAt = t0.Add(time.Hour)
	code, body := call(newServer(&stub{list: []domain.Certificate{c}}, fakeAuth), "GET", "/v1/me/certificates", "")
	var got struct {
		Items []struct {
			Status string `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &got); err != nil || code != http.StatusOK || len(got.Items) != 1 || got.Items[0].Status != "revoked" {
		t.Fatalf("list: %d %s", code, body)
	}
	code, body = call(newServer(&stub{list: nil}, fakeAuth), "GET", "/v1/me/certificates", "")
	if code != http.StatusOK || !strings.Contains(string(body), `"items":[]`) {
		t.Fatalf("empty list: %d %s", code, body)
	}
}

func TestVerifyIsPublicAndExposesOnlyThePublicFields(t *testing.T) {
	s := &stub{cert: validCert()}
	h := newServer(s, denyAuth) // authentication would reject everything else
	code, body := call(h, "GET", "/v1/certificates/"+strings.Repeat("A", 26), "")
	if code != http.StatusOK {
		t.Fatalf("verify: %d %s", code, body)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"code": strings.Repeat("A", 26), "student_name": "Asha Rai", "course_title": "Go",
		"issued_at": "2026-10-08T09:00:00Z", "status": "valid",
	}
	if len(got) != len(want) {
		t.Fatalf("verify exposes %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %v, want %v", k, got[k], v)
		}
	}
	if s.code != strings.Repeat("A", 26) {
		t.Fatalf("code passed = %q", s.code)
	}

	if code, _ := call(newServer(&stub{err: app.ErrNotFound}, denyAuth), "GET", "/v1/certificates/short", ""); code != http.StatusNotFound {
		t.Fatalf("unknown code: %d", code)
	}
	if code, _ := call(h, "GET", "/v1/courses/10/certificate", ""); code != http.StatusUnauthorized {
		t.Fatalf("own certificate route is not authenticated: %d", code)
	}
}

func TestPolicyRoutes(t *testing.T) {
	s := &stub{policy: domain.Policy{CourseID: 10, Mode: domain.ModeCompletionAndExam, ExamID: 700}}
	h := newServer(s, fakeAuth)
	code, body := call(h, "PUT", "/v1/courses/10/certificate-policy", `{"mode":"completion_and_exam","exam_id":"700"}`)
	if code != http.StatusOK || s.mode != domain.ModeCompletionAndExam || s.examID != 700 || s.courseID != 10 {
		t.Fatalf("put: %d %s stub=%+v", code, body, s)
	}
	var got map[string]any
	_ = json.Unmarshal(body, &got)
	if got["mode"] != "completion_and_exam" || got["exam_id"] != "700" {
		t.Fatalf("put body = %v", got)
	}

	s.policy = domain.Policy{CourseID: 10, Mode: domain.ModeCompletion}
	code, body = call(h, "PUT", "/v1/courses/10/certificate-policy", `{"mode":"completion"}`)
	if code != http.StatusOK || s.examID != 0 || strings.Contains(string(body), "exam_id") {
		t.Fatalf("put without exam: %d %s examID=%d", code, body, s.examID)
	}

	if code, body := call(h, "GET", "/v1/courses/10/certificate-policy", ""); code != http.StatusOK || !strings.Contains(string(body), `"mode":"completion"`) {
		t.Fatalf("get: %d %s", code, body)
	}
}

func TestPolicyBadInput(t *testing.T) {
	h := newServer(&stub{}, fakeAuth)
	for name, body := range map[string]string{
		"unknown field": `{"mode":"completion","extra":1}`,
		"not json":      `nope`,
		"numeric id":    `{"mode":"completion_and_exam","exam_id":700}`,
	} {
		if code, b := call(h, "PUT", "/v1/courses/10/certificate-policy", body); code != http.StatusBadRequest || problemType(t, b) != "invalid_request" {
			t.Errorf("%s: %d %s", name, code, b)
		}
	}
	code, b := call(newServer(&stub{err: app.ErrInvalidInput}, fakeAuth), "PUT", "/v1/courses/10/certificate-policy", `{"mode":"always"}`)
	if code != http.StatusBadRequest || problemType(t, b) != "invalid_input" {
		t.Errorf("domain invalid: %d %s", code, b)
	}
	if code, _ := call(newServer(&stub{err: app.ErrForbidden}, fakeAuth), "PUT", "/v1/courses/10/certificate-policy", `{"mode":"off"}`); code != http.StatusForbidden {
		t.Errorf("forbidden: %d", code)
	}
	for _, path := range []string{"/v1/courses/abc/certificate", "/v1/courses/0/certificate-policy"} {
		if code, _ := call(h, "GET", path, ""); code != http.StatusNotFound {
			t.Errorf("%s: %d", path, code)
		}
	}
}
```

- [ ] **Step 2: Run it to see it fail**

Run: `go test ./internal/certificate/adapters/httpapi/`
Expected: FAIL, package has no non-test files.

- [ ] **Step 3: Implement the wire types**

Create `internal/certificate/adapters/httpapi/wire.go`:

```go
package httpapi

import (
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/certificate/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

type policyRequest struct {
	Mode   string `json:"mode"`
	ExamID *id.ID `json:"exam_id"`
}

type policyWire struct {
	Mode   string `json:"mode"`
	ExamID *id.ID `json:"exam_id,omitempty"`
}

// certificateWire is a certificate as its owner sees it.
type certificateWire struct {
	ID          id.ID     `json:"id"`
	Code        string    `json:"code"`
	CourseID    id.ID     `json:"course_id"`
	CourseTitle string    `json:"course_title"`
	StudentName string    `json:"student_name"`
	IssuedAt    time.Time `json:"issued_at"`
	Status      string    `json:"status"`
}

type certificateListWire struct {
	Items []certificateWire `json:"items"`
}

// verifyWire is what anyone holding a code sees. It carries no ids.
type verifyWire struct {
	Code        string    `json:"code"`
	StudentName string    `json:"student_name"`
	CourseTitle string    `json:"course_title"`
	IssuedAt    time.Time `json:"issued_at"`
	Status      string    `json:"status"`
}

func status(c domain.Certificate) string {
	if c.Revoked() {
		return "revoked"
	}
	return "valid"
}

func toPolicyWire(p domain.Policy) policyWire {
	w := policyWire{Mode: string(p.Mode)}
	if !p.ExamID.IsZero() {
		exam := p.ExamID
		w.ExamID = &exam
	}
	return w
}

func toCertificateWire(c domain.Certificate) certificateWire {
	return certificateWire{
		ID: c.ID, Code: c.Code, CourseID: c.CourseID, CourseTitle: c.CourseTitle,
		StudentName: c.StudentName, IssuedAt: c.IssuedAt, Status: status(c),
	}
}

func toListWire(cs []domain.Certificate) certificateListWire {
	w := certificateListWire{Items: make([]certificateWire, len(cs))}
	for i, c := range cs {
		w.Items[i] = toCertificateWire(c)
	}
	return w
}

func toVerifyWire(c domain.Certificate) verifyWire {
	return verifyWire{Code: c.Code, StudentName: c.StudentName, CourseTitle: c.CourseTitle, IssuedAt: c.IssuedAt, Status: status(c)}
}
```

- [ ] **Step 4: Implement the handler**

Create `internal/certificate/adapters/httpapi/httpapi.go`:

```go
// Package httpapi exposes certificates over HTTP.
package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/santoshkc2200/ioe-backend/internal/certificate/app"
	"github.com/santoshkc2200/ioe-backend/internal/certificate/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/httpserver"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
	"github.com/santoshkc2200/ioe-backend/internal/platform/problem"
)

// Service is the certificate use-case surface the handlers call.
type Service interface {
	GetPolicy(ctx context.Context, p auth.Principal, courseID id.ID) (domain.Policy, error)
	SetPolicy(ctx context.Context, p auth.Principal, courseID id.ID, mode domain.Mode, examID id.ID) (domain.Policy, error)
	Claim(ctx context.Context, p auth.Principal, courseID id.ID) (domain.Certificate, bool, error)
	GetMine(ctx context.Context, p auth.Principal, courseID id.ID) (domain.Certificate, error)
	ListMine(ctx context.Context, p auth.Principal) ([]domain.Certificate, error)
	Verify(ctx context.Context, code string) (domain.Certificate, error)
}

type Config struct {
	RequireAuth   httpserver.Middleware
	VerifyLimiter *httpserver.RateLimiter
	IPs           httpserver.IPResolver
	Logger        *slog.Logger
}

type Handler struct {
	svc Service
	cfg Config
}

func New(svc Service, cfg Config) *Handler { return &Handler{svc: svc, cfg: cfg} }

// Register mounts the certificate routes. Only verification is public.
func (h *Handler) Register(r *httpserver.Router) {
	a := func(f http.HandlerFunc) http.Handler { return h.cfg.RequireAuth(f) }
	r.Handle("GET /v1/courses/{courseID}/certificate-policy", a(h.getPolicy))
	r.Handle("PUT /v1/courses/{courseID}/certificate-policy", a(h.setPolicy))
	r.Handle("POST /v1/courses/{courseID}/certificate", a(h.claim))
	r.Handle("GET /v1/courses/{courseID}/certificate", a(h.getMine))
	r.Handle("GET /v1/me/certificates", a(h.listMine))
	r.Handle("GET /v1/certificates/{code}", h.cfg.VerifyLimiter.Middleware(h.cfg.IPs)(http.HandlerFunc(h.verify)))
}

func (h *Handler) getPolicy(w http.ResponseWriter, r *http.Request) {
	courseID, ok := courseID(w, r)
	if !ok {
		return
	}
	p, err := h.svc.GetPolicy(r.Context(), principal(r), courseID)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toPolicyWire(p))
}

func (h *Handler) setPolicy(w http.ResponseWriter, r *http.Request) {
	courseID, ok := courseID(w, r)
	if !ok {
		return
	}
	var req policyRequest
	if !httpserver.DecodeJSON(w, r, &req) {
		return
	}
	var examID id.ID
	if req.ExamID != nil {
		examID = *req.ExamID
	}
	p, err := h.svc.SetPolicy(r.Context(), principal(r), courseID, domain.Mode(req.Mode), examID)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toPolicyWire(p))
}

func (h *Handler) claim(w http.ResponseWriter, r *http.Request) {
	courseID, ok := courseID(w, r)
	if !ok {
		return
	}
	c, created, err := h.svc.Claim(r.Context(), principal(r), courseID)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	httpserver.WriteJSON(w, status, toCertificateWire(c))
}

func (h *Handler) getMine(w http.ResponseWriter, r *http.Request) {
	courseID, ok := courseID(w, r)
	if !ok {
		return
	}
	c, err := h.svc.GetMine(r.Context(), principal(r), courseID)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toCertificateWire(c))
}

func (h *Handler) listMine(w http.ResponseWriter, r *http.Request) {
	cs, err := h.svc.ListMine(r.Context(), principal(r))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toListWire(cs))
}

func (h *Handler) verify(w http.ResponseWriter, r *http.Request) {
	c, err := h.svc.Verify(r.Context(), r.PathValue("code"))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toVerifyWire(c))
}

// courseID parses the courseID path value. A malformed ID names no resource: 404.
func courseID(w http.ResponseWriter, r *http.Request) (id.ID, bool) {
	v, err := id.Parse(r.PathValue("courseID"))
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
	{app.ErrNotFound, http.StatusNotFound, "not_found", "Not Found"},
	{app.ErrForbidden, http.StatusForbidden, "forbidden", "Forbidden"},
	{app.ErrInvalidInput, http.StatusBadRequest, "invalid_input", "Invalid Input"},
	{app.ErrCertificatesDisabled, http.StatusConflict, "certificates_disabled", "Certificates Disabled"},
	{app.ErrNotEnrolled, http.StatusConflict, "not_enrolled", "Not Enrolled"},
	{app.ErrProgressIncomplete, http.StatusConflict, "progress_incomplete", "Course Not Complete"},
	{app.ErrExamNotPassed, http.StatusConflict, "exam_not_passed", "Exam Not Passed"},
}

func (h *Handler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	for _, m := range errorMappings {
		if errors.Is(err, m.err) {
			detail := ""
			if m.status == http.StatusBadRequest {
				detail = err.Error()
			}
			problem.Write(w, r, m.status, m.typ, m.title, detail)
			return
		}
	}
	h.cfg.Logger.ErrorContext(r.Context(), "certificate request failed", "error", err)
	problem.Write(w, r, http.StatusInternalServerError, problem.TypeInternal, "Internal Server Error", "")
}
```

If `go vet` reports that the local function `courseID` shadows its result variable in the handlers, rename the helper to `pathCourseID`. `r.Handle` accepting an `http.Handler` is already how progress mounts routes.

- [ ] **Step 5: Run the tests and commit**

Run: `gofmt -l internal/certificate; go test -race ./internal/certificate/... && golangci-lint run ./internal/certificate/...`
Expected: PASS, no lint issues. If `TestPolicyBadInput`'s `numeric id` case returns 400 with a type other than `invalid_request`, print the body and align the expectation with `httpserver.DecodeJSON`'s actual problem type (the unknown-field case in progress's tests uses `invalid_request`).

```bash
git add internal/certificate
git commit -m "feat(certificate): expose certificates over HTTP"
```

---

### Task 7: Refund event consumer

**Files:**
- Create: `internal/certificate/adapters/events/events.go`
- Test: `internal/certificate/adapters/events/events_test.go`

**Interfaces:**
- Consumes: `(*app.Service).RevokeForRefund(ctx, userID, courseID id.ID) error` through a local interface.
- Produces: `events.PurchaseRefundedTopic`, `events.New(revoker Revoker, logger *slog.Logger) *Handlers`, `(*Handlers).PurchaseRefunded(msg *message.Message) error`. Task 8 registers it with `fw.Handle`.

- [ ] **Step 1: Write the failing tests**

Create `internal/certificate/adapters/events/events_test.go`:

```go
package events_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/ThreeDotsLabs/watermill/message"

	"github.com/santoshkc2200/ioe-backend/internal/certificate/adapters/events"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

type revoker struct {
	calls  [][2]id.ID
	failed error
}

func (r *revoker) RevokeForRefund(_ context.Context, userID, courseID id.ID) error {
	r.calls = append(r.calls, [2]id.ID{userID, courseID})
	return r.failed
}

func handlers(r *revoker) *events.Handlers {
	return events.New(r, slog.New(slog.NewJSONHandler(io.Discard, nil)))
}

func TestTopicMatchesPaymentEventName(t *testing.T) {
	if events.PurchaseRefundedTopic != "payment.purchase.refunded" {
		t.Fatalf("topic = %q", events.PurchaseRefundedTopic)
	}
}

func TestRevokesWhenAccessWasRevoked(t *testing.T) {
	r := &revoker{}
	msg := message.NewMessage("1", []byte(`{"purchase_id":"5","user_id":"200","course_id":"10","access_revoked":true,"occurred_at":"2026-10-08T09:00:00Z"}`))
	if err := handlers(r).PurchaseRefunded(msg); err != nil {
		t.Fatal(err)
	}
	if len(r.calls) != 1 || r.calls[0] != [2]id.ID{200, 10} {
		t.Fatalf("calls = %v", r.calls)
	}
}

func TestIgnoresRefundsThatKeepAccess(t *testing.T) {
	r := &revoker{}
	msg := message.NewMessage("1", []byte(`{"user_id":"200","course_id":"10","access_revoked":false}`))
	if err := handlers(r).PurchaseRefunded(msg); err != nil || len(r.calls) != 0 {
		t.Fatalf("err=%v calls=%v", err, r.calls)
	}
}

func TestMalformedPayloadsAreAcknowledged(t *testing.T) {
	for name, payload := range map[string]string{
		"not json":         `nope`,
		"empty object":     `{}`,
		"missing course":   `{"user_id":"200","access_revoked":true}`,
		"missing user":     `{"course_id":"10","access_revoked":true}`,
		"zero id":          `{"user_id":"0","course_id":"10","access_revoked":true}`,
		"numeric ids":      `{"user_id":200,"course_id":10,"access_revoked":true}`,
		"null":             `null`,
	} {
		r := &revoker{}
		if err := handlers(r).PurchaseRefunded(message.NewMessage("1", []byte(payload))); err != nil || len(r.calls) != 0 {
			t.Errorf("%s: err=%v calls=%v", name, err, r.calls)
		}
	}
}

func TestStorageFailureIsRetried(t *testing.T) {
	r := &revoker{failed: errors.New("db down")}
	msg := message.NewMessage("1", []byte(`{"user_id":"200","course_id":"10","access_revoked":true}`))
	if err := handlers(r).PurchaseRefunded(msg); !errors.Is(err, r.failed) {
		t.Fatalf("err = %v, want the storage error so the outbox retries", err)
	}
}
```

- [ ] **Step 2: Run it to see it fail**

Run: `go test ./internal/certificate/adapters/events/`
Expected: FAIL, package has no non-test files.

- [ ] **Step 3: Implement the handler**

Create `internal/certificate/adapters/events/events.go`:

```go
// Package events subscribes certificate to the domain events it consumes.
package events

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/ThreeDotsLabs/watermill/message"

	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

// PurchaseRefundedTopic is payment's PurchaseRefunded event name.
const PurchaseRefundedTopic = "payment.purchase.refunded"

const handlerTimeout = 30 * time.Second

// Revoker revokes a user's certificate for a course after a refund.
type Revoker interface {
	RevokeForRefund(ctx context.Context, userID, courseID id.ID) error
}

// Handlers process the events certificate subscribes to.
type Handlers struct {
	revoker Revoker
	logger  *slog.Logger
}

func New(revoker Revoker, logger *slog.Logger) *Handlers {
	return &Handlers{revoker: revoker, logger: logger}
}

// purchaseRefunded mirrors the payment.purchase.refunded fields this context relies on.
type purchaseRefunded struct {
	UserID        id.ID `json:"user_id"`
	CourseID      id.ID `json:"course_id"`
	AccessRevoked bool  `json:"access_revoked"`
}

// PurchaseRefunded revokes the buyer's certificate when the refund also ended their access.
// A malformed payload is logged and acknowledged: retrying it can never succeed. Only a
// storage failure is returned, so the outbox redelivers it; revoking is idempotent.
func (h *Handlers) PurchaseRefunded(msg *message.Message) error {
	var ev purchaseRefunded
	if err := json.Unmarshal(msg.Payload, &ev); err != nil || ev.UserID.IsZero() || ev.CourseID.IsZero() {
		h.logger.WarnContext(msg.Context(), "certificate: dropping malformed purchase.refunded event",
			"message_id", msg.UUID, "error", err)
		return nil
	}
	if !ev.AccessRevoked {
		return nil
	}
	ctx, cancel := context.WithTimeout(msg.Context(), handlerTimeout)
	defer cancel()
	return h.revoker.RevokeForRefund(ctx, ev.UserID, ev.CourseID)
}
```

- [ ] **Step 4: Run the tests and commit**

Run: `gofmt -l internal/certificate; go test -race ./internal/certificate/... && golangci-lint run ./internal/certificate/...`
Expected: PASS. The `"null"` payload decodes without error into a zero struct; the zero-id check is what rejects it.

```bash
git add internal/certificate
git commit -m "feat(certificate): revoke certificates when a refund ends access"
```

---

### Task 8: Wiring and end-to-end test

**Files:**
- Create: `cmd/api/certificate.go`
- Modify: `cmd/api/progress.go`, `cmd/api/app.go`
- Test: `cmd/api/e2e_integration_test.go`

**Interfaces:**
- Consumes: Tasks 1, 2, 5, 6, 7. `*progressapp.Service` satisfies `certificateapp.Progress`; `*assessmentapp.AssessmentQuery` satisfies `certificateapp.Exams`; `enrollmentAccess` satisfies `certificateapp.Enrollments`.
- Produces: `registerCertificate(...) *certificateapp.Service`; `registerProgress` now returns `*progressapp.Service`.

- [ ] **Step 1: Write the failing e2e test**

Append to `cmd/api/e2e_integration_test.go` (the file's existing imports cover `context`, `io`, `log/slog`, `net/http`, `net/http/httptest`, `testing`, `time`, `pgtest`, `googletest`; add any that are missing):

```go
func TestCertificateEndToEnd(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pool := pgtest.New(t)
	google := googletest.NewIssuer(t)
	a, err := buildApp(ctx, baseConfig(t, google), slog.New(slog.NewJSONHandler(io.Discard, nil)), pool)
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

	// A priced, published course with one lecture.
	courseID := mustStatus(t, c, http.MethodPost, "/v1/courses", `{"title":"Go","description":"d"}`, admin, http.StatusCreated)["id"].(string)
	mustStatus(t, c, http.MethodPost, "/v1/courses/"+courseID+"/price", `{"amount_minor":150000,"currency":"NPR"}`, admin, http.StatusOK)
	lectures := mustStatus(t, c, http.MethodPost, "/v1/courses/"+courseID+"/lectures", `{"title":"L1","text_body":"<p>x</p>"}`, admin, http.StatusCreated)["lectures"].([]any)
	lectureID := lectures[len(lectures)-1].(map[string]any)["id"].(string)
	mustStatus(t, c, http.MethodPost, "/v1/courses/"+courseID+"/publish", "", admin, http.StatusNoContent)

	claim := func(who map[string]string) (int, map[string]any) {
		resp, body := c.do(http.MethodPost, "/v1/courses/"+courseID+"/certificate", "", who)
		return resp.StatusCode, body
	}
	refused := func(who map[string]string, typ string) {
		t.Helper()
		if code, body := claim(who); code != http.StatusConflict || body["type"] != typ {
			t.Fatalf("claim: %d %v, want 409 %s", code, body, typ)
		}
	}

	refused(student, "certificates_disabled")
	mustStatus(t, c, http.MethodPut, "/v1/courses/"+courseID+"/certificate-policy", `{"mode":"completion"}`, student, http.StatusForbidden)
	mustStatus(t, c, http.MethodPut, "/v1/courses/"+courseID+"/certificate-policy", `{"mode":"completion_and_exam"}`, admin, http.StatusBadRequest)
	policy := mustStatus(t, c, http.MethodPut, "/v1/courses/"+courseID+"/certificate-policy", `{"mode":"completion"}`, admin, http.StatusOK)
	if policy["mode"] != "completion" {
		t.Fatalf("policy = %v", policy)
	}

	refused(student, "not_enrolled")
	record := `{"course_id":"` + courseID + `","amount_minor":150000,"currency":"NPR","method":"cash","reference":"R-1","note":"desk"}`
	mustStatus(t, c, http.MethodPost, "/v1/users/"+studentID+"/purchases", record, admin, http.StatusCreated)
	refused(student, "progress_incomplete")
	mustStatus(t, c, http.MethodPut, "/v1/courses/"+courseID+"/lectures/"+lectureID+"/progress/"+studentID,
		`{"state":"completed","position_ms":0}`, student, http.StatusNoContent)

	code, cert := claim(student)
	if code != http.StatusCreated || cert["status"] != "valid" || cert["course_title"] != "Go" || cert["student_name"] == "" {
		t.Fatalf("claim: %d %v", code, cert)
	}
	certCode := cert["code"].(string)
	if code, again := claim(student); code != http.StatusOK || again["code"] != certCode {
		t.Fatalf("second claim: %d %v", code, again)
	}

	// Verification needs no credentials and exposes no ids.
	verify := mustStatus(t, c, http.MethodGet, "/v1/certificates/"+certCode, "", nil, http.StatusOK)
	if verify["status"] != "valid" || verify["course_title"] != "Go" || len(verify) != 5 {
		t.Fatalf("verify = %v", verify)
	}
	mustStatus(t, c, http.MethodGet, "/v1/certificates/"+strings.Repeat("A", 26), "", nil, http.StatusNotFound)
	mustStatus(t, c, http.MethodGet, "/v1/certificates/not-a-code", "", nil, http.StatusNotFound)
	if items := mustStatus(t, c, http.MethodGet, "/v1/me/certificates", "", student, http.StatusOK)["items"].([]any); len(items) != 1 {
		t.Fatalf("my certificates = %v", items)
	}
	mustStatus(t, c, http.MethodGet, "/v1/courses/"+courseID+"/certificate", "", student, http.StatusOK)

	// A refund that ends access revokes the certificate through the outbox.
	purchases := mustStatus(t, c, http.MethodGet, "/v1/users/"+studentID+"/purchases", "", admin, http.StatusOK)["items"].([]any)
	purchaseID := purchases[0].(map[string]any)["id"].(string)
	mustStatus(t, c, http.MethodPost, "/v1/purchases/"+purchaseID+"/refund", `{"reference":"RF-1","note":"duplicate"}`, admin, http.StatusOK)
	waitFor(t, func() bool {
		_, body := c.do(http.MethodGet, "/v1/certificates/"+certCode, "", nil)
		return body["status"] == "revoked"
	})
	mustStatus(t, c, http.MethodGet, "/v1/courses/"+courseID+"/certificate", "", student, http.StatusNotFound)
	refused(student, "not_enrolled")
	if items := mustStatus(t, c, http.MethodGet, "/v1/me/certificates", "", student, http.StatusOK)["items"].([]any); len(items) != 1 ||
		items[0].(map[string]any)["status"] != "revoked" {
		t.Fatalf("my certificates after refund = %v", items)
	}
}
```

Check `mustStatus` accepts a `nil` headers map (it passes `headers` straight to `c.do`, which `TestEndToEnd` already calls with `nil`). Add `"strings"` to the file's imports if missing.

- [ ] **Step 2: Run it to see it fail**

Run: `go test -race -tags integration ./cmd/api/ -run TestCertificateEndToEnd`
Expected: FAIL, the first claim returns 404 because no certificate routes are mounted.

- [ ] **Step 3: Make `registerProgress` return the service**

Replace `registerProgress` in `cmd/api/progress.go`:

```go
func registerProgress(r *httpserver.Router, pool *pgxpool.Pool, courses *courseauthoringapp.CourseService, enrollments progressapp.EnrollmentQuery, clk clock.Clock, requireAuth httpserver.Middleware, logger *slog.Logger) *progressapp.Service {
	svc := progressapp.NewService(progresspg.NewTxRunner(pool), progressCourseCatalog{courses: courses}, enrollments, clk)
	progresshttp.New(svc, progresshttp.Config{RequireAuth: requireAuth, Logger: logger}).Register(r)
	return svc
}
```

- [ ] **Step 4: Write the certificate wiring**

Create `cmd/api/certificate.go`:

```go
package main

import (
	"context"
	"errors"
	"log/slog"

	certificatehttp "github.com/santoshkc2200/ioe-backend/internal/certificate/adapters/httpapi"
	certificatepg "github.com/santoshkc2200/ioe-backend/internal/certificate/adapters/postgres"
	certificateapp "github.com/santoshkc2200/ioe-backend/internal/certificate/app"
	courseauthoringapp "github.com/santoshkc2200/ioe-backend/internal/courseauthoring/app"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/clock"
	"github.com/santoshkc2200/ioe-backend/internal/platform/httpserver"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
	progressapp "github.com/santoshkc2200/ioe-backend/internal/progress/app"

	"github.com/jackc/pgx/v5/pgxpool"
)

// certificateDirectory lets certificate ask course authoring who manages a course and what it
// is called, and identity what a student is called.
type certificateDirectory struct {
	courses *courseauthoringapp.CourseService
	users   identityUsers
}

func (d certificateDirectory) CanManage(ctx context.Context, p auth.Principal, courseID id.ID) error {
	err := d.courses.CheckManagerRead(ctx, p, courseID)
	switch {
	case errors.Is(err, courseauthoringapp.ErrNotFound):
		return certificateapp.ErrNotFound
	case errors.Is(err, courseauthoringapp.ErrForbidden):
		return certificateapp.ErrForbidden
	}
	return err
}

func (d certificateDirectory) CourseTitle(ctx context.Context, courseID id.ID) (string, error) {
	f, err := d.courses.Facts(ctx, courseID)
	if errors.Is(err, courseauthoringapp.ErrNotFound) {
		return "", certificateapp.ErrNotFound
	}
	return f.Title, err
}

func (d certificateDirectory) StudentName(ctx context.Context, userID id.ID) (string, error) {
	names, err := d.users.Names(ctx, []id.ID{userID})
	if err != nil {
		return "", err
	}
	name, ok := names[userID]
	if !ok {
		return "", certificateapp.ErrNotFound
	}
	return name, nil
}

// registerCertificate mounts certificates and returns the service the refund consumer calls.
// progress and exams already have the methods certificate's ports ask for.
func registerCertificate(r *httpserver.Router, pool *pgxpool.Pool, courses *courseauthoringapp.CourseService, users identityUsers, enrollments certificateapp.Enrollments, progress certificateapp.Progress, exams certificateapp.Exams, ids *id.Generator, clk clock.Clock, ips httpserver.IPResolver, requireAuth httpserver.Middleware, logger *slog.Logger) *certificateapp.Service {
	dir := certificateDirectory{courses: courses, users: users}
	svc := certificateapp.NewService(certificatepg.NewTxRunner(pool), dir, enrollments, progress, exams, dir, ids, clk)
	certificatehttp.New(svc, certificatehttp.Config{
		RequireAuth: requireAuth, VerifyLimiter: httpserver.NewRateLimiter(120), IPs: ips, Logger: logger,
	}).Register(r)
	return svc
}

var _ certificateapp.Progress = (*progressapp.Service)(nil)
```

Move the `pgxpool` import into the third-party group so `gofmt`/`goimports` ordering matches `cmd/api/progress.go`.

- [ ] **Step 5: Wire it in `buildApp`**

In `cmd/api/app.go`:

1. Add imports:

```go
certificateevents "github.com/santoshkc2200/ioe-backend/internal/certificate/adapters/events"
```

2. Replace the `registerProgress(...)` line and add the certificate registration after `registerBlog(...)`:

```go
	progress := registerProgress(router, pool, courses, enrollmentAccess, clk, identityHandler.RequireAuth, logger)
```

```go
	certificates := registerCertificate(router, pool, courses, identityUsers{svc: identity}, enrollmentAccess, progress, assessmentQ, ids, clk, ips, identityHandler.RequireAuth, logger)
```

3. After the existing `fw.Handle(assessmentevents.DraftDiscardedTopic, ...)` call:

```go
	fw.Handle(certificateevents.PurchaseRefundedTopic, certificateevents.New(certificates, logger).PurchaseRefunded)
```

- [ ] **Step 6: Run the e2e test**

Run: `go test -race -tags integration ./cmd/api/ -run TestCertificateEndToEnd -v`
Expected: PASS. If the final `waitFor` times out, check the outbox forwarder is running in the test (`go func() { _ = a.forwarder.Run(ctx) }()`) and that `payment.purchase.refunded` reaches the new handler (add a temporary log in `PurchaseRefunded`, then remove it).

- [ ] **Step 7: Run the full unit suite and commit**

Run: `go build ./... && go test -race ./... && golangci-lint run ./...`
Expected: all PASS, no issues.

```bash
git add cmd internal
git commit -m "feat(certificate): wire the certificate context into the API"
```

---

### Task 9: OpenAPI contract and final gates

**Files:**
- Modify: `api/openapi.yaml`

- [ ] **Step 1: Add the paths**

Use Edit on the unique line `  /v1/me/purchases:` and insert the following paths before it (keep the line itself after them):

```yaml
  /v1/courses/{courseID}/certificate-policy:
    get:
      summary: Read a course's certificate policy
      description: Course managers only. A course without a policy reports `off`.
      security:
        - bearer: []
      parameters:
        - $ref: "#/components/parameters/CourseID"
      responses:
        "200":
          description: The policy
          content:
            application/json:
              schema: { $ref: "#/components/schemas/CertificatePolicy" }
        "401": { $ref: "#/components/responses/Problem" }
        "403": { $ref: "#/components/responses/Problem" }
        "404": { $ref: "#/components/responses/Problem" }
        "405": { $ref: "#/components/responses/MethodNotAllowed" }
        "500": { $ref: "#/components/responses/InternalError" }
    put:
      summary: Set a course's certificate policy
      description: >
        Course managers only. `completion_and_exam` requires an `exam_id` that belongs to the
        course; the other modes reject `exam_id`. Certificates already issued are unaffected.
      security:
        - bearer: []
      parameters:
        - $ref: "#/components/parameters/CourseID"
      requestBody:
        required: true
        content:
          application/json:
            schema: { $ref: "#/components/schemas/CertificatePolicyRequest" }
      responses:
        "200":
          description: The stored policy
          content:
            application/json:
              schema: { $ref: "#/components/schemas/CertificatePolicy" }
        "400": { $ref: "#/components/responses/Problem" }
        "401": { $ref: "#/components/responses/Problem" }
        "403": { $ref: "#/components/responses/Problem" }
        "404": { $ref: "#/components/responses/Problem" }
        "405": { $ref: "#/components/responses/MethodNotAllowed" }
        "500": { $ref: "#/components/responses/InternalError" }
  /v1/courses/{courseID}/certificate:
    post:
      summary: Claim the caller's certificate for a course
      description: >
        Issues a certificate when the caller is actively enrolled and meets the course's policy.
        Returns 201 for a new certificate and 200 with the existing one on a repeat claim.
        Refusals are 409 with a problem `type` of `certificates_disabled`, `not_enrolled`,
        `progress_incomplete` or `exam_not_passed`.
      security:
        - bearer: []
      parameters:
        - $ref: "#/components/parameters/CourseID"
      responses:
        "200":
          description: The caller already held a valid certificate
          content:
            application/json:
              schema: { $ref: "#/components/schemas/Certificate" }
        "201":
          description: Certificate issued
          content:
            application/json:
              schema: { $ref: "#/components/schemas/Certificate" }
        "401": { $ref: "#/components/responses/Problem" }
        "404": { $ref: "#/components/responses/Problem" }
        "405": { $ref: "#/components/responses/MethodNotAllowed" }
        "409": { $ref: "#/components/responses/Problem" }
        "500": { $ref: "#/components/responses/InternalError" }
    get:
      summary: Read the caller's valid certificate for a course
      description: 404 when the caller holds no valid certificate, including after a refund revoked it.
      security:
        - bearer: []
      parameters:
        - $ref: "#/components/parameters/CourseID"
      responses:
        "200":
          description: The certificate
          content:
            application/json:
              schema: { $ref: "#/components/schemas/Certificate" }
        "401": { $ref: "#/components/responses/Problem" }
        "404": { $ref: "#/components/responses/Problem" }
        "405": { $ref: "#/components/responses/MethodNotAllowed" }
        "500": { $ref: "#/components/responses/InternalError" }
  /v1/me/certificates:
    get:
      summary: List the caller's certificates
      description: Newest first, revoked ones included.
      security:
        - bearer: []
      responses:
        "200":
          description: The caller's certificates
          content:
            application/json:
              schema: { $ref: "#/components/schemas/CertificateList" }
        "401": { $ref: "#/components/responses/Problem" }
        "405": { $ref: "#/components/responses/MethodNotAllowed" }
        "500": { $ref: "#/components/responses/InternalError" }
  /v1/certificates/{code}:
    get:
      summary: Verify a certificate by code
      description: >
        Public; no credentials. Reports whether the certificate is valid or revoked and shows
        only the student name, course title and issue time. Rate limited per client IP.
        A malformed or unknown code is 404.
      parameters:
        - { name: code, in: path, required: true, schema: { type: string, pattern: "^[A-Z2-7]{26}$" } }
      responses:
        "200":
          description: The certificate
          content:
            application/json:
              schema: { $ref: "#/components/schemas/CertificateVerification" }
        "404": { $ref: "#/components/responses/Problem" }
        "405": { $ref: "#/components/responses/MethodNotAllowed" }
        "429": { $ref: "#/components/responses/Problem" }
        "500": { $ref: "#/components/responses/InternalError" }
```

- [ ] **Step 2: Add the schemas**

Use Edit on the unique line `    PurchasePage:` and insert before it:

```yaml
    CertificatePolicyRequest:
      type: object
      additionalProperties: false
      required: [mode]
      properties:
        mode: { type: string, enum: [off, completion, completion_and_exam] }
        exam_id: { type: [string, "null"], description: Required with completion_and_exam; the exam must belong to the course }
    CertificatePolicy:
      type: object
      required: [mode]
      properties:
        mode: { type: string, enum: [off, completion, completion_and_exam] }
        exam_id: { type: string, description: Present only with completion_and_exam }
    Certificate:
      type: object
      required: [id, code, course_id, course_title, student_name, issued_at, status]
      properties:
        id: { type: string }
        code: { type: string, pattern: "^[A-Z2-7]{26}$" }
        course_id: { type: string }
        course_title: { type: string, description: Snapshot at issue time }
        student_name: { type: string, description: Snapshot at issue time }
        issued_at: { type: string, format: date-time }
        status: { type: string, enum: [valid, revoked] }
    CertificateList:
      type: object
      required: [items]
      properties:
        items:
          type: array
          items: { $ref: "#/components/schemas/Certificate" }
    CertificateVerification:
      type: object
      required: [code, student_name, course_title, issued_at, status]
      properties:
        code: { type: string }
        student_name: { type: string }
        course_title: { type: string }
        issued_at: { type: string, format: date-time }
        status: { type: string, enum: [valid, revoked] }
```

- [ ] **Step 3: Validate the document**

Run: `python3 -c "import yaml,sys; yaml.safe_load(open('api/openapi.yaml'))" && grep -c "certificate" api/openapi.yaml`
Expected: no parse error; a count above 20. If the repo has an OpenAPI linter configured (`grep -rn openapi .github Makefile lefthook.yml`), run it and fix findings.

- [ ] **Step 4: Run every gate**

```bash
make check
make test-integration
docker compose config > /dev/null
make docker-build
git diff --check
```

Expected: every command exits 0. `make test-integration` needs a running Docker daemon; if Docker is down, say the integration suite did not run rather than reporting it passing.

- [ ] **Step 5: Commit**

```bash
git add api/openapi.yaml
git commit -m "docs(api): describe certificate endpoints"
```

---

## Self-Review

**Spec coverage**

- Modes `off`/`completion`/`completion_and_exam`, default off: Task 3 (`NewPolicy`), Task 4 (CHECK constraints), Task 5 (`GetPolicy` default).
- Instructor picks one exam, rejected if foreign or missing: Task 3, Task 5 (`SetPolicy` + `ExamInCourse`), Task 2.
- Claim on demand with ordered checks and 409 reasons: Task 5 (`Claim`), Task 6 (mappings).
- Record only, snapshots, public verify with exactly five fields: Task 3, Task 4, Task 6, Task 8 e2e.
- Revoke on refund when `access_revoked`: Task 7, Task 8 e2e. Reissue with a new code after revoke: Task 5, Task 4.
- One valid certificate per (course, user) under concurrency: Task 4 migration index + integration test, Task 5 convergence branch.
- Lint rules, sqlc target, OpenAPI: Task 3, Task 9.
- Spec risk about the definition of "complete": resolved in Task 1. The definition is every lecture of the live course version completed, using progress's own `CourseFacts.LectureIDs`.

**Deviations from the spec text, on purpose**

- Routes carry the `/v1` prefix every other route uses.
- The verify route gets the same per-IP rate limiter pattern as catalog routes.
- `GET /v1/me/certificates` returns `{"items":[...]}` including revoked certificates, so the frontend can show a revoked state.

**Type consistency**

Names used across tasks: `CompletedAll`, `IsComplete`, `ExamInCourse`, `HasPassed`, `NewPolicy`, `NewCertificate`, `NewCode`, `ValidCode`, `RevokeForRefund`, `PurchaseRefundedTopic`, `registerCertificate`. Port method sets in Task 5 match the concrete methods added in Tasks 1 and 2, so Task 8 wires `*progressapp.Service` and `*assessmentapp.AssessmentQuery` directly.
