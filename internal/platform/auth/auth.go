// Package auth holds the authenticated principal shared by every bounded context.
package auth

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// Role is a user's single application role.
type Role string

const (
	RoleStudent    Role = "student"
	RoleInstructor Role = "instructor"
	RoleRootAdmin  Role = "root_admin"
)

// ParseRole validates s as a known role.
func ParseRole(s string) (Role, error) {
	switch r := Role(s); r {
	case RoleStudent, RoleInstructor, RoleRootAdmin:
		return r, nil
	default:
		return "", fmt.Errorf("unknown role %q", s)
	}
}

// Principal is the authenticated caller of a request.
type Principal struct {
	UserID uuid.UUID
	Role   Role
}

type principalKey struct{}

// WithPrincipal returns a context carrying p.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// PrincipalFrom returns the principal stored by WithPrincipal.
func PrincipalFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}
