-- +goose Up
ALTER TABLE payment.purchases
  ADD COLUMN refunded_at      timestamptz,
  ADD COLUMN refunded_by      bigint,
  ADD COLUMN refund_reference text,
  ADD COLUMN refund_note      text,
  ADD COLUMN revoked_at       timestamptz,
  DROP CONSTRAINT purchases_status_check,
  ADD CONSTRAINT purchases_status_check CHECK (status IN ('pending', 'paid', 'failed', 'refunded')),
  DROP CONSTRAINT purchases_paid_txn,
  ADD CONSTRAINT purchases_paid_txn CHECK ((status IN ('paid', 'refunded')) = (gateway_txn <> '')),
  DROP CONSTRAINT purchases_granted_paid,
  ADD CONSTRAINT purchases_granted_paid CHECK (granted_at IS NULL OR status IN ('paid', 'refunded')),
  ADD CONSTRAINT purchases_refund_consistent CHECK (
    (status = 'refunded') = (refunded_at IS NOT NULL AND refunded_by IS NOT NULL AND refund_reference IS NOT NULL)),
  ADD CONSTRAINT purchases_revoked_refunded CHECK (revoked_at IS NULL OR status = 'refunded');

DROP INDEX payment.purchases_unsettled;
CREATE INDEX purchases_unsettled ON payment.purchases (id)
  WHERE status = 'pending' OR (status = 'paid' AND granted_at IS NULL)
     OR (status = 'refunded' AND revoked_at IS NULL);

-- +goose Down
-- Fails while refunded rows exist, so refund records are never silently dropped.
DROP INDEX payment.purchases_unsettled;
CREATE INDEX purchases_unsettled ON payment.purchases (id)
  WHERE status = 'pending' OR (status = 'paid' AND granted_at IS NULL);

ALTER TABLE payment.purchases
  DROP CONSTRAINT purchases_revoked_refunded,
  DROP CONSTRAINT purchases_refund_consistent,
  DROP CONSTRAINT purchases_granted_paid,
  ADD CONSTRAINT purchases_granted_paid CHECK (granted_at IS NULL OR status = 'paid'),
  DROP CONSTRAINT purchases_paid_txn,
  ADD CONSTRAINT purchases_paid_txn CHECK ((status = 'paid') = (gateway_txn <> '')),
  DROP CONSTRAINT purchases_status_check,
  ADD CONSTRAINT purchases_status_check CHECK (status IN ('pending', 'paid', 'failed')),
  DROP COLUMN revoked_at,
  DROP COLUMN refund_note,
  DROP COLUMN refund_reference,
  DROP COLUMN refunded_by,
  DROP COLUMN refunded_at;
