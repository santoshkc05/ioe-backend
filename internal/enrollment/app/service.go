package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/santoshkc2200/ioe-backend/internal/enrollment/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/clock"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

const (
	DefaultPageLimit = 50
	MaxPageLimit     = 200
)

// Service implements the enrollment use cases.
type Service struct {
	tx      TxRunner
	courses CourseCatalog
	ids     *id.Generator
	clock   clock.Clock
}

func NewService(tx TxRunner, courses CourseCatalog, ids *id.Generator, c clock.Clock) *Service {
	return &Service{tx: tx, courses: courses, ids: ids, clock: c}
}

// Enroll makes userID actively enrolled in courseID. activated is false when the user was
// already active.
func (s *Service) Enroll(ctx context.Context, p auth.Principal, courseID, userID id.ID) (domain.Enrollment, bool, error) {
	c, err := s.courses.CourseFacts(ctx, courseID)
	if err != nil {
		return domain.Enrollment{}, false, err
	}
	if err := domain.AuthorizeEnroll(p, c, userID); err != nil {
		return domain.Enrollment{}, false, err
	}
	return s.enrollRetrying(ctx, courseID, userID)
}

// EnrollPurchased actively enrolls userID after the payment context confirmed a purchase.
// It applies no principal check and does not require the course to still be published:
// the buyer has paid. An already active enrollment is success.
func (s *Service) EnrollPurchased(ctx context.Context, courseID, userID id.ID) error {
	if _, err := s.courses.CourseFacts(ctx, courseID); err != nil {
		return err
	}
	_, _, err := s.enrollRetrying(ctx, courseID, userID)
	return err
}

// enrollRetrying runs enroll again when a concurrent first enrollment won the unique
// constraint; that row is then visible.
func (s *Service) enrollRetrying(ctx context.Context, courseID, userID id.ID) (domain.Enrollment, bool, error) {
	e, activated, err := s.enroll(ctx, courseID, userID)
	if errors.Is(err, ErrDuplicate) {
		e, activated, err = s.enroll(ctx, courseID, userID)
	}
	return e, activated, err
}

func (s *Service) enroll(ctx context.Context, courseID, userID id.ID) (domain.Enrollment, bool, error) {
	var (
		e         domain.Enrollment
		activated bool
	)
	err := s.tx.RunInTx(ctx, func(r Repos) error {
		existing, found, err := r.Enrollments.FindByCourseAndUser(ctx, courseID, userID)
		if err != nil {
			return err
		}
		now := s.clock.Now()
		var ev domain.Event
		if found {
			e = existing
			if ev = e.Reactivate(now); ev == nil {
				return nil // already active
			}
			err = r.Enrollments.Update(ctx, &e)
		} else {
			e, ev = domain.NewEnrollment(s.ids.New(), courseID, userID, now)
			err = r.Enrollments.Insert(ctx, &e)
		}
		if err != nil {
			return err
		}
		activated = true
		return r.Events.Publish(ctx, ev)
	})
	if err != nil {
		return domain.Enrollment{}, false, err
	}
	return e, activated, nil
}

// Cancel ends userID's enrollment. The enrolled user may always cancel their own;
// anyone else must manage the course.
func (s *Service) Cancel(ctx context.Context, p auth.Principal, courseID, userID id.ID, reason string) (domain.Enrollment, error) {
	if p.UserID != userID {
		c, err := s.courses.CourseFacts(ctx, courseID)
		if err != nil {
			return domain.Enrollment{}, err
		}
		if err := domain.AuthorizeManage(p, c); err != nil {
			return domain.Enrollment{}, err
		}
	}
	var e domain.Enrollment
	err := s.tx.RunInTx(ctx, func(r Repos) error {
		var (
			found bool
			err   error
		)
		e, found, err = r.Enrollments.FindByCourseAndUser(ctx, courseID, userID)
		if err != nil {
			return err
		}
		if !found {
			return ErrNotFound
		}
		ev, err := e.Cancel(reason, s.clock.Now())
		if err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidInput, err)
		}
		if ev == nil {
			return nil
		}
		if err := r.Enrollments.Update(ctx, &e); err != nil {
			return err
		}
		return r.Events.Publish(ctx, ev)
	})
	if err != nil {
		return domain.Enrollment{}, err
	}
	return e, nil
}

// ListByCourse returns one page of a course's active enrollments and the active total.
func (s *Service) ListByCourse(ctx context.Context, p auth.Principal, courseID id.ID, limit, offset int) ([]domain.Enrollment, int, error) {
	if limit < 1 || limit > MaxPageLimit || offset < 0 {
		return nil, 0, fmt.Errorf("%w: limit must be 1-%d and offset must not be negative", ErrInvalidInput, MaxPageLimit)
	}
	c, err := s.courses.CourseFacts(ctx, courseID)
	if err != nil {
		return nil, 0, err
	}
	if err := domain.AuthorizeManage(p, c); err != nil {
		return nil, 0, err
	}
	var (
		page  []domain.Enrollment
		total int
	)
	err = s.tx.RunInTx(ctx, func(r Repos) error {
		var err error
		page, total, err = r.Enrollments.ListActiveByCourse(ctx, courseID, limit, offset)
		return err
	})
	return page, total, err
}

// ListByUser returns userID's active enrollments.
func (s *Service) ListByUser(ctx context.Context, p auth.Principal, userID id.ID) ([]domain.Enrollment, error) {
	if err := domain.AuthorizeListUser(p, userID); err != nil {
		return nil, err
	}
	var out []domain.Enrollment
	err := s.tx.RunInTx(ctx, func(r Repos) error {
		var err error
		out, err = r.Enrollments.ListActiveByUser(ctx, userID)
		return err
	})
	return out, err
}
