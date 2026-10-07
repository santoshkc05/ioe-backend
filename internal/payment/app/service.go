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
)

// Service implements the payment use cases.
type Service struct {
	tx       TxRunner
	courses  CourseCatalog
	enroll   EnrollmentGranter
	gateways map[string]Gateway
	ids      *id.Generator
	clock    clock.Clock
	logger   *slog.Logger
}

// NewService keys gateways by name; an unconfigured gateway is simply absent.
func NewService(tx TxRunner, courses CourseCatalog, enroll EnrollmentGranter, gateways map[string]Gateway, ids *id.Generator, c clock.Clock, logger *slog.Logger) *Service {
	return &Service{tx: tx, courses: courses, enroll: enroll, gateways: gateways, ids: ids, clock: c, logger: logger}
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
// grant. Errors on single purchases are logged so one bad purchase never blocks the rest.
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
	if p.Status != domain.StatusPaid {
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
