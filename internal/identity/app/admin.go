package app

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/santoshkc2200/ioe-backend/internal/identity/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/clock"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

const (
	minEmailQueryLen       = 3
	maxSearchResults int32 = 20
)

// AdminService implements user management for root admins.
type AdminService struct {
	tx    TxRunner
	clock clock.Clock
}

func NewAdminService(tx TxRunner, c clock.Clock) *AdminService {
	return &AdminService{tx: tx, clock: c}
}

// SearchUsers returns up to 20 users whose email starts with prefix, ignoring case.
func (s *AdminService) SearchUsers(ctx context.Context, p auth.Principal, prefix string) ([]domain.User, error) {
	if p.Role != auth.RoleRootAdmin {
		return nil, ErrForbidden
	}
	prefix = strings.TrimSpace(prefix)
	if utf8.RuneCountInString(prefix) < minEmailQueryLen {
		return nil, ErrEmailQueryTooShort
	}
	var users []domain.User
	err := s.tx.RunInTx(ctx, func(r Repos) error {
		var err error
		users, err = r.Users.SearchByEmailPrefix(ctx, prefix, maxSearchResults)
		return err
	})
	return users, err
}

// SetRole makes the user a student or an instructor and records the change in the outbox.
// Setting the role the user already has succeeds without writing anything. The user sees
// the new role in the access token issued at their next refresh.
func (s *AdminService) SetRole(ctx context.Context, p auth.Principal, userID id.ID, role auth.Role) (domain.User, error) {
	if p.Role != auth.RoleRootAdmin {
		return domain.User{}, ErrForbidden
	}
	var user domain.User
	err := s.tx.RunInTx(ctx, func(r Repos) error {
		var err error
		user, err = r.Users.FindByIDForUpdate(ctx, userID)
		if err != nil {
			return err
		}
		ev, changed, err := user.ChangeRole(role, p.UserID, s.clock.Now())
		if err != nil || !changed {
			return err
		}
		if err := r.Users.Update(ctx, user); err != nil {
			return err
		}
		return r.Events.Publish(ctx, ev)
	})
	if err != nil {
		return domain.User{}, err
	}
	return user, nil
}
