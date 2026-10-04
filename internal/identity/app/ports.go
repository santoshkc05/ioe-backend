// Package app contains the identity use cases and the ports they depend on.
package app

import (
	"context"
	"errors"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/identity/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

var (
	ErrInvalidToken       = errors.New("invalid token")
	ErrRefreshReuse       = errors.New("refresh token reuse detected")
	ErrEmailUnverified    = errors.New("email not verified")
	ErrNotFound           = errors.New("not found")
	ErrConflict           = errors.New("conflict")
	ErrForbidden          = errors.New("forbidden")
	ErrEmailQueryTooShort = errors.New("email query too short")
	ErrInvalidRole        = domain.ErrInvalidRole
	ErrRoleNotAssignable  = domain.ErrRoleNotAssignable
)

// UserRepository returns ErrNotFound for missing users and ErrConflict when Insert
// would duplicate a Google subject. The ForUpdate finders lock the row until the
// transaction ends, so a read-modify-Update cannot overwrite a concurrent change.
type UserRepository interface {
	FindByGoogleSubjectForUpdate(ctx context.Context, subject string) (domain.User, error)
	FindByID(ctx context.Context, id id.ID) (domain.User, error)
	FindByIDForUpdate(ctx context.Context, id id.ID) (domain.User, error)
	// SearchByEmailPrefix matches case-insensitively, treats prefix literally, orders by
	// email, and returns at most limit users.
	SearchByEmailPrefix(ctx context.Context, prefix string, limit int32) ([]domain.User, error)
	Insert(ctx context.Context, u domain.User) error
	Update(ctx context.Context, u domain.User) error
}

// RefreshTokenRepository returns ErrNotFound for unknown hashes. FindByHashForUpdate
// locks the row until the transaction ends.
type RefreshTokenRepository interface {
	Insert(ctx context.Context, t domain.RefreshToken) error
	FindByHashForUpdate(ctx context.Context, hash []byte) (domain.RefreshToken, error)
	MarkUsed(ctx context.Context, id id.ID, at time.Time) error
	RevokeFamily(ctx context.Context, familyID id.ID, at time.Time) error
}

// EventPublisher records events in the current transaction.
type EventPublisher interface {
	Publish(ctx context.Context, events ...domain.Event) error
}

// Repos are bound to one transaction.
type Repos struct {
	Users  UserRepository
	Tokens RefreshTokenRepository
	Events EventPublisher
}

// TxRunner commits when fn returns nil and rolls back otherwise.
type TxRunner interface {
	RunInTx(ctx context.Context, fn func(Repos) error) error
}

// GoogleVerifier validates a Google ID token. Failures wrap ErrInvalidToken.
type GoogleVerifier interface {
	Verify(ctx context.Context, idToken string) (domain.GoogleIdentity, error)
}

// AccessTokenIssuer creates access tokens and reports their lifetime.
type AccessTokenIssuer interface {
	Issue(userID id.ID, role auth.Role) (string, time.Duration, error)
}
