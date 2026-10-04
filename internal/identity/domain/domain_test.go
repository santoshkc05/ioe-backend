package domain_test

import (
	"encoding/json"
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

func TestChangeRole(t *testing.T) {
	by := id.ID(9)
	later := t0.Add(time.Hour)
	cases := []struct {
		name        string
		from, to    auth.Role
		wantRole    auth.Role
		wantChanged bool
		wantErr     error
	}{
		{"promote", auth.RoleStudent, auth.RoleInstructor, auth.RoleInstructor, true, nil},
		{"demote", auth.RoleInstructor, auth.RoleStudent, auth.RoleStudent, true, nil},
		{"same role", auth.RoleInstructor, auth.RoleInstructor, auth.RoleInstructor, false, nil},
		{"grant root admin", auth.RoleStudent, auth.RoleRootAdmin, auth.RoleStudent, false, domain.ErrInvalidRole},
		{"unknown role", auth.RoleStudent, auth.Role("teacher"), auth.RoleStudent, false, domain.ErrInvalidRole},
		{"wrong case", auth.RoleStudent, auth.Role("Instructor"), auth.RoleStudent, false, domain.ErrInvalidRole},
		{"empty role", auth.RoleStudent, auth.Role(""), auth.RoleStudent, false, domain.ErrInvalidRole},
		{"root admin target", auth.RoleRootAdmin, auth.RoleStudent, auth.RoleRootAdmin, false, domain.ErrRoleNotAssignable},
		{"root admin target, invalid role", auth.RoleRootAdmin, auth.RoleRootAdmin, auth.RoleRootAdmin, false, domain.ErrInvalidRole},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			u := domain.NewUser(id.ID(1), domain.GoogleIdentity{Subject: "s"}, t0)
			u.Role = c.from
			ev, changed, err := u.ChangeRole(c.to, by, later)
			if !errors.Is(err, c.wantErr) || changed != c.wantChanged || u.Role != c.wantRole {
				t.Fatalf("role=%s changed=%v err=%v", u.Role, changed, err)
			}
			if !changed {
				if ev != (domain.UserRoleChanged{}) || !u.UpdatedAt.Equal(t0) {
					t.Fatalf("unchanged user mutated: event=%+v updated=%v", ev, u.UpdatedAt)
				}
				return
			}
			want := domain.UserRoleChanged{UserID: u.ID, PreviousRole: c.from, Role: c.to, ChangedBy: by, OccurredAt: later}
			if ev != want || !u.UpdatedAt.Equal(later) {
				t.Fatalf("event=%+v updated=%v", ev, u.UpdatedAt)
			}
		})
	}
}

func TestUserRoleChangedEvent(t *testing.T) {
	var e domain.Event = domain.UserRoleChanged{}
	if e.EventName() != "identity.user_role_changed" {
		t.Fatal(e.EventName())
	}
	b, err := json.Marshal(domain.UserRoleChanged{
		UserID: id.ID(1), PreviousRole: auth.RoleStudent, Role: auth.RoleInstructor, ChangedBy: id.ID(2), OccurredAt: t0,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"user_id":"1","previous_role":"student","role":"instructor","changed_by":"2","occurred_at":"2026-10-04T12:00:00Z"}`
	if string(b) != want {
		t.Fatalf("payload %s", b)
	}
}
