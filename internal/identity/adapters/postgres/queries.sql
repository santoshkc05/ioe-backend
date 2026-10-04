-- name: GetUserByGoogleSub :one
SELECT * FROM identity.users WHERE google_sub = $1;

-- name: GetUserByID :one
SELECT * FROM identity.users WHERE id = $1;

-- name: InsertUser :exec
INSERT INTO identity.users (id, google_sub, email, name, avatar_url, role, created_at, updated_at, last_login_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9);

-- name: UpdateUser :exec
UPDATE identity.users
SET email = $2, name = $3, avatar_url = $4, role = $5, updated_at = $6, last_login_at = $7
WHERE id = $1;

-- name: InsertRefreshToken :exec
INSERT INTO identity.refresh_tokens (id, user_id, family_id, token_hash, family_expires_at, expires_at, created_at, user_agent, ip)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9);

-- name: GetRefreshTokenByHashForUpdate :one
SELECT * FROM identity.refresh_tokens WHERE token_hash = $1 FOR UPDATE;

-- name: MarkRefreshTokenUsed :exec
UPDATE identity.refresh_tokens SET used_at = $2 WHERE id = $1;

-- name: RevokeRefreshFamily :exec
UPDATE identity.refresh_tokens SET revoked_at = $2 WHERE family_id = $1 AND revoked_at IS NULL;
