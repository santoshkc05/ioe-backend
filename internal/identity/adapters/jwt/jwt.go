// Package jwt issues and verifies the API's EdDSA access tokens.
package jwt

import (
	"errors"
	"fmt"
	"time"

	gojwt "github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/santoshkc2200/ioe-backend/internal/identity/app"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/clock"
)

// TTL is the access-token lifetime.
const TTL = 15 * time.Minute

type claims struct {
	Role string `json:"role"`
	gojwt.RegisteredClaims
}

// Tokens issues and verifies access tokens.
type Tokens struct {
	keys     Keys
	issuer   string
	audience string
	clock    clock.Clock
}

func New(keys Keys, issuer, audience string, c clock.Clock) *Tokens {
	return &Tokens{keys: keys, issuer: issuer, audience: audience, clock: c}
}

// Issue signs an access token for the user.
func (t *Tokens) Issue(userID uuid.UUID, role auth.Role) (string, time.Duration, error) {
	now := t.clock.Now()
	tok := gojwt.NewWithClaims(gojwt.SigningMethodEdDSA, claims{
		Role: string(role),
		RegisteredClaims: gojwt.RegisteredClaims{
			Issuer:    t.issuer,
			Audience:  gojwt.ClaimStrings{t.audience},
			Subject:   userID.String(),
			IssuedAt:  gojwt.NewNumericDate(now),
			ExpiresAt: gojwt.NewNumericDate(now.Add(TTL)),
			ID:        uuid.NewString(),
		},
	})
	tok.Header["kid"] = t.keys.SigningKID
	s, err := tok.SignedString(t.keys.Signing)
	if err != nil {
		return "", 0, err
	}
	return s, TTL, nil
}

// Verify validates an access token. Every failure wraps app.ErrInvalidToken.
func (t *Tokens) Verify(raw string) (auth.Principal, error) {
	var c claims
	_, err := gojwt.ParseWithClaims(raw, &c, t.keyFor,
		gojwt.WithValidMethods([]string{gojwt.SigningMethodEdDSA.Alg()}),
		gojwt.WithIssuer(t.issuer),
		gojwt.WithAudience(t.audience),
		gojwt.WithExpirationRequired(),
		gojwt.WithIssuedAt(),
		gojwt.WithTimeFunc(t.clock.Now),
	)
	if err != nil {
		return auth.Principal{}, fmt.Errorf("%w: %w", app.ErrInvalidToken, err)
	}
	id, err := uuid.Parse(c.Subject)
	if err != nil {
		return auth.Principal{}, fmt.Errorf("%w: subject: %w", app.ErrInvalidToken, err)
	}
	role, err := auth.ParseRole(c.Role)
	if err != nil {
		return auth.Principal{}, fmt.Errorf("%w: %w", app.ErrInvalidToken, err)
	}
	return auth.Principal{UserID: id, Role: role}, nil
}

func (t *Tokens) keyFor(tok *gojwt.Token) (any, error) {
	kid, _ := tok.Header["kid"].(string)
	key, ok := t.keys.Verify[kid]
	if !ok {
		return nil, errors.New("unknown kid")
	}
	return key, nil
}
