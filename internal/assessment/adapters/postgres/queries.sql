-- name: InsertQuiz :exec
INSERT INTO assessment.quizzes (id, course_id, lecture_id, head_revision, created_at, updated_at)
VALUES ($1, $2, $3, 1, $4, $4);

-- name: InsertQuizRevision :exec
INSERT INTO assessment.quiz_revisions (quiz_id, revision, position, questions, created_by, created_at)
VALUES ($1, $2, $3, $4, $5, $6);

-- name: MoveQuizHead :execrows
UPDATE assessment.quizzes SET head_revision = $1, updated_at = $2
WHERE id = $3 AND head_revision = $1 - 1 AND deleted_at IS NULL;

-- name: GetQuizHead :one
SELECT * FROM assessment.quiz_revision_rows
WHERE id = $1 AND revision = head_revision AND deleted_at IS NULL;

-- name: LockQuiz :one
SELECT id FROM assessment.quizzes WHERE id = $1 AND deleted_at IS NULL FOR UPDATE;

-- name: ListQuizRevisions :many
SELECT v.* FROM assessment.quiz_revision_rows v
JOIN ROWS FROM (unnest(@quiz_ids::bigint[]), unnest(@revisions::integer[])) AS p(quiz_id, revision)
  ON v.id = p.quiz_id AND v.revision = p.revision
ORDER BY v.position, v.id;

-- name: ListQuizzesByLecture :many
SELECT * FROM assessment.quiz_revision_rows
WHERE course_id = $1 AND lecture_id = $2 AND revision = head_revision AND deleted_at IS NULL
ORDER BY position, id;

-- name: SoftDeleteQuiz :exec
UPDATE assessment.quizzes SET deleted_at = $2, updated_at = $2 WHERE id = $1 AND deleted_at IS NULL;

-- name: UndeleteQuiz :exec
UPDATE assessment.quizzes SET deleted_at = NULL, updated_at = $2 WHERE id = $1 AND deleted_at IS NOT NULL;

-- name: ListQuizHeads :many
SELECT id, head_revision, (deleted_at IS NOT NULL)::boolean AS deleted FROM assessment.quizzes
WHERE course_id = $1 ORDER BY id;

-- name: ListQuizLectures :many
SELECT id, lecture_id FROM assessment.quizzes
WHERE course_id = @course_id AND id = ANY(@quiz_ids::bigint[]) AND deleted_at IS NULL;

-- name: InsertQuizAttempt :one
-- Returns no row when an attempt with the same quiz, user and key exists.
INSERT INTO assessment.quiz_attempts (id, quiz_id, revision, user_id, answers, idempotency_key, submitted_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (quiz_id, user_id, idempotency_key) WHERE idempotency_key IS NOT NULL DO NOTHING
RETURNING id;

-- name: FindQuizAttemptByKey :one
SELECT id FROM assessment.quiz_attempts
WHERE quiz_id = $1 AND user_id = $2 AND idempotency_key = $3;

-- name: InsertExam :exec
INSERT INTO assessment.exams (id, course_id, head_revision, created_at, updated_at)
VALUES ($1, $2, 1, $3, $3);

-- name: InsertExamRevision :exec
INSERT INTO assessment.exam_revisions (exam_id, revision, title, description, position, status, pass_mark,
  time_limit_seconds, retakes_allowed, opens_at, closes_at, reveal_policy, questions, created_by, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15);

-- name: MoveExamHead :execrows
UPDATE assessment.exams SET head_revision = $1, updated_at = $2
WHERE id = $3 AND head_revision = $1 - 1 AND deleted_at IS NULL;

-- name: GetExamHead :one
SELECT * FROM assessment.exam_revision_rows
WHERE id = $1 AND revision = head_revision AND deleted_at IS NULL;

-- name: LockExam :one
SELECT id FROM assessment.exams WHERE id = $1 AND deleted_at IS NULL FOR UPDATE;

-- name: ListExamRevisions :many
SELECT v.* FROM assessment.exam_revision_rows v
JOIN ROWS FROM (unnest(@exam_ids::bigint[]), unnest(@revisions::integer[])) AS p(exam_id, revision)
  ON v.id = p.exam_id AND v.revision = p.revision
ORDER BY v.position, v.id;

-- name: ListExamsByCourse :many
SELECT * FROM assessment.exam_revision_rows
WHERE course_id = $1 AND revision = head_revision AND deleted_at IS NULL
ORDER BY position, id;

-- name: SoftDeleteExam :exec
UPDATE assessment.exams SET deleted_at = $2, updated_at = $2 WHERE id = $1 AND deleted_at IS NULL;

-- name: UndeleteExam :exec
UPDATE assessment.exams SET deleted_at = NULL, updated_at = $2 WHERE id = $1 AND deleted_at IS NOT NULL;

-- name: ListExamHeads :many
SELECT id, head_revision, (deleted_at IS NOT NULL)::boolean AS deleted FROM assessment.exams
WHERE course_id = $1 ORDER BY id;

-- name: NextExamPosition :one
SELECT COALESCE(MAX(position) + 1, 0)::integer AS next FROM assessment.exam_revision_rows
WHERE course_id = $1 AND revision = head_revision AND deleted_at IS NULL;

-- name: InsertExamAttempt :exec
INSERT INTO assessment.exam_attempts (id, exam_id, revision, course_id, user_id, started_at)
VALUES ($1, $2, $3, $4, $5, $6);

-- name: GetExamAttempt :one
SELECT * FROM assessment.exam_attempts WHERE id = $1;

-- name: GetExamAttemptForUpdate :one
SELECT * FROM assessment.exam_attempts WHERE id = $1 FOR UPDATE;

-- name: GetOpenExamAttempt :one
SELECT * FROM assessment.exam_attempts WHERE exam_id = $1 AND user_id = $2 AND submitted_at IS NULL;

-- name: HasSubmittedExamAttempt :one
SELECT EXISTS (
  SELECT 1 FROM assessment.exam_attempts WHERE exam_id = $1 AND user_id = $2 AND submitted_at IS NOT NULL
) AS submitted;

-- name: MergeExamAnswer :execrows
UPDATE assessment.exam_attempts SET answers = answers || @patch::jsonb
WHERE id = @id AND submitted_at IS NULL;

-- name: SaveExamResult :execrows
UPDATE assessment.exam_attempts
SET submitted_at = $2, score = $3, passed = $4, auto_submitted = $5, answers = $6
WHERE id = $1 AND submitted_at IS NULL;

-- name: ListExamAttempts :many
SELECT * FROM assessment.exam_attempts WHERE exam_id = $1 ORDER BY started_at, id;

-- name: ListUserExamAttempts :many
SELECT * FROM assessment.exam_attempts WHERE course_id = $1 AND user_id = $2 ORDER BY started_at, id;
