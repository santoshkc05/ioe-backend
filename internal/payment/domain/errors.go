// Package domain holds the payment model.
package domain

import "errors"

var (
	ErrFreePrice       = errors.New("price must be positive")
	ErrInvalidPurchase = errors.New("invalid purchase")
	ErrNotRefundable   = errors.New("only a paid purchase can be refunded")
)
