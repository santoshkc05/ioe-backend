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

-- name: InsertExam :exec
INSERT INTO assessment.exams (id, course_id, title, description, position, status, pass_mark,
  time_limit_seconds, retakes_allowed, opens_at, closes_at, reveal_policy, questions, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15);

-- name: ReplaceExam :execrows
UPDATE assessment.exams
SET title = $2, description = $3, position = $4, status = $5, pass_mark = $6, time_limit_seconds = $7,
    retakes_allowed = $8, opens_at = $9, closes_at = $10, reveal_policy = $11, questions = $12, updated_at = $13
WHERE id = $1;

-- name: GetExam :one
SELECT * FROM assessment.exams WHERE id = $1;

-- name: GetExamForShare :one
SELECT * FROM assessment.exams WHERE id = $1 FOR SHARE;

-- name: GetExamForUpdate :one
SELECT * FROM assessment.exams WHERE id = $1 FOR UPDATE;

-- name: ListExamsByCourse :many
SELECT * FROM assessment.exams
WHERE course_id = @course_id AND (NOT @published_only::boolean OR status = 'published')
ORDER BY position, id;

-- name: DeleteExam :exec
DELETE FROM assessment.exams WHERE id = $1;

-- name: NextExamPosition :one
SELECT COALESCE(MAX(position) + 1, 0)::integer AS next FROM assessment.exams WHERE course_id = $1;

-- name: SetExamPositions :exec
UPDATE assessment.exams AS e SET position = (o.ord - 1)::integer
FROM unnest(@exam_ids::bigint[]) WITH ORDINALITY AS o(exam_id, ord)
WHERE e.id = o.exam_id AND e.course_id = @course_id;

-- name: CountExamAttempts :one
SELECT count(*) FILTER (WHERE submitted_at IS NULL)::integer AS open_attempts,
       count(*) FILTER (WHERE submitted_at IS NOT NULL)::integer AS submitted_attempts
FROM assessment.exam_attempts WHERE exam_id = $1;

-- name: ListAnsweredExamQuestions :many
-- Question IDs (as text) with a non-empty answer in any attempt of the exam.
SELECT DISTINCT a.key::text AS question_id
FROM assessment.exam_attempts AS t, jsonb_each(t.answers) AS a
WHERE t.exam_id = $1 AND jsonb_array_length(a.value -> 'option_ids') > 0;

-- name: InsertExamAttempt :exec
INSERT INTO assessment.exam_attempts (id, exam_id, course_id, user_id, started_at)
VALUES ($1, $2, $3, $4, $5);

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
