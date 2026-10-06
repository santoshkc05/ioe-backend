-- name: UpsertLectureProgress :one
-- Returns no row when a completed lecture rejects an in_progress write.
INSERT INTO progress.lecture_progress AS lp (course_id, user_id, lecture_id, state, position_ms, updated_at)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (course_id, user_id, lecture_id) DO UPDATE SET
  state = EXCLUDED.state,
  position_ms = EXCLUDED.position_ms,
  updated_at = EXCLUDED.updated_at
WHERE lp.state <> 'completed' OR EXCLUDED.state = 'completed'
RETURNING lecture_id;

-- name: UpsertCourseProgress :exec
INSERT INTO progress.course_progress (course_id, user_id, last_lecture_id, updated_at)
VALUES ($1, $2, $3, $4)
ON CONFLICT (course_id, user_id) DO UPDATE SET
  last_lecture_id = EXCLUDED.last_lecture_id,
  updated_at = EXCLUDED.updated_at;

-- name: GetCourseProgress :one
SELECT * FROM progress.course_progress WHERE course_id = $1 AND user_id = $2;

-- name: ListLectureProgressForCourse :many
SELECT * FROM progress.lecture_progress
WHERE course_id = $1 AND user_id = $2
ORDER BY lecture_id;

-- name: ListCourseProgressByUser :many
SELECT * FROM progress.course_progress
WHERE user_id = $1
ORDER BY updated_at DESC, course_id;

-- name: ListLectureProgressByUser :many
SELECT * FROM progress.lecture_progress
WHERE user_id = $1
ORDER BY course_id, lecture_id;

-- name: ListActivityDaysByUser :many
-- Buckets by UTC date regardless of the session time zone.
SELECT to_char(updated_at AT TIME ZONE 'UTC', 'YYYY-MM-DD')::text AS day,
       count(DISTINCT lecture_id)::bigint AS lecture_count
FROM progress.lecture_progress
WHERE user_id = $1
GROUP BY day
ORDER BY day DESC;
