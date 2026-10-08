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
