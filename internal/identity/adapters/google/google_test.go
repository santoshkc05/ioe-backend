package google_test

import (
	"context"
	"errors"
	"testing"
	"time"

	gojwt "github.com/golang-jwt/jwt/v5"

	"github.com/santoshkc2200/ioe-backend/internal/identity/adapters/google"
	"github.com/santoshkc2200/ioe-backend/internal/identity/adapters/google/googletest"
	"github.com/santoshkc2200/ioe-backend/internal/identity/app"
	"github.com/santoshkc2200/ioe-backend/internal/platform/clock"
)

const aud = "web.apps.googleusercontent.com"

var now = time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)

func setup(t *testing.T, audiences ...string) (*googletest.Issuer, *google.Verifier) {
	t.Helper()
	iss := googletest.NewIssuer(t)
	if len(audiences) == 0 {
		audiences = []string{aud}
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	v, err := google.NewVerifier(ctx, iss.JWKSURL(), audiences, clock.NewFake(now))
	if err != nil {
		t.Fatal(err)
	}
	return iss, v
}

func TestVerifyValid(t *testing.T) {
	iss, v := setup(t)
	id, err := v.Verify(context.Background(), iss.Sign(t, googletest.Claims("sub-1", "a@example.com", aud, now)))
	if err != nil {
		t.Fatal(err)
	}
	if id.Subject != "sub-1" || id.Email != "a@example.com" || !id.EmailVerified || id.Name != "Test User" || id.Picture == "" {
		t.Fatalf("%+v", id)
	}
}

func TestVerifyAcceptedVariants(t *testing.T) {
	iss, v := setup(t, aud, "admin.apps.googleusercontent.com")
	variants := map[string]func(gojwt.MapClaims){
		"bare issuer":           func(c gojwt.MapClaims) { c["iss"] = "accounts.google.com" },
		"second audience":       func(c gojwt.MapClaims) { c["aud"] = "admin.apps.googleusercontent.com" },
		"email_verified string": func(c gojwt.MapClaims) { c["email_verified"] = "true" },
	}
	for name, mutate := range variants {
		c := googletest.Claims("sub-1", "a@example.com", aud, now)
		mutate(c)
		if id, err := v.Verify(context.Background(), iss.Sign(t, c)); err != nil || !id.EmailVerified {
			t.Errorf("%s: %+v %v", name, id, err)
		}
	}
}

func TestVerifyPassesUnverifiedEmailThrough(t *testing.T) {
	iss, v := setup(t)
	c := googletest.Claims("sub-1", "a@example.com", aud, now)
	c["email_verified"] = false
	id, err := v.Verify(context.Background(), iss.Sign(t, c))
	if err != nil || id.EmailVerified {
		t.Fatalf("%+v %v", id, err)
	}
}

func TestVerifyRejects(t *testing.T) {
	iss, v := setup(t)
	cases := map[string]func() string{
		"wrong audience": func() string {
			return iss.Sign(t, googletest.Claims("s", "a@example.com", "other", now))
		},
		"wrong issuer": func() string {
			c := googletest.Claims("s", "a@example.com", aud, now)
			c["iss"] = "https://evil.example"
			return iss.Sign(t, c)
		},
		"expired": func() string {
			return iss.Sign(t, googletest.Claims("s", "a@example.com", aud, now.Add(-2*time.Hour)))
		},
		"forged signature": func() string {
			return iss.Forge(t, googletest.Claims("s", "a@example.com", aud, now))
		},
		"hmac": func() string {
			s, err := gojwt.NewWithClaims(gojwt.SigningMethodHS256, googletest.Claims("s", "a@example.com", aud, now)).SignedString([]byte("k"))
			if err != nil {
				t.Fatal(err)
			}
			return s
		},
		"missing subject": func() string {
			c := googletest.Claims("s", "a@example.com", aud, now)
			delete(c, "sub")
			return iss.Sign(t, c)
		},
		"missing email": func() string {
			c := googletest.Claims("s", "a@example.com", aud, now)
			delete(c, "email")
			return iss.Sign(t, c)
		},
		"garbage": func() string { return "not.a.jwt" },
	}
	for name, mk := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := v.Verify(context.Background(), mk()); !errors.Is(err, app.ErrInvalidToken) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}
