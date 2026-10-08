-- +goose Up
CREATE SCHEMA blog;

CREATE TABLE blog.posts (
  id                 bigint PRIMARY KEY,
  author_id          bigint NOT NULL,
  title              text NOT NULL,
  summary            text NOT NULL DEFAULT '',
  cover_url          text NOT NULL DEFAULT '' CHECK (cover_url = '' OR cover_url ~ '^https://'),
  tags               text[] NOT NULL DEFAULT '{}',
  slug               text NOT NULL UNIQUE,
  status             text NOT NULL CHECK (status IN ('draft', 'published', 'archived')),
  content_revision   bigint NOT NULL DEFAULT 0,
  last_version       integer NOT NULL DEFAULT 0 CHECK (last_version >= 0),
  live_version       integer,
  first_published_at timestamptz,
  version            bigint NOT NULL,
  created_at         timestamptz NOT NULL,
  updated_at         timestamptz NOT NULL,
  CONSTRAINT posts_live_iff_published CHECK ((status = 'published') = (live_version IS NOT NULL)),
  CONSTRAINT posts_live_has_first_publish CHECK (live_version IS NULL OR first_published_at IS NOT NULL)
);
CREATE INDEX posts_author_idx ON blog.posts (author_id, id DESC);
CREATE INDEX posts_live_idx ON blog.posts (first_published_at DESC, id DESC) WHERE live_version IS NOT NULL;

CREATE TABLE blog.post_blocks (
  id              bigint PRIMARY KEY,
  post_id         bigint NOT NULL REFERENCES blog.posts (id) ON DELETE CASCADE,
  kind            text NOT NULL CHECK (kind IN ('text', 'image')),
  position        integer NOT NULL,
  client_block_id text NOT NULL CHECK (length(client_block_id) BETWEEN 1 AND 64),
  payload         jsonb NOT NULL,
  created_at      timestamptz NOT NULL,
  updated_at      timestamptz NOT NULL,
  UNIQUE (post_id, client_block_id),
  CONSTRAINT post_blocks_position_unique UNIQUE (post_id, position) DEFERRABLE INITIALLY DEFERRED
);

-- Published snapshots. Readers are served posts.live_version.
CREATE TABLE blog.post_versions (
  post_id              bigint NOT NULL REFERENCES blog.posts (id) ON DELETE CASCADE,
  number               integer NOT NULL CHECK (number > 0),
  title                text NOT NULL,
  summary              text NOT NULL,
  cover_url            text NOT NULL,
  tags                 text[] NOT NULL,
  reading_time_minutes integer NOT NULL CHECK (reading_time_minutes > 0),
  published_by         bigint NOT NULL,
  published_at         timestamptz NOT NULL,
  PRIMARY KEY (post_id, number)
);
CREATE INDEX post_versions_tags_idx ON blog.post_versions USING gin (tags);

CREATE TABLE blog.post_version_blocks (
  post_id         bigint NOT NULL,
  number          integer NOT NULL,
  id              bigint NOT NULL,
  kind            text NOT NULL,
  position        integer NOT NULL,
  client_block_id text NOT NULL,
  payload         jsonb NOT NULL,
  PRIMARY KEY (post_id, number, position),
  FOREIGN KEY (post_id, number) REFERENCES blog.post_versions (post_id, number) ON DELETE CASCADE
);

ALTER TABLE blog.posts
  ADD CONSTRAINT posts_live_version_fk FOREIGN KEY (id, live_version)
    REFERENCES blog.post_versions (post_id, number);

-- Every slug a post ever had. Rows are never deleted, so an old link keeps resolving and
-- no other post can take it. The foreign key is deferred so a post's first slug can be
-- reserved before the post row is inserted in the same transaction.
CREATE TABLE blog.post_slugs (
  slug       text PRIMARY KEY,
  post_id    bigint NOT NULL REFERENCES blog.posts (id) ON DELETE CASCADE DEFERRABLE INITIALLY DEFERRED,
  created_at timestamptz NOT NULL
);
CREATE INDEX post_slugs_post_idx ON blog.post_slugs (post_id);

-- +goose Down
DROP SCHEMA blog CASCADE;
