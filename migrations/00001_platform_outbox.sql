-- +goose Up
CREATE SCHEMA platform;

CREATE TABLE platform.outbox_messages (
  "offset"       BIGSERIAL,
  uuid           VARCHAR(36) NOT NULL,
  created_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  payload        JSON DEFAULT NULL,
  metadata       JSON DEFAULT NULL,
  transaction_id xid8 NOT NULL,
  PRIMARY KEY (transaction_id, "offset")
);

CREATE TABLE platform.outbox_offsets (
  consumer_group                VARCHAR(255) NOT NULL,
  offset_acked                  BIGINT,
  last_processed_transaction_id xid8 NOT NULL,
  PRIMARY KEY (consumer_group)
);

-- +goose Down
DROP SCHEMA platform CASCADE;
