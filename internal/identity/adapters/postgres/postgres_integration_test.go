//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/santoshkc2200/ioe-backend/internal/identity/adapters/postgres"
	"github.com/santoshkc2200/ioe-backend/internal/identity/app"
	"github.com/santoshkc2200/ioe-backend/internal/identity/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/clock"
	"github.com/santoshkc2200/ioe-backend/internal/platform/postgres/pgtest"
)

var now = time.Now().UTC().Truncate(time.Microsecond)

func newUser(sub string) domain.User {
	return domain.NewUser(uuid.Must(uuid.NewV7()), domain.GoogleIdentity{Subject: sub, Email: "Mixed@Example.com", Name: "N"}, now)
}

func TestUserRepository(t *testing.T) {
	ctx := context.Background()
	runner := postgres.NewTxRunner(pgtest.New(t))
	u := newUser("sub-1")

	if err := runner.RunInTx(ctx, func(r app.Repos) error { return r.Users.Insert(ctx, u) }); err != nil {
		t.Fatal(err)
	}
	err := runner.RunInTx(ctx, func(r app.Repos) error { return r.Users.Insert(ctx, newUser("sub-1")) })
	if !errors.Is(err, app.ErrConflict) {
		t.Fatalf("duplicate subject err = %v", err)
	}

	err = runner.RunInTx(ctx, func(r app.Repos) error {
		got, err := r.Users.FindByGoogleSubject(ctx, "sub-1")
		if err != nil {
			return err
		}
		if got.ID != u.ID || got.Email != "Mixed@Example.com" || got.Role != auth.RoleStudent || !got.CreatedAt.Equal(now) {
			t.Errorf("found %+v", got)
		}
		got.Email = "new@example.com"
		got.PromoteToRootAdmin(now)
		if err := r.Users.Update(ctx, got); err != nil {
			return err
		}
		byID, err := r.Users.FindByID(ctx, u.ID)
		if err != nil {
			return err
		}
		if byID.Email != "new@example.com" || byID.Role != auth.RoleRootAdmin {
			t.Errorf("after update %+v", byID)
		}
		if _, err := r.Users.FindByID(ctx, uuid.New()); !errors.Is(err, app.ErrNotFound) {
			t.Errorf("missing id err = %v", err)
		}
		if _, err := r.Users.FindByGoogleSubject(ctx, "nope"); !errors.Is(err, app.ErrNotFound) {
			t.Errorf("missing sub err = %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRefreshTokenRepository(t *testing.T) {
	ctx := context.Background()
	runner := postgres.NewTxRunner(pgtest.New(t))
	u := newUser("sub-2")
	famA, famB := uuid.New(), uuid.New()
	a1 := domain.NewRefreshFamily(uuid.New(), famA, u.ID, []byte("hash-a1-0123456789012345678901"), now, "ua", "203.0.113.1")
	a2 := a1.Successor(uuid.New(), []byte("hash-a2-0123456789012345678901"), now, "ua", "203.0.113.1")
	b1 := domain.NewRefreshFamily(uuid.New(), famB, u.ID, []byte("hash-b1-0123456789012345678901"), now, "ua", "")

	err := runner.RunInTx(ctx, func(r app.Repos) error {
		if err := r.Users.Insert(ctx, u); err != nil {
			return err
		}
		for _, tok := range []domain.RefreshToken{a1, a2, b1} {
			if err := r.Tokens.Insert(ctx, tok); err != nil {
				return err
			}
		}
		if err := r.Tokens.MarkUsed(ctx, a1.ID, now); err != nil {
			return err
		}
		return r.Tokens.RevokeFamily(ctx, famA, now)
	})
	if err != nil {
		t.Fatal(err)
	}

	err = runner.RunInTx(ctx, func(r app.Repos) error {
		got, err := r.Tokens.FindByHashForUpdate(ctx, a1.TokenHash)
		if err != nil {
			return err
		}
		if got.UsedAt == nil || got.RevokedAt == nil || got.IP != "203.0.113.1" || !got.FamilyExpiresAt.Equal(a1.FamilyExpiresAt) {
			t.Errorf("a1 %+v", got)
		}
		if got, _ := r.Tokens.FindByHashForUpdate(ctx, a2.TokenHash); got.RevokedAt == nil {
			t.Error("a2 not revoked with its family")
		}
		if got, _ := r.Tokens.FindByHashForUpdate(ctx, b1.TokenHash); got.RevokedAt != nil {
			t.Error("other family revoked")
		}
		if _, err := r.Tokens.FindByHashForUpdate(ctx, []byte("missing")); !errors.Is(err, app.ErrNotFound) {
			t.Errorf("missing err = %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestEventCommitsWithUserOrNotAtAll(t *testing.T) {
	ctx := context.Background()
	pool := pgtest.New(t)
	runner := postgres.NewTxRunner(pool)
	count := func() (users, events int) {
		_ = pool.QueryRow(ctx, "SELECT count(*) FROM identity.users").Scan(&users)
		_ = pool.QueryRow(ctx, "SELECT count(*) FROM platform.outbox_messages").Scan(&events)
		return
	}
	errAbort := errors.New("abort")
	write := func(u domain.User, fail bool) error {
		return runner.RunInTx(ctx, func(r app.Repos) error {
			if err := r.Users.Insert(ctx, u); err != nil {
				return err
			}
			if err := r.Events.Publish(ctx, domain.UserRegistered{UserID: u.ID, Email: u.Email, OccurredAt: now}); err != nil {
				return err
			}
			if fail {
				return errAbort
			}
			return nil
		})
	}
	if err := write(newUser("sub-rollback"), true); !errors.Is(err, errAbort) {
		t.Fatal(err)
	}
	if u, e := count(); u != 0 || e != 0 {
		t.Fatalf("after rollback users=%d events=%d", u, e)
	}
	if err := write(newUser("sub-commit"), false); err != nil {
		t.Fatal(err)
	}
	if u, e := count(); u != 1 || e != 1 {
		t.Fatalf("after commit users=%d events=%d", u, e)
	}
}

type stubGoogle struct{}

func (stubGoogle) Verify(context.Context, string) (domain.GoogleIdentity, error) {
	return domain.GoogleIdentity{Subject: "sub-race", Email: "race@example.com", EmailVerified: true}, nil
}

type stubIssuer struct{}

func (stubIssuer) Issue(uuid.UUID, auth.Role) (string, time.Duration, error) {
	return "access", time.Minute, nil
}

func TestConcurrentRefreshOneWins(t *testing.T) {
	ctx := context.Background()
	svc := app.NewService(postgres.NewTxRunner(pgtest.New(t)), stubGoogle{}, stubIssuer{}, clock.System{}, nil)
	s, err := svc.SignInWithGoogle(ctx, "x", app.Client{})
	if err != nil {
		t.Fatal(err)
	}

	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = svc.Refresh(ctx, s.RefreshToken, app.Client{})
		}()
	}
	wg.Wait()

	ok, reused := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, app.ErrRefreshReuse):
			reused++
		default:
			t.Fatalf("unexpected err %v", err)
		}
	}
	if ok != 1 || reused != 1 {
		t.Fatalf("ok=%d reused=%d", ok, reused)
	}
}
