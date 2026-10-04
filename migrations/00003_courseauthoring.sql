-- +goose Up
CREATE SCHEMA courseauthoring;

CREATE TABLE courseauthoring.courses (
  id                 bigint PRIMARY KEY,
  owner_id           bigint NOT NULL,
  title              text NOT NULL,
  description        text NOT NULL DEFAULT '',
  level              text NOT NULL DEFAULT '' CHECK (level IN ('', 'beginner', 'intermediate', 'advanced')),
  thumbnail_url      text NOT NULL DEFAULT '' CHECK (thumbnail_url = '' OR thumbnail_url ~ '^https?://'),
  status             text NOT NULL CHECK (status IN ('draft', 'published', 'archived')),
  price_amount_minor bigint NOT NULL DEFAULT 0 CHECK (price_amount_minor >= 0),
  price_currency     text NOT NULL DEFAULT '',
  version            bigint NOT NULL,
  created_at         timestamptz NOT NULL,
  updated_at         timestamptz NOT NULL,
  CONSTRAINT courses_price_consistent CHECK (
    (price_amount_minor = 0 AND price_currency = '') OR
    (price_amount_minor > 0 AND price_currency = 'NPR'))
);
CREATE INDEX courses_owner_idx ON courseauthoring.courses (owner_id);
CREATE INDEX courses_published_idx ON courseauthoring.courses (id) WHERE status = 'published';

CREATE TABLE courseauthoring.sections (
  id         bigint PRIMARY KEY,
  course_id  bigint NOT NULL REFERENCES courseauthoring.courses (id) ON DELETE CASCADE,
  title      text NOT NULL,
  sort_order integer NOT NULL,
  UNIQUE (course_id, title)
);

CREATE TABLE courseauthoring.lectures (
  id               bigint PRIMARY KEY,
  course_id        bigint NOT NULL REFERENCES courseauthoring.courses (id) ON DELETE CASCADE,
  section_id       bigint REFERENCES courseauthoring.sections (id) ON DELETE SET NULL,
  title            text NOT NULL,
  free_preview     boolean NOT NULL DEFAULT false,
  sort_order       integer NOT NULL,
  content_revision bigint NOT NULL DEFAULT 0,
  created_at       timestamptz NOT NULL,
  updated_at       timestamptz NOT NULL
);
CREATE INDEX lectures_course_idx ON courseauthoring.lectures (course_id, sort_order);

CREATE TABLE courseauthoring.lecture_blocks (
  id              bigint PRIMARY KEY,
  lecture_id      bigint NOT NULL REFERENCES courseauthoring.lectures (id) ON DELETE CASCADE,
  course_id       bigint NOT NULL,
  kind            text NOT NULL CHECK (kind IN ('text', 'video', 'quiz', 'image', 'flashcard')),
  position        integer NOT NULL,
  client_block_id text NOT NULL CHECK (length(client_block_id) BETWEEN 1 AND 64),
  payload         jsonb NOT NULL,
  created_at      timestamptz NOT NULL,
  updated_at      timestamptz NOT NULL,
  UNIQUE (lecture_id, client_block_id),
  CONSTRAINT lecture_blocks_position_unique UNIQUE (lecture_id, position) DEFERRABLE INITIALLY DEFERRED
);
CREATE INDEX lecture_blocks_lecture_kind_idx ON courseauthoring.lecture_blocks (lecture_id, kind);

-- +goose Down
DROP SCHEMA courseauthoring CASCADE;
