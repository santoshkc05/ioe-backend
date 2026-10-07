package domain

import (
	"strings"
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

// GatewayManual names purchases a root admin records for payments made outside any gateway.
// It is never registered as a Gateway, so such purchases are never sent to one.
const GatewayManual = "manual"

// Offline payment methods of a manual purchase.
const (
	MethodBankTransfer = "bank_transfer"
	MethodCash         = "cash"
	MethodOther        = "other"
)

const (
	maxReferenceLen = 200
	maxNoteLen      = 1000
)

// Purchase is one buyer's attempt to pay for one course through one gateway. A buyer may have
// several; each is settled independently from the gateway's status.
type Purchase struct {
	ID           id.ID
	UserID       id.ID
	CourseID     id.ID
	CourseTitle  string // snapshot taken when the purchase is created
	Price        Money  // snapshot taken at checkout
	Gateway      string // gateway name, such as "esewa"
	GatewayRef   string // our reference at the gateway; for eSewa, transaction_uuid
	GatewayTxn   string // the gateway's transaction id; set when paid
	Status       Status
	CreatedAt    time.Time
	SettledAt    time.Time // zero while pending
	GrantedAt    time.Time // zero until the enrollment grant succeeds
	ManualMethod string    // one of the Method constants; empty unless Gateway is GatewayManual
	RecordedBy   id.ID     // root admin who recorded a manual purchase; zero otherwise
	Note         string    // admin's remark on a manual purchase; may be empty
	Version      int64
}

// NewPurchase starts a pending purchase. Its gateway reference is the purchase ID.
func NewPurchase(purchaseID, userID, courseID id.ID, courseTitle string, price Money, gateway string, now time.Time) (Purchase, Event, error) {
	if price.AmountMinor <= 0 {
		return Purchase{}, nil, ErrFreePrice
	}
	if price.Currency == "" || gateway == "" {
		return Purchase{}, nil, ErrInvalidPurchase
	}
	p := Purchase{
		ID: purchaseID, UserID: userID, CourseID: courseID, CourseTitle: courseTitle, Price: price, Gateway: gateway,
		GatewayRef: purchaseID.String(), Status: StatusPending, CreatedAt: now,
	}
	return p, PurchaseInitiated{
		PurchaseID: p.ID, UserID: userID, CourseID: courseID, AmountMinor: price.AmountMinor,
		Currency: price.Currency, Gateway: gateway, OccurredAt: now,
	}, nil
}

// ManualPayment describes an offline payment a root admin records.
type ManualPayment struct {
	Method     string
	Reference  string // bank voucher number, receipt number or similar; stored as GatewayTxn
	Note       string
	RecordedBy id.ID
}

// RecordManualPurchase creates a purchase that is already paid. The amount may differ from the
// course price. It emits PurchasePaid and no PurchaseInitiated.
func RecordManualPurchase(purchaseID, userID, courseID id.ID, courseTitle string, price Money, m ManualPayment, now time.Time) (Purchase, Event, error) {
	if price.AmountMinor <= 0 {
		return Purchase{}, nil, ErrFreePrice
	}
	ref, note := strings.TrimSpace(m.Reference), strings.TrimSpace(m.Note)
	if price.Currency == "" || !validMethod(m.Method) || ref == "" || len(ref) > maxReferenceLen ||
		len(note) > maxNoteLen || m.RecordedBy == 0 {
		return Purchase{}, nil, ErrInvalidPurchase
	}
	p := Purchase{
		ID: purchaseID, UserID: userID, CourseID: courseID, CourseTitle: courseTitle, Price: price,
		Gateway: GatewayManual, GatewayRef: purchaseID.String(), GatewayTxn: ref, Status: StatusPaid,
		CreatedAt: now, SettledAt: now, ManualMethod: m.Method, RecordedBy: m.RecordedBy, Note: note,
	}
	return p, p.paidEvent(now), nil
}

func validMethod(m string) bool {
	return m == MethodBankTransfer || m == MethodCash || m == MethodOther
}

func (p *Purchase) paidEvent(now time.Time) PurchasePaid {
	return PurchasePaid{
		PurchaseID: p.ID, UserID: p.UserID, CourseID: p.CourseID, CourseTitle: p.CourseTitle,
		AmountMinor: p.Price.AmountMinor, Currency: p.Price.Currency, Gateway: p.Gateway,
		GatewayTxn: p.GatewayTxn, ManualMethod: p.ManualMethod, OccurredAt: now,
	}
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
	return p.paidEvent(now), nil
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
