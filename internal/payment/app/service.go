package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/payment/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/clock"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

const (
	// pendingGrace gives the buyer time to finish paying before the reconciler asks the gateway.
	pendingGrace = 15 * time.Minute
	// stalePending is how long a purchase may stay pending before each pass warns about it.
	stalePending = 24 * time.Hour
	// reconcilePage bounds one ListUnsettled read.
	reconcilePage = 50
	// maxPage bounds one ListByUser page.
	maxPage = 50
)

// Service implements the payment use cases.
type Service struct {
	tx       TxRunner
	courses  CourseCatalog
	users    UserDirectory
	enroll   EnrollmentGranter
	gateways map[string]Gateway
	ids      *id.Generator
	clock    clock.Clock
	logger   *slog.Logger
}

// NewService keys gateways by name; an unconfigured gateway is simply absent.
func NewService(tx TxRunner, courses CourseCatalog, users UserDirectory, enroll EnrollmentGranter, gateways map[string]Gateway, ids *id.Generator, c clock.Clock, logger *slog.Logger) *Service {
	return &Service{tx: tx, courses: courses, users: users, enroll: enroll, gateways: gateways, ids: ids, clock: c, logger: logger}
}

// Checkout starts a new purchase of a published paid course and returns how to reach the
// gateway. Every call creates a new pending purchase; earlier ones are settled independently.
func (s *Service) Checkout(ctx context.Context, p auth.Principal, courseID id.ID, gateway string) (domain.Purchase, Checkout, error) {
	if gateway == "" {
		return domain.Purchase{}, Checkout{}, fmt.Errorf("%w: gateway is required", ErrInvalidInput)
	}
	gw, ok := s.gateways[gateway]
	if !ok {
		return domain.Purchase{}, Checkout{}, ErrGatewayUnavailable
	}
	c, err := s.courses.CourseFacts(ctx, courseID)
	if err != nil {
		return domain.Purchase{}, Checkout{}, err
	}
	if !c.Published {
		return domain.Purchase{}, Checkout{}, ErrNotFound
	}
	if c.Price.AmountMinor == 0 {
		return domain.Purchase{}, Checkout{}, ErrCourseFree
	}
	enrolled, err := s.enroll.IsEnrolled(ctx, courseID, p.UserID)
	if err != nil {
		return domain.Purchase{}, Checkout{}, err
	}
	if enrolled {
		return domain.Purchase{}, Checkout{}, ErrAlreadyEnrolled
	}
	var purchase domain.Purchase
	err = s.tx.RunInTx(ctx, func(r Repos) error {
		paid, err := r.Purchases.CountPaid(ctx, p.UserID, courseID)
		if err != nil {
			return err
		}
		if paid > 0 {
			return ErrAlreadyPurchased
		}
		var ev domain.Event
		purchase, ev, err = domain.NewPurchase(s.ids.New(), p.UserID, courseID, c.Title, c.Price, gateway, s.clock.Now())
		if err != nil {
			return err
		}
		if err := r.Purchases.Insert(ctx, &purchase); err != nil {
			return err
		}
		return r.Events.Publish(ctx, ev)
	})
	if err != nil {
		return domain.Purchase{}, Checkout{}, err
	}
	co, err := gw.StartCheckout(ctx, purchase)
	if err != nil {
		// The pending purchase stays; the reconciler fails it once the gateway reports it unknown.
		return domain.Purchase{}, Checkout{}, fmt.Errorf("%w: %w", ErrGatewayUnavailable, err)
	}
	return purchase, co, nil
}

// Confirm asks the gateway about the caller's purchase and records the outcome.
func (s *Service) Confirm(ctx context.Context, p auth.Principal, purchaseID id.ID) (domain.Purchase, error) {
	purchase, err := s.Get(ctx, p, purchaseID)
	if err != nil {
		return domain.Purchase{}, err
	}
	return s.settle(ctx, purchase)
}

// Get returns a purchase to its buyer or a root admin; anyone else gets ErrNotFound.
func (s *Service) Get(ctx context.Context, p auth.Principal, purchaseID id.ID) (domain.Purchase, error) {
	var purchase domain.Purchase
	err := s.tx.RunInTx(ctx, func(r Repos) error {
		var (
			found bool
			err   error
		)
		purchase, found, err = r.Purchases.Find(ctx, purchaseID)
		if err != nil {
			return err
		}
		if !found || !purchase.CanView(p) {
			return ErrNotFound
		}
		return nil
	})
	return purchase, err
}

// Reconcile settles every pending purchase older than pendingGrace and retries every failed
// grant and every failed revocation. Errors on single purchases are logged so one bad purchase
// never blocks the rest.
func (s *Service) Reconcile(ctx context.Context) error {
	now := s.clock.Now()
	var after id.ID
	for {
		var page []domain.Purchase
		err := s.tx.RunInTx(ctx, func(r Repos) error {
			var err error
			page, err = r.Purchases.ListUnsettled(ctx, now.Add(-pendingGrace), after, reconcilePage)
			return err
		})
		if err != nil {
			return err
		}
		for _, p := range page {
			if err := ctx.Err(); err != nil {
				return err
			}
			if p.Status == domain.StatusRefunded {
				if _, err := s.revoke(ctx, p); err != nil {
					s.logger.WarnContext(ctx, "purchase reconcile failed", "purchase_id", p.ID, "error", err)
				}
				continue
			}
			settled, err := s.settle(ctx, p)
			if err != nil {
				s.logger.WarnContext(ctx, "purchase reconcile failed", "purchase_id", p.ID, "error", err)
				continue
			}
			if settled.Status == domain.StatusPending && now.Sub(settled.CreatedAt) >= stalePending {
				s.logger.WarnContext(ctx, "purchase pending for over 24 hours", "purchase_id", p.ID, "gateway", p.Gateway)
			}
		}
		if len(page) < reconcilePage {
			return nil
		}
		after = page[len(page)-1].ID
	}
}

// settle asks the gateway about an unpaid purchase, records the outcome, and grants the
// enrollment of a paid purchase that was not granted yet. It is idempotent.
func (s *Service) settle(ctx context.Context, p domain.Purchase) (domain.Purchase, error) {
	if p.AwaitsGateway() {
		gw, ok := s.gateways[p.Gateway]
		if !ok {
			return p, ErrGatewayUnavailable
		}
		res, err := gw.FetchStatus(ctx, p)
		if err != nil {
			return p, fmt.Errorf("%w: %w", ErrGatewayUnavailable, err)
		}
		switch res.Kind {
		case ResultComplete:
			p, err = s.update(ctx, p.ID, func(cur *domain.Purchase) (domain.Event, error) {
				return cur.MarkPaid(res.Txn, s.clock.Now())
			})
			if err != nil {
				return p, err
			}
			s.warnIfDuplicate(ctx, p)
		case ResultFailed:
			p, err = s.update(ctx, p.ID, func(cur *domain.Purchase) (domain.Event, error) {
				return cur.MarkFailed(s.clock.Now()), nil
			})
			if err != nil {
				return p, err
			}
		case ResultPending:
		}
	}
	if !p.NeedsGrant() {
		return p, nil
	}
	if err := s.enroll.GrantPurchased(ctx, p.CourseID, p.UserID); err != nil {
		// The purchase stays paid and ungranted; the next confirm or reconcile pass retries.
		s.logger.ErrorContext(ctx, "enrollment grant failed", "purchase_id", p.ID, "error", err)
		return p, nil
	}
	return s.update(ctx, p.ID, func(cur *domain.Purchase) (domain.Event, error) {
		cur.MarkGranted(s.clock.Now())
		return nil, nil
	})
}

// update reloads the purchase, applies change, and writes it with its event in one
// transaction. A stale version from a concurrent writer is retried once on a fresh read.
func (s *Service) update(ctx context.Context, purchaseID id.ID, change func(*domain.Purchase) (domain.Event, error)) (domain.Purchase, error) {
	p, err := s.updateOnce(ctx, purchaseID, change)
	if errors.Is(err, ErrConcurrentModification) {
		p, err = s.updateOnce(ctx, purchaseID, change)
	}
	return p, err
}

func (s *Service) updateOnce(ctx context.Context, purchaseID id.ID, change func(*domain.Purchase) (domain.Event, error)) (domain.Purchase, error) {
	var p domain.Purchase
	err := s.tx.RunInTx(ctx, func(r Repos) error {
		var (
			found bool
			err   error
		)
		p, found, err = r.Purchases.Find(ctx, purchaseID)
		if err != nil {
			return err
		}
		if !found {
			return ErrNotFound
		}
		before := p
		ev, err := change(&p)
		if err != nil {
			return err
		}
		if p == before {
			return nil
		}
		if err := r.Purchases.Update(ctx, &p); err != nil {
			return err
		}
		if ev == nil {
			return nil
		}
		return r.Events.Publish(ctx, ev)
	})
	return p, err
}

// warnIfDuplicate logs when the buyer now holds more than one paid purchase of the course,
// which happens when two checkouts were both paid. The extra payment is refunded by hand.
func (s *Service) warnIfDuplicate(ctx context.Context, p domain.Purchase) {
	var n int
	err := s.tx.RunInTx(ctx, func(r Repos) error {
		var err error
		n, err = r.Purchases.CountPaid(ctx, p.UserID, p.CourseID)
		return err
	})
	if err != nil {
		s.logger.ErrorContext(ctx, "duplicate payment check failed", "purchase_id", p.ID, "error", err)
		return
	}
	if n > 1 {
		s.logger.WarnContext(ctx, "duplicate paid purchase needs a manual refund",
			"purchase_id", p.ID, "user_id", p.UserID, "course_id", p.CourseID, "paid_count", n)
	}
}

// ListByUser returns userID's purchases, newest first, below the before cursor. The caller must
// be that user or a root admin; anyone else gets ErrNotFound. next is zero on the last page.
func (s *Service) ListByUser(ctx context.Context, p auth.Principal, userID, before id.ID, limit int) ([]domain.Purchase, id.ID, error) {
	if p.UserID != userID && p.Role != auth.RoleRootAdmin {
		return nil, 0, ErrNotFound
	}
	if limit < 1 || limit > maxPage {
		return nil, 0, fmt.Errorf("%w: limit must be between 1 and %d", ErrInvalidInput, maxPage)
	}
	var page []domain.Purchase
	err := s.tx.RunInTx(ctx, func(r Repos) error {
		var err error
		page, err = r.Purchases.ListByUser(ctx, userID, before, limit+1)
		return err
	})
	if err != nil {
		return nil, 0, err
	}
	if len(page) <= limit {
		return page, 0, nil
	}
	page = page[:limit]
	return page, page[limit-1].ID, nil
}

// ManualInput is an offline payment a root admin records for a user.
type ManualInput struct {
	UserID      id.ID
	CourseID    id.ID
	AmountMinor int64
	Currency    string
	Method      string
	Reference   string
	Note        string
}

// RecordManual records a payment made outside any gateway as a paid purchase and enrolls the
// buyer. Only a root admin may record one. An existing enrollment does not block it.
func (s *Service) RecordManual(ctx context.Context, p auth.Principal, in ManualInput) (domain.Purchase, error) {
	if p.Role != auth.RoleRootAdmin {
		return domain.Purchase{}, ErrForbidden
	}
	c, err := s.courses.CourseFacts(ctx, in.CourseID)
	if err != nil {
		return domain.Purchase{}, err
	}
	if !c.Published {
		return domain.Purchase{}, ErrNotFound
	}
	if c.Price.AmountMinor == 0 {
		return domain.Purchase{}, ErrCourseFree
	}
	if in.Currency != c.Price.Currency {
		return domain.Purchase{}, fmt.Errorf("%w: currency must be %s", ErrInvalidInput, c.Price.Currency)
	}
	exists, err := s.users.UserExists(ctx, in.UserID)
	if err != nil {
		return domain.Purchase{}, err
	}
	if !exists {
		return domain.Purchase{}, ErrNotFound
	}
	var purchase domain.Purchase
	err = s.tx.RunInTx(ctx, func(r Repos) error {
		paid, err := r.Purchases.CountPaid(ctx, in.UserID, in.CourseID)
		if err != nil {
			return err
		}
		if paid > 0 {
			return ErrAlreadyPurchased
		}
		var ev domain.Event
		purchase, ev, err = domain.RecordManualPurchase(s.ids.New(), in.UserID, in.CourseID, c.Title,
			domain.Money{AmountMinor: in.AmountMinor, Currency: in.Currency},
			domain.ManualPayment{Method: in.Method, Reference: in.Reference, Note: in.Note, RecordedBy: p.UserID},
			s.clock.Now())
		if errors.Is(err, domain.ErrFreePrice) || errors.Is(err, domain.ErrInvalidPurchase) {
			return fmt.Errorf("%w: %w", ErrInvalidInput, err)
		}
		if err != nil {
			return err
		}
		if err := r.Purchases.Insert(ctx, &purchase); err != nil {
			return err
		}
		return r.Events.Publish(ctx, ev)
	})
	if err != nil {
		return domain.Purchase{}, err
	}
	// The purchase is paid, so settle skips the gateway and only grants.
	return s.settle(ctx, purchase)
}

// RefundInput is a full refund a root admin records after paying the buyer back.
type RefundInput struct {
	Reference string
	Note      string
}

// Refund records a full refund of a paid purchase and cancels the buyer's enrollment, unless
// the buyer holds another paid purchase of the course. Only a root admin may refund; anyone
// else gets ErrNotFound.
func (s *Service) Refund(ctx context.Context, p auth.Principal, purchaseID id.ID, in RefundInput) (domain.Purchase, error) {
	if p.Role != auth.RoleRootAdmin {
		return domain.Purchase{}, ErrNotFound
	}
	r := domain.Refund{Reference: in.Reference, Note: in.Note, RefundedBy: p.UserID}
	purchase, err := s.refundOnce(ctx, purchaseID, r)
	if errors.Is(err, ErrConcurrentModification) {
		purchase, err = s.refundOnce(ctx, purchaseID, r)
	}
	if err != nil {
		return domain.Purchase{}, err
	}
	return s.revoke(ctx, purchase)
}

func (s *Service) refundOnce(ctx context.Context, purchaseID id.ID, refund domain.Refund) (domain.Purchase, error) {
	var p domain.Purchase
	err := s.tx.RunInTx(ctx, func(r Repos) error {
		var (
			found bool
			err   error
		)
		p, found, err = r.Purchases.Find(ctx, purchaseID)
		if err != nil {
			return err
		}
		if !found {
			return ErrNotFound
		}
		paid, err := r.Purchases.CountPaid(ctx, p.UserID, p.CourseID)
		if err != nil {
			return err
		}
		// p itself is still paid, so another paid purchase makes the count exceed one.
		ev, err := p.Refund(refund, paid > 1, s.clock.Now())
		switch {
		case errors.Is(err, domain.ErrNotRefundable):
			return fmt.Errorf("%w: %w", ErrNotRefundable, err)
		case errors.Is(err, domain.ErrInvalidPurchase):
			return fmt.Errorf("%w: reference is required (at most 200 characters) and note is at most 1000 characters", ErrInvalidInput)
		case err != nil:
			return err
		}
		if err := r.Purchases.Update(ctx, &p); err != nil {
			return err
		}
		return r.Events.Publish(ctx, ev)
	})
	return p, err
}

// revoke cancels the enrollment of a refunded purchase that still needs it. A failed cancel is
// logged and left for the next reconcile pass.
func (s *Service) revoke(ctx context.Context, p domain.Purchase) (domain.Purchase, error) {
	if !p.NeedsRevoke() {
		return p, nil
	}
	if err := s.enroll.RevokePurchased(ctx, p.CourseID, p.UserID); err != nil {
		s.logger.ErrorContext(ctx, "enrollment revoke failed", "purchase_id", p.ID, "error", err)
		return p, nil
	}
	return s.update(ctx, p.ID, func(cur *domain.Purchase) (domain.Event, error) {
		cur.MarkRevoked(s.clock.Now())
		return nil, nil
	})
}
