// Package app contains the identity use cases and the ports they depend on.
package app

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/santoshkc2200/ioe-backend/internal/identity/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
)

var (
	ErrInvalidToken    = errors.New("invalid token")
	ErrRefreshReuse    = errors.New("refresh token reuse detected")
	ErrEmailUnverified = errors.New("email not verified")
	ErrNotFound        = errors.New("not found")
	ErrConflict        = errors.New("conflict")
)

// UserRepository returns ErrNotFound for missing users and ErrConflict when Insert
// would duplicate a Google subject.
type UserRepository interface {
	FindByGoogleSubject(ctx context.Context, subject string) (domain.User, error)
	FindByID(ctx context.Context, id uuid.UUID) (domain.User, error)
	Insert(ctx context.Context, u domain.User) error
	Update(ctx context.Context, u domain.User) error
}

// RefreshTokenRepository returns ErrNotFound for unknown hashes. FindByHashForUpdate
// locks the row until the transaction ends.
type RefreshTokenRepository interface {
	Insert(ctx context.Context, t domain.RefreshToken) error
	FindByHashForUpdate(ctx context.Context, hash []byte) (domain.RefreshToken, error)
	MarkUsed(ctx context.Context, id uuid.UUID, at time.Time) error
	RevokeFamily(ctx context.Context, familyID uuid.UUID, at time.Time) error
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
	Issue(userID uuid.UUID, role auth.Role) (string, time.Duration, error)
}
