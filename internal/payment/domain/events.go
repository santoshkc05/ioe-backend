package domain

import (
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

// Event is a domain event written to the outbox under its EventName.
type Event interface {
	EventName() string
}

// PurchaseInitiated is emitted when a buyer starts a checkout.
type PurchaseInitiated struct {
	PurchaseID  id.ID     `json:"purchase_id"`
	UserID      id.ID     `json:"user_id"`
	CourseID    id.ID     `json:"course_id"`
	AmountMinor int64     `json:"amount_minor"`
	Currency    string    `json:"currency"`
	Gateway     string    `json:"gateway"`
	OccurredAt  time.Time `json:"occurred_at"`
}

func (PurchaseInitiated) EventName() string { return "payment.purchase.initiated" }

// PurchasePaid is emitted when the gateway confirms the payment.
type PurchasePaid struct {
	PurchaseID  id.ID     `json:"purchase_id"`
	UserID      id.ID     `json:"user_id"`
	CourseID    id.ID     `json:"course_id"`
	AmountMinor int64     `json:"amount_minor"`
	Currency    string    `json:"currency"`
	Gateway     string    `json:"gateway"`
	GatewayTxn  string    `json:"gateway_txn"`
	OccurredAt  time.Time `json:"occurred_at"`
}

func (PurchasePaid) EventName() string { return "payment.purchase.paid" }

// PurchaseFailed is emitted when the gateway reports that a pending payment will not complete.
type PurchaseFailed struct {
	PurchaseID id.ID     `json:"purchase_id"`
	UserID     id.ID     `json:"user_id"`
	CourseID   id.ID     `json:"course_id"`
	Gateway    string    `json:"gateway"`
	OccurredAt time.Time `json:"occurred_at"`
}

func (PurchaseFailed) EventName() string { return "payment.purchase.failed" }
