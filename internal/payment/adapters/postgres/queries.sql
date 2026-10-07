-- name: GetPurchase :one
SELECT * FROM payment.purchases WHERE id = $1;

-- name: CountPaidPurchases :one
SELECT count(*) FROM payment.purchases WHERE user_id = $1 AND course_id = $2 AND status = 'paid';

-- name: ListUnsettledPurchases :many
SELECT * FROM payment.purchases
WHERE id > sqlc.arg(after_id)::bigint
  AND ((status = 'pending' AND created_at < sqlc.arg(pending_before)::timestamptz)
       OR (status = 'paid' AND granted_at IS NULL))
ORDER BY id
LIMIT sqlc.arg(page_limit)::bigint;

-- name: InsertPurchase :exec
INSERT INTO payment.purchases
  (id, user_id, course_id, amount_minor, currency, gateway, gateway_ref, gateway_txn, status,
   created_at, settled_at, granted_at, version)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, 1);

-- name: UpdatePurchase :execrows
UPDATE payment.purchases
SET gateway_txn = $3, status = $4, settled_at = $5, granted_at = $6, version = version + 1
WHERE id = $1 AND version = $2;
