# Exams Design

Date: 2026-10-06

## Status

Approved in conversation on 2026-10-06. Pending written-spec review.

## Context

The quizzes slice (`2026-10-06-quizzes-design.md`) split assessment in two and deferred exams.
This spec covers exams and reuses the quiz question model.

`ioe-frontend` already implements exams against a fixed contract in
`packages/course-core/src/api/assessment.ts` and `packages/course-core/src/model/types.ts`.
Every one of its 18 exam routes is called by a component, and the components branch on these
error codes: `attempt_expired`, `attempt_submitted`, `edit_add_during_attempt`,
`edit_key_frozen`, `edit_question_answered`, `edit_would_truncate_attempt`, `exam_closed`,
`exam_has_attempts`, `exam_not_open`, `open_attempt_exists`, `retakes_not_allowed`,
`reveal_disabled`, `reveal_not_yet`. `ExamReviewPanel` reads `details.reveal_at` from the error
body.

Unlike quizzes, exams are graded on the server. The answer key never reaches a student before
the exam's reveal policy allows it. The client renders its countdown from the server-computed
`deadline` and saves each answer as the student moves.

`hitox-backend/internal/assessment` implements this contract with tenancy. This slice ports its
exam half, trims tenancy, drops the `during_attempt` reveal policy (the frontend type has no such
value), and moves the edit-safety rules into the domain.

## Goals

- A course manager (the owning instructor or a root admin) creates, edits, reorders, publishes,
  unpublishes, duplicates, and deletes exams in a course that is not archived, and reads exams and
  attempts in any course they manage, archived included.
- An actively enrolled student in a published course lists published exams, reads one without
  answer keys, starts an attempt inside the exam's window, saves answers as they go, submits, and
  reads a server-graded result.
- A timed or windowed attempt never accepts writes after its deadline, and an expired attempt is
  graded as of its deadline the next time anything reads or touches it.
- Answer keys are revealed to students only as the exam's reveal policy allows; managers always
  see them.
- Edits that would invalidate live or past attempts are refused with a specific code.

## Non-goals

- Domain events, notifications, progress or certificate integration. Results live only in
  assessment.
- The `during_attempt` reveal policy.
- Partial credit, hints, question pools, randomized order.
- Exam versioning or optimistic concurrency between two managers. The last write wins under the
  row lock.
- The problem-envelope mismatch: the frontend's `CourseApiError` reads `body.code`, while this
  backend's problem envelope carries the code in `type`. It affects every context and is fixed
  separately. Exams use the envelope as it is.
- A background job that settles expired attempts.

## Decisions

| Topic | Decision |
|---|---|
| Context | `internal/assessment`, schema `assessment` |
| Questions | `domain.Question` and `NewQuestion` from quizzes, unchanged |
| Exam storage | One row per exam, questions as `jsonb` |
| Attempt storage | One row per attempt, answers as a `jsonb` object keyed by question ID |
| Deadline | Computed, never stored: earlier of `started_at + time_limit` and `closes_at` |
| Expiry | Lazy settlement inside whichever transaction reads or touches the attempt |
| Grading | Exact option-set match per question, no partial credit; score is earned × 100 / possible, rounded down |
| Reveal policies | `after_attempt`, `after_close`, `never` |
| One open attempt | Partial unique index on `(exam_id, user_id) WHERE submitted_at IS NULL` |
| Draft visibility | Drafts do not exist for students: `404 not_found` |
| Unpublish | Allowed anytime; blocks new starts, open attempts continue |
| Delete | Refused once any attempt exists (`exam_has_attempts`) |
| Edit safety | Domain rule `CheckExamEdit`, ported from hitox |
| Authorization source | Courseauthoring, through ports wired in `cmd/api` |
| Events | None |

## Domain (`internal/assessment/domain`)

### Shared with quizzes

`Question`, `Option`, `QuestionType`, and `NewQuestion` are unchanged. The check that question and
option IDs are unique across a set moves from `NewQuiz` into an unexported helper
`checkUniqueIDs(questions) error` that `NewQuiz` and `NewExam` both call, each wrapping its own
error (`ErrInvalidQuiz`, `ErrInvalidExam`).

### Exam

```go
type ExamStatus string // "draft" | "published"

type RevealPolicy string // "after_attempt" | "after_close" | "never"

type Exam struct {
    ID             id.ID
    CourseID       id.ID
    Title          string
    Description    string
    Position       int
    Status         ExamStatus
    PassMark       int           // percent
    TimeLimit      time.Duration // zero means untimed
    RetakesAllowed bool
    OpensAt        *time.Time
    ClosesAt       *time.Time
    RevealPolicy   RevealPolicy
    Questions      []Question
    CreatedAt      time.Time
    UpdatedAt      time.Time
}
```

`NewExam(id, courseID, title, description, position, status, passMark, timeLimit, retakes,
opensAt, closesAt, revealPolicy, questions, createdAt, updatedAt)` trims title and description
and returns `ErrInvalidExam` unless:

- the title is 1 to 200 characters and the description at most 5,000;
- the position is 0 to 10,000;
- the status and reveal policy are known values;
- the pass mark is 0 to 100;
- the time limit is zero or 1 second to 24 hours, in whole seconds;
- `ClosesAt` is after `OpensAt` when both are set;
- `after_close` has a `ClosesAt`;
- there are 1 to 100 questions with unique question and option IDs.

Timestamps are stored in UTC.

`Exam.Availability(now)` returns `not_open` before `OpensAt`, `closed` at or after `ClosesAt`, and
`open` otherwise.

`Exam.TotalPoints()` sums question points.

### Exam attempt

```go
type ExamAnswer struct {
    QuestionID     id.ID
    OptionIDs      []id.ID
    IsCorrect      *bool // set by grading
    PointsPossible int   // set by grading
    PointsAwarded  *int  // set by grading
}

type ExamAttempt struct {
    ID            id.ID
    ExamID        id.ID
    CourseID      id.ID
    UserID        id.ID
    StartedAt     time.Time
    SubmittedAt   *time.Time
    Score         *int
    Passed        *bool
    AutoSubmitted bool
    Answers       []ExamAnswer // ordered as the exam's questions once graded
}
```

- `Deadline(exam) *time.Time` returns the earlier of `StartedAt + TimeLimit` (when timed) and
  `ClosesAt`, or nil. Because it is computed from the current exam, extending the time limit or
  the close time extends open attempts. The edit guard refuses shortening while attempts are open.
- `CheckWritable(exam, now)` returns `ErrAttemptSubmitted` when submitted and `ErrAttemptExpired`
  when `now` is after the deadline.
- `CheckAnswer(exam, answer)` returns `ErrInvalidAnswer` when the question is not in the exam, an
  option is not in that question, or an option repeats. `single_choice` and `true_false` accept at
  most one option. An empty option list clears the answer.
- `Grade(exam, at)` builds one answer per exam question in exam order. A question is correct when
  the chosen option set equals the correct set exactly. Each answer stores `PointsPossible`,
  `PointsAwarded`, and `IsCorrect`, so later point edits never rewrite past results. Unanswered
  questions get an empty option list and are wrong. The score is earned × 100 / possible, rounded
  down; `Passed` is score ≥ `PassMark`; `SubmittedAt` is `at`. Grading does not check
  writability; callers do.
- `Settle(exam, now) bool` grades at the deadline and sets `AutoSubmitted` when the attempt is
  open and `now` is after its deadline, and reports whether it changed the attempt.
- `CheckReveal(exam, now)` returns, in order: `ErrRevealAttemptOpen` when not submitted;
  `ErrRevealDisabled` for `never`; `RevealNotYetError{At: ClosesAt}` (wrapping `ErrRevealNotYet`)
  for `after_close` before `ClosesAt`; nil otherwise.

### Edit guard

```go
type Locks struct {
    OpenAttempts        int
    SubmittedAttempts   int
    AnsweredQuestionIDs map[id.ID]struct{} // questions with a non-empty answer in any attempt
}

// EditViolation wraps one of the ErrEdit* errors and names the offending question and option
// when there is one.
type EditViolation struct {
    Err        error
    QuestionID id.ID // zero when not applicable
    OptionID   id.ID // zero when not applicable
}

func CheckExamEdit(current, next Exam, locks Locks) error
```

`CheckExamEdit` returns the first violation, checked in this order:

1. Open attempts and the deadline shortens: `next.ClosesAt` is set and either `current.ClosesAt`
   is nil or later, or `next.TimeLimit` is non-zero and either `current.TimeLimit` is zero or
   longer. Returns `ErrEditWouldTruncateAttempt`.
2. Submitted attempts and the reveal policy loosens. Policies rank `after_attempt` (most open),
   `after_close`, `never`; loosening is a move to a more open rank. Returns `ErrEditKeyFrozen`.
3. For each question in `next` that is not in `current`: open attempts make it
   `ErrEditAddDuringAttempt` with that question ID.
4. For each question in both, when any attempt exists: a changed type, a changed option ID set, or
   a changed `IsCorrect` on any option is `ErrEditKeyFrozen` with the question ID and, when one
   option is at fault, its ID.
5. For each question in `current` that is not in `next`: an answered question is
   `ErrEditQuestionAnswered`; otherwise open attempts make it `ErrEditAddDuringAttempt`. Both
   carry the question ID.

Title, description, pass mark, retakes, opening the window earlier, extending the deadline,
tightening the reveal policy, prompt, explanation, option labels, reference lecture, points, and
question order are always editable.

### Errors

`ErrInvalidExam`, `ErrInvalidAnswer` (existing), `ErrAttemptSubmitted`, `ErrAttemptExpired`,
`ErrRevealAttemptOpen`, `ErrRevealDisabled`, `ErrRevealNotYet`, `ErrEditWouldTruncateAttempt`,
`ErrEditAddDuringAttempt`, `ErrEditQuestionAnswered`, `ErrEditKeyFrozen`, plus
`RevealNotYetError{At}` and `EditViolation`.

## Application (`internal/assessment/app`)

### Ports

```go
// Repos are the repositories of one transaction.
type Repos struct {
    Quizzes QuizRepository
    Exams   ExamRepository
}

type TxRunner interface {
    RunInTx(ctx context.Context, fn func(Repos) error) error
}

// ExamRepository reads and writes exams and attempts in the current transaction.
type ExamRepository interface {
    // Find returns ErrNotFound when absent. lock selects no lock, FOR SHARE, or FOR UPDATE
    // (see Concurrency).
    Find(ctx context.Context, examID id.ID, lock LockMode) (domain.Exam, error)
    // ListByCourse orders by position, then ID.
    ListByCourse(ctx context.Context, courseID id.ID, publishedOnly bool) ([]domain.Exam, error)
    Insert(ctx context.Context, e domain.Exam) error
    // Replace overwrites every mutable column; ErrNotFound when absent.
    Replace(ctx context.Context, e domain.Exam) error
    // Delete removes the exam; ErrExamHasAttempts when an attempt references it.
    Delete(ctx context.Context, examID id.ID) error
    // NextPosition returns one past the highest position in the course, or 0.
    NextPosition(ctx context.Context, courseID id.ID) (int, error)
    // SetPositions sets each exam's position to its index in examIDs.
    SetPositions(ctx context.Context, courseID id.ID, examIDs []id.ID) error

    Locks(ctx context.Context, examID id.ID) (domain.Locks, error)
    // FindAttempt returns ErrNotFound when absent.
    FindAttempt(ctx context.Context, attemptID id.ID, forUpdate bool) (domain.ExamAttempt, error)
    // FindOpenAttempt returns ErrNotFound when the user has no open attempt.
    FindOpenAttempt(ctx context.Context, examID, userID id.ID) (domain.ExamAttempt, error)
    HasSubmitted(ctx context.Context, examID, userID id.ID) (bool, error)
    // InsertAttempt returns ErrOpenAttemptExists when the user already has an open attempt.
    InsertAttempt(ctx context.Context, a domain.ExamAttempt) error
    // MergeAnswer sets one answer on an open attempt; ErrAttemptSubmitted when it is closed.
    MergeAnswer(ctx context.Context, attemptID id.ID, a domain.ExamAnswer) error
    // SaveResult writes grading fields on an open attempt; ErrAttemptSubmitted when closed.
    SaveResult(ctx context.Context, a domain.ExamAttempt) error
    // ListAttempts orders by start time, then ID.
    ListAttempts(ctx context.Context, examID id.ID) ([]domain.ExamAttempt, error)
    // ListUserAttempts returns the user's attempts on every exam of the course.
    ListUserAttempts(ctx context.Context, courseID, userID id.ID) ([]domain.ExamAttempt, error)
}

type LockMode int // LockNone, LockShare, LockUpdate

// CourseAccess is backed by courseauthoring.
type CourseAccess interface {
    CanManageLecture(ctx context.Context, p auth.Principal, courseID, lectureID id.ID) error
    CanReadLecture(ctx context.Context, p auth.Principal, courseID, lectureID id.ID) error
    // CanManageCourse: p manages the course and it is not archived. ErrNotFound, ErrForbidden,
    // or ErrCourseNotEditable.
    CanManageCourse(ctx context.Context, p auth.Principal, courseID id.ID) error
    // CanReadAsManager: p manages the course, archived allowed. ErrNotFound or ErrForbidden.
    CanReadAsManager(ctx context.Context, p auth.Principal, courseID id.ID) error
    // CanReadCourse: the course is visible to p (published, or p manages it). ErrNotFound.
    CanReadCourse(ctx context.Context, p auth.Principal, courseID id.ID) error
}
```

`EnrollmentQuery` is unchanged. `QuizService` switches from `func(QuizRepository)` to
`func(Repos)` and uses `r.Quizzes`; its behavior is unchanged.

### Concurrency

- Cross-context checks run before the assessment transaction opens, because each takes its own
  pool connection.
- Save, SaveSettings, Reorder, Publish, Unpublish, and Delete load the exam with `LockUpdate`.
  Start loads it with `LockShare`. An edit's `Locks` read and a concurrent start therefore
  serialize, so the guard never decides on stale locks.
- Submit and every settlement load the attempt `forUpdate`, and `SaveResult` only writes an open
  attempt, so an attempt is graded once.
- `MergeAnswer` is a single statement that only writes an open attempt, so concurrent saves of
  different questions never lose each other, and a save racing a submit either lands before it or
  fails with `attempt_submitted`.

### ExamService

`NewExamService(tx, courses, enrollments, ids, clock)`. Inputs mirror the wire; question inputs
reuse `QuestionInput` and `OptionInput` from quizzes.

```go
type ExamInput struct {
    Title, Description string
    Position           int
    PassMark           int
    TimeLimitSeconds   *int // nil means untimed
    RetakesAllowed     bool
    OpensAt, ClosesAt  *time.Time
    RevealPolicy       string
    Questions          []QuestionInput
}

type ExamSettingsInput struct {
    Title, Description string
    PassMark           int
    Points             map[string]int // question ID → points; omitted questions keep theirs
    TimeLimitSeconds   *int
    RetakesAllowed     bool
    OpensAt, ClosesAt  *time.Time
    RevealPolicy       string
}
```

Use cases that take only an exam or attempt ID load it first (without a lock, in its own
transaction) to learn its course, run the course check, then do the work in a second transaction
that reloads it with the lock it needs. A concurrent delete between the two surfaces as
`ErrNotFound`.

| Use case | Check | Behavior |
|---|---|---|
| `ListAuthoring(p, courseID)` | `CanReadAsManager` | Every exam in the course |
| `GetAuthoring(p, examID)` | `CanReadAsManager` | Exam plus `Locks` |
| `Create(p, courseID, in)` | `CanManageCourse` | Draft; new question and option IDs (supplied IDs are `ErrInvalidInput`); points default to 1 |
| `Save(p, examID, in)` | `CanManageCourse` | Supplied IDs must belong to the exam, omitted IDs are generated; keeps status and `CreatedAt`; `CheckExamEdit` against `Locks`; returns exam plus locks |
| `SaveSettings(p, examID, in)` | `CanManageCourse` | Keeps questions, position, status; applies `Points` (unknown question ID or points < 1 is `ErrInvalidInput`); `CheckExamEdit`; returns exam plus locks |
| `Reorder(p, courseID, examIDs)` | `CanManageCourse` | `examIDs` must be exactly the course's exams, no duplicates; else `ErrInvalidInput` |
| `Publish`, `Unpublish(p, examID)` | `CanManageCourse` | Sets status; idempotent |
| `Duplicate(p, examID)` | `CanManageCourse` | Deep copy with fresh IDs, title + " (copy)" truncated to 200 characters, draft, `NextPosition` |
| `Delete(p, examID)` | `CanManageCourse` | `ErrExamHasAttempts` when any attempt exists |
| `ListAttempts(p, examID)` | `CanReadAsManager` | Settles expired attempts, then lists |
| `List(p, courseID)` | `CanReadCourse`, active enrollment | Published exams; settles the caller's expired attempts; summary per exam |
| `Get(p, examID)` | `CanReadCourse`, active enrollment | Published only, else `ErrNotFound` |
| `Start(p, examID)` | `CanReadCourse`, active enrollment | See below |
| `SaveAnswer(p, attemptID, answer)` | Owner | `CheckWritable`, `CheckAnswer`, `MergeAnswer` |
| `Submit(p, attemptID)` | Owner | `CheckWritable`, `Grade(now)`, `SaveResult` |
| `GetAttempt(p, attemptID)` | Owner | Settles, then returns |
| `Review(p, attemptID)` | Owner or `CanReadAsManager` | Settles; the owner must pass `CheckReveal`; a manager always may |

A caller who is neither the owner nor (for review) a manager of the attempt's course gets
`ErrNotFound`, so attempt IDs are not disclosed.

`Start`, in one transaction after the course and enrollment checks:

1. Load the exam with `LockShare`. Not published is `ErrNotFound`.
2. `not_open` is `ExamNotOpenError{At: OpensAt}`; `closed` is `ExamClosedError{At: ClosesAt}`.
3. If the user has an open attempt, settle it. If it is still open, `ErrOpenAttemptExists`.
4. Without retakes, a submitted attempt is `ErrRetakesNotAllowed`.
5. Insert a new attempt started now. A concurrent start that wins the unique index makes this one
   `ErrOpenAttemptExists`.

The student summary per exam computes `availability` from now, `open_attempt_id` from the
caller's open attempt, `best_score` and `best_passed` from the highest-scoring submitted attempt
(ties broken by earliest submission), and `attempt_count` from all of the caller's attempts.

### App errors

Existing: `ErrNotFound`, `ErrForbidden`, `ErrInvalidInput`, `ErrCourseNotEditable`,
`ErrEnrollmentRequired`. New: `ErrExamNotOpen`, `ErrExamClosed` (each wrapped by an error type
carrying `At`), `ErrOpenAttemptExists`, `ErrRetakesNotAllowed`, `ErrExamHasAttempts`. Domain
validation errors (`ErrInvalidExam`, `ErrInvalidQuestion`, `ErrInvalidAnswer`) are wrapped in
`ErrInvalidInput`. Other domain errors pass through.

## Courseauthoring change

`CourseService.CheckManagerRead(ctx, p, courseID)` returns `loadManaged`'s error only: nil for a
manager of any course status, `ErrNotFound` when invisible, `ErrForbidden` otherwise.

## Persistence (`internal/assessment/adapters/postgres`)

Migration `migrations/00008_assessment_exams.sql`:

```sql
-- +goose Up
CREATE TABLE assessment.exams (
  id                 bigint PRIMARY KEY,
  course_id          bigint NOT NULL,
  title              text NOT NULL,
  description        text NOT NULL,
  position           integer NOT NULL CHECK (position >= 0),
  status             text NOT NULL CHECK (status IN ('draft', 'published')),
  pass_mark          integer NOT NULL CHECK (pass_mark BETWEEN 0 AND 100),
  time_limit_seconds integer CHECK (time_limit_seconds > 0),
  retakes_allowed    boolean NOT NULL,
  opens_at           timestamptz,
  closes_at          timestamptz,
  reveal_policy      text NOT NULL CHECK (reveal_policy IN ('after_attempt', 'after_close', 'never')),
  questions          jsonb NOT NULL,
  created_at         timestamptz NOT NULL,
  updated_at         timestamptz NOT NULL
);

CREATE INDEX exams_course_idx ON assessment.exams (course_id, position, id);

CREATE TABLE assessment.exam_attempts (
  id             bigint PRIMARY KEY,
  exam_id        bigint NOT NULL REFERENCES assessment.exams (id) ON DELETE RESTRICT,
  course_id      bigint NOT NULL,
  user_id        bigint NOT NULL,
  started_at     timestamptz NOT NULL,
  submitted_at   timestamptz,
  score          integer,
  passed         boolean,
  auto_submitted boolean NOT NULL DEFAULT false,
  answers        jsonb NOT NULL DEFAULT '{}'
);

CREATE INDEX exam_attempts_exam_idx ON assessment.exam_attempts (exam_id, started_at, id);
CREATE INDEX exam_attempts_user_idx ON assessment.exam_attempts (course_id, user_id);
CREATE UNIQUE INDEX exam_attempts_one_open_idx
  ON assessment.exam_attempts (exam_id, user_id) WHERE submitted_at IS NULL;

-- +goose Down
DROP TABLE assessment.exam_attempts;
DROP TABLE assessment.exams;
```

- Exam questions use the same JSON shape as quiz questions through one shared encoder in the
  adapter. IDs are stored as strings.
- Attempt answers are an object keyed by question ID:
  `{"<question id>": {"option_ids": ["…"], "is_correct": true, "points_possible": 2, "points_awarded": 2}}`.
  Grading fields are absent until graded. The adapter returns answers in the exam's question
  order.
- `MergeAnswer`: `UPDATE assessment.exam_attempts SET answers = answers || @patch WHERE id = @id
  AND submitted_at IS NULL`; zero rows is `ErrAttemptSubmitted`.
- `SaveResult`: updates score, passed, submitted_at, auto_submitted, and answers `WHERE id = @id
  AND submitted_at IS NULL`; zero rows is `ErrAttemptSubmitted`.
- `Locks`: open and submitted counts plus `DISTINCT` keys from `jsonb_each(answers)` whose
  `option_ids` array is non-empty.
- `InsertAttempt` maps a unique violation on `exam_attempts_one_open_idx` to
  `ErrOpenAttemptExists`; `Delete` maps a foreign-key violation to `ErrExamHasAttempts`.
- `SetPositions`: one `UPDATE ... FROM unnest(@ids::bigint[]) WITH ORDINALITY` restricted to the
  course.
- Queries are generated by sqlc from `queries.sql`. Exam code lives in `exams.go`; `TxRunner`
  builds `app.Repos`.

## HTTP (`internal/assessment/adapters/httpapi`)

All routes require authentication. Identity comes from the token only; the frontend's
`X-Debug-User-Id` header is ignored. Request bodies are capped at 1 MiB.

| Route | Success |
|---|---|
| `GET /v1/courses/{courseID}/exams/authoring` | `200` authoring summary array |
| `POST /v1/courses/{courseID}/exams` | `201` authoring exam |
| `PUT /v1/courses/{courseID}/exams-order` | `204` |
| `GET /v1/courses/{courseID}/exams` | `200` student summary array |
| `GET /v1/exams/{examID}/authoring` | `200` authoring exam |
| `PUT /v1/exams/{examID}` | `200` authoring exam |
| `PATCH /v1/exams/{examID}/settings` | `200` authoring exam |
| `POST /v1/exams/{examID}/publish` | `204` |
| `POST /v1/exams/{examID}/unpublish` | `204` |
| `POST /v1/exams/{examID}/duplicate` | `201` authoring exam |
| `DELETE /v1/exams/{examID}` | `204` |
| `GET /v1/exams/{examID}/attempts` | `200` attempt summary array |
| `GET /v1/exams/{examID}` | `200` student exam |
| `POST /v1/exams/{examID}/attempts` | `201` attempt |
| `POST /v1/exam-attempts/{attemptID}/answers` | `204` |
| `POST /v1/exam-attempts/{attemptID}/submit` | `200` attempt |
| `GET /v1/exam-attempts/{attemptID}` | `200` attempt |
| `GET /v1/exam-attempts/{attemptID}/review` | `200` review |

### Wire shapes

Field names and optionality follow `assessment.ts`. Optional timestamps (`opens_at`, `closes_at`,
`deadline`, `submitted_at`) and optional results (`score`, `passed`, `best_score`, `best_passed`,
`open_attempt_id`, `reference_lecture_id`) are omitted when absent. `time_limit_seconds` is a
number or `null`.

- Student question: `{id, prompt, type, points, options: [{id, label}], reference_lecture_id?}`.
  No `correct_option_ids` or `explanation`.
- Authoring question: student question plus `correct_option_ids` and `explanation`.
- Exam (`ExamWire`): `{id, course_id, title, description, position, pass_mark,
  time_limit_seconds, retakes_allowed, opens_at?, closes_at?, reveal_policy, questions}`.
- Authoring exam: exam plus `status` and `locks: {has_open_attempts, has_submitted_attempts,
  answered_question_ids, open_attempt_count, submitted_attempt_count}`, always present.
- Authoring summary: settings plus `status`, `question_count`, `total_points`.
- Student summary: settings plus `question_count`, `total_points`, `availability`,
  `open_attempt_id?`, `best_score?`, `best_passed?`, `attempt_count`.
- Attempt: `{id, course_id, exam_id, user_id, started_at, deadline?, submitted_at?, score?,
  passed?, auto_submitted, answers}`. Each answer is `{question_id, option_ids}` while open, and
  adds `is_correct`, `points_possible`, `points_awarded`, `reference_lecture_id?` once graded.
- Attempt summary: `{id, user_id, started_at, submitted_at?, score?, passed?, auto_submitted,
  still_open}`.
- Review: `{attempt_id, exam_id, title, score, passed, pass_mark, submitted_at, auto_submitted,
  questions}`, each question `{id, position, prompt, type, options, selected_option_ids,
  correct_option_ids, explanation, points_possible, points_awarded, reference_lecture_id?}`.
  Points come from the graded answer; prompt, options, and key come from the current exam.
  `pass_mark` is the exam's current pass mark.

Inputs:

- Save and create: `{title, description, position, pass_mark, time_limit_seconds, retakes_allowed,
  opens_at, closes_at, reveal_policy, questions}`. Question input is the quiz question input with
  `points`. An empty string `id` or `reference_lecture_id` means absent. `opens_at` and
  `closes_at` are RFC 3339 or `null`.
- Settings: `{title, description, pass_mark, points, time_limit_seconds, retakes_allowed,
  opens_at, closes_at, reveal_policy}`, where `points` is an object of question ID to points.
- Reorder: `{exam_ids}`. Save answer: `{question_id, option_ids}`.

### Errors

Errors use `problem.Write`, with the code in `type`. Codes that carry data use
`problem.WriteWithExtensions` with a `details` member.

| Error | Status | Code | `details` |
|---|---|---|---|
| `ErrInvalidInput`, malformed body or ID | 400 | `invalid_input` | |
| `ErrForbidden` | 403 | `forbidden` | |
| `ErrNotFound` | 404 | `not_found` | |
| `ErrCourseNotEditable` | 409 | `course_not_editable` | |
| `ErrEnrollmentRequired` | 409 | `enrollment_required` | |
| `ErrExamNotOpen` | 409 | `exam_not_open` | `opens_at` |
| `ErrExamClosed` | 409 | `exam_closed` | `closes_at` |
| `ErrOpenAttemptExists` | 409 | `open_attempt_exists` | |
| `ErrRetakesNotAllowed` | 409 | `retakes_not_allowed` | |
| `ErrAttemptSubmitted` | 409 | `attempt_submitted` | |
| `ErrAttemptExpired` | 409 | `attempt_expired` | |
| `ErrExamHasAttempts` | 409 | `exam_has_attempts` | |
| `ErrRevealAttemptOpen` | 409 | `reveal_attempt_open` | |
| `ErrRevealDisabled` | 403 | `reveal_disabled` | |
| `ErrRevealNotYet` | 403 | `reveal_not_yet` | `reveal_at` |
| `ErrEditWouldTruncateAttempt` | 409 | `edit_would_truncate_attempt` | |
| `ErrEditAddDuringAttempt` | 409 | `edit_add_during_attempt` | `question_id` |
| `ErrEditQuestionAnswered` | 409 | `edit_question_answered` | `question_id` |
| `ErrEditKeyFrozen` | 409 | `edit_key_frozen` | `question_id`, `option_id` when known |

## Wiring (`cmd/api`)

- `assessmentCourseAccess` gains `CanManageCourse` over `CourseService.CheckManage`,
  `CanReadAsManager` over `CourseService.CheckManagerRead`, and `CanReadCourse` over
  `CourseService.Get` (error only), all through `toAssessmentError`.
- `registerAssessment` builds `ExamService` beside `QuizService` and passes both to the assessment
  HTTP adapter.
- `api/openapi.yaml` gains the 18 routes, schemas, and error codes.
- No depguard change; the context already exists.

## Testing

- Domain: exam validation table; availability; deadline (time limit only, close only, both with
  each winning, neither); grading (exact set, multiple choice partial pick is wrong, rounding down,
  unanswered, pass mark boundary); settle before and after the deadline; reveal rules; every
  `CheckExamEdit` rule with and without each lock, and free edits under every lock; `CheckAnswer`.
- App with fakes: each row of the authorization table; drafts hidden from students; start window,
  stale open attempt settled on start, retakes; reorder set mismatch; settings with an unknown
  question ID; duplicate fresh IDs and draft status; delete with attempts; non-owner attempt
  access is `ErrNotFound`; manager review bypasses reveal; best-score summary.
- HTTP: wire shapes, including that student reads carry no `correct_option_ids` or
  `explanation`; status and code mapping; `details` members.
- Postgres integration: insert, replace, list order, `SetPositions`; concurrent starts yield one
  open attempt and `ErrOpenAttemptExists`; concurrent `MergeAnswer` on different questions keeps
  both; `MergeAnswer` and `SaveResult` after submit return `ErrAttemptSubmitted`; `Locks` counts
  and answered IDs; deleting an exam with attempts returns `ErrExamHasAttempts`.
- Courseauthoring: `CheckManagerRead` allows a manager of an archived course and rejects a
  non-manager and an invisible course.
- `cmd/api` end-to-end: an instructor creates and publishes an exam; an enrolled student lists it,
  starts, saves an answer, submits, and reviews under `after_attempt`; a second start without
  retakes is `409 retakes_not_allowed`; the instructor edits wording successfully and gets
  `409 edit_key_frozen` changing the key; a timed attempt past its deadline reads back
  `auto_submitted`; an unenrolled user gets `409 enrollment_required`.

## Rollout and rollback

The migration only adds two tables. Rolling back drops them with their exams and attempts. No
existing data changes.

## Required gates

`make check`, `make test-integration` (actually run), `docker compose config`,
`make docker-build`, `git diff --check`.
