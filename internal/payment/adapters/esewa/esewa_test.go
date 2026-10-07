package esewa_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/santoshkc2200/ioe-backend/internal/payment/adapters/esewa"
	"github.com/santoshkc2200/ioe-backend/internal/payment/app"
	"github.com/santoshkc2200/ioe-backend/internal/payment/domain"
)

// sandboxKey is eSewa's published ePay sandbox secret for product code EPAYTEST.
const sandboxKey = "8gBm/:&EnhH.1/q"

var ctx = context.Background()

func gateway(statusURL string, logs io.Writer) *esewa.Gateway {
	return esewa.New(esewa.Config{
		ProductCode: "EPAYTEST",
		SecretKey:   sandboxKey,
		FormURL:     "https://rc-epay.esewa.com.np/api/epay/main/v2/form",
		StatusURL:   statusURL,
		ReturnURL:   "https://app.test/",
	}, slog.New(slog.NewTextHandler(logs, nil)))
}

func purchase(amountMinor int64, ref string) domain.Purchase {
	return domain.Purchase{ID: 42, UserID: 200, CourseID: 10, Price: domain.Money{AmountMinor: amountMinor, Currency: "NPR"},
		Gateway: "esewa", GatewayRef: ref, Status: domain.StatusPending}
}

func TestStartCheckoutMatchesDocumentedSignature(t *testing.T) {
	co, err := gateway("https://unused.test/", io.Discard).StartCheckout(ctx, purchase(10000, "11-201-13"))
	if err != nil {
		t.Fatal(err)
	}
	if co.Method != http.MethodPost || co.URL != "https://rc-epay.esewa.com.np/api/epay/main/v2/form" {
		t.Fatalf("checkout = %+v", co)
	}
	want := map[string]string{
		"amount":                  "100",
		"tax_amount":              "0",
		"product_service_charge":  "0",
		"product_delivery_charge": "0",
		"total_amount":            "100",
		"transaction_uuid":        "11-201-13",
		"product_code":            "EPAYTEST",
		"success_url":             "https://app.test/payments/42/return",
		"failure_url":             "https://app.test/payments/42/return",
		"signed_field_names":      "total_amount,transaction_uuid,product_code",
		"signature":               "5DZywcrTKD0gia/rsSMcrRHmJl+4Tbol6S+lWgdJ94E=",
	}
	if len(co.Fields) != len(want) {
		t.Fatalf("fields = %v", co.Fields)
	}
	for k, v := range want {
		if co.Fields[k] != v {
			t.Fatalf("%s = %q, want %q", k, co.Fields[k], v)
		}
	}
}

func TestStartCheckoutFormatsRupees(t *testing.T) {
	cases := map[int64]string{1: "0.01", 12505: "125.05", 12550: "125.50", 150000: "1500"}
	for paisa, want := range cases {
		co, err := gateway("https://unused.test/", io.Discard).StartCheckout(ctx, purchase(paisa, "1"))
		if err != nil || co.Fields["total_amount"] != want || co.Fields["amount"] != want {
			t.Fatalf("%d paisa: fields=%v err=%v", paisa, co.Fields, err)
		}
	}
}

func TestStartCheckoutRejectsOtherCurrencies(t *testing.T) {
	p := purchase(100, "1")
	p.Price.Currency = "USD"
	if _, err := gateway("https://unused.test/", io.Discard).StartCheckout(ctx, p); err == nil {
		t.Fatal("USD accepted")
	}
}

// statusServer answers every status request with body and records the query.
func statusServer(t *testing.T, code int, body string, query *url.Values) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if query != nil {
			*query = r.URL.Query()
		}
		if r.Method != http.MethodGet || r.URL.Path != "/api/epay/transaction/status/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/api/epay/transaction/status/"
}

func TestFetchStatusComplete(t *testing.T) {
	var q url.Values
	u := statusServer(t, http.StatusOK,
		`{"product_code":"EPAYTEST","transaction_uuid":"42","total_amount":125.5,"status":"COMPLETE","ref_id":"0001TS9"}`, &q)
	res, err := gateway(u, io.Discard).FetchStatus(ctx, purchase(12550, "42"))
	if err != nil || res != (app.Result{Kind: app.ResultComplete, Txn: "0001TS9"}) {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if q.Get("product_code") != "EPAYTEST" || q.Get("total_amount") != "125.50" || q.Get("transaction_uuid") != "42" {
		t.Fatalf("query = %v", q)
	}
}

func TestFetchStatusMapping(t *testing.T) {
	cases := map[string]app.ResultKind{
		"PENDING":   app.ResultPending,
		"AMBIGUOUS": app.ResultPending,
		"NOT_FOUND": app.ResultFailed,
		"CANCELED":  app.ResultFailed,
	}
	for status, want := range cases {
		// eSewa may omit or null the echo fields for unsettled transactions.
		u := statusServer(t, http.StatusOK, `{"product_code":null,"transaction_uuid":null,"total_amount":null,"status":"`+status+`","ref_id":null}`, nil)
		res, err := gateway(u, io.Discard).FetchStatus(ctx, purchase(10000, "42"))
		if err != nil || res.Kind != want {
			t.Fatalf("%s: res=%+v err=%v", status, res, err)
		}
	}
}

func TestFetchStatusRefundIsLoggedAndPending(t *testing.T) {
	var logs bytes.Buffer
	u := statusServer(t, http.StatusOK, `{"product_code":"EPAYTEST","transaction_uuid":"42","total_amount":100.0,"status":"FULL_REFUND","ref_id":"R"}`, nil)
	res, err := gateway(u, &logs).FetchStatus(ctx, purchase(10000, "42"))
	if err != nil || res.Kind != app.ResultPending {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if !strings.Contains(logs.String(), "FULL_REFUND") {
		t.Fatalf("logs = %s", logs.String())
	}
}

func TestFetchStatusRejectsMismatch(t *testing.T) {
	bodies := map[string]string{
		"amount":       `{"product_code":"EPAYTEST","transaction_uuid":"42","total_amount":1.0,"status":"COMPLETE","ref_id":"T"}`,
		"reference":    `{"product_code":"EPAYTEST","transaction_uuid":"43","total_amount":100.0,"status":"COMPLETE","ref_id":"T"}`,
		"product":      `{"product_code":"OTHER","transaction_uuid":"42","total_amount":100.0,"status":"COMPLETE","ref_id":"T"}`,
		"missing txn":  `{"product_code":"EPAYTEST","transaction_uuid":"42","total_amount":100.0,"status":"COMPLETE","ref_id":""}`,
		"float amount": `{"product_code":"EPAYTEST","transaction_uuid":"42","total_amount":100.001,"status":"COMPLETE","ref_id":"T"}`,
	}
	for name, body := range bodies {
		u := statusServer(t, http.StatusOK, body, nil)
		if res, err := gateway(u, io.Discard).FetchStatus(ctx, purchase(10000, "42")); err == nil {
			t.Fatalf("%s: accepted %+v", name, res)
		}
	}
}

func TestFetchStatusErrors(t *testing.T) {
	cases := map[string]string{
		"non-200":   statusServer(t, http.StatusBadGateway, `{}`, nil),
		"malformed": statusServer(t, http.StatusOK, `not json`, nil),
		"unknown":   statusServer(t, http.StatusOK, `{"status":"SOMETHING_NEW"}`, nil),
	}
	for name, u := range cases {
		if res, err := gateway(u, io.Discard).FetchStatus(ctx, purchase(10000, "42")); err == nil {
			t.Fatalf("%s: res=%+v", name, res)
		}
	}
	if _, err := gateway("http://127.0.0.1:1/status/", io.Discard).FetchStatus(ctx, purchase(10000, "42")); err == nil {
		t.Fatal("unreachable server accepted")
	}
}

func TestFetchStatusErrorsNeverContainTheSecret(t *testing.T) {
	u := statusServer(t, http.StatusInternalServerError, `{}`, nil)
	_, err := gateway(u, io.Discard).FetchStatus(ctx, purchase(10000, "42"))
	if err == nil || strings.Contains(err.Error(), sandboxKey) {
		t.Fatalf("err = %v", err)
	}
}
