-- +goose Up
CREATE SCHEMA progress;

CREATE TABLE progress.course_progress (
  course_id       bigint NOT NULL,
  user_id         bigint NOT NULL,
  last_lecture_id bigint NOT NULL,
  updated_at      timestamptz NOT NULL,
  PRIMARY KEY (course_id, user_id)
);

CREATE INDEX course_progress_user_idx ON progress.course_progress (user_id, updated_at DESC);

CREATE TABLE progress.lecture_progress (
  course_id   bigint NOT NULL,
  user_id     bigint NOT NULL,
  lecture_id  bigint NOT NULL,
  state       text NOT NULL CHECK (state IN ('in_progress', 'completed')),
  position_ms bigint NOT NULL CHECK (position_ms >= 0),
  updated_at  timestamptz NOT NULL,
  PRIMARY KEY (course_id, user_id, lecture_id)
);

CREATE INDEX lecture_progress_user_updated_idx ON progress.lecture_progress (user_id, updated_at);

-- +goose Down
DROP SCHEMA progress CASCADE;
