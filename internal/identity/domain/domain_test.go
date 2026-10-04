package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/identity/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

var t0 = time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)

func TestNewUserIsStudent(t *testing.T) {
	id := id.ID(1)
	u := domain.NewUser(id, domain.GoogleIdentity{Subject: "s", Email: "a@example.com", Name: "A", Picture: "p"}, t0)
	if u.ID != id || u.Role != auth.RoleStudent || u.GoogleSubject != "s" || u.AvatarURL != "p" {
		t.Fatalf("%+v", u)
	}
	if !u.CreatedAt.Equal(t0) || !u.UpdatedAt.Equal(t0) || !u.LastLoginAt.Equal(t0) {
		t.Fatalf("timestamps %+v", u)
	}
}

func TestRecordLoginUpdatesProfileOnly(t *testing.T) {
	u := domain.NewUser(id.ID(1), domain.GoogleIdentity{Subject: "s", Email: "a@example.com"}, t0)
	later := t0.Add(time.Hour)
	u.RecordLogin(domain.GoogleIdentity{Subject: "s", Email: "b@example.com", Name: "B", Picture: "q"}, later)
	if u.Email != "b@example.com" || u.Name != "B" || u.AvatarURL != "q" || u.Role != auth.RoleStudent {
		t.Fatalf("%+v", u)
	}
	if !u.LastLoginAt.Equal(later) || !u.CreatedAt.Equal(t0) {
		t.Fatalf("timestamps %+v", u)
	}
}

func TestPromoteToRootAdmin(t *testing.T) {
	u := domain.NewUser(id.ID(1), domain.GoogleIdentity{Subject: "s"}, t0)
	u.PromoteToRootAdmin(t0.Add(time.Minute))
	if u.Role != auth.RoleRootAdmin || !u.UpdatedAt.Equal(t0.Add(time.Minute)) {
		t.Fatalf("%+v", u)
	}
}

func TestRefreshFamilyLifetimes(t *testing.T) {
	tok := domain.NewRefreshFamily(id.ID(1), id.ID(2), id.ID(3), []byte("h"), t0, "ua", "ip")
	if !tok.ExpiresAt.Equal(t0.Add(domain.RefreshIdleLifetime)) {
		t.Fatalf("ExpiresAt %v", tok.ExpiresAt)
	}
	if !tok.FamilyExpiresAt.Equal(t0.Add(domain.RefreshAbsoluteLifetime)) {
		t.Fatalf("FamilyExpiresAt %v", tok.FamilyExpiresAt)
	}

	nearEnd := tok.FamilyExpiresAt.Add(-24 * time.Hour)
	next := tok.Successor(id.ID(2), []byte("h2"), nearEnd, "ua", "ip")
	if next.FamilyID != tok.FamilyID || next.UserID != tok.UserID {
		t.Fatalf("successor left family: %+v", next)
	}
	if !next.ExpiresAt.Equal(tok.FamilyExpiresAt) {
		t.Fatalf("successor not capped by family: %v", next.ExpiresAt)
	}
}

func TestRefreshCheck(t *testing.T) {
	base := domain.NewRefreshFamily(id.ID(1), id.ID(2), id.ID(3), []byte("h"), t0, "", "")
	used, revoked := base, base
	at := t0.Add(time.Minute)
	used.UsedAt = &at
	revoked.RevokedAt = &at
	revokedAndUsed := used
	revokedAndUsed.RevokedAt = &at

	cases := []struct {
		name string
		tok  domain.RefreshToken
		now  time.Time
		want error
	}{
		{"fresh", base, t0.Add(time.Hour), nil},
		{"expired", base, base.ExpiresAt, domain.ErrRefreshExpired},
		{"used", used, t0.Add(time.Hour), domain.ErrRefreshReused},
		{"revoked", revoked, t0.Add(time.Hour), domain.ErrRefreshRevoked},
		{"revoked wins over used", revokedAndUsed, t0.Add(time.Hour), domain.ErrRefreshRevoked},
	}
	for _, c := range cases {
		if err := c.tok.Check(c.now); !errors.Is(err, c.want) {
			t.Errorf("%s: got %v want %v", c.name, err, c.want)
		}
	}
}

func TestUserRegisteredName(t *testing.T) {
	var e domain.Event = domain.UserRegistered{}
	if e.EventName() != "identity.user_registered" {
		t.Fatal(e.EventName())
	}
}
