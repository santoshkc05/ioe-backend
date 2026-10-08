// Package app contains the certificate use cases and the ports they depend on.
package app

import (
	"context"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/certificate/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

// Repository reads and writes certificates and policies in the current transaction.
type Repository interface {
	FindPolicy(ctx context.Context, courseID id.ID) (domain.Policy, bool, error)
	UpsertPolicy(ctx context.Context, p domain.Policy, now time.Time) error
	// FindValid returns the user's unrevoked certificate for the course.
	FindValid(ctx context.Context, courseID, userID id.ID) (domain.Certificate, bool, error)
	// Insert returns false, and stores nothing, when the user already holds a valid
	// certificate for the course.
	Insert(ctx context.Context, c domain.Certificate) (bool, error)
	FindByCode(ctx context.Context, code string) (domain.Certificate, bool, error)
	// ListByUser returns every certificate of the user, newest first.
	ListByUser(ctx context.Context, userID id.ID) ([]domain.Certificate, error)
	// RevokeValid revokes the user's valid certificate for the course; a no-op when there is none.
	RevokeValid(ctx context.Context, courseID, userID id.ID, now time.Time) error
}

// TxRunner commits when fn returns nil and rolls back otherwise.
type TxRunner interface {
	RunInTx(ctx context.Context, fn func(Repository) error) error
}
