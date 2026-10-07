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
