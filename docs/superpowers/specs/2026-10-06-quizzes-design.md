# Quizzes Design

Date: 2026-10-06

## Status

Approved in conversation on 2026-10-06. Pending written-spec review.

## Context

Lecture content already has a `quiz` block kind, but the courseauthoring spec stores its `quiz_id`
unchecked and defers validation until an assessment context exists. Nothing stores quizzes, so a
quiz block references nothing.

Assessment covers quizzes and exams. It is split into two slices; this spec covers quizzes only.
Exams (timed attempts, open and close windows, reveal policy, retakes, settlement) follow in a
separate spec and reuse the question model defined here.

`ioe-frontend` already implements quizzes against a fixed contract in
`packages/course-core/src/api/assessment.ts`:

- `GET /v1/courses/{courseID}/lectures/{lectureID}/quizzes` returns every quiz of a lecture,
  answer keys included. The student client grades locally ("zero navigation, zero reload, zero
  spinner").
- `POST /v1/courses/{courseID}/lectures/{lectureID}/quizzes` and
  `PUT /v1/courses/{courseID}/lectures/{lectureID}/quizzes/{quizID}` take
  `{position, questions}` and return the quiz.
- `DELETE /v1/quizzes/{quizID}`.
- `POST /v1/quizzes/{quizID}/attempts` takes `{user_id, answers:[{question_id, option_ids}]}` with
  an `Idempotency-Key` header and returns `{id, recorded}`. The client retries failed writes with
  the same key.
- `PUT /v1/courses/{courseID}/lectures/{lectureID}/quizzes-order` exists in the client but no
  component calls it.

The flow: `QuizBlockEditor` creates or updates the quiz first, then `LectureEditor` stores the
returned ID in the block's `quiz_id` and saves lecture content. `LectureReader` lists the
lecture's quizzes and resolves each quiz block by ID, tolerating a missing quiz.

`hitox-backend/internal/assessment` implements this contract with tenancy and tenant-wide
permissions. This slice ports its quiz half, trims tenancy, and closes two gaps there:
`DeleteQuiz` checked only a tenant-wide permission, not the quiz's course, and
`RecordQuizAttempt` trusted `user_id` from the body, so any caller could record attempts for any
user.

## Goals

- A course manager (the owning instructor or a root admin) creates, replaces, and deletes quizzes
  in a lecture of a course that is not archived.
- Anyone who may read the lecture's content lists its quizzes with answer keys.
- An actively enrolled student records a quiz attempt for themselves; retrying with the same
  idempotency key records it once.
- Courseauthoring rejects a quiz block whose `quiz_id` is not a quiz of that course and lecture.

## Non-goals

- Exams. A separate spec.
- Reordering quizzes. The route is unused by the frontend; `position` is stored as sent.
- Server-side grading or reading attempts back. No consumer exists.
- Hints, quiz versions, optimistic concurrency, content-changed signals, domain events.
- Removing a quiz block when its quiz is deleted, or removing quizzes when their lecture or
  course is removed. The reader tolerates a missing quiz, and orphaned quizzes are unreachable.
- Echoing `points` and `reference_lecture_id` on the quiz wire. They are accepted and stored for
  the exam slice, but the frontend quiz wire has no field for them.

## Decisions

| Topic | Decision |
|---|---|
| Context | `internal/assessment`, schema `assessment` |
| Question types | `single_choice`, `multiple_choice`, `true_false` |
| Write quiz | Course manager, course not archived, lecture in course |
| List quizzes | Lecture content read rule: manager, free-preview lecture, or active enrollment; unpublished courses hidden from non-managers |
| Record attempt | Body `user_id` must equal the caller; lecture readable; active enrollment |
| Missing enrollment | `409 enrollment_required` |
| Attempt storage | Answers as submitted, no grading |
| Idempotency | Same quiz, user, and key returns the original attempt ID |
| Quiz storage | Questions as `jsonb` on the quiz row |
| Delete quiz | Cascades its attempts; blocks referencing it stay |
| `quiz_id` validation | Courseauthoring checks upserted quiz blocks through a `QuizCatalog` port; `400 invalid_quiz_reference` |
| Authorization source | Courseauthoring, through ports wired in `cmd/api` (as media) |

## Domain (`internal/assessment/domain`)

```go
type QuestionType string

const (
    QuestionSingleChoice   QuestionType = "single_choice"
    QuestionMultipleChoice QuestionType = "multiple_choice"
    QuestionTrueFalse      QuestionType = "true_false"
)

type Option struct {
    ID        id.ID
    Label     string
    IsCorrect bool
}

type Question struct {
    ID                 id.ID
    Prompt             string
    Type               QuestionType
    Explanation        string
    Points             int
    ReferenceLectureID id.ID // zero when absent
    Options            []Option
}

type Quiz struct {
    ID        id.ID
    CourseID  id.ID
    LectureID id.ID
    Position  int
    Questions []Question
    CreatedAt time.Time
    UpdatedAt time.Time
}

type Answer struct {
    QuestionID id.ID
    OptionIDs  []id.ID
}

type QuizAttempt struct {
    ID             id.ID
    QuizID         id.ID
    UserID         id.ID
    Answers        []Answer
    IdempotencyKey string // empty when the request had no key
    SubmittedAt    time.Time
}
```

`NewQuestion(id, prompt, type, explanation, points, referenceLectureID, options)` trims prompt,
explanation, and labels, and returns `ErrInvalidQuestion` unless:

- the prompt is non-empty and at most 5,000 characters; the explanation is at most 5,000;
- the type is one of the three types;
- points are at least 1;
- there are 2 to 10 options, each with a non-empty label of at most 1,000 characters;
- at least one option is correct; `single_choice` and `true_false` have exactly one correct
  option; `true_false` has exactly two options.

`NewQuiz(id, courseID, lectureID, position, questions, createdAt, updatedAt)` returns
`ErrInvalidQuiz` unless the position is non-negative, there are 1 to 100 questions, and question
and option IDs are unique across the quiz.

`Quiz.CheckAnswers(answers)` returns `ErrInvalidAnswer` for an unknown question, an option not in
its question, a question answered twice, or an option repeated within an answer. Unanswered
questions are allowed.

## Application (`internal/assessment/app`)

Ports:

```go
// QuizRepository reads and writes quizzes in the current transaction.
type QuizRepository interface {
    // Find returns ErrNotFound when the quiz does not exist.
    Find(ctx context.Context, quizID id.ID) (domain.Quiz, error)
    // ListByLecture orders by position, then ID.
    ListByLecture(ctx context.Context, courseID, lectureID id.ID) ([]domain.Quiz, error)
    Insert(ctx context.Context, q domain.Quiz) error
    // Replace overwrites position, questions and updated time; ErrNotFound when absent, so a
    // concurrent delete is never undone.
    Replace(ctx context.Context, q domain.Quiz) error
    // Delete removes the quiz and its attempts; a no-op when absent.
    Delete(ctx context.Context, quizID id.ID) error
    // LecturesOf returns the lecture of each given quiz that belongs to courseID.
    LecturesOf(ctx context.Context, courseID id.ID, quizIDs []id.ID) (map[id.ID]id.ID, error)
    // RecordAttempt inserts the attempt and returns its ID, or returns the ID of the existing
    // attempt with the same quiz, user, and non-empty idempotency key.
    RecordAttempt(ctx context.Context, a domain.QuizAttempt) (id.ID, error)
}

type TxRunner interface {
    RunInTx(ctx context.Context, fn func(QuizRepository) error) error
}

// CourseAccess is backed by courseauthoring.
type CourseAccess interface {
    // CanManageLecture returns nil when p manages the course, the course is not archived, and
    // the lecture is in the course; otherwise ErrNotFound, ErrForbidden, or
    // ErrCourseNotEditable.
    CanManageLecture(ctx context.Context, p auth.Principal, courseID, lectureID id.ID) error
    // CanReadLecture applies the lecture content read rule: ErrNotFound or
    // ErrEnrollmentRequired.
    CanReadLecture(ctx context.Context, p auth.Principal, courseID, lectureID id.ID) error
}

// EnrollmentQuery is backed by enrollment.
type EnrollmentQuery interface {
    IsActivelyEnrolled(ctx context.Context, courseID, userID id.ID) (bool, error)
}
```

`QuizService` depends on `TxRunner`, `CourseAccess`, `EnrollmentQuery`, an ID generator, and
`clock.Clock`. Cross-context calls run before the assessment transaction opens, because each takes
its own pool connection (see 32ede6d).

- `List(ctx, p, courseID, lectureID)`: `CanReadLecture`, then `ListByLecture`.
- `Create(ctx, p, courseID, lectureID, in)`: `CanManageLecture`; questions and options get new IDs
  (any supplied IDs are rejected as `ErrInvalidInput`); points default to 1 when omitted; saves.
- `Update(ctx, p, courseID, lectureID, quizID, in)`: `CanManageLecture`; in the transaction, finds
  the quiz and returns `ErrNotFound` unless its course and lecture match the path. Supplied
  question and option IDs must already belong to that quiz (`ErrInvalidInput` otherwise);
  omitted IDs are generated. `CreatedAt` is kept; `UpdatedAt` is now.
- `Delete(ctx, p, quizID)`: finds the quiz, `CanManageLecture` on its course and lecture, deletes.
  The find and the delete use separate transactions; a concurrent delete makes the second a
  no-op.
- `RecordAttempt(ctx, p, quizID, userID, answers, key)`: `ErrForbidden` when `userID` is not
  `p.UserID`; `ErrInvalidInput` when the key is longer than 128 characters; finds the quiz;
  `CanReadLecture`; `IsActivelyEnrolled` or `ErrEnrollmentRequired`; `CheckAnswers`; records.
`QuizQuery` (`NewQuizQuery(tx)`) has one method, `Lectures(ctx, courseID, quizIDs)`, backed by
`LecturesOf`. It applies no authorization and is not exposed over HTTP. It is separate from
`QuizService` because courseauthoring needs it at construction time, while `QuizService` needs
courseauthoring's services; the split breaks that cycle the way media's `AssetQuery` does.

App errors: `ErrNotFound`, `ErrForbidden`, `ErrInvalidInput`, `ErrCourseNotEditable`,
`ErrEnrollmentRequired`. Domain validation errors wrap `ErrInvalidInput`.

## Courseauthoring changes

- `ContentService.CheckLectureRead(ctx, p, courseID, lectureID)` applies `Get`'s rules and returns
  only the error. `CheckAssetRead` keeps its behavior.
- `CourseService.CheckLectureManage(ctx, p, courseID, lectureID)` applies `CheckManage` and returns
  `ErrNotFound` when the lecture is not in the course.
- A `QuizCatalog` port (`Lectures(ctx, courseID, quizIDs) (map[id.ID]id.ID, error)`) and
  `checkQuizRefs`, mirroring `AssetCatalog` and `checkAssetRefs`. Both `ContentService` and
  `CourseService` take it. On replace, patch, and `AddLecture`, every upserted quiz block's
  `quiz_id` must map to the lecture being saved (a new lecture has no quizzes, so `AddLecture`
  rejects every quiz block), otherwise
  `ErrInvalidQuizReference` (`400 invalid_quiz_reference`). The check runs before the transaction
  opens and the error is returned only after the caller is authorized, as for assets.
- `blocks.go` stops describing quiz references as unchecked.

## Persistence (`internal/assessment/adapters/postgres`)

Migration `migrations/00007_assessment.sql`:

```sql
-- +goose Up
CREATE SCHEMA assessment;

CREATE TABLE assessment.quizzes (
  id          bigint PRIMARY KEY,
  course_id   bigint NOT NULL,
  lecture_id  bigint NOT NULL,
  position    integer NOT NULL CHECK (position >= 0),
  questions   jsonb NOT NULL,
  created_at  timestamptz NOT NULL,
  updated_at  timestamptz NOT NULL
);

CREATE INDEX quizzes_lecture_idx ON assessment.quizzes (course_id, lecture_id, position, id);

CREATE TABLE assessment.quiz_attempts (
  id              bigint PRIMARY KEY,
  quiz_id         bigint NOT NULL REFERENCES assessment.quizzes (id) ON DELETE CASCADE,
  user_id         bigint NOT NULL,
  answers         jsonb NOT NULL,
  idempotency_key text,
  submitted_at    timestamptz NOT NULL
);

CREATE INDEX quiz_attempts_quiz_idx ON assessment.quiz_attempts (quiz_id);

CREATE UNIQUE INDEX quiz_attempts_idempotency_idx
  ON assessment.quiz_attempts (quiz_id, user_id, idempotency_key)
  WHERE idempotency_key IS NOT NULL;

-- +goose Down
DROP SCHEMA assessment CASCADE;
```

- A quiz is always read and written whole and nothing queries single questions, so questions live
  in `jsonb`. The adapter owns the JSON shape; IDs are stored as strings.
- `RecordAttempt` runs `INSERT ... ON CONFLICT DO NOTHING RETURNING id`; when no row returns, it
  selects the existing attempt's ID by quiz, user, and key.
- Queries are generated by sqlc from `queries.sql`, as in the other contexts.

## HTTP (`internal/assessment/adapters/httpapi`)

All routes require authentication. Wire shapes match `assessment.ts`.

| Route | Success |
|---|---|
| `GET /v1/courses/{courseID}/lectures/{lectureID}/quizzes` | `200` quiz array |
| `POST /v1/courses/{courseID}/lectures/{lectureID}/quizzes` | `201` quiz |
| `PUT /v1/courses/{courseID}/lectures/{lectureID}/quizzes/{quizID}` | `200` quiz |
| `DELETE /v1/quizzes/{quizID}` | `204` |
| `POST /v1/quizzes/{quizID}/attempts` | `201 {id, recorded: true}` |

Quiz wire:

```json
{
  "id": "…",
  "lecture_id": "…",
  "position": 0,
  "questions": [
    {
      "id": "…",
      "prompt": "…",
      "type": "single_choice",
      "options": [{"id": "…", "label": "…"}],
      "correct_option_ids": ["…"],
      "explanation": "…"
    }
  ]
}
```

Question input: `{id?, prompt, type, explanation, points?, reference_lecture_id?, options:[{id?,
label, is_correct}]}`. An empty string `id` or `reference_lecture_id` means absent, because the
client sends `reference_lecture_id: ""` for questions without one. Request bodies are capped at
1 MiB.

| Error | Status | Code |
|---|---|---|
| `ErrInvalidInput`, malformed body or ID | 400 | `invalid_input` |
| `ErrForbidden` | 403 | `forbidden` |
| `ErrNotFound` | 404 | `not_found` |
| `ErrCourseNotEditable` | 409 | `course_not_editable` |
| `ErrEnrollmentRequired` | 409 | `enrollment_required` |

## Wiring (`cmd/api`)

`cmd/api/assessment.go` builds the context:

- `assessmentCourseAccess` over `CourseService.CheckLectureManage` and
  `ContentService.CheckLectureRead`, translating courseauthoring errors to assessment errors as
  `toMediaError` does for media.
- An enrollment adapter over `AccessQuery.IsActivelyEnrolled`.
- `*assessmentapp.QuizQuery` passed directly as courseauthoring's `QuizCatalog`; the method sets
  match, so no adapter is needed.

`.golangci.yml` gains `assessment-domain` and `assessment-app` depguard rules, adds the context to
`platform-independent-of-contexts`, and denies it from every other context. `api/openapi.yaml`
gains the five routes.

## Testing

- Domain: question rule table, quiz size and ID uniqueness, `CheckAnswers`.
- App with fakes: each access branch, path mismatch on update, foreign IDs on update, user
  mismatch, missing enrollment, key length, replay returning the same ID.
- HTTP: wire shapes and status mapping.
- Postgres integration: save and replace, list order, `LecturesOf`, delete cascading attempts,
  idempotent record including two concurrent inserts with the same key.
- Courseauthoring: replace and patch reject unknown and foreign `quiz_id` and accept the lecture's
  own quiz; a non-manager gets the authorization error, not the reference error.
- `cmd/api` end-to-end: an instructor creates a quiz and saves a block referencing it; an enrolled
  student lists the quiz and records an attempt twice with one key and gets one ID; an unenrolled
  user gets `409 enrollment_required`; recording for another user gets `403`.

## Rollout and rollback

The migration only adds a schema. Rolling back drops it with its quizzes and attempts. Existing
quiz blocks keep their unchecked IDs; only blocks upserted after this change are validated.

## Required gates

`make check`, `make test-integration` (actually run), `docker compose config`,
`make docker-build`, `git diff --check`.
