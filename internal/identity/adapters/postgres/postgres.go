// Package postgres implements identity persistence on the identity schema.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/santoshkc2200/ioe-backend/internal/identity/adapters/postgres/sqlcgen"
	"github.com/santoshkc2200/ioe-backend/internal/identity/app"
	"github.com/santoshkc2200/ioe-backend/internal/identity/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/outbox"
)

const uniqueViolation = "23505"

// TxRunner runs identity use cases in one PostgreSQL transaction.
type TxRunner struct {
	pool *pgxpool.Pool
}

func NewTxRunner(pool *pgxpool.Pool) *TxRunner { return &TxRunner{pool: pool} }

func (r *TxRunner) RunInTx(ctx context.Context, fn func(app.Repos) error) error {
	return pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		q := sqlcgen.New(tx)
		return fn(app.Repos{Users: users{q}, Tokens: tokens{q}, Events: events{tx}})
	})
}

type users struct{ q *sqlcgen.Queries }

func (u users) FindByGoogleSubject(ctx context.Context, sub string) (domain.User, error) {
	row, err := u.q.GetUserByGoogleSub(ctx, sub)
	return toUser(row, err)
}

func (u users) FindByID(ctx context.Context, id uuid.UUID) (domain.User, error) {
	row, err := u.q.GetUserByID(ctx, id)
	return toUser(row, err)
}

func (u users) Insert(ctx context.Context, x domain.User) error {
	err := u.q.InsertUser(ctx, sqlcgen.InsertUserParams{
		ID: x.ID, GoogleSub: x.GoogleSubject, Email: x.Email, Name: x.Name, AvatarURL: x.AvatarURL,
		Role: string(x.Role), CreatedAt: x.CreatedAt, UpdatedAt: x.UpdatedAt, LastLoginAt: x.LastLoginAt,
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		return fmt.Errorf("%w: google subject exists", app.ErrConflict)
	}
	return err
}

func (u users) Update(ctx context.Context, x domain.User) error {
	return u.q.UpdateUser(ctx, sqlcgen.UpdateUserParams{
		ID: x.ID, Email: x.Email, Name: x.Name, AvatarURL: x.AvatarURL,
		Role: string(x.Role), UpdatedAt: x.UpdatedAt, LastLoginAt: x.LastLoginAt,
	})
}

func toUser(row sqlcgen.IdentityUser, err error) (domain.User, error) {
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.User{}, app.ErrNotFound
	}
	if err != nil {
		return domain.User{}, err
	}
	role, err := auth.ParseRole(row.Role)
	if err != nil {
		return domain.User{}, err
	}
	return domain.User{
		ID: row.ID, GoogleSubject: row.GoogleSub, Email: row.Email, Name: row.Name, AvatarURL: row.AvatarURL,
		Role: role, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, LastLoginAt: row.LastLoginAt,
	}, nil
}

type tokens struct{ q *sqlcgen.Queries }

func (t tokens) Insert(ctx context.Context, x domain.RefreshToken) error {
	return t.q.InsertRefreshToken(ctx, sqlcgen.InsertRefreshTokenParams{
		ID: x.ID, UserID: x.UserID, FamilyID: x.FamilyID, TokenHash: x.TokenHash,
		FamilyExpiresAt: x.FamilyExpiresAt, ExpiresAt: x.ExpiresAt, CreatedAt: x.CreatedAt,
		UserAgent: x.UserAgent, IP: x.IP,
	})
}

func (t tokens) FindByHashForUpdate(ctx context.Context, hash []byte) (domain.RefreshToken, error) {
	row, err := t.q.GetRefreshTokenByHashForUpdate(ctx, hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.RefreshToken{}, app.ErrNotFound
	}
	if err != nil {
		return domain.RefreshToken{}, err
	}
	return domain.RefreshToken{
		ID: row.ID, UserID: row.UserID, FamilyID: row.FamilyID, TokenHash: row.TokenHash,
		FamilyExpiresAt: row.FamilyExpiresAt, ExpiresAt: row.ExpiresAt,
		UsedAt: row.UsedAt, RevokedAt: row.RevokedAt, CreatedAt: row.CreatedAt,
		UserAgent: row.UserAgent, IP: row.IP,
	}, nil
}

func (t tokens) MarkUsed(ctx context.Context, id uuid.UUID, at time.Time) error {
	return t.q.MarkRefreshTokenUsed(ctx, sqlcgen.MarkRefreshTokenUsedParams{ID: id, UsedAt: &at})
}

func (t tokens) RevokeFamily(ctx context.Context, familyID uuid.UUID, at time.Time) error {
	return t.q.RevokeRefreshFamily(ctx, sqlcgen.RevokeRefreshFamilyParams{FamilyID: familyID, RevokedAt: &at})
}

type events struct{ tx pgx.Tx }

// Publish writes each event to the outbox in the current transaction, topic = event name.
func (e events) Publish(ctx context.Context, evs ...domain.Event) error {
	for _, ev := range evs {
		payload, err := json.Marshal(ev)
		if err != nil {
			return err
		}
		msg := message.NewMessage(uuid.NewString(), payload)
		msg.Metadata.Set("event_name", ev.EventName())
		if err := outbox.Publish(ctx, e.tx, ev.EventName(), msg); err != nil {
			return err
		}
	}
	return nil
}
