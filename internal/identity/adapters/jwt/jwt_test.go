package jwt_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"strings"
	"testing"
	"time"

	gojwt "github.com/golang-jwt/jwt/v5"

	"github.com/santoshkc2200/ioe-backend/internal/identity/adapters/jwt"
	"github.com/santoshkc2200/ioe-backend/internal/identity/app"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/clock"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

const (
	issuer   = "https://api.test"
	audience = "ioe"
)

func genKey(t *testing.T) (priv ed25519.PrivateKey, privPEM, pubPEM string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	p8, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	pk, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return priv,
		string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: p8})),
		string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pk}))
}

func newTokens(t *testing.T, c clock.Clock) (*jwt.Tokens, jwt.Keys) {
	t.Helper()
	_, privPEM, _ := genKey(t)
	keys, err := jwt.ParseKeys(privPEM, "k1", "")
	if err != nil {
		t.Fatal(err)
	}
	return jwt.New(keys, issuer, audience, c), keys
}

func TestIssueVerifyRoundTrip(t *testing.T) {
	tokens, _ := newTokens(t, clock.System{})
	uid := id.ID(1840396745219883008)
	tok, ttl, err := tokens.Issue(uid, auth.RoleInstructor)
	if err != nil || ttl != 15*time.Minute {
		t.Fatalf("ttl=%v err=%v", ttl, err)
	}
	header, err := base64.RawURLEncoding.DecodeString(strings.Split(tok, ".")[0])
	if err != nil || !strings.Contains(string(header), `"kid":"k1"`) || !strings.Contains(string(header), `"alg":"EdDSA"`) {
		t.Fatalf("header %s", header)
	}
	p, err := tokens.Verify(tok)
	if err != nil || p != (auth.Principal{UserID: uid, Role: auth.RoleInstructor}) {
		t.Fatalf("%+v %v", p, err)
	}
}

func TestVerifyRejectsNonSnowflakeSubject(t *testing.T) {
	tokens, keys := newTokens(t, clock.System{})
	raw := signWithSubject(t, keys, "01920000-0000-7000-8000-000000000001")
	if _, err := tokens.Verify(raw); !errors.Is(err, app.ErrInvalidToken) {
		t.Fatalf("err = %v, want ErrInvalidToken", err)
	}
}

func TestIssueUsesDecimalSubject(t *testing.T) {
	tokens, keys := newTokens(t, clock.System{})
	raw, _, err := tokens.Issue(id.ID(1840396745219883008), auth.RoleInstructor)
	if err != nil {
		t.Fatal(err)
	}
	// The subject claim must be the decimal string, not a UUID.
	parser := gojwt.NewParser()
	var mc gojwt.MapClaims
	if _, _, err := parser.ParseUnverified(raw, &mc); err != nil {
		t.Fatal(err)
	}
	if mc["sub"] != "1840396745219883008" {
		t.Fatalf("sub = %v", mc["sub"])
	}
	_ = keys
	p, err := tokens.Verify(raw)
	if err != nil || p.UserID != 1840396745219883008 || p.Role != auth.RoleInstructor {
		t.Fatalf("principal = %+v, %v", p, err)
	}
}

func signWithSubject(t *testing.T, keys jwt.Keys, sub string) string {
	t.Helper()
	now := clock.System{}.Now()
	claims := gojwt.MapClaims{
		"iss":  issuer,
		"aud":  audience,
		"sub":  sub,
		"role": "student",
		"iat":  now.Unix(),
		"exp":  now.Add(time.Minute).Unix(),
		"jti":  "j",
	}
	signed := gojwt.NewWithClaims(gojwt.SigningMethodEdDSA, claims)
	signed.Header["kid"] = "k1"
	s, err := signed.SignedString(keys.Signing)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestVerifyRejects(t *testing.T) {
	c := clock.NewFake(time.Now().UTC())
	tokens, keys := newTokens(t, c)
	uid := id.ID(1840396745219883008)
	sign := func(m gojwt.SigningMethod, key any, kid string, claims gojwt.MapClaims) string {
		tok := gojwt.NewWithClaims(m, claims)
		tok.Header["kid"] = kid
		s, err := tok.SignedString(key)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	valid := func() gojwt.MapClaims {
		return gojwt.MapClaims{"iss": issuer, "aud": audience, "sub": uid.String(), "role": "student",
			"iat": c.Now().Unix(), "exp": c.Now().Add(time.Minute).Unix(), "jti": "j"}
	}
	otherPriv, _, _ := genKey(t)

	cases := map[string]func() string{
		"wrong audience": func() string {
			cl := valid()
			cl["aud"] = "other"
			return sign(gojwt.SigningMethodEdDSA, keys.Signing, "k1", cl)
		},
		"wrong issuer": func() string {
			cl := valid()
			cl["iss"] = "https://evil"
			return sign(gojwt.SigningMethodEdDSA, keys.Signing, "k1", cl)
		},
		"unknown kid": func() string { return sign(gojwt.SigningMethodEdDSA, otherPriv, "k2", valid()) },
		"wrong key":   func() string { return sign(gojwt.SigningMethodEdDSA, otherPriv, "k1", valid()) },
		"bad role": func() string {
			cl := valid()
			cl["role"] = "god"
			return sign(gojwt.SigningMethodEdDSA, keys.Signing, "k1", cl)
		},
		"missing exp": func() string {
			cl := valid()
			delete(cl, "exp")
			return sign(gojwt.SigningMethodEdDSA, keys.Signing, "k1", cl)
		},
		"alg none": func() string {
			return sign(gojwt.SigningMethodNone, gojwt.UnsafeAllowNoneSignatureType, "k1", valid())
		},
		"hmac confusion": func() string {
			return sign(gojwt.SigningMethodHS256, []byte(keys.Verify["k1"]), "k1", valid())
		},
		"tampered": func() string {
			tok := sign(gojwt.SigningMethodEdDSA, keys.Signing, "k1", valid())
			parts := strings.Split(tok, ".")
			// Flip the first payload character (always real data bits, never
			// padding) so the decoded claims always change and the signature
			// can no longer match. Mutating trailing signature characters is
			// not deterministic: if those bits are already zero the token is
			// unchanged and still verifies.
			p := []byte(parts[1])
			if p[0] == 'A' {
				p[0] = 'B'
			} else {
				p[0] = 'A'
			}
			parts[1] = string(p)
			return strings.Join(parts, ".")
		},
	}
	for name, mk := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := tokens.Verify(mk()); !errors.Is(err, app.ErrInvalidToken) {
				t.Fatalf("err = %v", err)
			}
		})
	}

	t.Run("expired", func(t *testing.T) {
		tok, _, err := tokens.Issue(uid, auth.RoleStudent)
		if err != nil {
			t.Fatal(err)
		}
		c.Advance(16 * time.Minute)
		if _, err := tokens.Verify(tok); !errors.Is(err, app.ErrInvalidToken) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestVerifyAcceptsRotatedKey(t *testing.T) {
	_, oldPriv, oldPub := genKey(t)
	_, newPriv, _ := genKey(t)
	oldKeys, err := jwt.ParseKeys(oldPriv, "old", "")
	if err != nil {
		t.Fatal(err)
	}
	tok, _, err := jwt.New(oldKeys, issuer, audience, clock.System{}).Issue(id.ID(1840396745219883008), auth.RoleStudent)
	if err != nil {
		t.Fatal(err)
	}
	newKeys, err := jwt.ParseKeys(newPriv, "new", "old="+strings.ReplaceAll(oldPub, "\n", `\n`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := jwt.New(newKeys, issuer, audience, clock.System{}).Verify(tok); err != nil {
		t.Fatalf("rotated key rejected: %v", err)
	}
}

func TestParseKeys(t *testing.T) {
	_, privPEM, pubPEM := genKey(t)
	if _, err := jwt.ParseKeys(strings.ReplaceAll(privPEM, "\n", `\n`), "k1", ""); err != nil {
		t.Fatalf("escaped newlines: %v", err)
	}
	bad := map[string][3]string{
		"not pem":           {"nope", "k1", ""},
		"public as private": {pubPEM, "k1", ""},
		"empty kid":         {privPEM, "", ""},
		"entry without =":   {privPEM, "k1", "k2"},
		"duplicate kid":     {privPEM, "k1", "k1=" + pubPEM},
		"bad verify pem":    {privPEM, "k1", "k2=nope"},
	}
	for name, in := range bad {
		if _, err := jwt.ParseKeys(in[0], in[1], in[2]); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}
