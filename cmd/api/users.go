package main

import (
	"context"
	"errors"

	identityapp "github.com/santoshkc2200/ioe-backend/internal/identity/app"
	notificationapp "github.com/santoshkc2200/ioe-backend/internal/notification/app"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

// identityUsers lets other contexts look users up in identity.
type identityUsers struct{ svc *identityapp.Service }

func (u identityUsers) UserExists(ctx context.Context, userID id.ID) (bool, error) {
	_, err := u.svc.GetMe(ctx, userID)
	if errors.Is(err, identityapp.ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

func (u identityUsers) Contact(ctx context.Context, userID string) (string, string, error) {
	uid, err := id.Parse(userID)
	if err != nil {
		return "", "", notificationapp.ErrUnknownUser
	}
	user, err := u.svc.GetMe(ctx, uid)
	if errors.Is(err, identityapp.ErrNotFound) {
		return "", "", notificationapp.ErrUnknownUser
	}
	if err != nil {
		return "", "", err
	}
	return user.Email, user.Name, nil
}
