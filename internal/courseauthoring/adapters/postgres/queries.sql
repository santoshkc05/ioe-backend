-- name: InsertCourse :exec
INSERT INTO courseauthoring.courses
  (id, owner_id, title, description, level, thumbnail_url, status, price_amount_minor, price_currency, version, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 1, $10, $11);

-- name: UpdateCourse :execrows
UPDATE courseauthoring.courses
SET title = $3, description = $4, level = $5, thumbnail_url = $6, status = $7,
    price_amount_minor = $8, price_currency = $9, updated_at = $10, version = version + 1
WHERE id = $1 AND version = $2;

-- name: ListCoursesByIDs :many
SELECT * FROM courseauthoring.courses WHERE id = ANY(sqlc.arg(ids)::bigint[]);

-- name: ListCourseIDsByOwner :many
SELECT id FROM courseauthoring.courses WHERE owner_id = $1 ORDER BY created_at DESC, id DESC;

-- name: ListSectionsByCourseIDs :many
SELECT * FROM courseauthoring.sections
WHERE course_id = ANY(sqlc.arg(course_ids)::bigint[])
ORDER BY course_id, sort_order;

-- name: ListLecturesByCourseIDs :many
SELECT l.id, l.course_id, l.section_id, l.title, l.free_preview, l.sort_order,
       EXISTS (SELECT 1 FROM courseauthoring.lecture_blocks b WHERE b.lecture_id = l.id AND b.kind = 'text')::bool AS has_text,
       EXISTS (SELECT 1 FROM courseauthoring.lecture_blocks b WHERE b.lecture_id = l.id AND b.kind = 'video')::bool AS has_video
FROM courseauthoring.lectures l
WHERE l.course_id = ANY(sqlc.arg(course_ids)::bigint[])
ORDER BY l.course_id, l.sort_order;

-- name: UpsertSection :exec
INSERT INTO courseauthoring.sections (id, course_id, title, sort_order)
VALUES ($1, $2, $3, $4)
ON CONFLICT (id) DO UPDATE SET title = EXCLUDED.title, sort_order = EXCLUDED.sort_order;

-- name: DeleteSectionsNotIn :exec
DELETE FROM courseauthoring.sections
WHERE course_id = $1 AND NOT (id = ANY(sqlc.arg(keep)::bigint[]));

-- name: UpsertLecture :exec
INSERT INTO courseauthoring.lectures (id, course_id, section_id, title, free_preview, sort_order, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $7)
ON CONFLICT (id) DO UPDATE SET section_id = EXCLUDED.section_id, title = EXCLUDED.title,
  free_preview = EXCLUDED.free_preview, sort_order = EXCLUDED.sort_order, updated_at = EXCLUDED.updated_at;

-- name: DeleteLecturesNotIn :exec
DELETE FROM courseauthoring.lectures
WHERE course_id = $1 AND NOT (id = ANY(sqlc.arg(keep)::bigint[]));

-- name: GetLectureHeader :one
SELECT id, course_id, title, free_preview, content_revision
FROM courseauthoring.lectures WHERE course_id = $1 AND id = $2;

-- name: GetLectureHeaderForUpdate :one
SELECT id, course_id, title, free_preview, content_revision
FROM courseauthoring.lectures WHERE course_id = $1 AND id = $2
FOR UPDATE;

-- name: ListLectureBlocks :many
SELECT id, client_block_id, kind, position, payload
FROM courseauthoring.lecture_blocks WHERE lecture_id = $1 ORDER BY position;

-- name: BumpLectureContentRevision :execrows
UPDATE courseauthoring.lectures
SET content_revision = content_revision + 1, updated_at = $3
WHERE id = $1 AND content_revision = $2;

-- name: ForceBumpLectureContentRevision :one
UPDATE courseauthoring.lectures
SET content_revision = content_revision + 1, updated_at = $2
WHERE id = $1
RETURNING content_revision;

-- name: DeleteLectureBlocks :exec
DELETE FROM courseauthoring.lecture_blocks WHERE lecture_id = $1;

-- name: DeleteLectureBlocksByClientIDs :exec
DELETE FROM courseauthoring.lecture_blocks
WHERE lecture_id = $1 AND client_block_id = ANY(sqlc.arg(client_block_ids)::text[]);

-- name: UpsertLectureBlocks :exec
-- One jsonb array of {id, client_block_id, kind, position, payload}: sqlc cannot
-- analyze a multi-array unnest(), and one bind value is one round trip either way.
INSERT INTO courseauthoring.lecture_blocks
  (id, course_id, lecture_id, client_block_id, kind, position, payload, created_at, updated_at)
SELECT (elem->>'id')::bigint, sqlc.arg(course_id)::bigint, sqlc.arg(lecture_id)::bigint,
       elem->>'client_block_id', elem->>'kind', (elem->>'position')::int, (elem->'payload')::jsonb,
       sqlc.arg(now)::timestamptz, sqlc.arg(now)::timestamptz
FROM jsonb_array_elements(sqlc.arg(blocks)::jsonb) AS elem
ON CONFLICT (lecture_id, client_block_id) DO UPDATE SET
  kind = EXCLUDED.kind, position = EXCLUDED.position, payload = EXCLUDED.payload, updated_at = EXCLUDED.updated_at;

-- name: ApplyLectureBlockOrder :exec
-- The position unique constraint is deferred, so permuting positions in one
-- statement is allowed. Rows already in place are skipped.
UPDATE courseauthoring.lecture_blocks b
SET position = o.position - 1, updated_at = sqlc.arg(now)::timestamptz
FROM unnest(sqlc.arg(client_block_ids)::text[]) WITH ORDINALITY AS o(client_block_id, position)
WHERE b.lecture_id = sqlc.arg(lecture_id)::bigint
  AND b.client_block_id = o.client_block_id
  AND b.position IS DISTINCT FROM (o.position - 1);
