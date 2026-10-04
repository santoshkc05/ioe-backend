package app_test

import (
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/identity/app"
	"github.com/santoshkc2200/ioe-backend/internal/identity/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/clock"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

var rootAdmin = auth.Principal{UserID: id.ID(7), Role: auth.RoleRootAdmin}

type adminFixture struct {
	store *memStore
	clock *clock.Fake
	svc   *app.AdminService
}

func newAdminFixture(users ...domain.User) *adminFixture {
	f := &adminFixture{store: newMemStore(), clock: clock.NewFake(t0)}
	for _, u := range users {
		f.store.state.users[u.ID] = u
	}
	f.svc = app.NewAdminService(f.store, f.clock)
	return f
}

func userWithRole(n int64, email string, r auth.Role) domain.User {
	u := domain.NewUser(id.ID(n), domain.GoogleIdentity{Subject: fmt.Sprintf("sub-%d", n), Email: email}, t0)
	u.Role = r
	return u
}

func TestAdminServiceRequiresRootAdmin(t *testing.T) {
	f := newAdminFixture(userWithRole(1, "s@example.com", auth.RoleStudent))
	for _, role := range []auth.Role{auth.RoleStudent, auth.RoleInstructor} {
		p := auth.Principal{UserID: id.ID(1), Role: role}
		if _, err := f.svc.SearchUsers(ctx, p, "s@example"); !errors.Is(err, app.ErrForbidden) {
			t.Errorf("%s search err = %v", role, err)
		}
		if _, err := f.svc.SetRole(ctx, p, id.ID(1), auth.RoleInstructor); !errors.Is(err, app.ErrForbidden) {
			t.Errorf("%s set role err = %v", role, err)
		}
	}
	st := f.store.snapshot()
	if st.users[id.ID(1)].Role != auth.RoleStudent || len(st.events) != 0 {
		t.Fatalf("state changed: %+v events=%d", st.users[id.ID(1)], len(st.events))
	}
}

func TestSetRoleChangesRoleAndPublishes(t *testing.T) {
	f := newAdminFixture(userWithRole(1, "s@example.com", auth.RoleStudent))
	f.clock.Advance(time.Hour)
	later := t0.Add(time.Hour)

	got, err := f.svc.SetRole(ctx, rootAdmin, id.ID(1), auth.RoleInstructor)
	if err != nil {
		t.Fatal(err)
	}
	if got.Role != auth.RoleInstructor || !got.UpdatedAt.Equal(later) {
		t.Fatalf("returned %+v", got)
	}
	st := f.store.snapshot()
	if st.users[id.ID(1)].Role != auth.RoleInstructor {
		t.Fatalf("stored %+v", st.users[id.ID(1)])
	}
	want := domain.UserRoleChanged{UserID: id.ID(1), PreviousRole: auth.RoleStudent, Role: auth.RoleInstructor, ChangedBy: rootAdmin.UserID, OccurredAt: later}
	if len(st.events) != 1 || st.events[0] != want {
		t.Fatalf("events %+v", st.events)
	}
}

func TestSetRoleSameRoleWritesNothing(t *testing.T) {
	f := newAdminFixture(userWithRole(1, "i@example.com", auth.RoleInstructor))
	f.clock.Advance(time.Hour)
	got, err := f.svc.SetRole(ctx, rootAdmin, id.ID(1), auth.RoleInstructor)
	if err != nil {
		t.Fatal(err)
	}
	st := f.store.snapshot()
	if got.Role != auth.RoleInstructor || !st.users[id.ID(1)].UpdatedAt.Equal(t0) || len(st.events) != 0 {
		t.Fatalf("returned %+v stored %+v events %d", got, st.users[id.ID(1)], len(st.events))
	}
}

func TestSetRoleErrors(t *testing.T) {
	cases := []struct {
		name   string
		target id.ID
		role   auth.Role
		want   error
	}{
		{"unknown user", id.ID(99), auth.RoleInstructor, app.ErrNotFound},
		{"root admin target", id.ID(2), auth.RoleStudent, app.ErrRoleNotAssignable},
		{"grant root admin", id.ID(1), auth.RoleRootAdmin, app.ErrInvalidRole},
		{"unknown role", id.ID(1), auth.Role("teacher"), app.ErrInvalidRole},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newAdminFixture(userWithRole(1, "s@example.com", auth.RoleStudent), userWithRole(2, "r@example.com", auth.RoleRootAdmin))
			if _, err := f.svc.SetRole(ctx, rootAdmin, c.target, c.role); !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
			st := f.store.snapshot()
			if len(st.events) != 0 || st.users[id.ID(1)].Role != auth.RoleStudent || st.users[id.ID(2)].Role != auth.RoleRootAdmin {
				t.Fatalf("state changed: %+v events=%d", st.users, len(st.events))
			}
		})
	}
}

func TestSearchUsers(t *testing.T) {
	f := newAdminFixture(
		userWithRole(1, "Alicia@example.com", auth.RoleStudent),
		userWithRole(2, "alice@example.com", auth.RoleInstructor),
		userWithRole(3, "bob@example.com", auth.RoleStudent),
	)
	users, err := f.svc.SearchUsers(ctx, rootAdmin, "  ALI  ")
	if err != nil {
		t.Fatal(err)
	}
	var emails []string
	for _, u := range users {
		emails = append(emails, u.Email)
	}
	if !slices.Equal(emails, []string{"alice@example.com", "Alicia@example.com"}) {
		t.Fatalf("emails %v", emails)
	}
	if got := f.store.lastSearchLimit(); got != 20 {
		t.Fatalf("limit %d", got)
	}

	for _, short := range []string{"", "ab", "  ab  ", "éé"} {
		if _, err := f.svc.SearchUsers(ctx, rootAdmin, short); !errors.Is(err, app.ErrEmailQueryTooShort) {
			t.Errorf("%q err = %v", short, err)
		}
	}
}
