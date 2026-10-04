package domain

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

const (
	// RefreshIdleLifetime is how long a refresh token stays valid without use.
	RefreshIdleLifetime = 7 * 24 * time.Hour
	// RefreshAbsoluteLifetime caps a token family from its first sign-in.
	RefreshAbsoluteLifetime = 30 * 24 * time.Hour
)

var (
	ErrRefreshExpired = errors.New("refresh token expired")
	ErrRefreshRevoked = errors.New("refresh token revoked")
	ErrRefreshReused  = errors.New("refresh token already used")
)

// RefreshToken is one link in a rotating refresh-token family. Only the hash is stored.
type RefreshToken struct {
	ID              uuid.UUID
	UserID          uuid.UUID
	FamilyID        uuid.UUID
	TokenHash       []byte
	FamilyExpiresAt time.Time
	ExpiresAt       time.Time
	UsedAt          *time.Time
	RevokedAt       *time.Time
	CreatedAt       time.Time
	UserAgent       string
	IP              string
}

// NewRefreshFamily starts a family at sign-in.
func NewRefreshFamily(id, familyID, userID uuid.UUID, hash []byte, now time.Time, userAgent, ip string) RefreshToken {
	familyExpiresAt := now.Add(RefreshAbsoluteLifetime)
	return RefreshToken{
		ID:              id,
		UserID:          userID,
		FamilyID:        familyID,
		TokenHash:       hash,
		FamilyExpiresAt: familyExpiresAt,
		ExpiresAt:       earlier(familyExpiresAt, now.Add(RefreshIdleLifetime)),
		CreatedAt:       now,
		UserAgent:       userAgent,
		IP:              ip,
	}
}

// Check reports why the token cannot be exchanged now, or nil. Revocation is checked first
// so a token from an already revoked family never triggers reuse handling again.
func (t RefreshToken) Check(now time.Time) error {
	switch {
	case t.RevokedAt != nil:
		return ErrRefreshRevoked
	case t.UsedAt != nil:
		return ErrRefreshReused
	case !now.Before(t.ExpiresAt):
		return ErrRefreshExpired
	default:
		return nil
	}
}

// Successor returns the rotated token in the same family.
func (t RefreshToken) Successor(id uuid.UUID, hash []byte, now time.Time, userAgent, ip string) RefreshToken {
	return RefreshToken{
		ID:              id,
		UserID:          t.UserID,
		FamilyID:        t.FamilyID,
		TokenHash:       hash,
		FamilyExpiresAt: t.FamilyExpiresAt,
		ExpiresAt:       earlier(t.FamilyExpiresAt, now.Add(RefreshIdleLifetime)),
		CreatedAt:       now,
		UserAgent:       userAgent,
		IP:              ip,
	}
}

func earlier(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
