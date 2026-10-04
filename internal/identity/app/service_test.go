package app_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/santoshkc2200/ioe-backend/internal/identity/app"
	"github.com/santoshkc2200/ioe-backend/internal/identity/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/clock"
)

var (
	t0     = time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	ctx    = context.Background()
	client = app.Client{UserAgent: "test-agent", IP: "203.0.113.5"}
	google = fakeGoogle{
		"alice":           {Subject: "sub-alice", Email: "alice@example.com", EmailVerified: true, Name: "Alice", Picture: "https://img/alice"},
		"alice-new-email": {Subject: "sub-alice", Email: "alice@new.example", EmailVerified: true, Name: "Alice B"},
		"admin":           {Subject: "sub-admin", Email: "Admin@Example.com", EmailVerified: true},
		"unverified":      {Subject: "sub-bob", Email: "bob@example.com", EmailVerified: false},
	}
)

type fixture struct {
	store *memStore
	clock *clock.Fake
	svc   *app.Service
}

func newFixture(rootAdmins ...string) *fixture {
	f := &fixture{store: newMemStore(), clock: clock.NewFake(t0)}
	f.svc = app.NewService(f.store, google, fakeIssuer{}, f.clock, rootAdmins)
	return f
}

func (f *fixture) signIn(t *testing.T, token string) app.Session {
	t.Helper()
	s, err := f.svc.SignInWithGoogle(ctx, token, client)
	if err != nil {
		t.Fatalf("sign in %q: %v", token, err)
	}
	return s
}

func (f *fixture) refresh(t *testing.T, raw string) app.Session {
	t.Helper()
	s, err := f.svc.Refresh(ctx, raw, client)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	return s
}

func TestSignInCreatesStudentAndPublishesEvent(t *testing.T) {
	f := newFixture()
	s := f.signIn(t, "alice")
	if !s.Created || s.User.Role != auth.RoleStudent || s.User.Email != "alice@example.com" {
		t.Fatalf("%+v", s)
	}
	if s.AccessToken != "access:"+s.User.ID.String()+":student" || s.AccessTokenTTL != 15*time.Minute {
		t.Fatalf("access %q %v", s.AccessToken, s.AccessTokenTTL)
	}
	if s.RefreshToken == "" || s.RefreshTokenTTL != domain.RefreshIdleLifetime {
		t.Fatalf("refresh %q %v", s.RefreshToken, s.RefreshTokenTTL)
	}
	st := f.store.snapshot()
	if len(st.events) != 1 {
		t.Fatalf("events = %d", len(st.events))
	}
	if ev, ok := st.events[0].(domain.UserRegistered); !ok || ev.UserID != s.User.ID || !ev.OccurredAt.Equal(t0) {
		t.Fatalf("event %+v", st.events[0])
	}
	if len(st.tokens) != 1 {
		t.Fatalf("tokens = %d", len(st.tokens))
	}
	for _, tok := range st.tokens {
		if bytes.Contains(tok.TokenHash, []byte(s.RefreshToken)) || len(tok.TokenHash) != 32 {
			t.Fatal("refresh token stored in plaintext or wrong hash size")
		}
		if tok.UserAgent != "test-agent" || tok.IP != "203.0.113.5" {
			t.Fatalf("client metadata %+v", tok)
		}
	}
}

func TestSignInExistingUserMatchedBySubject(t *testing.T) {
	f := newFixture()
	first := f.signIn(t, "alice")
	f.clock.Advance(time.Hour)
	second := f.signIn(t, "alice-new-email")
	if second.Created || second.User.ID != first.User.ID || second.User.Email != "alice@new.example" {
		t.Fatalf("%+v", second.User)
	}
	if !second.User.LastLoginAt.Equal(t0.Add(time.Hour)) || !second.User.CreatedAt.Equal(t0) {
		t.Fatalf("timestamps %+v", second.User)
	}
	st := f.store.snapshot()
	if len(st.users) != 1 || len(st.events) != 1 {
		t.Fatalf("users=%d events=%d", len(st.users), len(st.events))
	}
}

func TestSignInBootstrapsRootAdminCaseInsensitively(t *testing.T) {
	f := newFixture(" admin@example.com ")
	s := f.signIn(t, "admin")
	if s.User.Role != auth.RoleRootAdmin || !strings.HasSuffix(s.AccessToken, ":root_admin") {
		t.Fatalf("%+v %q", s.User, s.AccessToken)
	}
}

func TestExistingUserPromotedWhenAddedToAllowlist(t *testing.T) {
	f := newFixture()
	if s := f.signIn(t, "admin"); s.User.Role != auth.RoleStudent {
		t.Fatalf("role %s", s.User.Role)
	}
	svc := app.NewService(f.store, google, fakeIssuer{}, f.clock, []string{"admin@example.com"})
	s, err := svc.SignInWithGoogle(ctx, "admin", client)
	if err != nil || s.User.Role != auth.RoleRootAdmin {
		t.Fatalf("%+v %v", s.User, err)
	}
}

func TestRootAdminKeptWhenRemovedFromAllowlist(t *testing.T) {
	f := newFixture("admin@example.com")
	f.signIn(t, "admin")
	svc := app.NewService(f.store, google, fakeIssuer{}, f.clock, nil)
	s, err := svc.SignInWithGoogle(ctx, "admin", client)
	if err != nil || s.User.Role != auth.RoleRootAdmin {
		t.Fatalf("%+v %v", s.User, err)
	}
}

func TestSignInRejectsUnverifiedEmail(t *testing.T) {
	f := newFixture()
	_, err := f.svc.SignInWithGoogle(ctx, "unverified", client)
	if !errors.Is(err, app.ErrEmailUnverified) {
		t.Fatalf("err = %v", err)
	}
	if n := len(f.store.snapshot().users); n != 0 {
		t.Fatalf("users = %d", n)
	}
}

func TestSignInRejectsInvalidToken(t *testing.T) {
	f := newFixture()
	if _, err := f.svc.SignInWithGoogle(ctx, "forged", client); !errors.Is(err, app.ErrInvalidToken) {
		t.Fatalf("err = %v", err)
	}
}

func TestSignInRetriesOnceOnConflict(t *testing.T) {
	f := newFixture()
	f.store.conflictsLeft = 1
	s := f.signIn(t, "alice")
	if !s.Created || len(f.store.snapshot().users) != 1 {
		t.Fatalf("%+v", s)
	}
}

func TestSignInGivesUpAfterSecondConflict(t *testing.T) {
	f := newFixture()
	f.store.conflictsLeft = 2
	if _, err := f.svc.SignInWithGoogle(ctx, "alice", client); !errors.Is(err, app.ErrConflict) {
		t.Fatalf("err = %v", err)
	}
}

func TestSignInTruncatesUserAgent(t *testing.T) {
	f := newFixture()
	if _, err := f.svc.SignInWithGoogle(ctx, "alice", app.Client{UserAgent: strings.Repeat("x", 1000)}); err != nil {
		t.Fatal(err)
	}
	for _, tok := range f.store.snapshot().tokens {
		if len(tok.UserAgent) != 512 {
			t.Fatalf("user agent length %d", len(tok.UserAgent))
		}
	}
}

func TestRefreshRotates(t *testing.T) {
	f := newFixture()
	s := f.signIn(t, "alice")
	f.clock.Advance(time.Minute)
	r := f.refresh(t, s.RefreshToken)
	if r.RefreshToken == s.RefreshToken || r.User.ID != s.User.ID || r.AccessToken == "" {
		t.Fatalf("%+v", r)
	}
	f.refresh(t, r.RefreshToken)
}

func TestRefreshUsesCurrentRole(t *testing.T) {
	f := newFixture()
	s := f.signIn(t, "alice")
	f.store.setRole(s.User.ID, auth.RoleInstructor)
	if r := f.refresh(t, s.RefreshToken); !strings.HasSuffix(r.AccessToken, ":instructor") {
		t.Fatalf("access %q", r.AccessToken)
	}
}

func TestRefreshReuseRevokesFamily(t *testing.T) {
	f := newFixture()
	s := f.signIn(t, "alice")
	r1 := f.refresh(t, s.RefreshToken)

	if _, err := f.svc.Refresh(ctx, s.RefreshToken, client); !errors.Is(err, app.ErrRefreshReuse) {
		t.Fatalf("reuse err = %v", err)
	}
	if _, err := f.svc.Refresh(ctx, r1.RefreshToken, client); !errors.Is(err, app.ErrInvalidToken) {
		t.Fatalf("sibling after reuse err = %v (revocation must survive the failed request)", err)
	}
}

func TestRefreshIdleExpiry(t *testing.T) {
	f := newFixture()
	s := f.signIn(t, "alice")
	f.clock.Advance(domain.RefreshIdleLifetime)
	if _, err := f.svc.Refresh(ctx, s.RefreshToken, client); !errors.Is(err, app.ErrInvalidToken) {
		t.Fatalf("err = %v", err)
	}
}

func TestRefreshFamilyAbsoluteExpiry(t *testing.T) {
	f := newFixture()
	tok := f.signIn(t, "alice").RefreshToken
	var last app.Session
	for range 4 {
		f.clock.Advance(6 * 24 * time.Hour)
		last = f.refresh(t, tok)
		tok = last.RefreshToken
	}
	if last.RefreshTokenTTL != 6*24*time.Hour {
		t.Fatalf("TTL at day 24 = %v, want capped at family end", last.RefreshTokenTTL)
	}
	f.clock.Advance(6 * 24 * time.Hour)
	if _, err := f.svc.Refresh(ctx, tok, client); !errors.Is(err, app.ErrInvalidToken) {
		t.Fatalf("day 30 err = %v", err)
	}
}

func TestRefreshUnknownOrEmpty(t *testing.T) {
	f := newFixture()
	for _, raw := range []string{"", "nope"} {
		if _, err := f.svc.Refresh(ctx, raw, client); !errors.Is(err, app.ErrInvalidToken) {
			t.Fatalf("%q: err = %v", raw, err)
		}
	}
}

func TestLogoutRevokesFamily(t *testing.T) {
	f := newFixture()
	s := f.signIn(t, "alice")
	if err := f.svc.Logout(ctx, s.RefreshToken); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Refresh(ctx, s.RefreshToken, client); !errors.Is(err, app.ErrInvalidToken) {
		t.Fatalf("err = %v", err)
	}
}

func TestLogoutUnknownTokenSucceeds(t *testing.T) {
	f := newFixture()
	for _, raw := range []string{"", "nope"} {
		if err := f.svc.Logout(ctx, raw); err != nil {
			t.Fatalf("%q: %v", raw, err)
		}
	}
}

func TestGetMe(t *testing.T) {
	f := newFixture()
	s := f.signIn(t, "alice")
	u, err := f.svc.GetMe(ctx, s.User.ID)
	if err != nil || u.ID != s.User.ID {
		t.Fatalf("%+v %v", u, err)
	}
	if _, err := f.svc.GetMe(ctx, uuid.New()); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}
