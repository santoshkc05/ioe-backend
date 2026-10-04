package auth_test

import (
	"context"
	"testing"

	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

func TestParseRole(t *testing.T) {
	for _, s := range []string{"student", "instructor", "root_admin"} {
		r, err := auth.ParseRole(s)
		if err != nil || string(r) != s {
			t.Fatalf("ParseRole(%q) = %q, %v", s, r, err)
		}
	}
	for _, s := range []string{"", "admin", "Student"} {
		if _, err := auth.ParseRole(s); err == nil {
			t.Fatalf("ParseRole(%q) succeeded, want error", s)
		}
	}
}

func TestPrincipalContext(t *testing.T) {
	if _, ok := auth.PrincipalFrom(context.Background()); ok {
		t.Fatal("empty context reported a principal")
	}
	p := auth.Principal{UserID: id.ID(42), Role: auth.RoleInstructor}
	got, ok := auth.PrincipalFrom(auth.WithPrincipal(context.Background(), p))
	if !ok || got != p {
		t.Fatalf("PrincipalFrom = %+v, %v; want %+v", got, ok, p)
	}
}
