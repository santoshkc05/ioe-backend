// Package app contains the payment use cases and the ports they depend on.
package app

import (
	"context"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/payment/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

// Repository reads and writes purchases in the current transaction. Insert sets p.Version to 1.
// Update returns ErrConcurrentModification when p.Version is stale and increments it on success.
type Repository interface {
	Find(ctx context.Context, purchaseID id.ID) (domain.Purchase, bool, error)
	CountPaid(ctx context.Context, userID, courseID id.ID) (int, error)
	// ListUnsettled returns purchases with an ID above afterID that are pending and created
	// before pendingBefore, or paid and not yet granted, in ID order.
	ListUnsettled(ctx context.Context, pendingBefore time.Time, afterID id.ID, limit int) ([]domain.Purchase, error)
	Insert(ctx context.Context, p *domain.Purchase) error
	Update(ctx context.Context, p *domain.Purchase) error
}

// EventPublisher records events in the current transaction.
type EventPublisher interface {
	Publish(ctx context.Context, events ...domain.Event) error
}

// Repos are bound to one transaction.
type Repos struct {
	Purchases Repository
	Events    EventPublisher
}

// TxRunner commits when fn returns nil and rolls back otherwise.
type TxRunner interface {
	RunInTx(ctx context.Context, fn func(Repos) error) error
}

// CourseFacts is what payment knows about a course. A zero Price.AmountMinor means free.
type CourseFacts struct {
	Published bool
	Price     domain.Money
}

// CourseCatalog returns ErrNotFound when the course does not exist.
type CourseCatalog interface {
	CourseFacts(ctx context.Context, courseID id.ID) (CourseFacts, error)
}

// EnrollmentGranter is backed by the enrollment context.
type EnrollmentGranter interface {
	IsEnrolled(ctx context.Context, courseID, userID id.ID) (bool, error)
	// GrantPurchased actively enrolls the user. It is idempotent.
	GrantPurchased(ctx context.Context, courseID, userID id.ID) error
}

// Gateway is one payment provider.
type Gateway interface {
	StartCheckout(ctx context.Context, p domain.Purchase) (Checkout, error)
	FetchStatus(ctx context.Context, p domain.Purchase) (Result, error)
}

// Checkout tells the client how to send the buyer to the gateway.
type Checkout struct {
	Method string
	URL    string
	Fields map[string]string // form fields; empty for a plain redirect
}

type ResultKind int

const (
	ResultPending  ResultKind = iota // not settled yet, or the gateway is unsure
	ResultComplete                   // paid; Result.Txn is the gateway's transaction id
	ResultFailed                     // will not complete
)

// Result is the gateway's authoritative view of one purchase.
type Result struct {
	Kind ResultKind
	Txn  string
}
