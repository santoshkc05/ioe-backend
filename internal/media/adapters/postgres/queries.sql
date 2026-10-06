-- name: InsertAsset :exec
INSERT INTO media.assets (id, course_id, kind, created_by, created_at)
VALUES ($1, $2, $3, $4, $5);

-- name: GetAsset :one
SELECT * FROM media.assets WHERE id = $1;

-- name: DeleteAsset :exec
DELETE FROM media.assets WHERE id = $1;

-- name: ListAssetKindsInCourse :many
SELECT id, kind FROM media.assets
WHERE course_id = sqlc.arg(course_id) AND id = ANY(sqlc.arg(ids)::bigint[]);
