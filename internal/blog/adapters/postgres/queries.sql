-- name: InsertPost :exec
INSERT INTO blog.posts (id, author_id, title, summary, cover_url, tags, slug, status, version, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 1, $9, $10);

-- name: UpdatePost :execrows
UPDATE blog.posts
SET title = $3, summary = $4, cover_url = $5, tags = $6, slug = $7, status = $8,
    last_version = $9, live_version = $10, first_published_at = $11, updated_at = $12,
    version = version + 1
WHERE id = $1 AND version = $2;

-- name: ListPostsByIDs :many
SELECT p.id, p.author_id, p.title, p.summary, p.cover_url, p.tags, p.slug, p.status,
       p.last_version, p.live_version, p.first_published_at, p.version, p.created_at, p.updated_at,
       v.published_at AS live_published_at
FROM blog.posts p
LEFT JOIN blog.post_versions v ON v.post_id = p.id AND v.number = p.live_version
WHERE p.id = ANY(sqlc.arg(ids)::bigint[])
ORDER BY p.id DESC;

-- name: ListPostIDsByAuthor :many
SELECT id FROM blog.posts WHERE author_id = $1 ORDER BY id DESC;

-- name: InsertPostVersion :exec
INSERT INTO blog.post_versions
  (post_id, number, title, summary, cover_url, tags, reading_time_minutes, published_by, published_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9);

-- name: CopyPostVersionBlocks :exec
INSERT INTO blog.post_version_blocks (post_id, number, id, kind, position, client_block_id, payload)
SELECT post_id, sqlc.arg(number)::integer, id, kind, position, client_block_id, payload
FROM blog.post_blocks WHERE post_id = sqlc.arg(post_id)::bigint;

-- name: ListPostVersions :many
SELECT number, published_by, published_at FROM blog.post_versions
WHERE post_id = $1 ORDER BY number DESC;

-- name: GetPostVersion :one
SELECT number, title, summary, cover_url, tags, reading_time_minutes, published_by, published_at
FROM blog.post_versions WHERE post_id = $1 AND number = $2;

-- name: ListPostVersionBlocks :many
SELECT id, client_block_id, kind, position, payload FROM blog.post_version_blocks
WHERE post_id = $1 AND number = $2 ORDER BY position;

-- name: GetPostContentHeader :one
SELECT id, content_revision FROM blog.posts WHERE id = $1;

-- name: GetPostContentHeaderForUpdate :one
SELECT id, content_revision FROM blog.posts WHERE id = $1 FOR UPDATE;

-- name: ListPostBlocks :many
SELECT id, client_block_id, kind, position, payload
FROM blog.post_blocks WHERE post_id = $1 ORDER BY position;

-- name: BumpPostContentRevision :execrows
UPDATE blog.posts SET content_revision = content_revision + 1
WHERE id = $1 AND content_revision = $2;

-- name: ForceBumpPostContentRevision :one
UPDATE blog.posts SET content_revision = content_revision + 1
WHERE id = $1
RETURNING content_revision;

-- name: DeletePostBlocks :exec
DELETE FROM blog.post_blocks WHERE post_id = $1;

-- name: DeletePostBlocksByClientIDs :exec
DELETE FROM blog.post_blocks
WHERE post_id = $1 AND client_block_id = ANY(sqlc.arg(client_block_ids)::text[]);

-- name: UpsertPostBlocks :exec
-- One jsonb array of {id, client_block_id, kind, position, payload}, as for lecture blocks.
INSERT INTO blog.post_blocks (id, post_id, client_block_id, kind, position, payload, created_at, updated_at)
SELECT (elem->>'id')::bigint, sqlc.arg(post_id)::bigint,
       elem->>'client_block_id', elem->>'kind', (elem->>'position')::int, (elem->'payload')::jsonb,
       sqlc.arg(now)::timestamptz, sqlc.arg(now)::timestamptz
FROM jsonb_array_elements(sqlc.arg(blocks)::jsonb) AS elem
ON CONFLICT (post_id, client_block_id) DO UPDATE SET
  kind = EXCLUDED.kind, position = EXCLUDED.position, payload = EXCLUDED.payload, updated_at = EXCLUDED.updated_at;

-- name: ApplyPostBlockOrder :exec
-- The position unique constraint is deferred, so permuting positions in one statement is allowed.
UPDATE blog.post_blocks b
SET position = o.position - 1, updated_at = sqlc.arg(now)::timestamptz
FROM unnest(sqlc.arg(client_block_ids)::text[]) WITH ORDINALITY AS o(client_block_id, position)
WHERE b.post_id = sqlc.arg(post_id)::bigint
  AND b.client_block_id = o.client_block_id
  AND b.position IS DISTINCT FROM (o.position - 1);

-- name: ReservePostSlug :exec
INSERT INTO blog.post_slugs (slug, post_id, created_at) VALUES ($1, $2, $3)
ON CONFLICT (slug) DO NOTHING;

-- name: GetPostSlugOwner :one
SELECT post_id FROM blog.post_slugs WHERE slug = $1;

-- name: ListLivePosts :many
SELECT p.id, p.author_id, p.slug, p.first_published_at, v.number, v.title, v.summary, v.cover_url,
       v.tags, v.reading_time_minutes, v.published_at
FROM blog.posts p
JOIN blog.post_versions v ON v.post_id = p.id AND v.number = p.live_version
WHERE (sqlc.arg(tag)::text = '' OR v.tags @> ARRAY[sqlc.arg(tag)::text])
  AND (sqlc.narg(after_at)::timestamptz IS NULL
       OR p.first_published_at < sqlc.narg(after_at)::timestamptz
       OR (p.first_published_at = sqlc.narg(after_at)::timestamptz AND p.id < sqlc.arg(after_id)::bigint))
ORDER BY p.first_published_at DESC, p.id DESC
LIMIT sqlc.arg(row_limit)::integer;

-- name: GetLivePost :one
SELECT p.id, p.author_id, p.slug, p.first_published_at, v.number, v.title, v.summary, v.cover_url,
       v.tags, v.reading_time_minutes, v.published_at
FROM blog.posts p
JOIN blog.post_versions v ON v.post_id = p.id AND v.number = p.live_version
WHERE p.id = $1;

-- name: ListLiveTags :many
SELECT t.tag::text AS tag, count(*)::integer AS post_count
FROM blog.posts p
JOIN blog.post_versions v ON v.post_id = p.id AND v.number = p.live_version
CROSS JOIN LATERAL unnest(v.tags) AS t(tag)
GROUP BY t.tag
ORDER BY post_count DESC, t.tag;

-- name: ListLiveIndex :many
SELECT p.id, p.slug, p.first_published_at, v.published_at
FROM blog.posts p
JOIN blog.post_versions v ON v.post_id = p.id AND v.number = p.live_version
WHERE sqlc.narg(after_at)::timestamptz IS NULL
   OR p.first_published_at < sqlc.narg(after_at)::timestamptz
   OR (p.first_published_at = sqlc.narg(after_at)::timestamptz AND p.id < sqlc.arg(after_id)::bigint)
ORDER BY p.first_published_at DESC, p.id DESC
LIMIT sqlc.arg(row_limit)::integer;
