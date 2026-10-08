-- name: UpsertPolicy :exec
INSERT INTO certificate.policies (course_id, mode, exam_id, updated_at)
VALUES ($1, $2, $3, $4)
ON CONFLICT (course_id) DO UPDATE SET
  mode = EXCLUDED.mode,
  exam_id = EXCLUDED.exam_id,
  updated_at = EXCLUDED.updated_at;

-- name: GetPolicy :one
SELECT * FROM certificate.policies WHERE course_id = $1;

-- name: InsertCertificate :one
-- Returns no row when the user already holds a valid certificate for the course.
INSERT INTO certificate.certificates (id, code, user_id, course_id, student_name, course_title, issued_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT DO NOTHING
RETURNING id;

-- name: GetValidCertificate :one
SELECT * FROM certificate.certificates
WHERE course_id = $1 AND user_id = $2 AND revoked_at IS NULL;

-- name: GetCertificateByCode :one
SELECT * FROM certificate.certificates WHERE code = $1;

-- name: ListCertificatesByUser :many
SELECT * FROM certificate.certificates
WHERE user_id = $1
ORDER BY issued_at DESC, id DESC;

-- name: RevokeValidCertificate :exec
UPDATE certificate.certificates SET revoked_at = $3
WHERE course_id = $1 AND user_id = $2 AND revoked_at IS NULL AND issued_at <= sqlc.arg(issued_by);
