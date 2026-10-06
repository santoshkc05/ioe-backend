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
