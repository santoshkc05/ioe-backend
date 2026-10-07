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
	}
	if !p.SettledAt.IsZero() {
		at := p.SettledAt
		w.SettledAt = &at
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
