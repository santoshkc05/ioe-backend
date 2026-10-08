package httpapi

import (
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/payment/app"
	"github.com/santoshkc2200/ioe-backend/internal/payment/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

type purchaseWire struct {
	ID          id.ID      `json:"id"`
	CourseID    id.ID      `json:"course_id"`
	UserID      id.ID      `json:"user_id"`
	AmountMinor int64      `json:"amount_minor"`
	Currency    string     `json:"currency"`
	Gateway     string     `json:"gateway"`
	Status      string     `json:"status"`
	CreatedAt   time.Time  `json:"created_at"`
	SettledAt   *time.Time `json:"settled_at"` // null while pending
	Granted     bool       `json:"granted"`

	CourseTitle  string  `json:"course_title"`
	ManualMethod *string `json:"manual_method"` // null unless gateway is manual
	Reference    *string `json:"reference"`     // manual purchases only
	Note         *string `json:"note"`          // manual purchases only
	RecordedBy   *id.ID  `json:"recorded_by"`   // manual purchases only

	RefundedAt      *time.Time `json:"refunded_at"`      // null unless refunded
	RefundedBy      *id.ID     `json:"refunded_by"`      // null unless refunded
	RefundReference *string    `json:"refund_reference"` // null unless refunded
	RefundNote      *string    `json:"refund_note"`      // null unless refunded
	RevokePending   bool       `json:"revoke_pending"`   // refunded, enrollment cancel not done yet
}

type checkoutWire struct {
	Method string            `json:"method"`
	URL    string            `json:"url"`
	Fields map[string]string `json:"fields"`
}

type checkoutRequest struct {
	Gateway string `json:"gateway"`
}

type checkoutResponse struct {
	Purchase purchaseWire `json:"purchase"`
	Checkout checkoutWire `json:"checkout"`
}

type purchaseResponse struct {
	Purchase purchaseWire `json:"purchase"`
}

func toWire(p domain.Purchase) purchaseWire {
	w := purchaseWire{
		ID: p.ID, CourseID: p.CourseID, UserID: p.UserID, AmountMinor: p.Price.AmountMinor, Currency: p.Price.Currency,
		Gateway: p.Gateway, Status: string(p.Status), CreatedAt: p.CreatedAt, Granted: !p.GrantedAt.IsZero(),
		CourseTitle: p.CourseTitle, RevokePending: p.NeedsRevoke(),
	}
	if p.Gateway == domain.GatewayManual {
		method, ref, note, by := p.ManualMethod, p.GatewayTxn, p.Note, p.RecordedBy
		w.ManualMethod, w.Reference, w.Note, w.RecordedBy = &method, &ref, &note, &by
	}
	if !p.SettledAt.IsZero() {
		at := p.SettledAt
		w.SettledAt = &at
	}
	if p.Status == domain.StatusRefunded {
		at, by, ref, note := p.RefundedAt, p.RefundedBy, p.RefundReference, p.RefundNote
		w.RefundedAt, w.RefundedBy, w.RefundReference, w.RefundNote = &at, &by, &ref, &note
	}
	return w
}

func toCheckoutWire(c app.Checkout) checkoutWire {
	fields := c.Fields
	if fields == nil {
		fields = map[string]string{}
	}
	return checkoutWire{Method: c.Method, URL: c.URL, Fields: fields}
}

type refundRequest struct {
	Reference string `json:"reference"`
	Note      string `json:"note"`
}

type manualRequest struct {
	CourseID    id.ID  `json:"course_id"`
	AmountMinor int64  `json:"amount_minor"`
	Currency    string `json:"currency"`
	Method      string `json:"method"`
	Reference   string `json:"reference"`
	Note        string `json:"note"`
}

type pageWire struct {
	Items      []purchaseWire `json:"items"`
	NextCursor string         `json:"next_cursor,omitempty"`
}

func toPageWire(items []domain.Purchase, next id.ID) pageWire {
	out := pageWire{Items: make([]purchaseWire, len(items))}
	for i, p := range items {
		out.Items[i] = toWire(p)
	}
	if next != 0 {
		out.NextCursor = next.String()
	}
	return out
}
