-- +goose Up
CREATE EXTENSION IF NOT EXISTS citext;
CREATE SCHEMA identity;

CREATE TABLE identity.users (
  id            bigint PRIMARY KEY,
  google_sub    text NOT NULL UNIQUE,
  email         citext NOT NULL,
  name          text NOT NULL DEFAULT '',
  avatar_url    text NOT NULL DEFAULT '',
  role          text NOT NULL CHECK (role IN ('student', 'instructor', 'root_admin')),
  created_at    timestamptz NOT NULL,
  updated_at    timestamptz NOT NULL,
  last_login_at timestamptz NOT NULL
);
CREATE INDEX users_email_idx ON identity.users (email);

CREATE TABLE identity.refresh_tokens (
  id                bigint PRIMARY KEY,
  user_id           bigint NOT NULL REFERENCES identity.users (id) ON DELETE CASCADE,
  family_id         bigint NOT NULL,
  token_hash        bytea NOT NULL UNIQUE,
  family_expires_at timestamptz NOT NULL,
  expires_at        timestamptz NOT NULL,
  used_at           timestamptz,
  revoked_at        timestamptz,
  created_at        timestamptz NOT NULL,
  user_agent        text NOT NULL DEFAULT '',
  ip                text NOT NULL DEFAULT ''
);
CREATE INDEX refresh_tokens_family_idx ON identity.refresh_tokens (family_id);
CREATE INDEX refresh_tokens_user_idx ON identity.refresh_tokens (user_id);

-- +goose Down
DROP SCHEMA identity CASCADE;
