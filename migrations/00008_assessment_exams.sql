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
