-- name: InsertCourse :exec
INSERT INTO courseauthoring.courses
  (id, owner_id, title, description, level, thumbnail_url, status, price_amount_minor, price_currency, version, created_at, updated_at, tags)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 1, $10, $11, $12);

-- name: UpdateCourse :execrows
UPDATE courseauthoring.courses
SET title = $3, description = $4, level = $5, thumbnail_url = $6, status = $7,
    price_amount_minor = $8, price_currency = $9, updated_at = $10,
    submitted_at = $11, reviewed_at = $12, review_note = $13, last_version = $14, live_version = $15,
    tags = $16, version = version + 1
WHERE id = $1 AND version = $2;

-- name: LockCourseForUpdate :one
SELECT id FROM courseauthoring.courses WHERE id = $1 FOR UPDATE;

-- name: InsertCourseVersion :exec
INSERT INTO courseauthoring.course_versions
  (course_id, number, title, description, level, thumbnail_url, price_amount_minor, price_currency, published_by, published_at, tags)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11);

-- name: CopyVersionSections :exec
INSERT INTO courseauthoring.course_version_sections (course_id, number, id, title, sort_order)
SELECT course_id, sqlc.arg(number)::integer, id, title, sort_order
FROM courseauthoring.sections WHERE course_id = sqlc.arg(course_id)::bigint;

-- name: CopyVersionLectures :exec
INSERT INTO courseauthoring.course_version_lectures (course_id, number, id, section_id, title, free_preview, sort_order)
SELECT course_id, sqlc.arg(number)::integer, id, section_id, title, free_preview, sort_order
FROM courseauthoring.lectures WHERE course_id = sqlc.arg(course_id)::bigint;

-- name: CopyVersionBlocks :exec
INSERT INTO courseauthoring.course_version_blocks (course_id, number, lecture_id, id, kind, position, client_block_id, payload)
SELECT course_id, sqlc.arg(number)::integer, lecture_id, id, kind, position, client_block_id, payload
FROM courseauthoring.lecture_blocks WHERE course_id = sqlc.arg(course_id)::bigint;

-- name: ListCourseVersions :many
SELECT number, published_by, published_at FROM courseauthoring.course_versions
WHERE course_id = $1 ORDER BY number DESC;

-- name: GetCourseVersion :one
SELECT course_id, number, title, description, level, thumbnail_url, price_amount_minor,
       price_currency, published_by, published_at, tags
FROM courseauthoring.course_versions WHERE course_id = $1 AND number = $2;

-- name: ListVersionSections :many
SELECT id, title, sort_order FROM courseauthoring.course_version_sections
WHERE course_id = $1 AND number = $2 ORDER BY sort_order;

-- name: ListVersionLectures :many
SELECT l.id, l.section_id, l.title, l.free_preview, l.sort_order,
       EXISTS (SELECT 1 FROM courseauthoring.course_version_blocks b
               WHERE b.course_id = l.course_id AND b.number = l.number AND b.lecture_id = l.id AND b.kind = 'text')::bool AS has_text,
       EXISTS (SELECT 1 FROM courseauthoring.course_version_blocks b
               WHERE b.course_id = l.course_id AND b.number = l.number AND b.lecture_id = l.id AND b.kind = 'video')::bool AS has_video
FROM courseauthoring.course_version_lectures l
WHERE l.course_id = $1 AND l.number = $2
ORDER BY l.sort_order;

-- name: GetVersionLecture :one
SELECT id, title, free_preview FROM courseauthoring.course_version_lectures
WHERE course_id = $1 AND number = $2 AND id = $3;

-- name: ListVersionBlocks :many
SELECT id, client_block_id, kind, position, payload FROM courseauthoring.course_version_blocks
WHERE course_id = $1 AND number = $2 AND lecture_id = $3 ORDER BY position;

-- name: ListInReviewCourseIDs :many
SELECT id FROM courseauthoring.courses WHERE status = 'in_review' ORDER BY submitted_at, id;

-- name: InsertCourseReview :exec
INSERT INTO courseauthoring.course_reviews (id, course_id, actor_id, decision, note, created_at)
VALUES ($1, $2, $3, $4, $5, $6);

-- name: ListCourseReviews :many
SELECT id, course_id, actor_id, decision, note, created_at
FROM courseauthoring.course_reviews WHERE course_id = $1 ORDER BY created_at, id;

-- name: ListCoursesByIDs :many
SELECT c.*, v.published_at AS live_published_at
FROM courseauthoring.courses c
LEFT JOIN courseauthoring.course_versions v ON v.course_id = c.id AND v.number = c.live_version
WHERE c.id = ANY(sqlc.arg(ids)::bigint[]);

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

-- name: ListPublishedCourses :many
-- Lists live versions. updated_at is when the live version was published.
SELECT c.id, c.owner_id, v.title, v.description, v.level, v.thumbnail_url,
       v.price_amount_minor, v.price_currency, c.created_at, v.published_at AS updated_at,
       (SELECT count(*) FROM courseauthoring.course_version_lectures l
        WHERE l.course_id = v.course_id AND l.number = v.number) AS lecture_count,
       (SELECT count(*) FROM courseauthoring.course_version_sections s
        WHERE s.course_id = v.course_id AND s.number = v.number) AS section_count
FROM courseauthoring.courses c
JOIN courseauthoring.course_versions v ON v.course_id = c.id AND v.number = c.live_version
WHERE (sqlc.arg(after)::bigint = 0 OR c.id < sqlc.arg(after)::bigint)
  AND (sqlc.arg(level)::text = '' OR v.level = sqlc.arg(level)::text)
  AND (sqlc.arg(price)::text = ''
       OR (sqlc.arg(price)::text = 'free' AND v.price_amount_minor = 0)
       OR (sqlc.arg(price)::text = 'paid' AND v.price_amount_minor > 0))
ORDER BY c.id DESC
LIMIT sqlc.arg(row_limit)::integer;

-- name: DeleteSubmittedAssessments :exec
DELETE FROM courseauthoring.course_submitted_assessments WHERE course_id = $1;

-- name: InsertSubmittedAssessments :exec
INSERT INTO courseauthoring.course_submitted_assessments (course_id, kind, assessment_id, revision)
SELECT @course_id, k, a, r
FROM ROWS FROM (unnest(@kinds::text[]), unnest(@assessment_ids::bigint[]), unnest(@revisions::integer[])) AS p(k, a, r);

-- name: ListSubmittedAssessments :many
SELECT kind, assessment_id, revision FROM courseauthoring.course_submitted_assessments
WHERE course_id = $1 ORDER BY kind, assessment_id;

-- name: InsertVersionAssessments :exec
INSERT INTO courseauthoring.course_version_assessments (course_id, number, kind, assessment_id, revision)
SELECT @course_id, @number, k, a, r
FROM ROWS FROM (unnest(@kinds::text[]), unnest(@assessment_ids::bigint[]), unnest(@revisions::integer[])) AS p(k, a, r);

-- name: ListVersionAssessments :many
SELECT kind, assessment_id, revision FROM courseauthoring.course_version_assessments
WHERE course_id = $1 AND number = $2 ORDER BY kind, assessment_id;

-- name: ListCategoriesByCourseIDs :many
SELECT cc.course_id, k.id, k.name, k.slug
FROM courseauthoring.course_categories cc
JOIN courseauthoring.categories k ON k.id = cc.category_id
WHERE cc.course_id = ANY(sqlc.arg(course_ids)::bigint[])
ORDER BY cc.course_id, cc.position;

-- name: ListVersionCategories :many
SELECT k.id, k.name, k.slug
FROM courseauthoring.course_version_categories vc
JOIN courseauthoring.categories k ON k.id = vc.category_id
WHERE vc.course_id = $1 AND vc.number = $2
ORDER BY vc.position;

-- name: DeleteCourseCategories :exec
DELETE FROM courseauthoring.course_categories WHERE course_id = $1;

-- name: InsertCourseCategories :exec
INSERT INTO courseauthoring.course_categories (course_id, category_id, position)
SELECT sqlc.arg(course_id)::bigint, t.category_id, (t.ord - 1)::integer
FROM unnest(sqlc.arg(category_ids)::bigint[]) WITH ORDINALITY AS t(category_id, ord);

-- name: InsertVersionCategories :exec
INSERT INTO courseauthoring.course_version_categories (course_id, number, category_id, position)
SELECT sqlc.arg(course_id)::bigint, sqlc.arg(number)::integer, t.category_id, (t.ord - 1)::integer
FROM unnest(sqlc.arg(category_ids)::bigint[]) WITH ORDINALITY AS t(category_id, ord);

-- name: InsertCategory :exec
INSERT INTO courseauthoring.categories (id, name, slug, created_at) VALUES ($1, $2, $3, $4);

-- name: UpdateCategory :execrows
UPDATE courseauthoring.categories SET name = $2, slug = $3 WHERE id = $1;

-- name: DeleteCategory :execrows
DELETE FROM courseauthoring.categories WHERE id = $1;

-- name: GetCategory :one
SELECT id, name, slug, created_at FROM courseauthoring.categories WHERE id = $1;

-- name: ListCategoriesWithCounts :many
SELECT k.id, k.name, k.slug, k.created_at,
       (SELECT count(*) FROM courseauthoring.course_version_categories vc
        JOIN courseauthoring.courses c ON c.id = vc.course_id AND c.live_version = vc.number
        WHERE vc.category_id = k.id) AS course_count
FROM courseauthoring.categories k
ORDER BY lower(k.name), k.id;

-- name: CountCategories :one
SELECT count(*) FROM courseauthoring.categories WHERE id = ANY(sqlc.arg(ids)::bigint[]);

