-- +goose Up
CREATE SCHEMA media;

CREATE TABLE media.assets (
  id         bigint PRIMARY KEY,
  course_id  bigint NOT NULL,
  kind       text NOT NULL CHECK (kind IN ('video', 'image')),
  created_by bigint NOT NULL,
  created_at timestamptz NOT NULL
);

CREATE INDEX assets_course_id_idx ON media.assets (course_id);

-- +goose Down
DROP SCHEMA media CASCADE;
