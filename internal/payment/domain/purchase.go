package domain

import (
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

// Money is an amount in the currency's minor unit (paisa for NPR). Currency is an ISO 4217 code.
type Money struct {
	AmountMinor int64
	Currency    string
}

type Status string

const (
	StatusPending Status = "pending"
	StatusPaid    Status = "paid"
	StatusFailed  Status = "failed"
)

// Purchase is one buyer's attempt to pay for one course through one gateway. A buyer may have
// several; each is settled independently from the gateway's status.
type Purchase struct {
	ID         id.ID
	UserID     id.ID
	CourseID   id.ID
	Price      Money  // snapshot taken at checkout
	Gateway    string // gateway name, such as "esewa"
	GatewayRef string // our reference at the gateway; for eSewa, transaction_uuid
	GatewayTxn string // the gateway's transaction id; set when paid
	Status     Status
	CreatedAt  time.Time
	SettledAt  time.Time // zero while pending
	GrantedAt  time.Time // zero until the enrollment grant succeeds
	Version    int64
}

// NewPurchase starts a pending purchase. Its gateway reference is the purchase ID.
func NewPurchase(purchaseID, userID, courseID id.ID, price Money, gateway string, now time.Time) (Purchase, Event, error) {
	if price.AmountMinor <= 0 {
		return Purchase{}, nil, ErrFreePrice
	}
	if price.Currency == "" || gateway == "" {
		return Purchase{}, nil, ErrInvalidPurchase
	}
	p := Purchase{
		ID: purchaseID, UserID: userID, CourseID: courseID, Price: price, Gateway: gateway,
		GatewayRef: purchaseID.String(), Status: StatusPending, CreatedAt: now,
	}
	return p, PurchaseInitiated{
		PurchaseID: p.ID, UserID: userID, CourseID: courseID, AmountMinor: price.AmountMinor,
		Currency: price.Currency, Gateway: gateway, OccurredAt: now,
	}, nil
}

// MarkPaid records the gateway's confirmation. A failed purchase can still become paid when the
// gateway completes it late. Marking a paid purchase changes nothing and returns no event.
func (p *Purchase) MarkPaid(txn string, now time.Time) (Event, error) {
	if txn == "" {
		return nil, ErrInvalidPurchase
	}
	if p.Status == StatusPaid {
		return nil, nil
	}
	p.Status, p.GatewayTxn, p.SettledAt = StatusPaid, txn, now
	return PurchasePaid{
		PurchaseID: p.ID, UserID: p.UserID, CourseID: p.CourseID, AmountMinor: p.Price.AmountMinor,
		Currency: p.Price.Currency, Gateway: p.Gateway, GatewayTxn: txn, OccurredAt: now,
	}, nil
}

// MarkFailed records that a pending payment will not complete. On any other status it changes
// nothing and returns no event.
func (p *Purchase) MarkFailed(now time.Time) Event {
	if p.Status != StatusPending {
		return nil
	}
	p.Status, p.SettledAt = StatusFailed, now
	return PurchaseFailed{PurchaseID: p.ID, UserID: p.UserID, CourseID: p.CourseID, Gateway: p.Gateway, OccurredAt: now}
}

// MarkGranted records that the buyer was enrolled. It changes only a paid, ungranted purchase.
func (p *Purchase) MarkGranted(now time.Time) {
	if p.NeedsGrant() {
		p.GrantedAt = now
	}
}

// NeedsGrant reports whether the purchase is paid but the buyer is not yet known to be enrolled.
func (p *Purchase) NeedsGrant() bool { return p.Status == StatusPaid && p.GrantedAt.IsZero() }

// CanView reports whether pr is the buyer or a root admin.
func (p *Purchase) CanView(pr auth.Principal) bool {
	return pr.UserID == p.UserID || pr.Role == auth.RoleRootAdmin
}
