-- +goose Up
CREATE SCHEMA enrollment;

CREATE TABLE enrollment.enrollments (
  id            bigint PRIMARY KEY,
  course_id     bigint NOT NULL,
  user_id       bigint NOT NULL,
  status        text NOT NULL CHECK (status IN ('active', 'canceled')),
  cancel_reason text NOT NULL DEFAULT '',
  enrolled_at   timestamptz NOT NULL,
  canceled_at   timestamptz,
  version       bigint NOT NULL,
  CONSTRAINT enrollments_course_user_unique UNIQUE (course_id, user_id),
  CONSTRAINT enrollments_canceled_consistent CHECK ((status = 'canceled') = (canceled_at IS NOT NULL))
);

CREATE INDEX enrollments_user_active ON enrollment.enrollments (user_id) WHERE status = 'active';

-- +goose Down
DROP SCHEMA enrollment CASCADE;
