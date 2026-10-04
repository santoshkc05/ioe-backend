package app

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/identity/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/clock"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

const maxUserAgentLen = 512

// Client describes the caller for refresh-token bookkeeping.
type Client struct {
	UserAgent string
	IP        string
}

// Session is the result of sign-in or refresh. RefreshToken is the raw secret for the cookie.
type Session struct {
	AccessToken     string
	AccessTokenTTL  time.Duration
	RefreshToken    string
	RefreshTokenTTL time.Duration
	User            domain.User
	Created         bool
}

// Service implements the identity use cases.
type Service struct {
	tx         TxRunner
	google     GoogleVerifier
	tokens     AccessTokenIssuer
	ids        *id.Generator
	clock      clock.Clock
	rootAdmins map[string]struct{}
}

func NewService(tx TxRunner, google GoogleVerifier, tokens AccessTokenIssuer, ids *id.Generator, c clock.Clock, rootAdminEmails []string) *Service {
	admins := make(map[string]struct{}, len(rootAdminEmails))
	for _, e := range rootAdminEmails {
		if e = normalizeEmail(e); e != "" {
			admins[e] = struct{}{}
		}
	}
	return &Service{tx: tx, google: google, tokens: tokens, ids: ids, clock: c, rootAdmins: admins}
}

// SignInWithGoogle verifies a Google ID token, creates or updates the user, and starts a session.
func (s *Service) SignInWithGoogle(ctx context.Context, idToken string, client Client) (Session, error) {
	identity, err := s.google.Verify(ctx, idToken)
	if err != nil {
		return Session{}, err
	}
	if !identity.EmailVerified {
		return Session{}, ErrEmailUnverified
	}
	var sess Session
	signIn := func(r Repos) error {
		var err error
		sess, err = s.signIn(ctx, r, identity, client)
		return err
	}
	err = s.tx.RunInTx(ctx, signIn)
	if errors.Is(err, ErrConflict) {
		// A concurrent first sign-in created the user; retry once to load it.
		err = s.tx.RunInTx(ctx, signIn)
	}
	if err != nil {
		return Session{}, err
	}
	return sess, nil
}

func (s *Service) signIn(ctx context.Context, r Repos, identity domain.GoogleIdentity, client Client) (Session, error) {
	now := s.clock.Now()
	// Lock the row: Update writes every column, so an unlocked read could undo a concurrent role change.
	user, err := r.Users.FindByGoogleSubjectForUpdate(ctx, identity.Subject)
	created := false
	switch {
	case errors.Is(err, ErrNotFound):
		user = domain.NewUser(s.ids.New(), identity, now)
		s.applyBootstrap(&user, now)
		if err := r.Users.Insert(ctx, user); err != nil {
			return Session{}, err
		}
		if err := r.Events.Publish(ctx, domain.UserRegistered{UserID: user.ID, Email: user.Email, Name: user.Name, OccurredAt: now}); err != nil {
			return Session{}, err
		}
		created = true
	case err != nil:
		return Session{}, err
	default:
		user.RecordLogin(identity, now)
		s.applyBootstrap(&user, now)
		if err := r.Users.Update(ctx, user); err != nil {
			return Session{}, err
		}
	}

	raw, hash, err := newRefreshSecret()
	if err != nil {
		return Session{}, err
	}
	ua, ip := clientMeta(client)
	token := domain.NewRefreshFamily(s.ids.New(), s.ids.New(), user.ID, hash, now, ua, ip)
	if err := r.Tokens.Insert(ctx, token); err != nil {
		return Session{}, err
	}
	return s.withAccessToken(Session{RefreshToken: raw, RefreshTokenTTL: token.ExpiresAt.Sub(now), User: user, Created: created})
}

// Refresh exchanges a refresh token for a new access token and a rotated refresh token.
// Presenting an already used token revokes its whole family; that revocation is committed
// even though the call fails.
func (s *Service) Refresh(ctx context.Context, raw string, client Client) (Session, error) {
	if raw == "" {
		return Session{}, ErrInvalidToken
	}
	var sess Session
	reused := false
	err := s.tx.RunInTx(ctx, func(r Repos) error {
		now := s.clock.Now()
		current, err := r.Tokens.FindByHashForUpdate(ctx, hashRefreshToken(raw))
		if errors.Is(err, ErrNotFound) {
			return ErrInvalidToken
		}
		if err != nil {
			return err
		}
		if err := current.Check(now); err != nil {
			if errors.Is(err, domain.ErrRefreshReused) {
				reused = true
				return r.Tokens.RevokeFamily(ctx, current.FamilyID, now)
			}
			return ErrInvalidToken
		}
		user, err := r.Users.FindByID(ctx, current.UserID)
		if errors.Is(err, ErrNotFound) {
			return ErrInvalidToken
		}
		if err != nil {
			return err
		}
		if err := r.Tokens.MarkUsed(ctx, current.ID, now); err != nil {
			return err
		}
		nextRaw, nextHash, err := newRefreshSecret()
		if err != nil {
			return err
		}
		ua, ip := clientMeta(client)
		next := current.Successor(s.ids.New(), nextHash, now, ua, ip)
		if err := r.Tokens.Insert(ctx, next); err != nil {
			return err
		}
		sess, err = s.withAccessToken(Session{RefreshToken: nextRaw, RefreshTokenTTL: next.ExpiresAt.Sub(now), User: user})
		return err
	})
	if err != nil {
		return Session{}, err
	}
	if reused {
		return Session{}, ErrRefreshReuse
	}
	return sess, nil
}

// Logout revokes the token's family. Unknown or empty tokens succeed.
func (s *Service) Logout(ctx context.Context, raw string) error {
	if raw == "" {
		return nil
	}
	return s.tx.RunInTx(ctx, func(r Repos) error {
		current, err := r.Tokens.FindByHashForUpdate(ctx, hashRefreshToken(raw))
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		return r.Tokens.RevokeFamily(ctx, current.FamilyID, s.clock.Now())
	})
}

// GetMe returns the user's current record.
func (s *Service) GetMe(ctx context.Context, userID id.ID) (domain.User, error) {
	var user domain.User
	err := s.tx.RunInTx(ctx, func(r Repos) error {
		var err error
		user, err = r.Users.FindByID(ctx, userID)
		return err
	})
	return user, err
}

func (s *Service) withAccessToken(sess Session) (Session, error) {
	token, ttl, err := s.tokens.Issue(sess.User.ID, sess.User.Role)
	if err != nil {
		return Session{}, err
	}
	sess.AccessToken, sess.AccessTokenTTL = token, ttl
	return sess, nil
}

func (s *Service) applyBootstrap(u *domain.User, now time.Time) {
	if _, ok := s.rootAdmins[normalizeEmail(u.Email)]; ok {
		u.PromoteToRootAdmin(now)
	}
}

func normalizeEmail(e string) string { return strings.ToLower(strings.TrimSpace(e)) }

func clientMeta(c Client) (userAgent, ip string) {
	userAgent = c.UserAgent
	if len(userAgent) > maxUserAgentLen {
		userAgent = userAgent[:maxUserAgentLen]
	}
	return userAgent, c.IP
}
