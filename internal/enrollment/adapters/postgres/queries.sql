-- name: GetEnrollment :one
SELECT * FROM enrollment.enrollments WHERE course_id = $1 AND user_id = $2;

-- name: InsertEnrollment :exec
INSERT INTO enrollment.enrollments
  (id, course_id, user_id, status, cancel_reason, enrolled_at, canceled_at, version)
VALUES ($1, $2, $3, $4, $5, $6, $7, 1);

-- name: UpdateEnrollment :execrows
UPDATE enrollment.enrollments
SET status = $3, cancel_reason = $4, enrolled_at = $5, canceled_at = $6, version = version + 1
WHERE id = $1 AND version = $2;

-- name: ListActiveEnrollmentsByCourse :many
SELECT * FROM enrollment.enrollments
WHERE course_id = $1 AND status = 'active'
ORDER BY enrolled_at, id
LIMIT sqlc.arg(page_limit)::bigint OFFSET sqlc.arg(page_offset)::bigint;

-- name: CountActiveEnrollmentsByCourse :one
SELECT count(*) FROM enrollment.enrollments WHERE course_id = $1 AND status = 'active';

-- name: ListActiveEnrollmentsByUser :many
SELECT * FROM enrollment.enrollments
WHERE user_id = $1 AND status = 'active'
ORDER BY enrolled_at DESC, id DESC;
