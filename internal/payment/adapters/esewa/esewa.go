// Package esewa implements the eSewa ePay v2 gateway. eSewa never calls the server, so the
// status API is the only source of truth; the browser redirect is ignored.
package esewa

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/santoshkc2200/ioe-backend/internal/payment/app"
	"github.com/santoshkc2200/ioe-backend/internal/payment/domain"
)

// Name is the gateway name stored on purchases and sent by clients.
const Name = "esewa"

const (
	requestTimeout = 10 * time.Second
	maxBodyBytes   = 1 << 16
	signedFields   = "total_amount,transaction_uuid,product_code"
	currency       = "NPR"
)

// Config holds the merchant settings. ReturnURL is the frontend origin: eSewa sends the buyer
// back to {ReturnURL}/payments/{purchaseID}/return after success or failure.
type Config struct {
	ProductCode string
	SecretKey   string
	FormURL     string
	StatusURL   string
	ReturnURL   string
}

// Gateway implements app.Gateway. Errors never include the secret key.
type Gateway struct {
	cfg    Config
	http   *http.Client
	logger *slog.Logger
}

var _ app.Gateway = (*Gateway)(nil)

func New(cfg Config, logger *slog.Logger) *Gateway {
	cfg.ReturnURL = strings.TrimRight(cfg.ReturnURL, "/")
	return &Gateway{
		cfg:    cfg,
		http:   &http.Client{Timeout: requestTimeout, Transport: otelhttp.NewTransport(http.DefaultTransport)},
		logger: logger,
	}
}

// StartCheckout returns the signed form the browser posts to eSewa.
func (g *Gateway) StartCheckout(_ context.Context, p domain.Purchase) (app.Checkout, error) {
	total, err := rupees(p.Price)
	if err != nil {
		return app.Checkout{}, err
	}
	ret := g.cfg.ReturnURL + "/payments/" + p.ID.String() + "/return"
	return app.Checkout{Method: http.MethodPost, URL: g.cfg.FormURL, Fields: map[string]string{
		"amount":                  total,
		"tax_amount":              "0",
		"product_service_charge":  "0",
		"product_delivery_charge": "0",
		"total_amount":            total,
		"transaction_uuid":        p.GatewayRef,
		"product_code":            g.cfg.ProductCode,
		"success_url":             ret,
		"failure_url":             ret,
		"signed_field_names":      signedFields,
		"signature":               g.sign(total, p.GatewayRef),
	}}, nil
}

func (g *Gateway) sign(total, transactionUUID string) string {
	mac := hmac.New(sha256.New, []byte(g.cfg.SecretKey))
	_, _ = mac.Write([]byte("total_amount=" + total + ",transaction_uuid=" + transactionUUID + ",product_code=" + g.cfg.ProductCode))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

type statusResponse struct {
	ProductCode     string      `json:"product_code"`
	TransactionUUID string      `json:"transaction_uuid"`
	TotalAmount     json.Number `json:"total_amount"`
	Status          string      `json:"status"`
	RefID           string      `json:"ref_id"`
}

// FetchStatus asks eSewa's status API about the purchase.
func (g *Gateway) FetchStatus(ctx context.Context, p domain.Purchase) (app.Result, error) {
	total, err := rupees(p.Price)
	if err != nil {
		return app.Result{}, err
	}
	u, err := url.Parse(g.cfg.StatusURL)
	if err != nil {
		return app.Result{}, fmt.Errorf("esewa status: parse url: %w", err)
	}
	q := u.Query()
	q.Set("product_code", g.cfg.ProductCode)
	q.Set("total_amount", total)
	q.Set("transaction_uuid", p.GatewayRef)
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return app.Result{}, fmt.Errorf("esewa status: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := g.http.Do(req)
	if err != nil {
		return app.Result{}, fmt.Errorf("esewa status: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return app.Result{}, fmt.Errorf("esewa status: HTTP %d", resp.StatusCode)
	}
	var body statusResponse
	dec := json.NewDecoder(io.LimitReader(resp.Body, maxBodyBytes))
	dec.UseNumber()
	if err := dec.Decode(&body); err != nil {
		return app.Result{}, fmt.Errorf("esewa status: decode: %w", err)
	}
	switch body.Status {
	case "COMPLETE":
		if err := g.matches(body, p); err != nil {
			return app.Result{}, err
		}
		return app.Result{Kind: app.ResultComplete, Txn: body.RefID}, nil
	case "PENDING", "AMBIGUOUS":
		return app.Result{Kind: app.ResultPending}, nil
	case "NOT_FOUND", "CANCELED":
		return app.Result{Kind: app.ResultFailed}, nil
	case "FULL_REFUND", "PARTIAL_REFUND":
		g.logger.WarnContext(ctx, "esewa reports a refund; access is unchanged", "purchase_id", p.ID, "status", body.Status)
		return app.Result{Kind: app.ResultPending}, nil
	default:
		return app.Result{}, fmt.Errorf("esewa status: unknown status %q", body.Status)
	}
}

// matches checks that a COMPLETE response is about this purchase and its exact amount.
func (g *Gateway) matches(body statusResponse, p domain.Purchase) error {
	amount, err := paisa(body.TotalAmount)
	if err != nil || amount != p.Price.AmountMinor || body.TransactionUUID != p.GatewayRef ||
		body.ProductCode != g.cfg.ProductCode || body.RefID == "" {
		return fmt.Errorf("esewa status: response does not match purchase %s", p.ID)
	}
	return nil
}

// rupees formats NPR paisa as eSewa expects: 10000 → "100", 12550 → "125.50".
func rupees(m domain.Money) (string, error) {
	if m.Currency != currency || m.AmountMinor <= 0 {
		return "", fmt.Errorf("esewa: unsupported amount %d %q", m.AmountMinor, m.Currency)
	}
	whole, frac := m.AmountMinor/100, m.AmountMinor%100
	if frac == 0 {
		return strconv.FormatInt(whole, 10), nil
	}
	return fmt.Sprintf("%d.%02d", whole, frac), nil
}

// paisa parses a decimal rupee amount such as "100", "100.0" or "125.5" without floating point.
func paisa(n json.Number) (int64, error) {
	whole, frac, _ := strings.Cut(n.String(), ".")
	frac = strings.TrimRight(frac, "0")
	if len(frac) > 2 {
		return 0, fmt.Errorf("esewa: amount %q has more than two decimals", n)
	}
	frac += strings.Repeat("0", 2-len(frac))
	w, err := strconv.ParseInt(whole, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("esewa: amount %q: %w", n, err)
	}
	f, err := strconv.ParseInt(frac, 10, 64)
	if err != nil || w < 0 {
		return 0, fmt.Errorf("esewa: amount %q is invalid", n)
	}
	return w*100 + f, nil
}
