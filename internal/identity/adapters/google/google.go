// Package google verifies Google ID tokens against Google's published signing keys.
package google

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/MicahParks/keyfunc/v3"
	gojwt "github.com/golang-jwt/jwt/v5"

	"github.com/santoshkc2200/ioe-backend/internal/identity/app"
	"github.com/santoshkc2200/ioe-backend/internal/identity/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/clock"
)

// DefaultJWKSURL serves Google's OAuth 2.0 signing keys.
const DefaultJWKSURL = "https://www.googleapis.com/oauth2/v3/certs"

const clockSkew = 30 * time.Second

var validIssuers = []string{"accounts.google.com", "https://accounts.google.com"}

// Verifier validates Google ID tokens.
type Verifier struct {
	keyfunc   gojwt.Keyfunc
	audiences []string
	clock     clock.Clock
}

// NewVerifier caches the JWKS at jwksURL and refreshes it in the background until ctx ends.
func NewVerifier(ctx context.Context, jwksURL string, audiences []string, c clock.Clock) (*Verifier, error) {
	kf, err := keyfunc.NewDefaultCtx(ctx, []string{jwksURL})
	if err != nil {
		return nil, fmt.Errorf("google jwks: %w", err)
	}
	return &Verifier{keyfunc: kf.Keyfunc, audiences: audiences, clock: c}, nil
}

// flexBool accepts both JSON booleans and the strings "true"/"false" Google has used.
type flexBool bool

func (b *flexBool) UnmarshalJSON(data []byte) error {
	switch string(data) {
	case "true", `"true"`:
		*b = true
	case "false", `"false"`, "null":
		*b = false
	default:
		return fmt.Errorf("invalid boolean %s", data)
	}
	return nil
}

type claims struct {
	Email         string   `json:"email"`
	EmailVerified flexBool `json:"email_verified"`
	Name          string   `json:"name"`
	Picture       string   `json:"picture"`
	gojwt.RegisteredClaims
}

// Verify checks signature, issuer, audience, and expiry. Failures wrap app.ErrInvalidToken.
// An unverified email is returned as data; the use case decides how to treat it.
func (v *Verifier) Verify(_ context.Context, raw string) (domain.GoogleIdentity, error) {
	var c claims
	_, err := gojwt.ParseWithClaims(raw, &c, v.keyfunc,
		gojwt.WithValidMethods([]string{gojwt.SigningMethodRS256.Alg()}),
		gojwt.WithExpirationRequired(),
		gojwt.WithTimeFunc(v.clock.Now),
		gojwt.WithLeeway(clockSkew),
	)
	if err != nil {
		return domain.GoogleIdentity{}, fmt.Errorf("%w: %w", app.ErrInvalidToken, err)
	}
	if !slices.Contains(validIssuers, c.Issuer) {
		return domain.GoogleIdentity{}, fmt.Errorf("%w: issuer", app.ErrInvalidToken)
	}
	if !slices.ContainsFunc(c.Audience, func(a string) bool { return slices.Contains(v.audiences, a) }) {
		return domain.GoogleIdentity{}, fmt.Errorf("%w: audience", app.ErrInvalidToken)
	}
	if c.Subject == "" || c.Email == "" {
		return domain.GoogleIdentity{}, fmt.Errorf("%w: %w", app.ErrInvalidToken, errors.New("missing sub or email"))
	}
	return domain.GoogleIdentity{
		Subject:       c.Subject,
		Email:         c.Email,
		EmailVerified: bool(c.EmailVerified),
		Name:          c.Name,
		Picture:       c.Picture,
	}, nil
}
