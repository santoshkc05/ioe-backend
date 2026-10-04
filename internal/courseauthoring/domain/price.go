package domain

// CurrencyNPR is the only supported currency.
const CurrencyNPR = "NPR"

// Price is an amount in paisa. A free price has AmountMinor 0 and Currency "".
type Price struct {
	AmountMinor int64
	Currency    string
}

// NewPrice validates a price. Any currency is normalized away for a zero amount.
func NewPrice(amountMinor int64, currency string) (Price, error) {
	switch {
	case amountMinor < 0:
		return Price{}, ErrInvalidPrice
	case amountMinor == 0:
		return Price{}, nil
	case currency != CurrencyNPR:
		return Price{}, ErrUnsupportedCurrency
	default:
		return Price{AmountMinor: amountMinor, Currency: CurrencyNPR}, nil
	}
}

// IsFree reports whether the course costs nothing.
func (p Price) IsFree() bool { return p.AmountMinor == 0 }
