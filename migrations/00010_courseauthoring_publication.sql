-- +goose Up
ALTER TABLE courseauthoring.courses DROP CONSTRAINT courses_status_check;
ALTER TABLE courseauthoring.courses ADD CONSTRAINT courses_status_check
  CHECK (status IN ('draft', 'in_review', 'approved', 'changes_requested', 'published', 'archived'));
ALTER TABLE courseauthoring.courses
  ADD COLUMN submitted_at timestamptz,
  ADD COLUMN reviewed_at  timestamptz,
  ADD COLUMN review_note  text NOT NULL DEFAULT '';
CREATE INDEX courses_in_review_idx ON courseauthoring.courses (submitted_at, id) WHERE status = 'in_review';

CREATE TABLE courseauthoring.course_reviews (
  id          bigint PRIMARY KEY,
  course_id   bigint NOT NULL REFERENCES courseauthoring.courses (id) ON DELETE CASCADE,
  actor_id    bigint NOT NULL,
  decision    text NOT NULL CHECK (decision IN ('submitted', 'approved', 'changes_requested', 'unpublished')),
  note        text NOT NULL DEFAULT '',
  created_at  timestamptz NOT NULL
);
CREATE INDEX course_reviews_course_idx ON courseauthoring.course_reviews (course_id, created_at, id);

-- Published snapshots. A version copies the course's details, sections, lectures and
-- blocks at publish time; readers are served courses.live_version.
CREATE TABLE courseauthoring.course_versions (
  course_id          bigint NOT NULL REFERENCES courseauthoring.courses (id) ON DELETE CASCADE,
  number             integer NOT NULL CHECK (number > 0),
  title              text NOT NULL,
  description        text NOT NULL,
  level              text NOT NULL,
  thumbnail_url      text NOT NULL,
  price_amount_minor bigint NOT NULL,
  price_currency     text NOT NULL,
  published_by       bigint NOT NULL,
  published_at       timestamptz NOT NULL,
  PRIMARY KEY (course_id, number)
);

CREATE TABLE courseauthoring.course_version_sections (
  course_id  bigint NOT NULL,
  number     integer NOT NULL,
  id         bigint NOT NULL,
  title      text NOT NULL,
  sort_order integer NOT NULL,
  PRIMARY KEY (course_id, number, id),
  FOREIGN KEY (course_id, number) REFERENCES courseauthoring.course_versions (course_id, number) ON DELETE CASCADE
);

CREATE TABLE courseauthoring.course_version_lectures (
  course_id    bigint NOT NULL,
  number       integer NOT NULL,
  id           bigint NOT NULL,
  section_id   bigint,
  title        text NOT NULL,
  free_preview boolean NOT NULL,
  sort_order   integer NOT NULL,
  PRIMARY KEY (course_id, number, id),
  FOREIGN KEY (course_id, number) REFERENCES courseauthoring.course_versions (course_id, number) ON DELETE CASCADE
);

CREATE TABLE courseauthoring.course_version_blocks (
  course_id       bigint NOT NULL,
  number          integer NOT NULL,
  lecture_id      bigint NOT NULL,
  id              bigint NOT NULL,
  kind            text NOT NULL,
  position        integer NOT NULL,
  client_block_id text NOT NULL,
  payload         jsonb NOT NULL,
  PRIMARY KEY (course_id, number, lecture_id, position),
  FOREIGN KEY (course_id, number, lecture_id)
    REFERENCES courseauthoring.course_version_lectures (course_id, number, id) ON DELETE CASCADE
);

ALTER TABLE courseauthoring.courses
  ADD COLUMN last_version integer NOT NULL DEFAULT 0 CHECK (last_version >= 0),
  ADD COLUMN live_version integer,
  ADD CONSTRAINT courses_live_version_fk FOREIGN KEY (id, live_version)
    REFERENCES courseauthoring.course_versions (course_id, number),
  ADD CONSTRAINT courses_live_version_published CHECK (live_version IS NULL OR status <> 'archived');
DROP INDEX courseauthoring.courses_published_idx;
CREATE INDEX courses_live_idx ON courseauthoring.courses (id) WHERE live_version IS NOT NULL;

-- Courses published before versioning become live at version 1.
INSERT INTO courseauthoring.course_versions
  (course_id, number, title, description, level, thumbnail_url, price_amount_minor, price_currency, published_by, published_at)
SELECT id, 1, title, description, level, thumbnail_url, price_amount_minor, price_currency, owner_id, updated_at
FROM courseauthoring.courses WHERE status = 'published';
INSERT INTO courseauthoring.course_version_sections (course_id, number, id, title, sort_order)
SELECT s.course_id, 1, s.id, s.title, s.sort_order
FROM courseauthoring.sections s JOIN courseauthoring.courses c ON c.id = s.course_id
WHERE c.status = 'published';
INSERT INTO courseauthoring.course_version_lectures (course_id, number, id, section_id, title, free_preview, sort_order)
SELECT l.course_id, 1, l.id, l.section_id, l.title, l.free_preview, l.sort_order
FROM courseauthoring.lectures l JOIN courseauthoring.courses c ON c.id = l.course_id
WHERE c.status = 'published';
INSERT INTO courseauthoring.course_version_blocks (course_id, number, lecture_id, id, kind, position, client_block_id, payload)
SELECT b.course_id, 1, b.lecture_id, b.id, b.kind, b.position, b.client_block_id, b.payload
FROM courseauthoring.lecture_blocks b JOIN courseauthoring.courses c ON c.id = b.course_id
WHERE c.status = 'published';
UPDATE courseauthoring.courses SET last_version = 1, live_version = 1 WHERE status = 'published';

-- +goose Down
-- A course whose working copy is a draft of a live course was published before.
UPDATE courseauthoring.courses SET status = 'published'
WHERE live_version IS NOT NULL AND status <> 'published';
DROP INDEX courseauthoring.courses_live_idx;
ALTER TABLE courseauthoring.courses
  DROP CONSTRAINT courses_live_version_published,
  DROP CONSTRAINT courses_live_version_fk,
  DROP COLUMN last_version,
  DROP COLUMN live_version;
CREATE INDEX courses_published_idx ON courseauthoring.courses (id) WHERE status = 'published';
DROP TABLE courseauthoring.course_version_blocks;
DROP TABLE courseauthoring.course_version_lectures;
DROP TABLE courseauthoring.course_version_sections;
DROP TABLE courseauthoring.course_versions;
DROP TABLE courseauthoring.course_reviews;
DROP INDEX courseauthoring.courses_in_review_idx;
UPDATE courseauthoring.courses SET status = 'draft'
WHERE status IN ('in_review', 'approved', 'changes_requested');
ALTER TABLE courseauthoring.courses
  DROP COLUMN submitted_at,
  DROP COLUMN reviewed_at,
  DROP COLUMN review_note;
ALTER TABLE courseauthoring.courses DROP CONSTRAINT courses_status_check;
ALTER TABLE courseauthoring.courses ADD CONSTRAINT courses_status_check
  CHECK (status IN ('draft', 'published', 'archived'));
