// Package mediasvc calls the standalone media service.
package mediasvc

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

	"github.com/santoshkc2200/ioe-backend/internal/media/app"
	"github.com/santoshkc2200/ioe-backend/internal/media/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

const (
	requestTimeout = 10 * time.Second
	maxBodyBytes   = 1 << 20
	namespace      = "ioe"
)

// Client implements app.Remote. Errors never include the API key or returned URLs.
type Client struct {
	base   string
	public string
	apiKey string
	http   *http.Client
}

var _ app.Remote = (*Client)(nil)

// New returns a client that calls baseURL and prefixes relative delivery URLs with publicURL.
func New(baseURL, publicURL, apiKey string) *Client {
	return &Client{
		base:   strings.TrimRight(baseURL, "/"),
		public: strings.TrimRight(publicURL, "/"),
		apiKey: apiKey,
		http:   &http.Client{Timeout: requestTimeout, Transport: otelhttp.NewTransport(http.DefaultTransport)},
	}
}

type imageVariant struct {
	Name    string `json:"name"`
	Width   int    `json:"width"`
	Format  string `json:"format"`
	Quality int    `json:"quality"`
}

type createRequest struct {
	OwnerID       string         `json:"owner_id"`
	ExternalRef   string         `json:"external_ref"`
	Kind          string         `json:"kind"`
	Visibility    string         `json:"visibility"`
	ContentType   string         `json:"content_type"`
	Filename      string         `json:"filename"`
	SizeBytes     int64          `json:"size_bytes"`
	ImageVariants []imageVariant `json:"image_variants,omitempty"`
}

type partWire struct {
	PartNumber int    `json:"part_number"`
	URL        string `json:"url"`
}

type uploadWire struct {
	AssetID   string     `json:"asset_id"`
	UploadID  string     `json:"upload_id"`
	UploadURL string     `json:"upload_url"`
	PartSize  int64      `json:"part_size"`
	PartURLs  []partWire `json:"part_urls"`
	ExpiresAt time.Time  `json:"expires_at"`
}

type completedPartWire struct {
	PartNumber int    `json:"part_number"`
	ETag       string `json:"etag"`
}

type assetWire struct {
	ID              string    `json:"id"`
	Status          string    `json:"status"`
	ProgressPercent int       `json:"progress_percent"`
	DurationMs      int64     `json:"duration_ms"`
	Width           int       `json:"width"`
	Height          int       `json:"height"`
	ErrorMessage    string    `json:"error_message"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type renditionWire struct {
	Name   string `json:"name"`
	URL    string `json:"url"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

type deliveryWire struct {
	URL        string          `json:"url"`
	ExpiresAt  time.Time       `json:"expires_at"`
	Renditions []renditionWire `json:"renditions"`
}

func (c *Client) Create(ctx context.Context, in app.RemoteCreate) (app.Upload, error) {
	req := createRequest{
		OwnerID: in.OwnerID.String(), ExternalRef: "course:" + in.CourseID.String(),
		Kind: string(in.Kind), Visibility: "private",
		ContentType: in.ContentType, Filename: in.Filename, SizeBytes: in.SizeBytes,
	}
	if in.Kind == domain.KindImage {
		req.ImageVariants = []imageVariant{{Name: app.DisplayVariant, Width: 1600, Format: "webp", Quality: 82}}
	}
	var out uploadWire
	if err := c.do(ctx, http.MethodPost, "/v1/assets", in.IdempotencyKey, req, &out); err != nil {
		return app.Upload{}, err
	}
	assetID, err := id.Parse(out.AssetID)
	if err != nil {
		return app.Upload{}, fmt.Errorf("%w: create returned an invalid asset id", app.ErrRemoteUnavailable)
	}
	return app.Upload{AssetID: assetID, UploadID: out.UploadID, UploadURL: out.UploadURL,
		PartSize: out.PartSize, PartURLs: toParts(out.PartURLs), ExpiresAt: out.ExpiresAt}, nil
}

func (c *Client) PresignParts(ctx context.Context, assetID id.ID, partNumbers []int) ([]app.UploadPart, error) {
	var out struct {
		PartURLs []partWire `json:"part_urls"`
	}
	in := struct {
		PartNumbers []int `json:"part_numbers"`
	}{partNumbers}
	if err := c.do(ctx, http.MethodPost, assetPath(assetID, "/parts"), "", in, &out); err != nil {
		return nil, err
	}
	return toParts(out.PartURLs), nil
}

func (c *Client) Complete(ctx context.Context, assetID id.ID, parts []app.CompletedPart) (app.RemoteAsset, error) {
	in := struct {
		Parts []completedPartWire `json:"parts,omitempty"`
	}{}
	for _, p := range parts {
		in.Parts = append(in.Parts, completedPartWire{PartNumber: p.PartNumber, ETag: p.ETag})
	}
	var out assetWire
	if err := c.do(ctx, http.MethodPost, assetPath(assetID, "/complete"), "", in, &out); err != nil {
		return app.RemoteAsset{}, err
	}
	return toAsset(out)
}

func (c *Client) Get(ctx context.Context, assetID id.ID) (app.RemoteAsset, error) {
	var out assetWire
	if err := c.do(ctx, http.MethodGet, assetPath(assetID, ""), "", nil, &out); err != nil {
		return app.RemoteAsset{}, err
	}
	return toAsset(out)
}

func (c *Client) Delete(ctx context.Context, assetID id.ID) error {
	return c.do(ctx, http.MethodDelete, assetPath(assetID, ""), "", nil, nil)
}

func (c *Client) Delivery(ctx context.Context, assetID id.ID) (app.Delivery, error) {
	var out deliveryWire
	if err := c.do(ctx, http.MethodPost, assetPath(assetID, "/delivery"), "", nil, &out); err != nil {
		return app.Delivery{}, err
	}
	d := app.Delivery{URL: c.absolute(out.URL), ExpiresAt: out.ExpiresAt, Renditions: make([]app.Rendition, len(out.Renditions))}
	for i, r := range out.Renditions {
		d.Renditions[i] = app.Rendition{Name: r.Name, URL: c.absolute(r.URL), Width: r.Width, Height: r.Height}
	}
	return d, nil
}

// absolute prefixes the public base to a path-only URL; absolute URLs pass through.
func (c *Client) absolute(u string) string {
	if strings.HasPrefix(u, "/") {
		return c.public + u
	}
	return u
}

func (c *Client) do(ctx context.Context, method, path, idempotencyKey string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("encode media request: %w", err)
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return fmt.Errorf("build media request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("X-Namespace-ID", namespace)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %s %s: %w", app.ErrRemoteUnavailable, method, path, err)
	}
	defer resp.Body.Close()
	limited := io.LimitReader(resp.Body, maxBodyBytes)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return remoteError(resp.StatusCode, limited)
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, limited)
		return nil
	}
	if err := json.NewDecoder(limited).Decode(out); err != nil {
		return fmt.Errorf("%w: decode %s %s response: %w", app.ErrRemoteUnavailable, method, path, err)
	}
	return nil
}

// remoteError maps a non-2xx response. Only a 400's detail is kept, for the caller.
func remoteError(status int, body io.Reader) error {
	var p struct {
		Code      string `json:"code"`
		Detail    string `json:"detail"`
		RequestID string `json:"request_id"`
	}
	_ = json.NewDecoder(body).Decode(&p)
	failure := fmt.Errorf("media service responded %d (code %q, request %q)", status, p.Code, p.RequestID)
	switch status {
	case http.StatusBadRequest:
		return fmt.Errorf("%w: %s", app.ErrRemoteInvalid, sanitizeDetail(p.Detail))
	case http.StatusNotFound:
		return fmt.Errorf("%w: %w", app.ErrRemoteNotFound, failure)
	case http.StatusConflict:
		return fmt.Errorf("%w: %w", app.ErrRemoteConflict, failure)
	case http.StatusRequestEntityTooLarge:
		return fmt.Errorf("%w: %w", app.ErrRemoteTooLarge, failure)
	default:
		return fmt.Errorf("%w: %w", app.ErrRemoteUnavailable, failure)
	}
}

// sanitizeDetail keeps a validation message short and drops anything that looks like a
// credential, since it is shown to the caller.
func sanitizeDetail(s string) string {
	if strings.Contains(strings.ToLower(s), "bearer") {
		return "rejected by the media service"
	}
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

func assetPath(assetID id.ID, suffix string) string {
	return "/v1/assets/" + assetID.String() + suffix
}

func toParts(in []partWire) []app.UploadPart {
	out := make([]app.UploadPart, len(in))
	for i, p := range in {
		out[i] = app.UploadPart{PartNumber: p.PartNumber, URL: p.URL}
	}
	return out
}

func toAsset(w assetWire) (app.RemoteAsset, error) {
	assetID, err := id.Parse(w.ID)
	if err != nil {
		return app.RemoteAsset{}, fmt.Errorf("%w: invalid asset id in response", app.ErrRemoteUnavailable)
	}
	return app.RemoteAsset{ID: assetID, Status: w.Status, ProgressPercent: w.ProgressPercent,
		DurationMs: w.DurationMs, Width: w.Width, Height: w.Height, ErrorMessage: w.ErrorMessage, UpdatedAt: w.UpdatedAt}, nil
}
