-- name: InsertQuiz :exec
INSERT INTO assessment.quizzes (id, course_id, lecture_id, position, questions, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7);

-- name: ReplaceQuiz :execrows
UPDATE assessment.quizzes SET position = $2, questions = $3, updated_at = $4 WHERE id = $1;

-- name: GetQuiz :one
SELECT * FROM assessment.quizzes WHERE id = $1;

-- name: ListQuizzesByLecture :many
SELECT * FROM assessment.quizzes
WHERE course_id = $1 AND lecture_id = $2
ORDER BY position, id;

-- name: DeleteQuiz :exec
DELETE FROM assessment.quizzes WHERE id = $1;

-- name: ListQuizLectures :many
SELECT id, lecture_id FROM assessment.quizzes
WHERE course_id = @course_id AND id = ANY(@quiz_ids::bigint[]);

-- name: InsertQuizAttempt :one
-- Returns no row when an attempt with the same quiz, user and key exists.
INSERT INTO assessment.quiz_attempts (id, quiz_id, user_id, answers, idempotency_key, submitted_at)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (quiz_id, user_id, idempotency_key) WHERE idempotency_key IS NOT NULL DO NOTHING
RETURNING id;

-- name: FindQuizAttemptByKey :one
SELECT id FROM assessment.quiz_attempts
WHERE quiz_id = $1 AND user_id = $2 AND idempotency_key = $3;
