// Package googletest issues Google-style RS256 ID tokens and serves their JWKS for tests.
package googletest

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	gojwt "github.com/golang-jwt/jwt/v5"
)

const kid = "test-kid"

// Issuer signs tokens with a test key and serves the matching JWKS.
type Issuer struct {
	server *httptest.Server
	key    *rsa.PrivateKey
}

func NewIssuer(t testing.TB) *Issuer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	enc := base64.RawURLEncoding.EncodeToString
	body, err := json.Marshal(map[string]any{"keys": []map[string]string{{
		"kty": "RSA", "kid": kid, "use": "sig", "alg": "RS256",
		"n": enc(key.N.Bytes()), "e": enc(big.NewInt(int64(key.E)).Bytes()),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return &Issuer{server: srv, key: key}
}

func (i *Issuer) JWKSURL() string { return i.server.URL }

// Claims returns valid Google ID-token claims.
func Claims(sub, email, aud string, now time.Time) gojwt.MapClaims {
	return gojwt.MapClaims{
		"iss": "https://accounts.google.com", "aud": aud, "sub": sub,
		"email": email, "email_verified": true, "name": "Test User", "picture": "https://example.com/p.png",
		"iat": now.Unix(), "exp": now.Add(time.Hour).Unix(),
	}
}

// Sign signs claims with the served key.
func (i *Issuer) Sign(t testing.TB, claims gojwt.MapClaims) string {
	t.Helper()
	return sign(t, i.key, claims)
}

// Forge signs claims with a different key under the same kid.
func (i *Issuer) Forge(t testing.TB, claims gojwt.MapClaims) string {
	t.Helper()
	other, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return sign(t, other, claims)
}

func sign(t testing.TB, key *rsa.PrivateKey, claims gojwt.MapClaims) string {
	tok := gojwt.NewWithClaims(gojwt.SigningMethodRS256, claims)
	tok.Header["kid"] = kid
	s, err := tok.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
