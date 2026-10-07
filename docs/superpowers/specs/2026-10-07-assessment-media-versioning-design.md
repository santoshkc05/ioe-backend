# Assessment and Media Versioning Design

Date: 2026-10-07

## Status

Approved in conversation on 2026-10-07. Pending written-spec review.

## Context

Course publication (`migrations/00010_courseauthoring_publication.sql`) snapshots a course's
details, sections, lectures and blocks into `course_versions*`. Students read
`courses.live_version`; managers edit the working copy; `DiscardDraft` rebuilds the working copy
from the live version.

Three kinds of content live outside that snapshot:

- **Quizzes** (`assessment.quizzes`) belong to a lecture; a `quiz` block references one by
  `quiz_id`. `QuizService.Update` replaces the questions in place. `QuizService.Delete` removes the
  row and cascades its attempts, leaving referencing blocks dangling.
- **Exams** (`assessment.exams`) belong to a course, have their own `draft`/`published` status,
  and use `CheckExamEdit` locks to keep edits from breaking existing attempts. Every saved edit is
  immediately visible to students.
- **Media assets** (`media.assets`) are immutable once uploaded. `AssetService.Delete` removes the
  remote asset and the row with no reference check, so a live video or image can break.

Problems this spec solves:

1. Editing a quiz or exam on a live course changes what students see immediately; deleting a media
   asset breaks the live course.
2. `DiscardDraft` restores blocks that may reference quizzes or assets deleted in the meantime.

## Goals

- Students see quizzes and exams exactly as the live course version published them.
- Quiz and exam changes go through the same edit freeze, review and publish as course content.
- Attempts stay consistent with the quiz or exam revision they started on, across republishes.
- `DiscardDraft` brings back the live version's quizzes and exams, and the assets it references
  still exist.
- Context boundaries in `AGENTS.md` hold: each context writes only its own schema.

## Non-goals

- Restoring the working copy from a version other than the live one.
- Keeping older versions' media intact. Versions older than the live one may reference deleted
  assets; only managers can read them.
- Pruning unused revisions.
- Versioning media content. Assets are immutable; only deletion is guarded.

## Decisions

| Topic | Decision |
|---|---|
| Scope | Full versioning for quizzes and exams. |
| Exam status | `draft`/`published` is part of the exam revision; it reaches students on course publish. |
| Attempts | Each attempt records the revision it started on and is graded, checked and revealed against it. |
| Media deletion | Refused while the working copy or the live version references the asset. |
| Mechanism | Append-only revisions in assessment; courseauthoring pins revision numbers per version. |
| Review | Quiz and exam edits follow the course edit freeze: rejected while `in_review` or `approved`; editing a `published` course reopens it to `draft`. |

## Data model

### assessment

`assessment.quizzes` and `assessment.exams` become head rows. Content moves into revisions.

```sql
ALTER TABLE assessment.quizzes
  ADD COLUMN head_revision integer NOT NULL DEFAULT 1 CHECK (head_revision > 0),
  ADD COLUMN deleted_at    timestamptz;

CREATE TABLE assessment.quiz_revisions (
  quiz_id    bigint  NOT NULL REFERENCES assessment.quizzes (id),
  revision   integer NOT NULL CHECK (revision > 0),
  position   integer NOT NULL CHECK (position >= 0),
  questions  jsonb   NOT NULL,
  created_by bigint  NOT NULL,
  created_at timestamptz NOT NULL,
  PRIMARY KEY (quiz_id, revision)
);

ALTER TABLE assessment.exams
  ADD COLUMN head_revision integer NOT NULL DEFAULT 1 CHECK (head_revision > 0),
  ADD COLUMN deleted_at    timestamptz;

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

ALTER TABLE assessment.quiz_attempts ADD COLUMN revision integer;  -- NOT NULL after backfill
ALTER TABLE assessment.exam_attempts ADD COLUMN revision integer;  -- NOT NULL after backfill
```

- Revisions are never updated or deleted. Every create or edit appends `head_revision + 1` and
  moves the head in the same transaction.
- The content columns (`position`, `questions`, and the exam settings and `status`) are dropped from
  the head tables once the backfill copies them into revision 1. The head keeps identity, course,
  lecture (quizzes), `created_at`, `updated_at`, `head_revision` and `deleted_at`.
- Delete becomes a soft delete (`deleted_at`). The `quiz_attempts` cascade is replaced by a plain
  foreign key; `exam_attempts` keeps `ON DELETE RESTRICT`. No head row is hard-deleted.
- An attempt's `(quiz_id, revision)` / `(exam_id, revision)` references its revision row.
- `CheckExamEdit` and its locks are removed. Attempts read their own revision, so editing the
  working copy cannot break them.

### courseauthoring

```sql
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
```

- `course_submitted_assessments` holds the head revisions captured at `Submit`; `Submit` replaces
  the course's rows. `Publish` copies them into `course_version_assessments`. What is published is
  therefore exactly what was reviewed, even if an assessment edit races with submission.
- Lecture blocks keep `quiz_id`; a version's pins say which revision of that quiz it shows.
- Every non-deleted exam is pinned. Student visibility depends on the pinned revision's `status`.

### media

No schema change.

### Migration and backfill

One goose migration covering both schemas, in order:

1. Create the revision tables and add the new columns.
2. Copy each quiz and exam into revision 1 (`created_by` = course owner for quizzes; exams likewise,
   via a subquery against `courseauthoring.courses`, which the migration may read).
3. Set existing attempts' `revision = 1`; make the column `NOT NULL`; add the revision foreign keys.
4. Drop the head tables' content columns and replace the `quiz_attempts` cascade.
5. For every course with a `live_version`, pin every quiz in that version's lectures and every exam
   of the course at revision 1. Do the same in `course_submitted_assessments` for courses currently
   `in_review` or `approved`.

The migration's Down step restores content columns from the head revision and drops the new tables.

## Ports

All ports are defined in the consuming context's `app` package and wired in `cmd/api` with adapters
backed by the providing context's application service.

| Consumer | Port method | Provider behaviour |
|---|---|---|
| assessment | `BeginAssessmentEdit(ctx, p, courseID, lectureID)` (`lectureID` zero for exams) | Authorizes `p` as a course manager (and the lecture exists, when given); applies `Course.BeginEdit`; saves in courseauthoring's transaction. Replaces `CanManageLecture` for writes. |
| assessment | `LivePins(ctx, courseID) (map[Ref]int, bool, error)` | The live version's pins, `false` when the course is not live. |
| assessment | `VersionPins(ctx, p, courseID, number) (map[Ref]int, error)` | A version's pins, managers only. |
| courseauthoring | `AssessmentHeads(ctx, courseID) ([]AssessmentHead, error)` | `{kind, id, revision}` of every non-deleted quiz and exam of the course. |
| media | `AssetUsage(ctx, courseID, assetID) ([]id.ID, error)` | IDs of lectures whose working-copy or live-version blocks reference the asset, via `blocksReference`. |

`Ref` is `{Kind, ID}`. The existing `QuizCatalog.Lectures` excludes deleted quizzes.

## Flows

### Authoring a quiz or exam

Create, update, delete, and exam publish/unpublish:

1. Assessment calls `BeginAssessmentEdit`. A frozen course yields `ErrCourseNotEditable`
   (409 `course_not_editable`); a `published` course moves to `draft`.
2. In its own transaction, assessment appends a revision and moves the head, or sets `deleted_at`.

The two steps are not atomic. If step 2 fails, the course is left in `draft` with no change. This is
harmless: `DiscardDraft` or a later submit clears it. Exam publish/unpublish no longer changes
student visibility directly; it appends a revision with the new `status`.

### Submit

Courseauthoring calls `AssessmentHeads`, then, in the submit transaction:

- re-checks every block's media and quiz references with `checkAssetRefs` and `checkQuizRefs`,
  failing with 400 `invalid_media_reference` / `invalid_quiz_reference`;
- replaces `course_submitted_assessments` with the heads.

### Publish

Inside the existing publish transaction, after `InsertVersion`, courseauthoring writes the new
version's pins to `course_version_assessments`:

- A course that was `in_review` or `approved` publishes `course_submitted_assessments`, so what
  goes live is what was reviewed.
- A reviewer publishing a `draft` or `changes_requested` course directly (the existing reviewer
  bypass) has no submission; courseauthoring calls `AssessmentHeads` before the transaction and
  pins those.

### Reading

- Students: assessment calls `LivePins`. Quizzes and exams without a live pin return 404 or are
  omitted from lists. An exam is listed and startable only when its pinned revision's `status` is
  `published`. Content comes from the pinned revision. The existing `CanReadLecture` check stays.
- Managers: read the head revision by default. `?version=n` reads that version's pin via
  `VersionPins`, mirroring course version reads.

### Attempts

- Recording a quiz attempt or starting an exam attempt stores the live pin's revision.
- Answer validation, grading, auto-submission, and result reveal read the attempt's revision only.
  A republish never affects an existing attempt.

### DiscardDraft

Inside the existing discard transaction, courseauthoring writes an outbox message
`courseauthoring.DraftDiscarded{course_id, pins: [{kind, id, revision}]}` with the live version's
pins.

Assessment's handler, in one transaction:

- For each pin whose quiz or exam has a head different from the pinned revision, or is deleted:
  append a revision copying the pinned one and clear `deleted_at`.
- Soft-delete the course's non-deleted quizzes and exams that have no pin.

The handler is idempotent: a head already equal to its pin and not deleted is skipped, and a
redelivery appends nothing. The working copy's assessments are eventually consistent with the
discard.

### Deleting a media asset

`AssetService.Delete` authorizes as today, then calls `AssetUsage`. A non-empty result fails with
409 `asset_in_use`; the problem response lists the lecture IDs. Otherwise it keeps the current
order: remote asset, then row.

A block written to the working copy concurrently with the delete can pass both checks. `Submit`'s
reference re-check catches it, and the live version only changes through publish, so the live
version never references a missing asset.

## Errors

| Status | Code | When |
|---|---|---|
| 409 | `course_not_editable` | Quiz or exam edit while the course is `in_review`, `approved` or `archived`. |
| 409 | `asset_in_use` | Deleting an asset referenced by the working copy or the live version. |
| 400 | `invalid_quiz_reference` | Submit with a block referencing a deleted, missing or foreign quiz. |
| 400 | `invalid_media_reference` | Submit with a block referencing a missing or wrongly-kinded asset. |
| 404 | `not_found` | Student reads a quiz or exam not pinned by the live version. |

Outbox handler failures retry; the handler is idempotent.

## API changes

`api/openapi.yaml`:

- New problem codes above on the affected operations.
- `?version=` query parameter on manager quiz and exam reads.
- `revision` on quiz and exam responses and on attempt responses.
- Exam publish/unpublish descriptions: takes effect on the next course publish.

## Testing

- Domain: appending revisions, soft delete and undelete, exam visibility by pinned status.
- App (fakes): edit freeze and reopen through `BeginAssessmentEdit`; Submit capturing pins and
  rejecting dangling references; Publish copying pins; `asset_in_use`; attempts pinned to their
  revision across a republish; discard handler idempotency, undelete and soft-delete of unpinned.
- Postgres integration: migration backfill (revision 1, attempt revisions, live and submitted pins);
  pins inserted on publish.
- E2E (`cmd/api/e2e_integration_test.go`):
  1. Publish a course, edit its quiz: students still see the old quiz.
  2. Republish: students see the new quiz; an exam attempt started before the republish is graded
     against its original revision.
  3. Delete the quiz, then discard the draft: the quiz is back.
  4. Deleting a video referenced by the live version returns 409.
