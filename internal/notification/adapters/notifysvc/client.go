// Package notifysvc enqueues email through the standalone notification service.
package notifysvc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/santoshkc2200/ioe-backend/internal/notification/app"
)

const (
	requestTimeout = 10 * time.Second
	maxBodyBytes   = 64 << 10
)

// Client calls POST /v1/notifications with a send key.
type Client struct {
	endpoint string
	apiKey   string
	http     *http.Client
}

// New returns a client for the service at baseURL, which may include a path prefix.
func New(baseURL, apiKey string) *Client {
	return &Client{
		endpoint: strings.TrimRight(baseURL, "/") + "/v1/notifications",
		apiKey:   apiKey,
		http:     &http.Client{Timeout: requestTimeout, Transport: otelhttp.NewTransport(http.DefaultTransport)},
	}
}

type enqueueRequest struct {
	Recipient string `json:"recipient"`
	Subject   string `json:"subject"`
	TextBody  string `json:"text_body"`
	HTMLBody  string `json:"html_body,omitempty"`
}

// Enqueue submits email under idempotencyKey. Errors never include the key, recipient,
// or bodies; permanent failures wrap app.ErrPermanent.
func (c *Client) Enqueue(ctx context.Context, email app.Email, idempotencyKey string) error {
	body, err := json.Marshal(enqueueRequest{Recipient: email.To, Subject: email.Subject, TextBody: email.Text, HTMLBody: email.HTML})
	if err != nil {
		return fmt.Errorf("%w: encode request: %w", app.ErrPermanent, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("%w: build request: %w", app.ErrPermanent, err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", idempotencyKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("notification service request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusAccepted {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxBodyBytes))
		return nil
	}
	failure := fmt.Errorf("notification service responded %d (type %q, request %q)",
		resp.StatusCode, problemType(resp.Body), resp.Header.Get("X-Request-ID"))
	switch {
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= http.StatusInternalServerError:
		return failure
	case resp.StatusCode >= http.StatusBadRequest:
		return fmt.Errorf("%w: %w", app.ErrPermanent, failure)
	default:
		return failure
	}
}

// problemType returns the problem+json "type" field, or "" for any other body.
func problemType(body io.Reader) string {
	var problem struct {
		Type string `json:"type"`
	}
	if err := json.NewDecoder(io.LimitReader(body, maxBodyBytes)).Decode(&problem); err != nil {
		return ""
	}
	return problem.Type
}
