-- +goose Up
ALTER TABLE payment.purchases
  ADD COLUMN course_title  text NOT NULL DEFAULT '',
  ADD COLUMN manual_method text,
  ADD COLUMN recorded_by   bigint,
  ADD COLUMN note          text NOT NULL DEFAULT '';

-- One-time backfill from the course's working copy. No runtime code reads courseauthoring.
UPDATE payment.purchases p
SET course_title = c.title
FROM courseauthoring.courses c
WHERE c.id = p.course_id;

ALTER TABLE payment.purchases
  ALTER COLUMN course_title DROP DEFAULT,
  ALTER COLUMN note DROP DEFAULT,
  ADD CONSTRAINT purchases_manual_consistent CHECK (
    (gateway = 'manual') = (manual_method IS NOT NULL AND recorded_by IS NOT NULL)),
  ADD CONSTRAINT purchases_manual_method CHECK (
    manual_method IS NULL OR manual_method IN ('bank_transfer', 'cash', 'other'));

CREATE INDEX purchases_user_id_desc ON payment.purchases (user_id, id DESC);

-- +goose Down
DROP INDEX payment.purchases_user_id_desc;
ALTER TABLE payment.purchases
  DROP CONSTRAINT purchases_manual_method,
  DROP CONSTRAINT purchases_manual_consistent,
  DROP COLUMN note,
  DROP COLUMN recorded_by,
  DROP COLUMN manual_method,
  DROP COLUMN course_title;
