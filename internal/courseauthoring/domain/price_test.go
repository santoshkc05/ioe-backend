package domain_test

import (
	"errors"
	"testing"

	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/domain"
)

func TestNewPrice(t *testing.T) {
	cases := []struct {
		amount   int64
		currency string
		want     domain.Price
		err      error
	}{
		{0, "", domain.Price{}, nil},
		{0, "NPR", domain.Price{}, nil},
		{0, "USD", domain.Price{}, nil},
		{150000, "NPR", domain.Price{AmountMinor: 150000, Currency: "NPR"}, nil},
		{150000, "npr", domain.Price{}, domain.ErrUnsupportedCurrency},
		{150000, "USD", domain.Price{}, domain.ErrUnsupportedCurrency},
		{150000, "", domain.Price{}, domain.ErrUnsupportedCurrency},
		{-1, "NPR", domain.Price{}, domain.ErrInvalidPrice},
	}
	for _, c := range cases {
		got, err := domain.NewPrice(c.amount, c.currency)
		if !errors.Is(err, c.err) || got != c.want {
			t.Fatalf("NewPrice(%d, %q) = %+v, %v; want %+v, %v", c.amount, c.currency, got, err, c.want, c.err)
		}
	}
	if !(domain.Price{}).IsFree() || (domain.Price{AmountMinor: 1, Currency: "NPR"}).IsFree() {
		t.Fatal("IsFree wrong")
	}
}
