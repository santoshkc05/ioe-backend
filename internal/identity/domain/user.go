// Package domain holds identity entities and rules. It has no infrastructure dependencies.
package domain

import (
	"errors"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

// GoogleIdentity is the verified content of a Google ID token.
type GoogleIdentity struct {
	Subject       string
	Email         string
	EmailVerified bool
	Name          string
	Picture       string
}

// User is an application account, keyed by Google subject.
type User struct {
	ID            id.ID
	GoogleSubject string
	Email         string
	Name          string
	AvatarURL     string
	Role          auth.Role
	CreatedAt     time.Time
	UpdatedAt     time.Time
	LastLoginAt   time.Time
}

// NewUser creates a student account from a verified Google identity.
func NewUser(id id.ID, g GoogleIdentity, now time.Time) User {
	return User{
		ID:            id,
		GoogleSubject: g.Subject,
		Email:         g.Email,
		Name:          g.Name,
		AvatarURL:     g.Picture,
		Role:          auth.RoleStudent,
		CreatedAt:     now,
		UpdatedAt:     now,
		LastLoginAt:   now,
	}
}

// RecordLogin refreshes profile fields from Google. The account stays keyed by subject.
func (u *User) RecordLogin(g GoogleIdentity, now time.Time) {
	u.Email = g.Email
	u.Name = g.Name
	u.AvatarURL = g.Picture
	u.LastLoginAt = now
	u.UpdatedAt = now
}

// PromoteToRootAdmin grants the root_admin role.
func (u *User) PromoteToRootAdmin(now time.Time) {
	if u.Role == auth.RoleRootAdmin {
		return
	}
	u.Role = auth.RoleRootAdmin
	u.UpdatedAt = now
}

var (
	ErrInvalidRole       = errors.New("role cannot be assigned")
	ErrRoleNotAssignable = errors.New("user's role cannot be changed")
)

// ChangeRole makes the user a student or an instructor. Root admins come only from bootstrap
// configuration, so their role never changes here. It returns changed=false and no event when
// the user already has the role.
func (u *User) ChangeRole(to auth.Role, by id.ID, now time.Time) (UserRoleChanged, bool, error) {
	if to != auth.RoleStudent && to != auth.RoleInstructor {
		return UserRoleChanged{}, false, ErrInvalidRole
	}
	if u.Role == auth.RoleRootAdmin {
		return UserRoleChanged{}, false, ErrRoleNotAssignable
	}
	if u.Role == to {
		return UserRoleChanged{}, false, nil
	}
	ev := UserRoleChanged{UserID: u.ID, PreviousRole: u.Role, Role: to, ChangedBy: by, OccurredAt: now}
	u.Role = to
	u.UpdatedAt = now
	return ev, true, nil
}
