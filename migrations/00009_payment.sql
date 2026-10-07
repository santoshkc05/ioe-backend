-- +goose Up
CREATE SCHEMA payment;

CREATE TABLE payment.purchases (
  id           bigint PRIMARY KEY,
  user_id      bigint NOT NULL,
  course_id    bigint NOT NULL,
  amount_minor bigint NOT NULL CHECK (amount_minor > 0),
  currency     text NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  gateway      text NOT NULL CHECK (gateway <> ''),
  gateway_ref  text NOT NULL,
  gateway_txn  text NOT NULL DEFAULT '',
  status       text NOT NULL CHECK (status IN ('pending', 'paid', 'failed')),
  created_at   timestamptz NOT NULL,
  settled_at   timestamptz,
  granted_at   timestamptz,
  version      bigint NOT NULL,
  CONSTRAINT purchases_gateway_ref_unique UNIQUE (gateway, gateway_ref),
  CONSTRAINT purchases_settled_consistent CHECK ((status = 'pending') = (settled_at IS NULL)),
  CONSTRAINT purchases_paid_txn CHECK ((status = 'paid') = (gateway_txn <> '')),
  CONSTRAINT purchases_granted_paid CHECK (granted_at IS NULL OR status = 'paid')
);

CREATE INDEX purchases_user_course ON payment.purchases (user_id, course_id);
CREATE INDEX purchases_unsettled ON payment.purchases (id)
  WHERE status = 'pending' OR (status = 'paid' AND granted_at IS NULL);

-- +goose Down
DROP SCHEMA payment CASCADE;
