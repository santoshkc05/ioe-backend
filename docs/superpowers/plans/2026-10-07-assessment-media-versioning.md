# Assessment and Media Versioning Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Students see quizzes and exams exactly as the live course version published them, attempts stay pinned to the revision they started on, `DiscardDraft` brings back the live version's quizzes and exams, and media used by the working copy or live version cannot be deleted.

**Architecture:** Assessment keeps append-only quiz and exam revisions behind head rows with soft delete. Courseauthoring pins `(kind, id, revision)` per published version, captured at submit (or from current heads on a reviewer's direct publish), and serves the live pins back to assessment through a port. `DiscardDraft` emits an outbox event that assessment handles idempotently to reset heads to the live pins. Media asks courseauthoring whether an asset is in use before deleting.

**Tech Stack:** Go, PostgreSQL, goose migrations, sqlc (`make sqlc`), pgx, Watermill outbox (`internal/platform/outbox`), net/http.

**Spec:** `docs/superpowers/specs/2026-10-07-assessment-media-versioning-design.md`

## Global Constraints

- A context never imports another context; cross-context calls go through ports in the consumer's `app` package, wired in `cmd/api` (`AGENTS.md`).
- A context reads and writes only its own schema at runtime. The single goose migration may touch both `assessment` and `courseauthoring` schemas.
- Outbox messages are written in the same transaction as the state change.
- Revisions are never updated or deleted; every quiz/exam create or edit appends `head_revision + 1`.
- Quiz and exam edits follow the course edit freeze: 409 `course_not_editable` while the course is `in_review`, `approved` or `archived`; editing a `published` course moves it to `draft`.
- Error codes: 409 `course_not_editable`, 409 `asset_in_use` (with `lecture_ids` extension), 400 `invalid_quiz_reference`, 400 `invalid_media_reference`, 404 `not_found` for unpinned student reads.
- Event name: `courseauthoring.course.draft_discarded`.
- Required gates before each commit that changes Go: `make check`; before tasks touching SQL: `make test-integration`.

## Review Focus

1. A quiz deleted in the draft while still pinned by the live version must still be served to students and accept attempts (the soft-deleted head must not hide pinned revisions). Pinned in Task 4.
2. A reviewer publishing a `draft` course directly (no submit) must still pin quizzes and exams; otherwise every assessment vanishes for students. Pinned in Task 3.
3. Redelivery of `DraftDiscarded` must not append extra revisions or undelete something deleted after the discard was processed twice in a row. Pinned in Task 6.
4. An exam open-window or time-limit extension saved in the draft does not reach attempts already open on the old revision until the course is republished, and even then only new attempts see it. Pinned in Task 5 (test documents the behaviour).
5. Unpublishing a course (live version cleared) must make every quiz and exam 404 for students, not fall back to heads. Pinned in Task 4.

---

## File Map

| File | Responsibility | Task |
|---|---|---|
| `migrations/00011_assessment_versioning.sql` | Revision tables, views, soft delete, attempt revisions, pins tables, backfill | 1 |
| `internal/assessment/domain/quiz.go`, `exam.go`, `exam_attempt.go` | `Revision` fields | 1 |
| `internal/assessment/domain/exam_edit.go`, `exam_edit_test.go` | Deleted (locks go away) | 5 |
| `internal/assessment/app/ports.go` | `Kind`, `Ref`, `Pins`, `Head`; new repository and `CourseAccess` methods | 1, 4 |
| `internal/assessment/adapters/postgres/queries.sql`, `quizzes.go`, `exams.go` | Revision storage | 2 |
| `internal/assessment/app/query.go` (renamed from `quiz_query.go`) | `AssessmentQuery`: `Lectures`, `Heads` | 2 |
| `internal/courseauthoring/domain/pins.go` | `AssessmentPin` | 3 |
| `internal/courseauthoring/domain/events.go` | `DraftDiscarded` | 3 |
| `internal/courseauthoring/app/pins.go` | `AssessmentCatalog` port; submit/publish pin logic; provider methods | 3 |
| `internal/courseauthoring/adapters/postgres/queries.sql`, `courses.go` | Pin storage | 3 |
| `internal/courseauthoring/app/assets.go` | `AssetUsage` | 7 |
| `internal/assessment/app/quiz_service.go` | Revisions, pins, version reads | 4 |
| `internal/assessment/app/exam_service.go`, `exam_attempts.go` | Revisions, pins, version reads, no locks | 5 |
| `internal/assessment/app/restore.go` | `RestoreService.RestorePins` | 6 |
| `internal/assessment/adapters/events/events.go` | Outbox handler decoding `DraftDiscarded` | 6 |
| `internal/media/app/ports.go`, `service.go`, `errors.go` | In-use check | 7 |
| `cmd/api/assessment.go`, `media.go`, `app.go` | Wiring | 8 |
| `internal/assessment/adapters/httpapi/*`, `internal/media/adapters/httpapi/httpapi.go` | `?version=`, `revision`, error codes | 8 |
| `api/openapi.yaml` | Contract | 8 |
| `cmd/api/e2e_integration_test.go` | End-to-end scenarios | 9 |

---

### Task 1: Migration and domain revision fields

**Files:**
- Create: `migrations/00011_assessment_versioning.sql`
- Modify: `internal/assessment/domain/quiz.go` (struct `Quiz`, `QuizAttempt`), `internal/assessment/domain/exam.go` (struct `Exam`), `internal/assessment/domain/exam_attempt.go` (struct `ExamAttempt`, `NewExamAttempt`)
- Test: `internal/assessment/domain/exam_test.go`

**Interfaces:**
- Produces: `domain.Quiz.Revision int`, `domain.Exam.Revision int`, `domain.QuizAttempt.Revision int`, `domain.ExamAttempt.Revision int`; `NewExamAttempt` copies `e.Revision`.
- Produces (SQL): tables `assessment.quiz_revisions`, `assessment.exam_revisions`, views `assessment.quiz_revision_rows`, `assessment.exam_revision_rows`, tables `courseauthoring.course_version_assessments`, `courseauthoring.course_submitted_assessments`.

- [x] **Step 1: Write the failing domain test**

Append to `internal/assessment/domain/exam_test.go`:

```go
func TestNewExamAttemptCopiesRevision(t *testing.T) {
	e := validExam(t)
	e.Revision = 3
	a := domain.NewExamAttempt(9, e, 200, time.Date(2026, 10, 7, 9, 0, 0, 0, time.UTC))
	if a.Revision != 3 {
		t.Fatalf("revision = %d, want 3", a.Revision)
	}
}
```

If `exam_test.go` has no `validExam(t)` helper, use the helper the file already uses to build a valid exam (grep `func .*Exam(t` in the file) and set `.Revision` on its result.

- [x] **Step 2: Run it to verify it fails**

Run: `go test ./internal/assessment/domain/ -run TestNewExamAttemptCopiesRevision`
Expected: FAIL to compile, `e.Revision undefined`.

- [x] **Step 3: Add the fields**

In `internal/assessment/domain/quiz.go`, struct `Quiz`, after `LectureID`:

```go
	Revision  int // set by storage; 0 before the first save
```

In struct `QuizAttempt`, after `QuizID`:

```go
	Revision       int // the quiz revision the answers were checked against
```

In `internal/assessment/domain/exam.go`, struct `Exam`, after `CourseID`:

```go
	Revision       int // set by storage; 0 before the first save
```

In `internal/assessment/domain/exam_attempt.go`, struct `ExamAttempt`, after `ExamID`:

```go
	Revision      int // the exam revision the attempt is taken, graded and revealed against
```

Replace `NewExamAttempt` with:

```go
func NewExamAttempt(attemptID id.ID, e Exam, userID id.ID, startedAt time.Time) ExamAttempt {
	return ExamAttempt{ID: attemptID, ExamID: e.ID, Revision: e.Revision, CourseID: e.CourseID, UserID: userID,
		StartedAt: startedAt.UTC(), Answers: []ExamAnswer{}}
}
```

Update the `Deadline` doc comment: replace "It follows the current exam, so extending either extends the attempt." with "It reads e, the attempt's own revision, so later edits never move it."

- [x] **Step 4: Run the domain tests**

Run: `go test ./internal/assessment/domain/`
Expected: PASS.

- [x] **Step 5: Write the migration**

Create `migrations/00011_assessment_versioning.sql`:

```sql
-- +goose Up
-- Quizzes and exams become head rows over append-only revisions; courses pin revisions per
-- published version. See docs/superpowers/specs/2026-10-07-assessment-media-versioning-design.md.

CREATE TABLE assessment.quiz_revisions (
  quiz_id    bigint  NOT NULL REFERENCES assessment.quizzes (id),
  revision   integer NOT NULL CHECK (revision > 0),
  position   integer NOT NULL CHECK (position >= 0),
  questions  jsonb   NOT NULL,
  created_by bigint  NOT NULL,
  created_at timestamptz NOT NULL,
  PRIMARY KEY (quiz_id, revision)
);

CREATE TABLE assessment.exam_revisions (
  exam_id            bigint  NOT NULL REFERENCES assessment.exams (id),
  revision           integer NOT NULL CHECK (revision > 0),
  title              text    NOT NULL,
  description        text    NOT NULL,
  position           integer NOT NULL CHECK (position >= 0),
  status             text    NOT NULL CHECK (status IN ('draft', 'published')),
  pass_mark          integer NOT NULL CHECK (pass_mark BETWEEN 0 AND 100),
  time_limit_seconds integer CHECK (time_limit_seconds > 0),
  retakes_allowed    boolean NOT NULL,
  opens_at           timestamptz,
  closes_at          timestamptz,
  reveal_policy      text    NOT NULL CHECK (reveal_policy IN ('after_attempt', 'after_close', 'never')),
  questions          jsonb   NOT NULL,
  created_by         bigint  NOT NULL,
  created_at         timestamptz NOT NULL,
  PRIMARY KEY (exam_id, revision)
);

INSERT INTO assessment.quiz_revisions (quiz_id, revision, position, questions, created_by, created_at)
SELECT q.id, 1, q.position, q.questions, COALESCE(c.owner_id, 0), q.updated_at
FROM assessment.quizzes q LEFT JOIN courseauthoring.courses c ON c.id = q.course_id;

INSERT INTO assessment.exam_revisions (exam_id, revision, title, description, position, status, pass_mark,
  time_limit_seconds, retakes_allowed, opens_at, closes_at, reveal_policy, questions, created_by, created_at)
SELECT e.id, 1, e.title, e.description, e.position, e.status, e.pass_mark, e.time_limit_seconds,
  e.retakes_allowed, e.opens_at, e.closes_at, e.reveal_policy, e.questions, COALESCE(c.owner_id, 0), e.updated_at
FROM assessment.exams e LEFT JOIN courseauthoring.courses c ON c.id = e.course_id;

DROP INDEX assessment.quizzes_lecture_idx;
ALTER TABLE assessment.quizzes
  DROP COLUMN position,
  DROP COLUMN questions,
  ADD COLUMN head_revision integer NOT NULL DEFAULT 1 CHECK (head_revision > 0),
  ADD COLUMN deleted_at    timestamptz;
ALTER TABLE assessment.quizzes ALTER COLUMN head_revision DROP DEFAULT;
CREATE INDEX quizzes_lecture_idx ON assessment.quizzes (course_id, lecture_id, id) WHERE deleted_at IS NULL;
CREATE INDEX quizzes_course_idx ON assessment.quizzes (course_id, id);

DROP INDEX assessment.exams_course_idx;
ALTER TABLE assessment.exams
  DROP COLUMN title, DROP COLUMN description, DROP COLUMN position, DROP COLUMN status,
  DROP COLUMN pass_mark, DROP COLUMN time_limit_seconds, DROP COLUMN retakes_allowed,
  DROP COLUMN opens_at, DROP COLUMN closes_at, DROP COLUMN reveal_policy, DROP COLUMN questions,
  ADD COLUMN head_revision integer NOT NULL DEFAULT 1 CHECK (head_revision > 0),
  ADD COLUMN deleted_at    timestamptz;
ALTER TABLE assessment.exams ALTER COLUMN head_revision DROP DEFAULT;
CREATE INDEX exams_course_idx ON assessment.exams (course_id, id);

ALTER TABLE assessment.quiz_attempts ADD COLUMN revision integer;
UPDATE assessment.quiz_attempts SET revision = 1;
ALTER TABLE assessment.quiz_attempts
  ALTER COLUMN revision SET NOT NULL,
  DROP CONSTRAINT quiz_attempts_quiz_id_fkey,
  ADD CONSTRAINT quiz_attempts_revision_fkey FOREIGN KEY (quiz_id, revision)
    REFERENCES assessment.quiz_revisions (quiz_id, revision);

ALTER TABLE assessment.exam_attempts ADD COLUMN revision integer;
UPDATE assessment.exam_attempts SET revision = 1;
ALTER TABLE assessment.exam_attempts
  ALTER COLUMN revision SET NOT NULL,
  ADD CONSTRAINT exam_attempts_revision_fkey FOREIGN KEY (exam_id, revision)
    REFERENCES assessment.exam_revisions (exam_id, revision);

-- One row per revision with its head's identity: the shape every adapter read uses.
CREATE VIEW assessment.quiz_revision_rows AS
SELECT q.id, q.course_id, q.lecture_id, r.revision, q.head_revision, q.deleted_at,
       r.position, r.questions, q.created_at, r.created_at AS updated_at
FROM assessment.quizzes q JOIN assessment.quiz_revisions r ON r.quiz_id = q.id;

CREATE VIEW assessment.exam_revision_rows AS
SELECT e.id, e.course_id, r.revision, e.head_revision, e.deleted_at, r.title, r.description,
       r.position, r.status, r.pass_mark, r.time_limit_seconds, r.retakes_allowed, r.opens_at,
       r.closes_at, r.reveal_policy, r.questions, e.created_at, r.created_at AS updated_at
FROM assessment.exams e JOIN assessment.exam_revisions r ON r.exam_id = e.id;

CREATE TABLE courseauthoring.course_version_assessments (
  course_id     bigint  NOT NULL,
  number        integer NOT NULL,
  kind          text    NOT NULL CHECK (kind IN ('quiz', 'exam')),
  assessment_id bigint  NOT NULL,
  revision      integer NOT NULL CHECK (revision > 0),
  PRIMARY KEY (course_id, number, kind, assessment_id),
  FOREIGN KEY (course_id, number)
    REFERENCES courseauthoring.course_versions (course_id, number) ON DELETE CASCADE
);

CREATE TABLE courseauthoring.course_submitted_assessments (
  course_id     bigint  NOT NULL REFERENCES courseauthoring.courses (id) ON DELETE CASCADE,
  kind          text    NOT NULL CHECK (kind IN ('quiz', 'exam')),
  assessment_id bigint  NOT NULL,
  revision      integer NOT NULL CHECK (revision > 0),
  PRIMARY KEY (course_id, kind, assessment_id)
);

-- Live versions pin revision 1 of their lectures' quizzes and every exam of the course.
INSERT INTO courseauthoring.course_version_assessments (course_id, number, kind, assessment_id, revision)
SELECT c.id, c.live_version, 'quiz', q.id, 1
FROM courseauthoring.courses c
JOIN courseauthoring.course_version_lectures l ON l.course_id = c.id AND l.number = c.live_version
JOIN assessment.quizzes q ON q.course_id = c.id AND q.lecture_id = l.id
WHERE c.live_version IS NOT NULL;
INSERT INTO courseauthoring.course_version_assessments (course_id, number, kind, assessment_id, revision)
SELECT c.id, c.live_version, 'exam', e.id, 1
FROM courseauthoring.courses c JOIN assessment.exams e ON e.course_id = c.id
WHERE c.live_version IS NOT NULL;

-- Courses awaiting or holding approval get their submission captured at revision 1.
INSERT INTO courseauthoring.course_submitted_assessments (course_id, kind, assessment_id, revision)
SELECT c.id, 'quiz', q.id, 1
FROM courseauthoring.courses c
JOIN courseauthoring.lectures l ON l.course_id = c.id
JOIN assessment.quizzes q ON q.course_id = c.id AND q.lecture_id = l.id
WHERE c.status IN ('in_review', 'approved');
INSERT INTO courseauthoring.course_submitted_assessments (course_id, kind, assessment_id, revision)
SELECT c.id, 'exam', e.id, 1
FROM courseauthoring.courses c JOIN assessment.exams e ON e.course_id = c.id
WHERE c.status IN ('in_review', 'approved');

-- +goose Down
DROP TABLE courseauthoring.course_submitted_assessments;
DROP TABLE courseauthoring.course_version_assessments;
DROP VIEW assessment.exam_revision_rows;
DROP VIEW assessment.quiz_revision_rows;

ALTER TABLE assessment.exam_attempts DROP CONSTRAINT exam_attempts_revision_fkey, DROP COLUMN revision;
ALTER TABLE assessment.quiz_attempts DROP CONSTRAINT quiz_attempts_revision_fkey, DROP COLUMN revision;

-- Soft-deleted rows had their attempts kept; drop them so the old cascade and restrict rules hold.
DELETE FROM assessment.quiz_attempts a USING assessment.quizzes q WHERE a.quiz_id = q.id AND q.deleted_at IS NOT NULL;
ALTER TABLE assessment.quiz_attempts ADD CONSTRAINT quiz_attempts_quiz_id_fkey
  FOREIGN KEY (quiz_id) REFERENCES assessment.quizzes (id) ON DELETE CASCADE;
DELETE FROM assessment.exam_attempts a USING assessment.exams e WHERE a.exam_id = e.id AND e.deleted_at IS NOT NULL;

DROP INDEX assessment.exams_course_idx;
ALTER TABLE assessment.exams
  ADD COLUMN title text, ADD COLUMN description text, ADD COLUMN position integer,
  ADD COLUMN status text, ADD COLUMN pass_mark integer, ADD COLUMN time_limit_seconds integer,
  ADD COLUMN retakes_allowed boolean, ADD COLUMN opens_at timestamptz, ADD COLUMN closes_at timestamptz,
  ADD COLUMN reveal_policy text, ADD COLUMN questions jsonb;
UPDATE assessment.exams e SET title = r.title, description = r.description, position = r.position,
  status = r.status, pass_mark = r.pass_mark, time_limit_seconds = r.time_limit_seconds,
  retakes_allowed = r.retakes_allowed, opens_at = r.opens_at, closes_at = r.closes_at,
  reveal_policy = r.reveal_policy, questions = r.questions
FROM assessment.exam_revisions r WHERE r.exam_id = e.id AND r.revision = e.head_revision;
DROP TABLE assessment.exam_revisions;
DELETE FROM assessment.exams WHERE deleted_at IS NOT NULL;
ALTER TABLE assessment.exams
  DROP COLUMN head_revision, DROP COLUMN deleted_at,
  ALTER COLUMN title SET NOT NULL, ALTER COLUMN description SET NOT NULL,
  ALTER COLUMN position SET NOT NULL, ALTER COLUMN status SET NOT NULL,
  ALTER COLUMN pass_mark SET NOT NULL, ALTER COLUMN retakes_allowed SET NOT NULL,
  ALTER COLUMN reveal_policy SET NOT NULL, ALTER COLUMN questions SET NOT NULL,
  ADD CHECK (position >= 0), ADD CHECK (status IN ('draft', 'published')),
  ADD CHECK (pass_mark BETWEEN 0 AND 100), ADD CHECK (time_limit_seconds > 0),
  ADD CHECK (reveal_policy IN ('after_attempt', 'after_close', 'never'));
CREATE INDEX exams_course_idx ON assessment.exams (course_id, position, id);

DROP INDEX assessment.quizzes_course_idx;
DROP INDEX assessment.quizzes_lecture_idx;
ALTER TABLE assessment.quizzes ADD COLUMN position integer, ADD COLUMN questions jsonb;
UPDATE assessment.quizzes q SET position = r.position, questions = r.questions
FROM assessment.quiz_revisions r WHERE r.quiz_id = q.id AND r.revision = q.head_revision;
DROP TABLE assessment.quiz_revisions;
DELETE FROM assessment.quizzes WHERE deleted_at IS NOT NULL;
ALTER TABLE assessment.quizzes
  DROP COLUMN head_revision, DROP COLUMN deleted_at,
  ALTER COLUMN position SET NOT NULL, ALTER COLUMN questions SET NOT NULL,
  ADD CHECK (position >= 0);
CREATE INDEX quizzes_lecture_idx ON assessment.quizzes (course_id, lecture_id, position, id);
```

- [x] **Step 6: Verify the migration up, down, up**

Run:
```bash
docker compose up -d postgres
make migrate-up && make migrate-down && make migrate-up && make migrate-status
```
Expected: all three succeed; status shows `00011_assessment_versioning.sql` applied. (If `make migrate-*` needs `DATABASE_URL`, export it from `.env.example`.)

- [x] **Step 7: Commit (domain fields and migration only; adapters are rebuilt in Task 2)**

The assessment postgres adapter no longer compiles against the new schema after `make sqlc`, so do NOT run `make sqlc` yet.

```bash
git add migrations/00011_assessment_versioning.sql internal/assessment/domain/quiz.go internal/assessment/domain/exam.go internal/assessment/domain/exam_attempt.go internal/assessment/domain/exam_test.go
git commit -m "feat(assessment): add revision schema and pins tables"
```

---

### Task 2: Assessment revision storage

**Files:**
- Modify: `internal/assessment/app/ports.go`
- Modify: `internal/assessment/adapters/postgres/queries.sql`, `quizzes.go`, `exams.go`
- Rename: `internal/assessment/app/quiz_query.go` → `internal/assessment/app/query.go`
- Modify: `cmd/api/app.go` (rename only), `internal/assessment/app/fakes_test.go` (compile only)
- Test: `internal/assessment/adapters/postgres/postgres_integration_test.go`, `exams_integration_test.go`

**Interfaces:**
- Consumes: Task 1 schema and `Revision` fields.
- Produces in `internal/assessment/app/ports.go`:

```go
// Kind names the assessment kinds a course version pins.
type Kind string

const (
	KindQuiz Kind = "quiz"
	KindExam Kind = "exam"
)

// Ref names one quiz or exam.
type Ref struct {
	Kind Kind
	ID   id.ID
}

// Pins maps each pinned quiz or exam to its revision.
type Pins map[Ref]int

// Head is a quiz's or exam's current revision, deleted ones included.
type Head struct {
	Ref      Ref
	Revision int
	Deleted  bool
}

// QuizRepository reads and writes quizzes in the current transaction. Find, FindForUpdate,
// ListByLecture and LecturesOf see only quizzes that are not deleted, at their head revision.
type QuizRepository interface {
	// Find returns ErrNotFound when the quiz does not exist or is deleted.
	Find(ctx context.Context, quizID id.ID) (domain.Quiz, error)
	// FindForUpdate is Find holding the quiz row lock until the transaction ends.
	FindForUpdate(ctx context.Context, quizID id.ID) (domain.Quiz, error)
	// FindRevisions returns the given revision of each quiz, deleted quizzes included, ordered
	// by position, then ID. Unknown pairs are absent.
	FindRevisions(ctx context.Context, revs map[id.ID]int) ([]domain.Quiz, error)
	// ListByLecture orders by position, then ID.
	ListByLecture(ctx context.Context, courseID, lectureID id.ID) ([]domain.Quiz, error)
	// Insert stores q as revision 1; q.Revision must be 1.
	Insert(ctx context.Context, q domain.Quiz, by id.ID) error
	// AppendRevision stores q as revision q.Revision and moves the head to it. It returns
	// ErrNotFound unless the head is q.Revision-1 and the quiz is not deleted.
	AppendRevision(ctx context.Context, q domain.Quiz, by id.ID) error
	// Delete marks the quiz deleted; a no-op when absent or already deleted.
	Delete(ctx context.Context, quizID id.ID, now time.Time) error
	// Undelete clears the deleted mark; a no-op when absent or not deleted.
	Undelete(ctx context.Context, quizID id.ID, now time.Time) error
	// Heads returns every quiz of the course, deleted ones included.
	Heads(ctx context.Context, courseID id.ID) ([]Head, error)
	// LecturesOf returns the lecture of each given quiz that belongs to courseID.
	LecturesOf(ctx context.Context, courseID id.ID, quizIDs []id.ID) (map[id.ID]id.ID, error)
	// RecordAttempt inserts the attempt and returns its ID, or returns the ID of the existing
	// attempt with the same quiz, user, and non-empty idempotency key.
	RecordAttempt(ctx context.Context, a domain.QuizAttempt) (id.ID, error)
}

// LockMode is the row lock ExamRepository.Find takes on the exam.
type LockMode int

const (
	LockNone   LockMode = iota
	LockUpdate          // FOR UPDATE: serializes edits
)

// ExamRepository reads and writes exams and their attempts in the current transaction. Find
// and ListByCourse see only exams that are not deleted, at their head revision.
type ExamRepository interface {
	// Find returns ErrNotFound when the exam does not exist or is deleted.
	Find(ctx context.Context, examID id.ID, lock LockMode) (domain.Exam, error)
	// FindRevisions has QuizRepository.FindRevisions' contract.
	FindRevisions(ctx context.Context, revs map[id.ID]int) ([]domain.Exam, error)
	// ListByCourse orders by position, then ID.
	ListByCourse(ctx context.Context, courseID id.ID) ([]domain.Exam, error)
	Insert(ctx context.Context, e domain.Exam, by id.ID) error
	AppendRevision(ctx context.Context, e domain.Exam, by id.ID) error
	Delete(ctx context.Context, examID id.ID, now time.Time) error
	Undelete(ctx context.Context, examID id.ID, now time.Time) error
	Heads(ctx context.Context, courseID id.ID) ([]Head, error)
	// NextPosition returns one past the highest head position of the course's exams, or 0.
	NextPosition(ctx context.Context, courseID id.ID) (int, error)

	// FindAttempt returns ErrNotFound when the attempt does not exist.
	FindAttempt(ctx context.Context, attemptID id.ID, forUpdate bool) (domain.ExamAttempt, error)
	// FindOpenAttempt returns ErrNotFound when the user has no open attempt on the exam.
	FindOpenAttempt(ctx context.Context, examID, userID id.ID) (domain.ExamAttempt, error)
	HasSubmitted(ctx context.Context, examID, userID id.ID) (bool, error)
	// InsertAttempt returns ErrOpenAttemptExists when the user already has an open attempt.
	InsertAttempt(ctx context.Context, a domain.ExamAttempt) error
	// MergeAnswer sets one answer on an open attempt; domain.ErrAttemptSubmitted when closed.
	MergeAnswer(ctx context.Context, attemptID id.ID, a domain.ExamAnswer) error
	// SaveResult writes an open attempt's grading fields and answers; domain.ErrAttemptSubmitted
	// when it was closed meanwhile.
	SaveResult(ctx context.Context, a domain.ExamAttempt) error
	// ListAttempts orders by start time, then ID.
	ListAttempts(ctx context.Context, examID id.ID) ([]domain.ExamAttempt, error)
	// ListUserAttempts returns the user's attempts on every exam of the course, by start time.
	ListUserAttempts(ctx context.Context, courseID, userID id.ID) ([]domain.ExamAttempt, error)
}
```

- Produces in `internal/assessment/app/query.go`: `type AssessmentQuery`, `NewAssessmentQuery(tx TxRunner) *AssessmentQuery`, methods `Lectures(ctx, courseID id.ID, quizIDs []id.ID) (map[id.ID]id.ID, error)` (unchanged behaviour) and `Heads(ctx, courseID id.ID) ([]Head, error)` returning only heads that are not deleted, quizzes then exams, each sorted by ID.

- [x] **Step 1: Write failing integration tests**

In `internal/assessment/adapters/postgres/postgres_integration_test.go`, replace the `run` helper callers' `Insert(ctx, q)` with `Insert(ctx, q, 100)` and `Replace` usages with `AppendRevision` (setting `q.Revision = 2` first); delete `TestReplaceMissingQuiz`. Add:

```go
func TestQuizRevisionsAndSoftDelete(t *testing.T) {
	tx := postgres.NewTxRunner(pgtest.Pool(t))
	q := quiz(t, 1, 10, 50, 0)
	q.Revision = 1
	run(t, tx, func(r app.QuizRepository) error { return r.Insert(ctx, q, 100) })

	edited := quiz(t, 1, 10, 50, 3)
	edited.Revision = 2
	run(t, tx, func(r app.QuizRepository) error { return r.AppendRevision(ctx, edited, 100) })

	run(t, tx, func(r app.QuizRepository) error {
		stale := edited // still claims revision 2: the head is already 2
		if err := r.AppendRevision(ctx, stale, 100); !errors.Is(err, app.ErrNotFound) {
			t.Fatalf("stale append: %v, want ErrNotFound", err)
		}
		head, err := r.Find(ctx, 1)
		if err != nil || head.Revision != 2 || head.Position != 3 {
			t.Fatalf("head = %+v, %v", head, err)
		}
		old, err := r.FindRevisions(ctx, map[id.ID]int{1: 1})
		if err != nil || len(old) != 1 || old[0].Revision != 1 || old[0].Position != 0 {
			t.Fatalf("revision 1 = %+v, %v", old, err)
		}
		return nil
	})

	run(t, tx, func(r app.QuizRepository) error { return r.Delete(ctx, 1, t0) })
	run(t, tx, func(r app.QuizRepository) error {
		if _, err := r.Find(ctx, 1); !errors.Is(err, app.ErrNotFound) {
			t.Fatalf("find deleted: %v", err)
		}
		if got, _ := r.ListByLecture(ctx, 10, 50); len(got) != 0 {
			t.Fatalf("list shows deleted quiz: %+v", got)
		}
		if got, _ := r.LecturesOf(ctx, 10, []id.ID{1}); len(got) != 0 {
			t.Fatalf("LecturesOf shows deleted quiz: %v", got)
		}
		// Pinned revisions stay readable after delete.
		if got, _ := r.FindRevisions(ctx, map[id.ID]int{1: 2}); len(got) != 1 {
			t.Fatalf("pinned revision of deleted quiz missing")
		}
		heads, err := r.Heads(ctx, 10)
		want := []app.Head{{Ref: app.Ref{Kind: app.KindQuiz, ID: 1}, Revision: 2, Deleted: true}}
		if err != nil || !slices.Equal(heads, want) {
			t.Fatalf("heads = %+v, %v", heads, err)
		}
		return nil
	})

	run(t, tx, func(r app.QuizRepository) error { return r.Undelete(ctx, 1, t0) })
	run(t, tx, func(r app.QuizRepository) error {
		_, err := r.Find(ctx, 1)
		return err
	})
}

func TestQuizAttemptKeepsRevision(t *testing.T) {
	tx := postgres.NewTxRunner(pgtest.Pool(t))
	q := quiz(t, 2, 10, 50, 0)
	q.Revision = 1
	run(t, tx, func(r app.QuizRepository) error { return r.Insert(ctx, q, 100) })
	a := domain.QuizAttempt{ID: 900, QuizID: 2, Revision: 1, UserID: 200, Answers: []domain.Answer{}, SubmittedAt: t0}
	run(t, tx, func(r app.QuizRepository) error {
		_, err := r.RecordAttempt(ctx, a)
		return err
	})
	a.ID, a.Revision = 901, 7 // no such revision
	err := tx.RunInTx(ctx, func(r app.Repos) error {
		_, err := r.Quizzes.RecordAttempt(ctx, a)
		return err
	})
	if err == nil {
		t.Fatal("attempt against a missing revision was stored")
	}
}
```

Add `"slices"` to the imports.

In `internal/assessment/adapters/postgres/exams_integration_test.go`, apply the same mechanical changes: `Insert(ctx, e)` → set `e.Revision = 1`, call `Insert(ctx, e, 100)`; `Replace(ctx, e)` → set `e.Revision++`, call `AppendRevision(ctx, e, 100)`; `ListByCourse(ctx, c, publishedOnly)` → `ListByCourse(ctx, c)`; delete tests of `Locks`, `SetPositions`, `ErrExamHasAttempts` and `LockShare`; set `Revision: e.Revision` on every attempt built for insertion. Add:

```go
func TestExamRevisionsAndSoftDelete(t *testing.T) {
	tx := postgres.NewTxRunner(pgtest.Pool(t))
	e := exam(t, 1, 10) // existing helper in this file; set position 0
	e.Revision = 1
	runExams(t, tx, func(r app.ExamRepository) error { return r.Insert(ctx, e, 100) })
	e2 := e
	e2.Revision, e2.Status, e2.Position = 2, domain.ExamPublished, 4
	runExams(t, tx, func(r app.ExamRepository) error { return r.AppendRevision(ctx, e2, 100) })
	runExams(t, tx, func(r app.ExamRepository) error { return r.Delete(ctx, 1, t0) })
	runExams(t, tx, func(r app.ExamRepository) error {
		if _, err := r.Find(ctx, 1, app.LockNone); !errors.Is(err, app.ErrNotFound) {
			t.Fatalf("find deleted: %v", err)
		}
		got, err := r.FindRevisions(ctx, map[id.ID]int{1: 1})
		if err != nil || len(got) != 1 || got[0].Status != domain.ExamDraft {
			t.Fatalf("revision 1 = %+v, %v", got, err)
		}
		next, err := r.NextPosition(ctx, 10)
		if err != nil || next != 0 {
			t.Fatalf("next position ignores deleted exams: %d, %v", next, err)
		}
		return nil
	})
}
```

If the file's helpers are named differently, use the existing exam builder and the existing per-repository runner; add a `runExams` helper mirroring `run` if none exists:

```go
func runExams(t *testing.T, tx *postgres.TxRunner, fn func(app.ExamRepository) error) {
	t.Helper()
	if err := tx.RunInTx(ctx, func(r app.Repos) error { return fn(r.Exams) }); err != nil {
		t.Fatal(err)
	}
}
```

- [x] **Step 2: Replace the queries**

Replace `internal/assessment/adapters/postgres/queries.sql` quiz and exam definition queries (keep the attempt queries, changed as shown) with:

```sql
-- name: InsertQuiz :exec
INSERT INTO assessment.quizzes (id, course_id, lecture_id, head_revision, created_at, updated_at)
VALUES ($1, $2, $3, 1, $4, $4);

-- name: InsertQuizRevision :exec
INSERT INTO assessment.quiz_revisions (quiz_id, revision, position, questions, created_by, created_at)
VALUES ($1, $2, $3, $4, $5, $6);

-- name: MoveQuizHead :execrows
UPDATE assessment.quizzes SET head_revision = @revision, updated_at = @updated_at
WHERE id = @id AND head_revision = @revision - 1 AND deleted_at IS NULL;

-- name: GetQuizHead :one
SELECT * FROM assessment.quiz_revision_rows
WHERE id = $1 AND revision = head_revision AND deleted_at IS NULL;

-- name: LockQuiz :one
SELECT id FROM assessment.quizzes WHERE id = $1 AND deleted_at IS NULL FOR UPDATE;

-- name: ListQuizRevisions :many
SELECT v.* FROM assessment.quiz_revision_rows v
JOIN unnest(@quiz_ids::bigint[], @revisions::integer[]) AS p(quiz_id, revision)
  ON v.id = p.quiz_id AND v.revision = p.revision
ORDER BY v.position, v.id;

-- name: ListQuizzesByLecture :many
SELECT * FROM assessment.quiz_revision_rows
WHERE course_id = $1 AND lecture_id = $2 AND revision = head_revision AND deleted_at IS NULL
ORDER BY position, id;

-- name: SoftDeleteQuiz :exec
UPDATE assessment.quizzes SET deleted_at = $2, updated_at = $2 WHERE id = $1 AND deleted_at IS NULL;

-- name: UndeleteQuiz :exec
UPDATE assessment.quizzes SET deleted_at = NULL, updated_at = $2 WHERE id = $1 AND deleted_at IS NOT NULL;

-- name: ListQuizHeads :many
SELECT id, head_revision, deleted_at IS NOT NULL AS deleted FROM assessment.quizzes
WHERE course_id = $1 ORDER BY id;

-- name: ListQuizLectures :many
SELECT id, lecture_id FROM assessment.quizzes
WHERE course_id = @course_id AND id = ANY(@quiz_ids::bigint[]) AND deleted_at IS NULL;

-- name: InsertQuizAttempt :one
-- Returns no row when an attempt with the same quiz, user and key exists.
INSERT INTO assessment.quiz_attempts (id, quiz_id, revision, user_id, answers, idempotency_key, submitted_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (quiz_id, user_id, idempotency_key) WHERE idempotency_key IS NOT NULL DO NOTHING
RETURNING id;

-- name: FindQuizAttemptByKey :one
SELECT id FROM assessment.quiz_attempts
WHERE quiz_id = $1 AND user_id = $2 AND idempotency_key = $3;

-- name: InsertExam :exec
INSERT INTO assessment.exams (id, course_id, head_revision, created_at, updated_at)
VALUES ($1, $2, 1, $3, $3);

-- name: InsertExamRevision :exec
INSERT INTO assessment.exam_revisions (exam_id, revision, title, description, position, status, pass_mark,
  time_limit_seconds, retakes_allowed, opens_at, closes_at, reveal_policy, questions, created_by, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15);

-- name: MoveExamHead :execrows
UPDATE assessment.exams SET head_revision = @revision, updated_at = @updated_at
WHERE id = @id AND head_revision = @revision - 1 AND deleted_at IS NULL;

-- name: GetExamHead :one
SELECT * FROM assessment.exam_revision_rows
WHERE id = $1 AND revision = head_revision AND deleted_at IS NULL;

-- name: LockExam :one
SELECT id FROM assessment.exams WHERE id = $1 AND deleted_at IS NULL FOR UPDATE;

-- name: ListExamRevisions :many
SELECT v.* FROM assessment.exam_revision_rows v
JOIN unnest(@exam_ids::bigint[], @revisions::integer[]) AS p(exam_id, revision)
  ON v.id = p.exam_id AND v.revision = p.revision
ORDER BY v.position, v.id;

-- name: ListExamsByCourse :many
SELECT * FROM assessment.exam_revision_rows
WHERE course_id = $1 AND revision = head_revision AND deleted_at IS NULL
ORDER BY position, id;

-- name: SoftDeleteExam :exec
UPDATE assessment.exams SET deleted_at = $2, updated_at = $2 WHERE id = $1 AND deleted_at IS NULL;

-- name: UndeleteExam :exec
UPDATE assessment.exams SET deleted_at = NULL, updated_at = $2 WHERE id = $1 AND deleted_at IS NOT NULL;

-- name: ListExamHeads :many
SELECT id, head_revision, deleted_at IS NOT NULL AS deleted FROM assessment.exams
WHERE course_id = $1 ORDER BY id;

-- name: NextExamPosition :one
SELECT COALESCE(MAX(position) + 1, 0)::integer AS next FROM assessment.exam_revision_rows
WHERE course_id = $1 AND revision = head_revision AND deleted_at IS NULL;

-- name: InsertExamAttempt :exec
INSERT INTO assessment.exam_attempts (id, exam_id, revision, course_id, user_id, started_at)
VALUES ($1, $2, $3, $4, $5, $6);
```

Delete the queries `ReplaceQuiz`, `GetQuiz`, `DeleteQuiz`, `ReplaceExam`, `GetExam`, `GetExamForShare`, `GetExamForUpdate`, `DeleteExam`, `SetExamPositions`, `CountExamAttempts`, `ListAnsweredExamQuestions`. Keep the remaining exam attempt queries unchanged.

- [x] **Step 3: Regenerate sqlc and check generated names**

Run: `make sqlc && grep -n "type AssessmentQuizRevisionRow\|type AssessmentExamRevisionRow" internal/assessment/adapters/postgres/sqlcgen/models.go`
Expected: both types exist. If sqlc named them differently, use the generated names in Step 4.

- [x] **Step 4: Rewrite the quiz adapter**

In `internal/assessment/adapters/postgres/quizzes.go`, keep the doc types and `questionsJSON`/`toQuestions`; replace the repository methods with:

```go
func (r quizzes) Find(ctx context.Context, quizID id.ID) (domain.Quiz, error) {
	row, err := r.q.GetQuizHead(ctx, int64(quizID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Quiz{}, app.ErrNotFound
	}
	if err != nil {
		return domain.Quiz{}, err
	}
	return toQuiz(row)
}

func (r quizzes) FindForUpdate(ctx context.Context, quizID id.ID) (domain.Quiz, error) {
	if _, err := r.q.LockQuiz(ctx, int64(quizID)); errors.Is(err, pgx.ErrNoRows) {
		return domain.Quiz{}, app.ErrNotFound
	} else if err != nil {
		return domain.Quiz{}, err
	}
	return r.Find(ctx, quizID)
}

func (r quizzes) FindRevisions(ctx context.Context, revs map[id.ID]int) ([]domain.Quiz, error) {
	ids, numbers := revisionArgs(revs)
	rows, err := r.q.ListQuizRevisions(ctx, sqlcgen.ListQuizRevisionsParams{QuizIds: ids, Revisions: numbers})
	if err != nil {
		return nil, err
	}
	return toQuizzes(rows)
}

func (r quizzes) ListByLecture(ctx context.Context, courseID, lectureID id.ID) ([]domain.Quiz, error) {
	rows, err := r.q.ListQuizzesByLecture(ctx, sqlcgen.ListQuizzesByLectureParams{CourseID: int64(courseID), LectureID: int64(lectureID)})
	if err != nil {
		return nil, err
	}
	return toQuizzes(rows)
}

func (r quizzes) Insert(ctx context.Context, q domain.Quiz, by id.ID) error {
	if q.Revision != 1 {
		return fmt.Errorf("insert quiz %s: revision %d, want 1", q.ID, q.Revision)
	}
	if err := r.q.InsertQuiz(ctx, sqlcgen.InsertQuizParams{
		ID: int64(q.ID), CourseID: int64(q.CourseID), LectureID: int64(q.LectureID), CreatedAt: q.CreatedAt,
	}); err != nil {
		return err
	}
	return r.insertRevision(ctx, q, by)
}

func (r quizzes) AppendRevision(ctx context.Context, q domain.Quiz, by id.ID) error {
	n, err := r.q.MoveQuizHead(ctx, sqlcgen.MoveQuizHeadParams{ID: int64(q.ID), Revision: int32(q.Revision), UpdatedAt: q.UpdatedAt}) //nolint:gosec // one per edit
	if err != nil {
		return err
	}
	if n == 0 {
		return app.ErrNotFound
	}
	return r.insertRevision(ctx, q, by)
}

func (r quizzes) insertRevision(ctx context.Context, q domain.Quiz, by id.ID) error {
	doc, err := questionsJSON(q.Questions)
	if err != nil {
		return err
	}
	return r.q.InsertQuizRevision(ctx, sqlcgen.InsertQuizRevisionParams{
		QuizID: int64(q.ID), Revision: int32(q.Revision), Position: int32(q.Position), //nolint:gosec // domain.NewQuiz bounds position; revisions grow one per edit
		Questions: doc, CreatedBy: int64(by), CreatedAt: q.UpdatedAt,
	})
}

func (r quizzes) Delete(ctx context.Context, quizID id.ID, now time.Time) error {
	return r.q.SoftDeleteQuiz(ctx, sqlcgen.SoftDeleteQuizParams{ID: int64(quizID), DeletedAt: &now})
}

func (r quizzes) Undelete(ctx context.Context, quizID id.ID, now time.Time) error {
	return r.q.UndeleteQuiz(ctx, sqlcgen.UndeleteQuizParams{ID: int64(quizID), UpdatedAt: now})
}

func (r quizzes) Heads(ctx context.Context, courseID id.ID) ([]app.Head, error) {
	rows, err := r.q.ListQuizHeads(ctx, int64(courseID))
	if err != nil {
		return nil, err
	}
	out := make([]app.Head, len(rows))
	for i, row := range rows {
		out[i] = app.Head{Ref: app.Ref{Kind: app.KindQuiz, ID: id.ID(row.ID)}, Revision: int(row.HeadRevision), Deleted: row.Deleted}
	}
	return out, nil
}
```

Keep `LecturesOf` unchanged. In `RecordAttempt`, add `Revision: int32(a.Revision), //nolint:gosec // bounded by stored revisions` to `InsertQuizAttemptParams`. Replace `toQuiz` with:

```go
// toQuiz rebuilds the quiz without re-validating: stored rows were validated on write.
func toQuiz(row sqlcgen.AssessmentQuizRevisionRow) (domain.Quiz, error) {
	qs, err := toQuestions(row.Questions)
	if err != nil {
		return domain.Quiz{}, err
	}
	return domain.Quiz{ID: id.ID(row.ID), CourseID: id.ID(row.CourseID), LectureID: id.ID(row.LectureID),
		Revision: int(row.Revision), Position: int(row.Position), Questions: qs,
		CreatedAt: row.CreatedAt.UTC(), UpdatedAt: row.UpdatedAt.UTC()}, nil
}

func toQuizzes(rows []sqlcgen.AssessmentQuizRevisionRow) ([]domain.Quiz, error) {
	out := make([]domain.Quiz, len(rows))
	for i, row := range rows {
		var err error
		if out[i], err = toQuiz(row); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// revisionArgs flattens revs into the parallel arrays the revision queries unnest.
func revisionArgs(revs map[id.ID]int) ([]int64, []int32) {
	ids, numbers := make([]int64, 0, len(revs)), make([]int32, 0, len(revs))
	for k, v := range revs {
		ids, numbers = append(ids, int64(k)), append(numbers, int32(v)) //nolint:gosec // revisions grow one per edit
	}
	return ids, numbers
}
```

The `SoftDeleteQuizParams` field holding `$2` may be generated as `DeletedAt *time.Time` or `time.Time` depending on inference; match the generated type (pass `now` directly if it is `time.Time`). Add `"fmt"` and `"time"` imports.

- [x] **Step 5: Rewrite the exam adapter**

In `internal/assessment/adapters/postgres/exams.go`, apply the same pattern:
- `Find(ctx, examID, lock)`: when `lock == app.LockUpdate`, call `r.q.LockExam` first (map `pgx.ErrNoRows` to `app.ErrNotFound`), then `r.q.GetExamHead`.
- `FindRevisions`, `ListByCourse(ctx, courseID)`, `Insert(ctx, e, by)` (insert head with `CreatedAt: e.CreatedAt`, then revision), `AppendRevision(ctx, e, by)` (`MoveExamHead` then revision; 0 rows → `app.ErrNotFound`), `Delete`, `Undelete`, `Heads` (with `Kind: app.KindExam`) mirror the quiz methods.
- `insertRevision` maps every setting to `InsertExamRevisionParams` exactly as the old `InsertExam` call mapped them to the head columns, plus `Revision`, `CreatedBy: int64(by)`, `CreatedAt: e.UpdatedAt`.
- The old row converter becomes `toExam(row sqlcgen.AssessmentExamRevisionRow)` and sets `Revision: int(row.Revision)`.
- `InsertAttempt` passes `Revision: int32(a.Revision)`; the attempt row converter sets `Revision: int(row.Revision)`.
- Delete `Replace`, `SetPositions`, `Locks`, and the `ErrExamHasAttempts` foreign-key mapping in `Delete`.

- [x] **Step 6: Rename QuizQuery and add Heads**

`git mv internal/assessment/app/quiz_query.go internal/assessment/app/query.go`, then replace its contents with:

```go
package app

import (
	"context"

	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

// AssessmentQuery answers internal questions about quizzes and exams. It applies no
// authorization and must not be exposed over HTTP. It is separate from the services so
// courseauthoring can depend on it while the services depend on courseauthoring.
type AssessmentQuery struct{ tx TxRunner }

func NewAssessmentQuery(tx TxRunner) *AssessmentQuery { return &AssessmentQuery{tx: tx} }

// Lectures returns the lecture of each given quiz that belongs to courseID; other IDs are absent.
func (q *AssessmentQuery) Lectures(ctx context.Context, courseID id.ID, quizIDs []id.ID) (map[id.ID]id.ID, error) {
	if len(quizIDs) == 0 {
		return map[id.ID]id.ID{}, nil
	}
	var out map[id.ID]id.ID
	err := q.tx.RunInTx(ctx, func(r Repos) error {
		var err error
		out, err = r.Quizzes.LecturesOf(ctx, courseID, quizIDs)
		return err
	})
	return out, err
}

// Heads returns the head revision of every quiz and exam of the course that is not deleted:
// quizzes first, then exams, each by ID.
func (q *AssessmentQuery) Heads(ctx context.Context, courseID id.ID) ([]Head, error) {
	var out []Head
	err := q.tx.RunInTx(ctx, func(r Repos) error {
		quizzes, err := r.Quizzes.Heads(ctx, courseID)
		if err != nil {
			return err
		}
		exams, err := r.Exams.Heads(ctx, courseID)
		if err != nil {
			return err
		}
		for _, h := range append(quizzes, exams...) {
			if !h.Deleted {
				out = append(out, h)
			}
		}
		return nil
	})
	return out, err
}
```

In `cmd/api/app.go`, replace `assessmentapp.NewQuizQuery(assessmentTx)` with `assessmentapp.NewAssessmentQuery(assessmentTx)`. Replace `fixture.query *app.QuizQuery` / `app.NewQuizQuery` in `internal/assessment/app/*_test.go` likewise.

- [x] **Step 7: Run the integration tests**

Run: `go test -tags integration ./internal/assessment/adapters/postgres/...`
Expected: PASS. (The `app` package and its tests do not compile yet; they are rebuilt in Tasks 4 and 5. `go build ./internal/assessment/adapters/postgres/` must succeed.)

- [x] **Step 8: Commit**

```bash
git add internal/assessment/app/ports.go internal/assessment/app/query.go internal/assessment/adapters/postgres sqlc.yaml cmd/api/app.go
git commit -m "feat(assessment): store quizzes and exams as revisions"
```

The `assessment/app` services are broken between this commit and Task 5. If the executor requires every commit to pass `make check`, squash Tasks 2, 4 and 5 into one commit at the end of Task 5 instead.

---

### Task 3: Courseauthoring pins, submit and publish

**Files:**
- Create: `internal/courseauthoring/domain/pins.go`
- Modify: `internal/courseauthoring/domain/events.go`
- Create: `internal/courseauthoring/app/pins.go`
- Modify: `internal/courseauthoring/app/ports.go`, `course_service.go` (`NewCourseService`, `Submit`, `Publish`, `DiscardDraft`), `quizrefs.go`, `assets.go`
- Modify: `internal/courseauthoring/adapters/postgres/queries.sql`, `courses.go`
- Modify: `internal/courseauthoring/app/fakes_test.go`, `cmd/api/app.go` (constructor argument)
- Test: `internal/courseauthoring/app/versions_test.go`, `internal/courseauthoring/adapters/postgres/postgres_integration_test.go`

**Interfaces:**
- Produces in `internal/courseauthoring/domain/pins.go`:

```go
package domain

import "github.com/santoshkc2200/ioe-backend/internal/platform/id"

// AssessmentKind is a kind of assessment a course version pins.
type AssessmentKind string

const (
	AssessmentQuiz AssessmentKind = "quiz"
	AssessmentExam AssessmentKind = "exam"
)

// AssessmentPin fixes the revision of one quiz or exam that a course version shows.
type AssessmentPin struct {
	Kind     AssessmentKind `json:"kind"`
	ID       id.ID          `json:"id"`
	Revision int            `json:"revision"`
}
```

- Produces in `events.go`:

```go
// DraftDiscarded carries the live version's pins so assessment can reset its working copy.
type DraftDiscarded struct {
	CourseID   id.ID           `json:"course_id"`
	ActorID    id.ID           `json:"actor_id"`
	Pins       []AssessmentPin `json:"pins"`
	OccurredAt time.Time       `json:"occurred_at"`
}

func (DraftDiscarded) EventName() string { return "courseauthoring.course.draft_discarded" }
```

- Produces in `app/pins.go`: `type AssessmentCatalog interface { Heads(ctx context.Context, courseID id.ID) ([]domain.AssessmentPin, error) }`.
- Produces on `CourseRepository`:

```go
	// ReplaceSubmittedPins replaces the pins captured at the course's last submit.
	ReplaceSubmittedPins(ctx context.Context, courseID id.ID, pins []domain.AssessmentPin) error
	ListSubmittedPins(ctx context.Context, courseID id.ID) ([]domain.AssessmentPin, error)
	InsertVersionPins(ctx context.Context, courseID id.ID, number int, pins []domain.AssessmentPin) error
	// ListVersionPins returns version number's pins, by kind then ID; empty when none.
	ListVersionPins(ctx context.Context, courseID id.ID, number int) ([]domain.AssessmentPin, error)
```

- Changes: `NewCourseService(tx TxRunner, ids *id.Generator, c clock.Clock, assets AssetCatalog, quizzes QuizCatalog, assessments AssessmentCatalog) *CourseService`.

- [x] **Step 1: Write failing app tests**

In `internal/courseauthoring/app/fakes_test.go`, add a fake catalog and pin storage to the fake course repository:

```go
type fakeAssessments struct {
	heads map[id.ID][]domain.AssessmentPin
	calls int
}

func (f *fakeAssessments) Heads(_ context.Context, courseID id.ID) ([]domain.AssessmentPin, error) {
	f.calls++
	return slices.Clone(f.heads[courseID]), nil
}
```

Give the in-memory course repository two maps, `submitted map[id.ID][]domain.AssessmentPin` and `versionPins map[[2]int64][]domain.AssessmentPin` (key `{courseID, number}`), copied and restored with the rest of its state on rollback, and implement the four new methods over them (`ListSubmittedPins` and `ListVersionPins` sort by kind, then ID, as the SQL does; `ListVersionPins` returns `nil` for a missing key). Pass `&fakeAssessments{}` as the new `NewCourseService` argument wherever tests build the service.

Append to `internal/courseauthoring/app/versions_test.go`:

```go
func TestSubmitCapturesPinsAndPublishCopiesThem(t *testing.T) {
	f := newFixture(t) // existing fixture builder; course with one lecture owned by owner
	quizPin := domain.AssessmentPin{Kind: domain.AssessmentQuiz, ID: 700, Revision: 2}
	examPin := domain.AssessmentPin{Kind: domain.AssessmentExam, ID: 800, Revision: 1}
	f.assessments.heads[f.courseID] = []domain.AssessmentPin{quizPin, examPin}

	if err := f.courses.Submit(ctx, owner, f.courseID); err != nil {
		t.Fatal(err)
	}
	// An edit landing after submit must not reach the published version.
	f.assessments.heads[f.courseID] = []domain.AssessmentPin{{Kind: domain.AssessmentQuiz, ID: 700, Revision: 3}, examPin}
	if err := f.courses.Approve(ctx, reviewer, f.courseID, ""); err != nil {
		t.Fatal(err)
	}
	if err := f.courses.Publish(ctx, owner, f.courseID); err != nil {
		t.Fatal(err)
	}
	pins, live, err := f.courses.LivePins(ctx, f.courseID)
	if err != nil || !live {
		t.Fatalf("live pins: %v, live=%v", err, live)
	}
	if want := []domain.AssessmentPin{examPin, quizPin}; !slices.Equal(pins, want) {
		t.Fatalf("pins = %+v, want %+v", pins, want)
	}
}

func TestReviewerDirectPublishPinsHeads(t *testing.T) {
	f := newFixture(t)
	head := domain.AssessmentPin{Kind: domain.AssessmentQuiz, ID: 700, Revision: 4}
	f.assessments.heads[f.courseID] = []domain.AssessmentPin{head}
	if err := f.courses.Publish(ctx, reviewer, f.courseID); err != nil { // draft, reviewer bypass
		t.Fatal(err)
	}
	pins, _, err := f.courses.LivePins(ctx, f.courseID)
	if err != nil || !slices.Equal(pins, []domain.AssessmentPin{head}) {
		t.Fatalf("pins = %+v, %v", pins, err)
	}
}

func TestSubmitRejectsDanglingQuizBlock(t *testing.T) {
	f := newFixture(t)
	f.putQuizBlock(t, f.lectureID, 700) // existing helper or Replace call writing a quiz block
	f.quizCatalog.lectures = map[id.ID]id.ID{} // quiz 700 deleted meanwhile
	err := f.courses.Submit(ctx, owner, f.courseID)
	if !errors.Is(err, app.ErrInvalidQuizReference) {
		t.Fatalf("submit = %v, want ErrInvalidQuizReference", err)
	}
}

func TestDiscardDraftPublishesLivePins(t *testing.T) {
	f := newFixture(t)
	pin := domain.AssessmentPin{Kind: domain.AssessmentExam, ID: 800, Revision: 1}
	f.assessments.heads[f.courseID] = []domain.AssessmentPin{pin}
	if err := f.courses.Publish(ctx, reviewer, f.courseID); err != nil {
		t.Fatal(err)
	}
	f.editDetails(t) // existing helper or UpdateDetails call: published → draft
	if _, err := f.courses.DiscardDraft(ctx, owner, f.courseID); err != nil {
		t.Fatal(err)
	}
	ev := f.lastEvent(t).(domain.DraftDiscarded)
	if ev.CourseID != f.courseID || ev.ActorID != owner.UserID || !slices.Equal(ev.Pins, []domain.AssessmentPin{pin}) {
		t.Fatalf("event = %+v", ev)
	}
}
```

Adapt `newFixture`, `reviewer`, `putQuizBlock`, `editDetails`, `lastEvent` and the quiz catalog field to what `fakes_test.go` and the existing `versions_test.go` / `review_test.go` already provide; add the missing ones with these names. `f.assessments` is the `*fakeAssessments` passed to `NewCourseService`, initialised with `heads: map[id.ID][]domain.AssessmentPin{}`.

- [x] **Step 2: Run them to verify they fail**

Run: `go test ./internal/courseauthoring/app/ -run 'Pins|DirectPublish|Dangling|DiscardDraftPublishes'`
Expected: FAIL to compile (`LivePins`, `AssessmentPin`, `DraftDiscarded` undefined).

- [x] **Step 3: Implement domain types, port and storage**

Create `internal/courseauthoring/domain/pins.go` and add `DraftDiscarded` to `events.go` as listed under Interfaces. Add the four methods to `CourseRepository` in `app/ports.go` with the doc comments listed.

Append to `internal/courseauthoring/adapters/postgres/queries.sql`:

```sql
-- name: DeleteSubmittedAssessments :exec
DELETE FROM courseauthoring.course_submitted_assessments WHERE course_id = $1;

-- name: InsertSubmittedAssessments :exec
INSERT INTO courseauthoring.course_submitted_assessments (course_id, kind, assessment_id, revision)
SELECT @course_id, k, a, r
FROM unnest(@kinds::text[], @assessment_ids::bigint[], @revisions::integer[]) AS p(k, a, r);

-- name: ListSubmittedAssessments :many
SELECT kind, assessment_id, revision FROM courseauthoring.course_submitted_assessments
WHERE course_id = $1 ORDER BY kind, assessment_id;

-- name: InsertVersionAssessments :exec
INSERT INTO courseauthoring.course_version_assessments (course_id, number, kind, assessment_id, revision)
SELECT @course_id, @number, k, a, r
FROM unnest(@kinds::text[], @assessment_ids::bigint[], @revisions::integer[]) AS p(k, a, r);

-- name: ListVersionAssessments :many
SELECT kind, assessment_id, revision FROM courseauthoring.course_version_assessments
WHERE course_id = $1 AND number = $2 ORDER BY kind, assessment_id;
```

Run `make sqlc`. In `internal/courseauthoring/adapters/postgres/courses.go` add:

```go
func pinArgs(pins []domain.AssessmentPin) ([]string, []int64, []int32) {
	kinds, ids, revs := make([]string, len(pins)), make([]int64, len(pins)), make([]int32, len(pins))
	for i, p := range pins {
		kinds[i], ids[i], revs[i] = string(p.Kind), int64(p.ID), int32(p.Revision) //nolint:gosec // revisions grow one per edit
	}
	return kinds, ids, revs
}

func (r courses) ReplaceSubmittedPins(ctx context.Context, courseID id.ID, pins []domain.AssessmentPin) error {
	if err := r.q.DeleteSubmittedAssessments(ctx, int64(courseID)); err != nil {
		return err
	}
	kinds, ids, revs := pinArgs(pins)
	return r.q.InsertSubmittedAssessments(ctx, sqlcgen.InsertSubmittedAssessmentsParams{
		CourseID: int64(courseID), Kinds: kinds, AssessmentIds: ids, Revisions: revs})
}

func (r courses) ListSubmittedPins(ctx context.Context, courseID id.ID) ([]domain.AssessmentPin, error) {
	rows, err := r.q.ListSubmittedAssessments(ctx, int64(courseID))
	if err != nil {
		return nil, err
	}
	out := make([]domain.AssessmentPin, len(rows))
	for i, row := range rows {
		out[i] = domain.AssessmentPin{Kind: domain.AssessmentKind(row.Kind), ID: id.ID(row.AssessmentID), Revision: int(row.Revision)}
	}
	return out, nil
}

func (r courses) InsertVersionPins(ctx context.Context, courseID id.ID, number int, pins []domain.AssessmentPin) error {
	kinds, ids, revs := pinArgs(pins)
	return r.q.InsertVersionAssessments(ctx, sqlcgen.InsertVersionAssessmentsParams{
		CourseID: int64(courseID), Number: int32(number), Kinds: kinds, AssessmentIds: ids, Revisions: revs}) //nolint:gosec // bounded by LastVersion
}

func (r courses) ListVersionPins(ctx context.Context, courseID id.ID, number int) ([]domain.AssessmentPin, error) {
	rows, err := r.q.ListVersionAssessments(ctx, sqlcgen.ListVersionAssessmentsParams{CourseID: int64(courseID), Number: int32(number)}) //nolint:gosec // bounded by LastVersion
	if err != nil {
		return nil, err
	}
	out := make([]domain.AssessmentPin, len(rows))
	for i, row := range rows {
		out[i] = domain.AssessmentPin{Kind: domain.AssessmentKind(row.Kind), ID: id.ID(row.AssessmentID), Revision: int(row.Revision)}
	}
	return out, nil
}
```

Match the generated param field names (e.g. `CourseID` vs `CourseID_2`) to what `make sqlc` produced.

- [x] **Step 4: Implement submit, publish, discard and reads**

Add the `assessments AssessmentCatalog` field and constructor parameter to `CourseService`. In `cmd/api/app.go`, `registerCourseAuthoring` gains a parameter `assessments courseauthoringapp.AssessmentCatalog`, passed through to `NewCourseService`; pass `assessmentHeads{query: assessmentapp.NewAssessmentQuery(assessmentTx)}` from `newApplication`, with this adapter in `cmd/api/assessment.go`:

```go
// assessmentHeads lets course authoring pin the current quiz and exam revisions.
type assessmentHeads struct{ query *assessmentapp.AssessmentQuery }

func (a assessmentHeads) Heads(ctx context.Context, courseID id.ID) ([]courseauthoringdomain.AssessmentPin, error) {
	heads, err := a.query.Heads(ctx, courseID)
	if err != nil {
		return nil, err
	}
	out := make([]courseauthoringdomain.AssessmentPin, len(heads))
	for i, h := range heads {
		out[i] = courseauthoringdomain.AssessmentPin{Kind: courseauthoringdomain.AssessmentKind(h.Ref.Kind), ID: h.Ref.ID, Revision: h.Revision}
	}
	return out, nil
}
```

Create `internal/courseauthoring/app/pins.go`:

```go
package app

import (
	"context"

	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/contentblocks"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

// AssessmentCatalog reports the head revision of every quiz and exam of a course that is not
// deleted.
type AssessmentCatalog interface {
	Heads(ctx context.Context, courseID id.ID) ([]domain.AssessmentPin, error)
}

// workingBlocks returns each working-copy lecture's blocks, after authorizing p as a manager.
func workingBlocks(ctx context.Context, r Repos, p auth.Principal, courseID id.ID) (map[id.ID][]contentblocks.Block, error) {
	c, err := loadManaged(ctx, r, p, courseID)
	if err != nil {
		return nil, err
	}
	out := make(map[id.ID][]contentblocks.Block, len(c.Lectures))
	for _, l := range c.Lectures {
		if out[l.ID], err = r.Contents.ListBlocks(ctx, l.ID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// checkStoredRefs re-checks the media and quiz references of stored blocks, lecture by lecture.
func (s *CourseService) checkStoredRefs(ctx context.Context, courseID id.ID, blocks map[id.ID][]contentblocks.Block) (refErr, err error) {
	for lectureID, bs := range blocks {
		if refErr, err = checkAssetRefs(ctx, s.assets, courseID, blockAssetRefs(bs)); refErr != nil || err != nil {
			return refErr, err
		}
		if refErr, err = checkQuizRefs(ctx, s.quizzes, courseID, lectureID, blockQuizRefs(bs)); refErr != nil || err != nil {
			return refErr, err
		}
	}
	return nil, nil
}

// LivePins returns the live version's pins, and false when the course is not live. It applies
// no authorization: assessment serves students from it after its own read checks.
func (s *CourseService) LivePins(ctx context.Context, courseID id.ID) ([]domain.AssessmentPin, bool, error) {
	var pins []domain.AssessmentPin
	var live bool
	err := s.tx.RunInTx(ctx, func(r Repos) error {
		c, err := r.Courses.FindByID(ctx, courseID)
		if err != nil || !c.IsLive() {
			return err
		}
		live = true
		pins, err = r.Courses.ListVersionPins(ctx, courseID, c.Live.Number)
		return err
	})
	return pins, live, err
}

// VersionPins returns a published version's pins to the course's managers.
func (s *CourseService) VersionPins(ctx context.Context, p auth.Principal, courseID id.ID, number int) ([]domain.AssessmentPin, error) {
	var pins []domain.AssessmentPin
	err := s.tx.RunInTx(ctx, func(r Repos) error {
		c, err := loadManaged(ctx, r, p, courseID)
		if err != nil {
			return err
		}
		if number < 1 || number > c.LastVersion {
			return ErrNotFound
		}
		pins, err = r.Courses.ListVersionPins(ctx, courseID, number)
		return err
	})
	return pins, err
}
```

In `internal/courseauthoring/app/assets.go`, add a block-based variant of `inputAssetRefs`:

```go
// blockAssetRefs lists the media assets stored blocks reference.
func blockAssetRefs(blocks []contentblocks.Block) []assetRef {
	var refs []assetRef
	for _, b := range blocks {
		cid := b.ClientBlockID()
		if v, ok := b.Video(); ok {
			refs = append(refs, assetRef{id: v.MediaAssetID(), kind: AssetVideo, clientBlockID: cid})
		}
		if img, ok := b.Image(); ok {
			refs = append(refs, assetRef{id: img.MediaAssetID(), kind: AssetImage, clientBlockID: cid})
		}
		if deck, ok := b.Deck(); ok {
			for _, c := range deck.Cards() {
				if !c.MediaAssetID.IsZero() {
					refs = append(refs, assetRef{id: c.MediaAssetID, kind: AssetImage, clientBlockID: cid})
				}
			}
		}
	}
	return refs
}
```

In `internal/courseauthoring/app/quizrefs.go`, add:

```go
// blockQuizRefs lists the quizzes stored blocks reference.
func blockQuizRefs(blocks []contentblocks.Block) []quizRef {
	var refs []quizRef
	for _, b := range blocks {
		if b.Type() == contentblocks.BlockTypeQuiz {
			refs = append(refs, quizRef{id: b.QuizID(), clientBlockID: b.ClientBlockID()})
		}
	}
	return refs
}
```

Replace `CourseService.Submit` in `course_service.go` with:

```go
// Submit sends the course to review after re-checking every block reference, and captures the
// current quiz and exam revisions as what review approves. Any manager may submit.
func (s *CourseService) Submit(ctx context.Context, p auth.Principal, courseID id.ID) error {
	var blocks map[id.ID][]contentblocks.Block
	if err := s.tx.RunInTx(ctx, func(r Repos) error {
		var err error
		blocks, err = workingBlocks(ctx, r, p, courseID)
		return err
	}); err != nil {
		return err
	}
	if refErr, err := s.checkStoredRefs(ctx, courseID, blocks); refErr != nil || err != nil {
		return cmp.Or(err, refErr)
	}
	heads, err := s.assessments.Heads(ctx, courseID)
	if err != nil {
		return err
	}
	_, err = s.mutate(ctx, p, courseID, func(r Repos, c *domain.Course) error {
		rev, err := c.Submit(s.ids.New(), p.UserID, s.clock.Now())
		if err != nil {
			return err
		}
		if err := r.Courses.ReplaceSubmittedPins(ctx, courseID, heads); err != nil {
			return err
		}
		return r.Courses.InsertReview(ctx, rev)
	})
	return err
}
```

Replace `CourseService.Publish` with:

```go
// Publish snapshots the working copy as the next live version. A reviewed course pins the
// revisions captured at submit; a reviewer's direct publish pins the current heads.
func (s *CourseService) Publish(ctx context.Context, p auth.Principal, courseID id.ID) error {
	heads, err := s.assessments.Heads(ctx, courseID)
	if err != nil {
		return err
	}
	_, err = s.mutate(ctx, p, courseID, func(r Repos, c *domain.Course) error {
		reviewed := c.Status == domain.StatusInReview || c.Status == domain.StatusApproved
		now := s.clock.Now()
		if err := c.Publish(p.Role == auth.RoleRootAdmin, now); err != nil {
			return err
		}
		if err := r.Courses.InsertVersion(ctx, c, p.UserID); err != nil {
			return err
		}
		pins := heads
		if reviewed {
			if pins, err = r.Courses.ListSubmittedPins(ctx, c.ID); err != nil {
				return err
			}
		}
		if err := r.Courses.InsertVersionPins(ctx, c.ID, c.Live.Number, pins); err != nil {
			return err
		}
		return r.Events.Publish(ctx, domain.CoursePublished{CourseID: c.ID, OwnerID: c.OwnerID,
			PriceAmountMinor: c.Price.AmountMinor, PriceCurrency: c.Price.Currency, OccurredAt: now})
	})
	return err
}
```

Heads are read before authorization here; that leaks nothing because they are discarded unless the publish succeeds. In `DiscardDraft`, after the block loop and before `out = c`, add:

```go
		pins, err := r.Courses.ListVersionPins(ctx, courseID, c.Live.Number)
		if err != nil {
			return err
		}
		if err := r.Events.Publish(ctx, domain.DraftDiscarded{CourseID: courseID, ActorID: p.UserID,
			Pins: pins, OccurredAt: s.clock.Now()}); err != nil {
			return err
		}
```

Add `"cmp"` and `contentblocks` imports to `course_service.go` as needed.

- [x] **Step 5: Run the app tests**

Run: `go test ./internal/courseauthoring/...`
Expected: PASS.

- [x] **Step 6: Integration test for pin storage**

Append to `internal/courseauthoring/adapters/postgres/postgres_integration_test.go` a test that inserts a course, publishes version 1 via `InsertVersion`, calls `InsertVersionPins(ctx, courseID, 1, pins)` with one quiz and one exam pin, then asserts `ListVersionPins(ctx, courseID, 1)` returns them ordered exam then quiz, that `ListVersionPins(ctx, courseID, 2)` is empty, and that `ReplaceSubmittedPins` twice leaves only the second set in `ListSubmittedPins`. Use the file's existing course builder and transaction helper.

Run: `go test -tags integration ./internal/courseauthoring/adapters/postgres/...`
Expected: PASS.

- [x] **Step 7: Commit**

```bash
git add internal/courseauthoring cmd/api/app.go cmd/api/assessment.go
git commit -m "feat(courseauthoring): pin quiz and exam revisions per version"
```

---

### Task 4: Quiz service on revisions and pins

**Files:**
- Modify: `internal/assessment/app/ports.go` (`CourseAccess`), `quiz_service.go`
- Create: `internal/assessment/app/pins.go`
- Modify: `internal/assessment/app/fakes_test.go`
- Modify: `internal/courseauthoring/app/pins.go` (`BeginAssessmentEdit`), `cmd/api/assessment.go`
- Test: `internal/assessment/app/quiz_service_test.go`

**Interfaces:**
- Consumes: Task 2 repository and `Pins`.
- Produces: `CourseAccess`:

```go
// CourseAccess is backed by courseauthoring.
type CourseAccess interface {
	// BeginEdit authorizes p to change the course's quizzes and exams and applies the course
	// edit rule: ErrNotFound, ErrForbidden, or ErrCourseNotEditable while archived, in review or
	// approved; a published course moves to draft. lectureID zero skips the lecture check;
	// otherwise ErrNotFound when the lecture is not in the course's working copy.
	BeginEdit(ctx context.Context, p auth.Principal, courseID, lectureID id.ID) error
	// CanReadLecture applies the lecture content read rule: ErrNotFound or
	// ErrEnrollmentRequired.
	CanReadLecture(ctx context.Context, p auth.Principal, courseID, lectureID id.ID) error
	// CanReadAsManager returns nil when p manages the course, archived included; otherwise
	// ErrNotFound or ErrForbidden.
	CanReadAsManager(ctx context.Context, p auth.Principal, courseID id.ID) error
	// CanReadCourse returns nil when the course is visible to p (published, or p manages it);
	// otherwise ErrNotFound.
	CanReadCourse(ctx context.Context, p auth.Principal, courseID id.ID) error
	// LivePins returns the live version's pins, and false when the course is not live.
	LivePins(ctx context.Context, courseID id.ID) (Pins, bool, error)
	// VersionPins returns version number's pins to a manager: ErrNotFound or ErrForbidden.
	VersionPins(ctx context.Context, p auth.Principal, courseID id.ID, number int) (Pins, error)
}
```

- Produces on `QuizService`:
  - `List(ctx, p, courseID, lectureID id.ID, version int) ([]domain.Quiz, error)` — `version` 0: managers get heads, everyone else the live pins; `version > 0`: that version's pins, managers only.
  - `Create`, `Update` unchanged signatures; `Delete(ctx, p, quizID)` soft-deletes.
  - `RecordAttempt` unchanged signature; checks against and records the live pin.

- [x] **Step 1: Update the fakes**

In `internal/assessment/app/fakes_test.go`:
- `memStore` stores quizzes as `quizRevs map[id.ID][]domain.Quiz` (index `revision-1`) and `quizDeleted map[id.ID]bool`; the same for exams (`examRevs`, `examDeleted`). Clone them in `RunInTx` like the existing maps (clone the slices too) and restore on error.
- Implement every `QuizRepository` and `ExamRepository` method from Task 2 over these maps with the documented contracts. `FindRevisions` sorts by `Position`, then `ID`. `AppendRevision` returns `app.ErrNotFound` unless `len(revs) == q.Revision-1` and not deleted. `Heads` sorts by ID. `FindForUpdate` = `Find`. Record every `LockMode` passed to exam `Find` in `locks` as today.
- `courseAccess` gains `live map[id.ID]app.Pins` (absent = not live), `versions map[[2]int64]app.Pins`, `edits []id.ID` (course IDs passed to `BeginEdit`), and `frozen map[id.ID]bool`:

```go
func (a *courseAccess) BeginEdit(_ context.Context, p auth.Principal, courseID, lectureID id.ID) error {
	outsideTx(a.t, a.store)
	switch {
	case !a.known(courseID), !lectureID.IsZero() && a.lectures[lectureID] != courseID:
		return app.ErrNotFound
	case !a.manages(p):
		return app.ErrForbidden
	case a.archived[courseID], a.frozen[courseID]:
		return app.ErrCourseNotEditable
	}
	a.edits = append(a.edits, courseID)
	return nil
}

func (a *courseAccess) LivePins(_ context.Context, courseID id.ID) (app.Pins, bool, error) {
	outsideTx(a.t, a.store)
	pins, ok := a.live[courseID]
	return maps.Clone(pins), ok, nil
}

func (a *courseAccess) VersionPins(ctx context.Context, p auth.Principal, courseID id.ID, number int) (app.Pins, error) {
	if err := a.CanReadAsManager(ctx, p, courseID); err != nil {
		return nil, err
	}
	pins, ok := a.versions[[2]int64{int64(courseID), int64(number)}]
	if !ok {
		return nil, app.ErrNotFound
	}
	return maps.Clone(pins), nil
}
```

Delete `CanManageLecture` and `CanManageCourse` from the fake. Add a fixture helper that pins the current heads as live:

```go
// goLive pins every non-deleted quiz and exam of course at its head, as a publish would.
func (f *fixture) goLive(t *testing.T, courseID id.ID) {
	t.Helper()
	heads, err := f.query.Heads(ctx, courseID)
	if err != nil {
		t.Fatal(err)
	}
	pins := app.Pins{}
	for _, h := range heads {
		pins[h.Ref] = h.Revision
	}
	f.access.live[courseID] = pins
}
```

- [x] **Step 2: Write failing tests**

In `internal/assessment/app/quiz_service_test.go`, change existing calls to `List(ctx, p, course, lecture)` → `List(ctx, p, course, lecture, 0)`, and call `f.goLive(t, course)` after creating quizzes in tests where a student lists or attempts. Replace the test asserting that Delete removes attempts with one asserting attempts survive. Add:

```go
func TestStudentsSeeLiveRevisionUntilRepublish(t *testing.T) {
	f := newFixture(t)
	q, err := f.svc.Create(ctx, owner, course, locked, quizInput(0, "v1"))
	if err != nil {
		t.Fatal(err)
	}
	f.goLive(t, course)
	if _, err := f.svc.Update(ctx, owner, course, locked, q.ID, quizInput(0, "v2")); err != nil {
		t.Fatal(err)
	}
	got, err := f.svc.List(ctx, student, course, locked, 0)
	if err != nil || len(got) != 1 || got[0].Questions[0].Prompt != "v1" || got[0].Revision != 1 {
		t.Fatalf("student sees %+v, %v", got, err)
	}
	mine, err := f.svc.List(ctx, owner, course, locked, 0)
	if err != nil || mine[0].Questions[0].Prompt != "v2" || mine[0].Revision != 2 {
		t.Fatalf("manager sees %+v, %v", mine, err)
	}
	f.goLive(t, course)
	got, _ = f.svc.List(ctx, student, course, locked, 0)
	if got[0].Questions[0].Prompt != "v2" {
		t.Fatalf("after republish student sees %q", got[0].Questions[0].Prompt)
	}
}

func TestDeletedDraftQuizStaysLive(t *testing.T) {
	f := newFixture(t)
	q, _ := f.svc.Create(ctx, owner, course, locked, quizInput(0, "v1"))
	f.goLive(t, course)
	if err := f.svc.Delete(ctx, owner, q.ID); err != nil {
		t.Fatal(err)
	}
	got, err := f.svc.List(ctx, student, course, locked, 0)
	if err != nil || len(got) != 1 {
		t.Fatalf("student list after draft delete: %+v, %v", got, err)
	}
	if _, err := f.svc.RecordAttempt(ctx, student, q.ID, student.UserID, nil, ""); err != nil {
		t.Fatalf("attempt on pinned deleted quiz: %v", err)
	}
	if mine, _ := f.svc.List(ctx, owner, course, locked, 0); len(mine) != 0 {
		t.Fatalf("manager still sees deleted quiz in draft: %+v", mine)
	}
}

func TestUnpublishedCourseHidesQuizzes(t *testing.T) {
	f := newFixture(t)
	q, _ := f.svc.Create(ctx, owner, course, locked, quizInput(0, "v1"))
	// Not live: no pins at all.
	if got, err := f.svc.List(ctx, student, course, locked, 0); err != nil || len(got) != 0 {
		t.Fatalf("list = %+v, %v", got, err)
	}
	if _, err := f.svc.RecordAttempt(ctx, student, q.ID, student.UserID, nil, ""); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("attempt = %v, want ErrNotFound", err)
	}
}

func TestQuizAttemptRecordsLiveRevision(t *testing.T) {
	f := newFixture(t)
	q, _ := f.svc.Create(ctx, owner, course, locked, quizInput(0, "v1"))
	f.goLive(t, course)
	_, _ = f.svc.Update(ctx, owner, course, locked, q.ID, quizInput(0, "v2"))
	if _, err := f.svc.RecordAttempt(ctx, student, q.ID, student.UserID, nil, ""); err != nil {
		t.Fatal(err)
	}
	if got := f.store.attempts[len(f.store.attempts)-1].Revision; got != 1 {
		t.Fatalf("attempt revision = %d, want 1", got)
	}
}

func TestQuizEditsFollowCourseFreeze(t *testing.T) {
	f := newFixture(t)
	f.access.frozen[course] = true
	if _, err := f.svc.Create(ctx, owner, course, locked, quizInput(0, "v1")); !errors.Is(err, app.ErrCourseNotEditable) {
		t.Fatalf("create on frozen course = %v", err)
	}
}

func TestManagerReadsVersion(t *testing.T) {
	f := newFixture(t)
	q, _ := f.svc.Create(ctx, owner, course, locked, quizInput(0, "v1"))
	_, _ = f.svc.Update(ctx, owner, course, locked, q.ID, quizInput(0, "v2"))
	f.access.versions[[2]int64{int64(course), 1}] = app.Pins{{Kind: app.KindQuiz, ID: q.ID}: 1}
	got, err := f.svc.List(ctx, owner, course, locked, 1)
	if err != nil || got[0].Questions[0].Prompt != "v1" {
		t.Fatalf("version 1 = %+v, %v", got, err)
	}
	if _, err := f.svc.List(ctx, student, course, locked, 1); !errors.Is(err, app.ErrForbidden) {
		t.Fatalf("student version read = %v, want ErrForbidden", err)
	}
}
```

If the test file has no `quizInput(position, prompt)` helper, add one returning an `app.QuizInput` with one single-choice question whose prompt is `prompt` and options `a` (correct) and `b`.

- [x] **Step 3: Run them to verify they fail**

Run: `go test ./internal/assessment/app/ -run 'Quiz|Live|Unpublished|Version'`
Expected: FAIL to compile.

- [x] **Step 4: Implement**

Replace `CourseAccess` in `ports.go` as listed. Rewrite the `QuizService` methods:

```go
// List returns a lecture's quizzes, answer keys included. version 0 serves managers the working
// copy and everyone else the live version; version n serves managers published version n.
func (s *QuizService) List(ctx context.Context, p auth.Principal, courseID, lectureID id.ID, version int) ([]domain.Quiz, error) {
	pins, err := readPins(ctx, s.courses, p, courseID, version, func() error {
		return s.courses.CanReadLecture(ctx, p, courseID, lectureID)
	})
	if err != nil {
		return nil, err
	}
	var out []domain.Quiz
	err = s.tx.RunInTx(ctx, func(r Repos) error {
		if pins == nil {
			out, err = r.Quizzes.ListByLecture(ctx, courseID, lectureID)
			return err
		}
		all, err := r.Quizzes.FindRevisions(ctx, pins.ids(KindQuiz))
		for _, q := range all {
			if q.CourseID == courseID && q.LectureID == lectureID {
				out = append(out, q)
			}
		}
		return err
	})
	return out, err
}

func (s *QuizService) Create(ctx context.Context, p auth.Principal, courseID, lectureID id.ID, in QuizInput) (domain.Quiz, error) {
	if err := s.courses.BeginEdit(ctx, p, courseID, lectureID); err != nil {
		return domain.Quiz{}, err
	}
	now := s.clock.Now()
	q, err := s.buildQuiz(s.ids.New(), courseID, lectureID, in, nil, now, now)
	if err != nil {
		return domain.Quiz{}, err
	}
	q.Revision = 1
	return q, s.tx.RunInTx(ctx, func(r Repos) error { return r.Quizzes.Insert(ctx, q, p.UserID) })
}

// Update appends a revision with the quiz's new position and questions. The quiz must belong to
// the path's course and lecture; supplied question and option IDs must already belong to it.
func (s *QuizService) Update(ctx context.Context, p auth.Principal, courseID, lectureID, quizID id.ID, in QuizInput) (domain.Quiz, error) {
	if err := s.courses.BeginEdit(ctx, p, courseID, lectureID); err != nil {
		return domain.Quiz{}, err
	}
	var out domain.Quiz
	err := s.tx.RunInTx(ctx, func(r Repos) error {
		cur, err := r.Quizzes.FindForUpdate(ctx, quizID)
		if err != nil {
			return err
		}
		if cur.CourseID != courseID || cur.LectureID != lectureID {
			return ErrNotFound
		}
		if out, err = s.buildQuiz(quizID, courseID, lectureID, in, cur.IDs(), cur.CreatedAt, s.clock.Now()); err != nil {
			return err
		}
		out.Revision = cur.Revision + 1
		return r.Quizzes.AppendRevision(ctx, out, p.UserID)
	})
	return out, err
}

// Delete removes a quiz from the working copy. Published versions and attempts keep it.
func (s *QuizService) Delete(ctx context.Context, p auth.Principal, quizID id.ID) error {
	q, err := s.find(ctx, quizID)
	if err != nil {
		return err
	}
	if err := s.courses.BeginEdit(ctx, p, q.CourseID, q.LectureID); err != nil {
		return err
	}
	return s.tx.RunInTx(ctx, func(r Repos) error { return r.Quizzes.Delete(ctx, quizID, s.clock.Now()) })
}
```

In `RecordAttempt`, replace the block from `q, err := s.find(ctx, quizID)` through `q.CheckAnswers` with:

```go
	q, err := s.liveQuiz(ctx, quizID)
	if err != nil {
		return 0, err
	}
	if err := s.courses.CanReadLecture(ctx, p, q.CourseID, q.LectureID); err != nil {
		return 0, err
	}
	active, err := s.enrollments.IsActivelyEnrolled(ctx, q.CourseID, userID)
	if err != nil {
		return 0, err
	}
	if !active {
		return 0, ErrEnrollmentRequired
	}
	if err := q.CheckAnswers(parsed); err != nil {
		return 0, fmt.Errorf("%w: %w", ErrInvalidInput, err)
	}
	attempt := domain.QuizAttempt{ID: s.ids.New(), QuizID: quizID, Revision: q.Revision, UserID: userID, Answers: parsed,
		IdempotencyKey: key, SubmittedAt: s.clock.Now()}
```

Add, in `quiz_service.go`:

```go
// liveQuiz returns the quiz at its live pin. Deleted quizzes resolve while still pinned.
func (s *QuizService) liveQuiz(ctx context.Context, quizID id.ID) (domain.Quiz, error) {
	var courseID id.ID
	err := s.tx.RunInTx(ctx, func(r Repos) error {
		heads, err := r.Quizzes.FindRevisions(ctx, map[id.ID]int{quizID: 1})
		if err != nil || len(heads) == 0 {
			return cmp.Or(err, ErrNotFound)
		}
		courseID = heads[0].CourseID
		return nil
	})
	if err != nil {
		return domain.Quiz{}, err
	}
	pins, live, err := s.courses.LivePins(ctx, courseID)
	if err != nil {
		return domain.Quiz{}, err
	}
	rev, ok := pins[Ref{Kind: KindQuiz, ID: quizID}]
	if !live || !ok {
		return domain.Quiz{}, ErrNotFound
	}
	var q []domain.Quiz
	err = s.tx.RunInTx(ctx, func(r Repos) error {
		q, err = r.Quizzes.FindRevisions(ctx, map[id.ID]int{quizID: rev})
		return err
	})
	if err != nil || len(q) == 0 {
		return domain.Quiz{}, cmp.Or(err, ErrNotFound)
	}
	return q[0], nil
}
```

Revision 1 always exists for any quiz ever created, so it identifies the course without depending on deletion state.

Create `internal/assessment/app/pins.go`:

```go
package app

import (
	"context"
	"errors"

	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

// ids returns the pinned revision of each pinned assessment of kind.
func (p Pins) ids(kind Kind) map[id.ID]int {
	out := map[id.ID]int{}
	for ref, rev := range p {
		if ref.Kind == kind {
			out[ref.ID] = rev
		}
	}
	return out
}

// readPins picks what a read serves. version > 0 returns that version's pins (managers only).
// Otherwise canRead must pass; then a manager gets nil pins (the working copy) and everyone else
// the live pins, empty when the course is not live.
func readPins(ctx context.Context, courses CourseAccess, p auth.Principal, courseID id.ID, version int, canRead func() error) (Pins, error) {
	if version > 0 {
		return courses.VersionPins(ctx, p, courseID, version)
	}
	if err := canRead(); err != nil {
		return nil, err
	}
	switch err := courses.CanReadAsManager(ctx, p, courseID); {
	case err == nil:
		return nil, nil
	case !errors.Is(err, ErrForbidden) && !errors.Is(err, ErrNotFound):
		return nil, err
	}
	pins, _, err := courses.LivePins(ctx, courseID)
	if pins == nil && err == nil {
		pins = Pins{}
	}
	return pins, err
}
```

Remove the now-unused `find`'s callers except `Delete`; keep `find` (it reads the head).

- [x] **Step 6: Wire the new CourseAccess methods in courseauthoring and cmd/api**

In `cmd/api/assessment.go`, replace `CanManageLecture` and `CanManageCourse` with:

```go
func (a assessmentCourseAccess) BeginEdit(ctx context.Context, p auth.Principal, courseID, lectureID id.ID) error {
	return toAssessmentError(a.courses.BeginAssessmentEdit(ctx, p, courseID, lectureID))
}

func (a assessmentCourseAccess) LivePins(ctx context.Context, courseID id.ID) (assessmentapp.Pins, bool, error) {
	pins, live, err := a.courses.LivePins(ctx, courseID)
	return toAssessmentPins(pins), live, toAssessmentError(err)
}

func (a assessmentCourseAccess) VersionPins(ctx context.Context, p auth.Principal, courseID id.ID, number int) (assessmentapp.Pins, error) {
	pins, err := a.courses.VersionPins(ctx, p, courseID, number)
	return toAssessmentPins(pins), toAssessmentError(err)
}

func toAssessmentPins(pins []courseauthoringdomain.AssessmentPin) assessmentapp.Pins {
	out := make(assessmentapp.Pins, len(pins))
	for _, p := range pins {
		out[assessmentapp.Ref{Kind: assessmentapp.Kind(p.Kind), ID: p.ID}] = p.Revision
	}
	return out
}
```

Add `BeginAssessmentEdit` to `internal/courseauthoring/app/pins.go`:

```go
// BeginAssessmentEdit authorizes p to change the course's quizzes and exams and applies the
// course edit rule: frozen courses refuse, a published course reopens as a draft. lectureID zero
// skips the lecture check. For internal callers.
func (s *CourseService) BeginAssessmentEdit(ctx context.Context, p auth.Principal, courseID, lectureID id.ID) error {
	_, err := s.mutate(ctx, p, courseID, func(_ Repos, c *domain.Course) error {
		if !lectureID.IsZero() {
			if _, ok := c.Lecture(lectureID); !ok {
				return ErrNotFound
			}
		}
		return c.BeginEdit()
	})
	return err
}
```

with a courseauthoring app test: on a published course, `BeginAssessmentEdit` moves status to `draft`; on an `in_review` course it returns `domain.ErrCourseNotEditable`; with an unknown lecture it returns `ErrNotFound`. Remove `CheckLectureManage` if nothing else calls it (grep).

Pass `toAssessmentError` the new courseauthoring error for an unknown version (`ErrNotFound`, already mapped).

- [x] **Step 7: Run the quiz and courseauthoring tests**

Run: `go test ./internal/assessment/app/ -run 'Quiz|Live|Unpublished|Version' && go test ./internal/courseauthoring/...`
Expected: PASS. (Exam tests may still fail; Task 5 fixes them.)

- [x] **Step 8: Commit**

```bash
git add internal/assessment/app internal/courseauthoring/app cmd/api/assessment.go
git commit -m "feat(assessment): serve quizzes from pinned revisions"
```

---

### Task 5: Exam service on revisions and pins

**Files:**
- Modify: `internal/assessment/app/exam_service.go`, `exam_attempts.go`, `errors.go`
- Delete: `internal/assessment/domain/exam_edit.go`, `exam_edit_test.go`
- Test: `internal/assessment/app/exam_service_test.go`, `exam_attempts_test.go`

**Interfaces:**
- Consumes: Task 4 `readPins`, `Pins.ids`, `CourseAccess`.
- Produces:
  - `ListAuthoring(ctx, p, courseID id.ID, version int) ([]domain.Exam, error)` — version 0 heads, else that version.
  - `GetAuthoring(ctx, p, examID id.ID) (domain.Exam, error)` (no locks).
  - `Create`, `Save`, `SaveSettings`, `Duplicate` return `(domain.Exam, error)`; `Reorder`, `Publish`, `Unpublish`, `Delete` return `error`. All call `BeginEdit(ctx, p, courseID, 0)` first.
  - Student `List`, `Get`, `Start` read live pins; an exam is visible only when its pinned revision's `Status` is `ExamPublished`.
  - Attempts read their own revision everywhere (`ownAttempt`, `Review`, `ListAttempts`, settling).
  - `ExamDetail` type and `ErrExamHasAttempts` are deleted.

- [x] **Step 1: Write failing tests**

In `exam_service_test.go` and `exam_attempts_test.go`: replace `ExamDetail` uses with `domain.Exam`; remove tests of edit locks (`EditViolation`, `CheckExamEdit`, `Locks`, `ErrExamHasAttempts`, `LockShare`); after `Publish` of an exam add `f.goLive(t, course)` wherever a student then lists, gets or starts. Add:

```go
func TestExamPublishWaitsForCoursePublish(t *testing.T) {
	f := newFixture(t)
	e, _ := f.exams.Create(ctx, owner, course, examInput("Final"))
	f.goLive(t, course) // exam pinned as draft
	if err := f.exams.Publish(ctx, owner, e.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.exams.List(ctx, student, course); len(got) != 0 {
		t.Fatalf("draft-pinned exam visible: %+v", got)
	}
	f.goLive(t, course)
	if got, _ := f.exams.List(ctx, student, course); len(got) != 1 {
		t.Fatalf("published exam missing after republish")
	}
}

func TestAttemptGradedAgainstItsRevision(t *testing.T) {
	f := newFixture(t)
	e, _ := f.exams.Create(ctx, owner, course, examInput("Final"))
	_ = f.exams.Publish(ctx, owner, e.ID)
	f.goLive(t, course)
	a, err := f.exams.Start(ctx, student, e.ID)
	if err != nil {
		t.Fatal(err)
	}
	// The draft swaps every question; the open attempt keeps revision 2's questions.
	in := examInput("Final")
	in.Questions = []app.QuestionInput{newQuestionInput("new?")}
	if _, err := f.exams.Save(ctx, owner, e.ID, in); err != nil {
		t.Fatalf("edit with an open attempt: %v", err)
	}
	f.goLive(t, course)
	q := a.Exam.Questions[0]
	if err := f.exams.SaveAnswer(ctx, student, a.Attempt.ID, app.AnswerInput{QuestionID: q.ID.String(),
		OptionIDs: []string{q.Options[0].ID.String()}}); err != nil {
		t.Fatalf("answer old revision's question: %v", err)
	}
	done, err := f.exams.Submit(ctx, student, a.Attempt.ID)
	if err != nil || done.Exam.Revision != a.Exam.Revision {
		t.Fatalf("graded against %d, want %d (%v)", done.Exam.Revision, a.Exam.Revision, err)
	}
}

func TestDraftDeadlineExtensionDoesNotMoveOpenAttempt(t *testing.T) {
	f := newFixture(t)
	in := examInput("Timed")
	limit := 600
	in.TimeLimitSeconds = &limit
	e, _ := f.exams.Create(ctx, owner, course, in)
	_ = f.exams.Publish(ctx, owner, e.ID)
	f.goLive(t, course)
	a, _ := f.exams.Start(ctx, student, e.ID)
	longer := 1200
	in.TimeLimitSeconds = &longer
	_, _ = f.exams.Save(ctx, owner, e.ID, in)
	f.goLive(t, course)
	got, _ := f.exams.GetAttempt(ctx, student, a.Attempt.ID)
	if d := got.Attempt.Deadline(got.Exam); !d.Equal(a.Attempt.StartedAt.Add(600 * time.Second)) {
		t.Fatalf("deadline = %v, want start + 10m", d)
	}
}

func TestDeleteExamWithAttemptsKeepsThem(t *testing.T) {
	f := newFixture(t)
	e, _ := f.exams.Create(ctx, owner, course, examInput("Final"))
	_ = f.exams.Publish(ctx, owner, e.ID)
	f.goLive(t, course)
	a, _ := f.exams.Start(ctx, student, e.ID)
	if err := f.exams.Delete(ctx, owner, e.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := f.exams.GetAttempt(ctx, student, a.Attempt.ID); err != nil {
		t.Fatalf("attempt after delete: %v", err)
	}
}
```

Use the test file's existing exam input and question input builders under the names `examInput` / `newQuestionInput`, adding them if absent.

- [x] **Step 2: Run them to verify they fail**

Run: `go test ./internal/assessment/...`
Expected: FAIL to compile.

- [x] **Step 3: Implement**

Delete `internal/assessment/domain/exam_edit.go` and `exam_edit_test.go`; remove `ErrExamHasAttempts` from `errors.go` and the `ErrEdit*` errors from `domain/errors.go` if only `exam_edit.go` used them (grep first).

In `exam_service.go`:
- Delete `ExamDetail`.
- `ListAuthoring(ctx, p, courseID, version)`: when `version > 0`, `pins, err := s.courses.VersionPins(ctx, p, courseID, version)` and return `r.Exams.FindRevisions(ctx, pins.ids(KindExam))`; otherwise `s.courses.CanReadAsManager(ctx, p, courseID)` then `r.Exams.ListByCourse(ctx, courseID)`. Non-managers never read authoring.
- `GetAuthoring`: authorize with `CanReadAsManager`, return `r.Exams.Find(ctx, examID, LockNone)`; drop settling and locks.
- `Create`: `BeginEdit(ctx, p, courseID, 0)`; build; `e.Revision = 1`; `Insert(ctx, e, p.UserID)`.
- `edit`:

```go
// edit authorizes p to change the exam's course, then appends the revision change builds.
func (s *ExamService) edit(ctx context.Context, p auth.Principal, examID id.ID, change func(cur domain.Exam, now time.Time) (domain.Exam, error)) (domain.Exam, error) {
	cur, err := s.find(ctx, examID)
	if err != nil {
		return domain.Exam{}, err
	}
	if err := s.courses.BeginEdit(ctx, p, cur.CourseID, 0); err != nil {
		return domain.Exam{}, err
	}
	var out domain.Exam
	err = s.tx.RunInTx(ctx, func(r Repos) error {
		cur, err := r.Exams.Find(ctx, examID, LockUpdate)
		if err != nil {
			return err
		}
		if out, err = change(cur, s.clock.Now()); err != nil {
			return err
		}
		out.Revision = cur.Revision + 1
		return r.Exams.AppendRevision(ctx, out, p.UserID)
	})
	return out, err
}
```

- `Reorder`: `BeginEdit(ctx, p, courseID, 0)`; in the transaction, validate `examIDs` against `ListByCourse` as today, then for each exam whose index differs from its `Position`, set `Position`, `UpdatedAt = now`, `Revision++` and `AppendRevision`.
- `Duplicate`: `BeginEdit` on the source's course; new exam gets `Revision = 1`, `Insert(ctx, out, p.UserID)`.
- `Delete`: `BeginEdit` on the exam's course; `r.Exams.Delete(ctx, examID, s.clock.Now())`.
- `Publish`/`Unpublish` keep using `setStatus` via `edit`; update the doc comments: "takes effect for students on the next course publish".

In `exam_attempts.go`:
- Add:

```go
// liveExams returns the course's exams that the live version pins as published.
func (s *ExamService) liveExams(ctx context.Context, r Repos, pins Pins) ([]domain.Exam, error) {
	all, err := r.Exams.FindRevisions(ctx, pins.ids(KindExam))
	if err != nil {
		return nil, err
	}
	out := all[:0]
	for _, e := range all {
		if e.Status == domain.ExamPublished {
			out = append(out, e)
		}
	}
	return out, nil
}

// liveExam returns one exam at its live pin when that revision is published; ErrNotFound otherwise.
func (s *ExamService) liveExam(ctx context.Context, examID id.ID) (domain.Exam, error) {
	var courseID id.ID
	err := s.tx.RunInTx(ctx, func(r Repos) error {
		first, err := r.Exams.FindRevisions(ctx, map[id.ID]int{examID: 1})
		if err != nil || len(first) == 0 {
			return cmp.Or(err, ErrNotFound)
		}
		courseID = first[0].CourseID
		return nil
	})
	if err != nil {
		return domain.Exam{}, err
	}
	pins, live, err := s.courses.LivePins(ctx, courseID)
	if err != nil {
		return domain.Exam{}, err
	}
	rev, ok := pins[Ref{Kind: KindExam, ID: examID}]
	if !live || !ok {
		return domain.Exam{}, ErrNotFound
	}
	var got []domain.Exam
	err = s.tx.RunInTx(ctx, func(r Repos) error {
		got, err = r.Exams.FindRevisions(ctx, map[id.ID]int{examID: rev})
		return err
	})
	if err != nil || len(got) == 0 || got[0].Status != domain.ExamPublished {
		return domain.Exam{}, cmp.Or(err, ErrNotFound)
	}
	return got[0], nil
}

// attemptExam returns the revision attempt a was taken against.
func attemptExam(ctx context.Context, r Repos, a domain.ExamAttempt) (domain.Exam, error) {
	got, err := r.Exams.FindRevisions(ctx, map[id.ID]int{a.ExamID: a.Revision})
	if err != nil || len(got) == 0 {
		return domain.Exam{}, cmp.Or(err, ErrNotFound)
	}
	return got[0], nil
}
```

- `List`: after `canTake`, fetch `pins, _, err := s.courses.LivePins(ctx, courseID)` outside the transaction; inside, `exams, err := s.liveExams(ctx, r, pins)`; for each user attempt, settle against `attemptExam(ctx, r, a)` rather than `e`.
- `Get`: `e, err := s.liveExam(ctx, examID)`, then `canTake`, then the window check.
- `Start`: `e, err := s.liveExam(ctx, examID)` then `canTake`; inside the transaction drop the `Find(..., LockShare)` and status check, keep the window, open-attempt and retake logic (settle the open attempt against `attemptExam`), and `domain.NewExamAttempt(s.ids.New(), e, p.UserID, now)` now records `e.Revision`.
- `ownAttempt`: replace `r.Exams.Find(ctx, a.ExamID, LockNone)` with `attemptExam(ctx, r, a)`.
- `Review`: replace the `Find` with `attemptExam(ctx, r, a)`.
- `ListAttempts`: authorize via the exam's course as today; settle each attempt against `attemptExam`, caching by revision in a `map[int]domain.Exam`.
- Delete `settleExamAttempts` if no longer called.

- [x] **Step 4: Run all assessment tests**

Run: `go test ./internal/assessment/...`
Expected: PASS.

- [x] **Step 5: Commit**

```bash
git add -A internal/assessment
git commit -m "feat(assessment): pin exams and attempts to revisions"
```

---

### Task 6: Restore assessments on DraftDiscarded

**Files:**
- Create: `internal/assessment/app/restore.go`, `internal/assessment/app/restore_test.go`
- Create: `internal/assessment/adapters/events/events.go`, `events_test.go`
- Modify: `cmd/api/app.go`, `cmd/api/assessment.go`; `.golangci.yml` only if depguard rules list adapter packages explicitly (grep `assessment` there)

**Interfaces:**
- Produces: `app.NewRestoreService(tx TxRunner, clk clock.Clock) *RestoreService`; `(*RestoreService).RestorePins(ctx context.Context, courseID, actorID id.ID, pins Pins) error`.
- Produces: `events.New(svc *app.RestoreService) *Handlers`; `(*Handlers).DraftDiscarded(msg *message.Message) error`; `events.DraftDiscardedTopic = "courseauthoring.course.draft_discarded"`.

- [x] **Step 1: Write failing tests**

Create `internal/assessment/app/restore_test.go`:

```go
package app_test

import (
	"testing"

	"github.com/santoshkc2200/ioe-backend/internal/assessment/app"
)

func TestRestorePins(t *testing.T) {
	f := newFixture(t)
	restore := app.NewRestoreService(f.store, f.clock)
	kept, _ := f.svc.Create(ctx, owner, course, locked, quizInput(0, "v1"))
	gone, _ := f.svc.Create(ctx, owner, course, locked, quizInput(1, "gone"))
	f.goLive(t, course)
	pins := f.access.live[course]

	_, _ = f.svc.Update(ctx, owner, course, locked, kept.ID, quizInput(0, "v2"))
	_ = f.svc.Delete(ctx, owner, gone.ID)
	added, _ := f.svc.Create(ctx, owner, course, locked, quizInput(2, "new"))

	for range 2 { // redelivery must change nothing
		if err := restore.RestorePins(ctx, course, owner.UserID, pins); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := f.svc.List(ctx, owner, course, locked, 0)
	if len(got) != 2 || got[0].ID != kept.ID || got[1].ID != gone.ID {
		t.Fatalf("working copy = %+v", got)
	}
	if got[0].Questions[0].Prompt != "v1" || got[0].Revision != 3 {
		t.Fatalf("kept = rev %d %q, want rev 3 v1", got[0].Revision, got[0].Questions[0].Prompt)
	}
	if got[1].Revision != 1 {
		t.Fatalf("undeleted quiz moved to rev %d, want 1", got[1].Revision)
	}
	if _, err := f.svc.Update(ctx, owner, course, locked, added.ID, quizInput(2, "x")); err == nil {
		t.Fatal("quiz created after the live version was not deleted")
	}
}
```

Create `internal/assessment/adapters/events/events_test.go` testing that a message whose payload is

```json
{"course_id":"10","actor_id":"100","pins":[{"kind":"quiz","id":"700","revision":2}],"occurred_at":"2026-10-07T09:00:00Z"}
```

reaches a `RestoreService` over an `app_test`-style in-memory store with pins `{quiz 700: 2}`, and that a payload with an unknown kind returns a non-nil error. Because `memStore` lives in `app_test`, test the decoding through an exported pure function instead:

```go
func TestDecodeDraftDiscarded(t *testing.T) {
	msg := message.NewMessage("1", []byte(`{"course_id":"10","actor_id":"100","pins":[{"kind":"quiz","id":"700","revision":2},{"kind":"exam","id":"800","revision":1}],"occurred_at":"2026-10-07T09:00:00Z"}`))
	courseID, actorID, pins, err := events.DecodeDraftDiscarded(msg)
	if err != nil || courseID != 10 || actorID != 100 {
		t.Fatalf("decode: %v %v %v", courseID, actorID, err)
	}
	want := app.Pins{{Kind: app.KindQuiz, ID: 700}: 2, {Kind: app.KindExam, ID: 800}: 1}
	if !maps.Equal(pins, want) {
		t.Fatalf("pins = %v", pins)
	}
	bad := message.NewMessage("2", []byte(`{"course_id":"10","actor_id":"1","pins":[{"kind":"poll","id":"1","revision":1}]}`))
	if _, _, _, err := events.DecodeDraftDiscarded(bad); err == nil {
		t.Fatal("unknown kind accepted")
	}
}
```

- [x] **Step 2: Run them to verify they fail**

Run: `go test ./internal/assessment/...`
Expected: FAIL to compile.

- [x] **Step 3: Implement the service**

Create `internal/assessment/app/restore.go`:

```go
package app

import (
	"context"

	"github.com/santoshkc2200/ioe-backend/internal/platform/clock"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

// RestoreService resets a course's working-copy quizzes and exams to a published version's pins.
type RestoreService struct {
	tx    TxRunner
	clock clock.Clock
}

func NewRestoreService(tx TxRunner, clk clock.Clock) *RestoreService {
	return &RestoreService{tx: tx, clock: clk}
}

// RestorePins makes each pinned revision the head again, undeleting as needed, and deletes the
// course's quizzes and exams that pins does not name. It is idempotent: an assessment already
// at its pin is left alone. actorID is recorded as the author of copied revisions.
func (s *RestoreService) RestorePins(ctx context.Context, courseID, actorID id.ID, pins Pins) error {
	now := s.clock.Now()
	return s.tx.RunInTx(ctx, func(r Repos) error {
		quizzes, err := r.Quizzes.Heads(ctx, courseID)
		if err != nil {
			return err
		}
		exams, err := r.Exams.Heads(ctx, courseID)
		if err != nil {
			return err
		}
		for _, h := range append(quizzes, exams...) {
			if err := s.restore(ctx, r, h, pins, actorID, now); err != nil {
				return err
			}
		}
		return nil
	})
}

func deleteRef(ctx context.Context, r Repos, ref Ref, now time.Time) error {
	if ref.Kind == KindQuiz {
		return r.Quizzes.Delete(ctx, ref.ID, now)
	}
	return r.Exams.Delete(ctx, ref.ID, now)
}

func undeleteRef(ctx context.Context, r Repos, ref Ref, now time.Time) error {
	if ref.Kind == KindQuiz {
		return r.Quizzes.Undelete(ctx, ref.ID, now)
	}
	return r.Exams.Undelete(ctx, ref.ID, now)
}

// copyRevision appends a copy of revision pin as the head's next revision.
func copyRevision(ctx context.Context, r Repos, h Head, pin int, actorID id.ID, now time.Time) error {
	if h.Ref.Kind == KindQuiz {
		got, err := r.Quizzes.FindRevisions(ctx, map[id.ID]int{h.Ref.ID: pin})
		if err != nil || len(got) == 0 {
			return cmp.Or(err, fmt.Errorf("quiz %s has no revision %d", h.Ref.ID, pin))
		}
		q := got[0]
		q.Revision, q.UpdatedAt = h.Revision+1, now
		return r.Quizzes.AppendRevision(ctx, q, actorID)
	}
	got, err := r.Exams.FindRevisions(ctx, map[id.ID]int{h.Ref.ID: pin})
	if err != nil || len(got) == 0 {
		return cmp.Or(err, fmt.Errorf("exam %s has no revision %d", h.Ref.ID, pin))
	}
	e := got[0]
	e.Revision, e.UpdatedAt = h.Revision+1, now
	return r.Exams.AppendRevision(ctx, e, actorID)
}
```

Add `"cmp"`, `"fmt"`, `"reflect"`, `"time"` to the imports. Comparing revision numbers alone is not idempotent: after the first run the restored head is a new revision (3) whose number differs from its pin (1), so redelivery would append again. `restore` therefore compares content:

```go
// samePinned reports whether the head's content equals revision pin's.
func samePinned(ctx context.Context, r Repos, h Head, pin int) (bool, error) {
	if h.Revision == pin {
		return true, nil
	}
	revs := map[id.ID]int{h.Ref.ID: h.Revision}
	pinned := map[id.ID]int{h.Ref.ID: pin}
	if h.Ref.Kind == KindQuiz {
		a, err := r.Quizzes.FindRevisions(ctx, revs)
		if err != nil {
			return false, err
		}
		b, err := r.Quizzes.FindRevisions(ctx, pinned)
		if err != nil || len(a) == 0 || len(b) == 0 {
			return false, err
		}
		x, y := a[0], b[0]
		x.Revision, x.UpdatedAt, y.Revision, y.UpdatedAt = 0, time.Time{}, 0, time.Time{}
		return reflect.DeepEqual(x, y), nil
	}
	a, err := r.Exams.FindRevisions(ctx, revs)
	if err != nil {
		return false, err
	}
	b, err := r.Exams.FindRevisions(ctx, pinned)
	if err != nil || len(a) == 0 || len(b) == 0 {
		return false, err
	}
	x, y := a[0], b[0]
	x.Revision, x.UpdatedAt, y.Revision, y.UpdatedAt = 0, time.Time{}, 0, time.Time{}
	return reflect.DeepEqual(x, y), nil
}
```

and `restore`:

```go
func (s *RestoreService) restore(ctx context.Context, r Repos, h Head, pins Pins, actorID id.ID, now time.Time) error {
	pin, pinned := pins[h.Ref]
	if !pinned {
		if h.Deleted {
			return nil
		}
		return deleteRef(ctx, r, h.Ref, now)
	}
	if h.Deleted {
		if err := undeleteRef(ctx, r, h.Ref, now); err != nil {
			return err
		}
	}
	same, err := samePinned(ctx, r, h, pin)
	if err != nil || same {
		return err
	}
	return copyRevision(ctx, r, h, pin, actorID, now)
}
```

The test above expects revision 3 for `kept` (1 original, 2 edit, 3 restored copy) and no revision 4 after redelivery.

- [x] **Step 4: Implement the outbox handler and wiring**

Create `internal/assessment/adapters/events/events.go`:

```go
// Package events subscribes assessment to the domain events it consumes.
package events

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ThreeDotsLabs/watermill/message"

	"github.com/santoshkc2200/ioe-backend/internal/assessment/app"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

// DraftDiscardedTopic is courseauthoring's DraftDiscarded event name.
const DraftDiscardedTopic = "courseauthoring.course.draft_discarded"

const handlerTimeout = 30 * time.Second

type pinDoc struct {
	Kind     string `json:"kind"`
	ID       id.ID  `json:"id"`
	Revision int    `json:"revision"`
}

type draftDiscardedDoc struct {
	CourseID id.ID    `json:"course_id"`
	ActorID  id.ID    `json:"actor_id"`
	Pins     []pinDoc `json:"pins"`
}

// DecodeDraftDiscarded reads a DraftDiscarded payload.
func DecodeDraftDiscarded(msg *message.Message) (courseID, actorID id.ID, pins app.Pins, err error) {
	var doc draftDiscardedDoc
	if err := json.Unmarshal(msg.Payload, &doc); err != nil {
		return 0, 0, nil, fmt.Errorf("decode draft discarded: %w", err)
	}
	pins = make(app.Pins, len(doc.Pins))
	for _, p := range doc.Pins {
		kind := app.Kind(p.Kind)
		if kind != app.KindQuiz && kind != app.KindExam {
			return 0, 0, nil, fmt.Errorf("decode draft discarded: unknown kind %q", p.Kind)
		}
		pins[app.Ref{Kind: kind, ID: p.ID}] = p.Revision
	}
	return doc.CourseID, doc.ActorID, pins, nil
}

// Handlers process the events assessment subscribes to.
type Handlers struct{ restore *app.RestoreService }

func New(restore *app.RestoreService) *Handlers { return &Handlers{restore: restore} }

// DraftDiscarded resets the course's working-copy quizzes and exams to the live pins.
func (h *Handlers) DraftDiscarded(msg *message.Message) error {
	courseID, actorID, pins, err := DecodeDraftDiscarded(msg)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), handlerTimeout)
	defer cancel()
	return h.restore.RestorePins(ctx, courseID, actorID, pins)
}
```

A malformed payload returns an error, so the forwarder retries it forever; that is acceptable because courseauthoring is the only producer and the payload is covered by Task 3's test. If `id.ID`'s JSON form differs from a quoted decimal string (check `internal/platform/id`), adjust the test payloads, not the decoder.

In `cmd/api/app.go`, after `registerNotifications(...)`:

```go
	fw.Handle(assessmentevents.DraftDiscardedTopic,
		assessmentevents.New(assessmentapp.NewRestoreService(assessmentTx, clk)).DraftDiscarded)
```

with import `assessmentevents "github.com/santoshkc2200/ioe-backend/internal/assessment/adapters/events"`. Check that `fw.Handle` is callable before `NewForwarder`'s run starts (it is used the same way for notifications).

- [x] **Step 5: Run tests and lint**

Run: `go test ./internal/assessment/... ./cmd/api/ && make lint`
Expected: PASS.

- [x] **Step 6: Commit**

```bash
git add internal/assessment cmd/api/app.go
git commit -m "feat(assessment): restore quizzes and exams when a draft is discarded"
```

---

### Task 7: Refuse deleting media in use

**Files:**
- Modify: `internal/courseauthoring/app/assets.go` (add `AssetUsage` on `ContentService`)
- Modify: `internal/media/app/ports.go`, `errors.go`, `service.go`
- Modify: `internal/media/app/fakes_test.go`
- Test: `internal/courseauthoring/app/assets_test.go`, `internal/media/app/service_test.go`

**Interfaces:**
- Produces: `(*courseauthoringapp.ContentService).AssetUsage(ctx context.Context, courseID, assetID id.ID) ([]id.ID, error)` — lecture IDs, sorted, whose working-copy or live-version blocks reference the asset; no authorization.
- Produces in media: `CourseAccess.AssetUsage(ctx, courseID, assetID id.ID) ([]id.ID, error)`; `ErrAssetInUse`; `type InUseError struct{ LectureIDs []id.ID }` with `Error()` and `Unwrap() error { return ErrAssetInUse }`.

- [x] **Step 1: Write failing tests**

Append to `internal/courseauthoring/app/assets_test.go`:

```go
func TestAssetUsage(t *testing.T) {
	f := newFixture(t)
	f.putVideoBlock(t, f.lectureID, 555) // working copy references asset 555
	got, err := f.contents.AssetUsage(ctx, f.courseID, 555)
	if err != nil || !slices.Equal(got, []id.ID{f.lectureID}) {
		t.Fatalf("draft usage = %v, %v", got, err)
	}
	if err := f.courses.Publish(ctx, reviewer, f.courseID); err != nil {
		t.Fatal(err)
	}
	f.putVideoBlock(t, f.lectureID, 556) // draft drops 555; live still has it
	got, err = f.contents.AssetUsage(ctx, f.courseID, 555)
	if err != nil || !slices.Equal(got, []id.ID{f.lectureID}) {
		t.Fatalf("live usage = %v, %v", got, err)
	}
	if got, _ := f.contents.AssetUsage(ctx, f.courseID, 999); len(got) != 0 {
		t.Fatalf("unused asset reported in use: %v", got)
	}
}
```

Use the fixture helpers `assets_test.go` already has for writing a video block (name it `putVideoBlock` if adding).

Append to `internal/media/app/service_test.go`:

```go
func TestDeleteRefusesAssetInUse(t *testing.T) {
	repo, remote := newRepo(), &fakeRemote{}
	repo.put(domain.Asset{ID: 777, CourseID: courseA, Kind: domain.KindVideo})
	svc := newService(t, repo, remote, fakeAccess{usage: map[id.ID][]id.ID{777: {50, 51}}})
	err := svc.Delete(ctx, owner, 777)
	var inUse *app.InUseError
	if !errors.As(err, &inUse) || !errors.Is(err, app.ErrAssetInUse) || !slices.Equal(inUse.LectureIDs, []id.ID{50, 51}) {
		t.Fatalf("delete = %v", err)
	}
	if len(remote.calls) != 0 {
		t.Fatalf("remote touched: %v", remote.calls)
	}
}
```

Use the repository seeding helper `service_test.go` already uses in its Delete tests in place of `repo.put` if named differently.

- [x] **Step 2: Run them to verify they fail**

Run: `go test ./internal/courseauthoring/app/ ./internal/media/app/ -run 'AssetUsage|InUse'`
Expected: FAIL to compile.

- [x] **Step 3: Implement**

In `internal/courseauthoring/app/assets.go`:

```go
// AssetUsage returns the lectures, by ID, whose working-copy or live-version blocks reference
// assetID. It applies no authorization: media calls it after authorizing a delete.
func (s *ContentService) AssetUsage(ctx context.Context, courseID, assetID id.ID) ([]id.ID, error) {
	used := map[id.ID]struct{}{}
	err := s.tx.RunInTx(ctx, func(r Repos) error {
		c, err := r.Courses.FindByID(ctx, courseID)
		if err != nil {
			return err
		}
		for _, l := range c.Lectures {
			blocks, err := r.Contents.ListBlocks(ctx, l.ID)
			if err != nil {
				return err
			}
			if blocksReference(blocks, assetID) {
				used[l.ID] = struct{}{}
			}
		}
		if !c.IsLive() {
			return nil
		}
		live, err := r.Courses.FindVersion(ctx, courseID, c.Live.Number)
		if err != nil {
			return err
		}
		for _, l := range live.Lectures {
			blocks, err := r.Contents.ListVersionBlocks(ctx, courseID, c.Live.Number, l.ID)
			if err != nil {
				return err
			}
			if blocksReference(blocks, assetID) {
				used[l.ID] = struct{}{}
			}
		}
		return nil
	})
	return slices.Sorted(maps.Keys(used)), err
}
```

`id.ID` must be ordered (an integer type) for `slices.Sorted`; it is (`int64` underlying, see `int64(courseID)` casts).

In `internal/media/app/errors.go` add `ErrAssetInUse = errors.New("asset is in use")` and:

```go
// InUseError is ErrAssetInUse with the lectures that reference the asset.
type InUseError struct{ LectureIDs []id.ID }

func (e *InUseError) Error() string { return ErrAssetInUse.Error() }
func (e *InUseError) Unwrap() error { return ErrAssetInUse }
```

In `ports.go` add to `CourseAccess`:

```go
	// AssetUsage returns the lectures whose working-copy or live-version blocks reference assetID.
	AssetUsage(ctx context.Context, courseID, assetID id.ID) ([]id.ID, error)
```

Replace `AssetService.Delete`:

```go
// Delete removes an asset no lecture of the working copy or live version uses: the remote asset
// first, then the row. A block added concurrently can slip past the check; course submit
// re-checks references, so the live version never points at a deleted asset.
func (s *AssetService) Delete(ctx context.Context, p auth.Principal, assetID id.ID) error {
	a, err := s.managed(ctx, p, assetID)
	if err != nil {
		return err
	}
	lectures, err := s.courses.AssetUsage(ctx, a.CourseID, assetID)
	if err != nil {
		return err
	}
	if len(lectures) > 0 {
		return &InUseError{LectureIDs: lectures}
	}
	if err := s.remote.Delete(ctx, assetID); err != nil && !errors.Is(err, ErrRemoteNotFound) {
		return err
	}
	return s.assets.Delete(ctx, assetID)
}
```

Check `managed`'s return value: if it returns only `error`, change it to return `(domain.Asset, error)` (grep its callers; `Status` already uses `a, err := s.managed(...)`, so it does).

In `internal/media/app/fakes_test.go` add `usage map[id.ID][]id.ID` to `fakeAccess` and:

```go
func (f fakeAccess) AssetUsage(_ context.Context, _, assetID id.ID) ([]id.ID, error) {
	return f.usage[assetID], nil
}
```

In `cmd/api/media.go` add to `mediaCourseAccess`:

```go
func (a mediaCourseAccess) AssetUsage(ctx context.Context, courseID, assetID id.ID) ([]id.ID, error) {
	return a.contents.AssetUsage(ctx, courseID, assetID)
}
```

- [x] **Step 4: Map the error over HTTP**

In `internal/media/adapters/httpapi/httpapi.go`, at the top of `writeError`, before the table loop:

```go
	if inUse := (*app.InUseError)(nil); errors.As(err, &inUse) {
		ids := make([]string, len(inUse.LectureIDs))
		for i, v := range inUse.LectureIDs {
			ids[i] = v.String()
		}
		problem.WriteWithExtensions(w, r, http.StatusConflict, "asset_in_use", "Asset In Use", "",
			map[string]any{"lecture_ids": ids})
		return
	}
```

Check `problem.WriteWithExtensions`' exact signature in `internal/platform/problem` (it is used in `internal/courseauthoring/adapters/httpapi/httpapi.go:195`) and match it. Add a handler test in the media httpapi test file asserting a 409 with `type` `asset_in_use` and `lecture_ids` `["50"]`, following the file's existing Delete test.

- [x] **Step 5: Run tests**

Run: `go test ./internal/courseauthoring/... ./internal/media/... ./cmd/api/`
Expected: PASS.

- [x] **Step 6: Commit**

```bash
git add internal/courseauthoring/app internal/media cmd/api/media.go
git commit -m "feat(media): refuse deleting assets the course uses"
```

---

### Task 8: Wiring, HTTP and OpenAPI

**Files:**
- Modify: `internal/assessment/adapters/httpapi/httpapi.go`, `wire.go`, `exam_wire.go`, `exams.go`, and the quiz handler file; their tests
- Modify: `api/openapi.yaml`

**Interfaces:**
- Consumes: Tasks 3–7.
- Produces HTTP: `?version=<n>` (integer ≥ 1) on `GET /v1/courses/{courseID}/lectures/{lectureID}/quizzes` and `GET /v1/courses/{courseID}/exams/authoring`; `revision` (integer) on quiz responses, exam authoring and student exam responses, and exam attempt responses; `locks` removed from exam authoring responses; `exam_has_attempts` and edit-violation problem types removed.

- [x] **Step 1: Update the HTTP handlers**

In `internal/assessment/adapters/httpapi`:
- Quiz list handler: parse `version` with a helper and pass it to `List`:

```go
// versionParam reads ?version=; 0 when absent. A malformed or non-positive value is invalid input.
func versionParam(r *http.Request) (int, error) {
	raw := r.URL.Query().Get("version")
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("%w: version must be a positive integer", app.ErrInvalidInput)
	}
	return n, nil
}
```

- Exam authoring list handler: same, passed to `ListAuthoring`.
- Wire types: add `Revision int \`json:"revision"\`` to the quiz, exam (authoring and student) and exam attempt wire structs, filled from `.Revision`; delete the `locks` wire fields and the `EditViolation` branch in `writeError`; delete the `ErrExamHasAttempts` row.
- Handlers that returned `app.ExamDetail` now receive `domain.Exam`.
- Update handler tests to the new shapes; add one test that `?version=0` and `?version=x` return 400 `invalid_input`.

- [x] **Step 2: Update OpenAPI**

In `api/openapi.yaml`:
- Add a `version` query parameter (integer, minimum 1, description "Published version to read; managers only. Omit for the working copy (managers) or the live version (everyone else).") to `GET /v1/courses/{courseID}/lectures/{lectureID}/quizzes` and `GET /v1/courses/{courseID}/exams/authoring`.
- Add `revision` (integer, minimum 1, required) to the quiz, exam and exam-attempt schemas; remove the `locks` property and any `ExamLocks` schema.
- Add response `409` (`course_not_editable`) to quiz create/update/delete and exam create/save/settings/order/publish/unpublish/duplicate/delete; remove `exam_has_attempts` and edit-violation types.
- Change `/v1/exams/{examID}/publish` and `/unpublish` descriptions to: "Changes the exam's draft. Students see the change after the course is next published."
- Add response `409` with type `asset_in_use` and an extension `lecture_ids` (array of ID strings) to `DELETE /v1/media/assets/{assetID}`.
- Add `400` `invalid_quiz_reference` and `invalid_media_reference` to `POST /v1/courses/{courseID}/submit` (path as in the file).

Run the repository's OpenAPI lint if one exists (`grep -n openapi Makefile .github -r`); otherwise `docker run --rm -v "$PWD":/w redocly/cli lint /w/api/openapi.yaml` is optional.

- [x] **Step 3: Run all gates**

Run: `make check`
Expected: PASS.

- [x] **Step 4: Commit**

```bash
git add internal/assessment/adapters/httpapi api/openapi.yaml
git commit -m "feat(api): expose pinned revisions and new conflicts"
```

---

### Task 9: End-to-end scenarios

**Files:**
- Modify: `cmd/api/e2e_integration_test.go`

- [x] **Step 1: Fix existing end-to-end tests for the new rules**

Run: `make test-integration`. Expected failures and fixes:
- `TestExamsEndToEnd`: publishing an exam on a published course now moves the course to draft and does not show the exam. After `POST /v1/exams/{id}/publish`, add `POST /v1/courses/{courseID}/publish` as admin (reviewer bypass) and expect 204 before students list or start.
- `TestQuizzesEndToEnd`: deleting a quiz on a published course now succeeds (204) and reopens the course as draft; students still see the quiz. Update assertions after the delete accordingly.
- Any assertion on `locks` or `exam_has_attempts` is removed.

- [x] **Step 2: Add the versioning scenario**

Add `TestAssessmentVersioningEndToEnd` to `cmd/api/e2e_integration_test.go`, built from the same setup steps `TestQuizzesEndToEnd` and `TestExamsEndToEnd` use (sign in admin, instructor and student; create course with a lecture; enroll the student). Then:

```go
	// 1. Publish with a quiz; edit it in the draft: students still see the original.
	quizID := createQuiz(t, c, quizzesPath, "2+2?", admin) // existing inline steps as a local helper
	putQuizBlock(t, c, courseID, lectures[0], quizID, admin)
	mustStatus(t, c, http.MethodPost, "/v1/courses/"+courseID+"/publish", "", admin, http.StatusNoContent)
	mustStatus(t, c, http.MethodPut, quizzesPath+"/"+quizID, quizBody("3+3?"), admin, http.StatusOK)
	_, list := c.do(http.MethodGet, quizzesPath, "", student)
	if prompt := firstPrompt(list); prompt != "2+2?" {
		t.Fatalf("student sees %q before republish", prompt)
	}

	// 2. Start an exam attempt, change the exam, republish: the attempt keeps its revision and
	//    the student now sees the new quiz.
	examID := createPublishedExam(t, c, courseID, admin) // create, publish exam, publish course
	_, started := c.do(http.MethodPost, "/v1/exams/"+examID+"/attempts", "", student)
	startedRevision := started["exam"].(map[string]any)["revision"]
	mustStatus(t, c, http.MethodPut, "/v1/exams/"+examID, examBody("Changed"), admin, http.StatusOK)
	mustStatus(t, c, http.MethodPost, "/v1/courses/"+courseID+"/publish", "", admin, http.StatusNoContent)
	_, list = c.do(http.MethodGet, quizzesPath, "", student)
	if prompt := firstPrompt(list); prompt != "3+3?" {
		t.Fatalf("student sees %q after republish", prompt)
	}
	attemptID := started["attempt"].(map[string]any)["id"].(string)
	_, submitted := c.do(http.MethodPost, "/v1/exam-attempts/"+attemptID+"/submit", "", student)
	if got := submitted["exam"].(map[string]any)["revision"]; got != startedRevision {
		t.Fatalf("graded against revision %v, want %v", got, startedRevision)
	}

	// 3. Delete the quiz, discard the draft: the quiz is back for the manager.
	mustStatus(t, c, http.MethodDelete, "/v1/quizzes/"+quizID, "", admin, http.StatusNoContent)
	mustStatus(t, c, http.MethodPost, "/v1/courses/"+courseID+"/discard-draft", "", admin, http.StatusOK)
	waitFor(t, func() bool { // the restore runs through the outbox
		_, mine := c.do(http.MethodGet, quizzesPath, "", admin)
		return firstPrompt(mine) == "3+3?"
	})
```

Write the small helpers (`mustStatus`, `firstPrompt`, `waitFor` polling every 100 ms for up to 10 s, `createQuiz`, `putQuizBlock`, `createPublishedExam`, `quizBody`, `examBody`) at the bottom of the file, reusing the request bodies already inlined in `TestQuizzesEndToEnd` and `TestExamsEndToEnd`. Match the attempt submit path, the discard-draft status code and the start/submit response field names to what `api/openapi.yaml` and the existing exam e2e test use; the names above are the expected ones.

Add the media case to `TestMediaEndToEnd` after its video block is published:

```go
	resp, body := c.do(http.MethodDelete, "/v1/media/assets/"+videoID, "", admin)
	if resp.StatusCode != http.StatusConflict || body["type"] != "asset_in_use" {
		t.Fatalf("delete live video: %d %v", resp.StatusCode, body)
	}
```

Confirm the test's outbox forwarder is running (the e2e harness starts `application`; if the forwarder is not started in tests, start it the way `main.go` does).

- [x] **Step 3: Run the gates**

Run: `make check && make test-integration && docker compose config >/dev/null && make docker-build && git diff --check`
Expected: all PASS.

- [ ] **Step 4: Commit**

```bash
git add cmd/api/e2e_integration_test.go
git commit -m "test(api): cover assessment and media versioning end to end"
```

---

## Self-Review Notes

- Spec coverage: revision tables and soft delete (Tasks 1–2); pins at submit, publish, reviewer bypass (Task 3); edit freeze via `BeginEdit` (Tasks 4, 5, 8); student reads from live pins, exam visibility by pinned status, manager `?version=` (Tasks 4, 5, 8); attempts pinned (Tasks 4, 5); `DraftDiscarded` restore, idempotent (Tasks 3, 6); media in-use (Task 7); submit reference re-check (Task 3); errors and OpenAPI (Tasks 7, 8); migration backfill (Task 1); E2E (Task 9).
- Known build gap: `internal/assessment/app` (and so `cmd/api`) does not compile between Task 2 and Task 5; Task 4 compiles `cmd/api` against the new `CourseAccess` but exam code is fixed in Task 5. Executors that require green commits squash Tasks 2, 4 and 5.
- Interface names used across tasks: `Pins`, `Ref`, `Kind{Quiz,Exam}`, `Head`, `AssessmentQuery.Heads`, `CourseAccess.{BeginEdit,LivePins,VersionPins}`, `CourseService.{BeginAssessmentEdit,LivePins,VersionPins}`, `ContentService.AssetUsage`, `domain.AssessmentPin`, `domain.DraftDiscarded`, `RestoreService.RestorePins`.
