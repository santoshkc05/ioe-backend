package main

import (
	"context"
	"errors"

	identityapp "github.com/santoshkc2200/ioe-backend/internal/identity/app"
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
