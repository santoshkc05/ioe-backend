package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/certificate/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/clock"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

// Service implements the certificate use cases.
type Service struct {
	tx          TxRunner
	courses     CourseManagement
	enrollments Enrollments
	progress    Progress
	exams       Exams
	directory   Directory
	ids         *id.Generator
	clock       clock.Clock
}

func NewService(tx TxRunner, courses CourseManagement, enrollments Enrollments, progress Progress, exams Exams, directory Directory, ids *id.Generator, c clock.Clock) *Service {
	return &Service{tx: tx, courses: courses, enrollments: enrollments, progress: progress, exams: exams, directory: directory, ids: ids, clock: c}
}

// GetPolicy returns the course's policy to a manager. A course without a policy is off.
func (s *Service) GetPolicy(ctx context.Context, p auth.Principal, courseID id.ID) (domain.Policy, error) {
	if err := s.courses.CanManage(ctx, p, courseID); err != nil {
		return domain.Policy{}, err
	}
	policy := domain.Policy{CourseID: courseID, Mode: domain.ModeOff}
	err := s.tx.RunInTx(ctx, func(r Repository) error {
		stored, found, err := r.FindPolicy(ctx, courseID)
		if found {
			policy = stored
		}
		return err
	})
	return policy, err
}

// SetPolicy replaces the course's policy. The caller is authorized before any input is
// examined, so a stranger learns nothing about the course.
func (s *Service) SetPolicy(ctx context.Context, p auth.Principal, courseID id.ID, mode domain.Mode, examID id.ID) (domain.Policy, error) {
	if err := s.courses.CanManage(ctx, p, courseID); err != nil {
		return domain.Policy{}, err
	}
	policy, err := domain.NewPolicy(courseID, mode, examID)
	if err != nil {
		return domain.Policy{}, fmt.Errorf("%w: %w", ErrInvalidInput, err)
	}
	if policy.Mode == domain.ModeCompletionAndExam {
		ok, err := s.exams.ExamInCourse(ctx, courseID, policy.ExamID)
		if err != nil {
			return domain.Policy{}, err
		}
		if !ok {
			return domain.Policy{}, fmt.Errorf("%w: %w", ErrInvalidInput, domain.ErrExamNotInCourse)
		}
	}
	err = s.tx.RunInTx(ctx, func(r Repository) error {
		return r.UpsertPolicy(ctx, policy, s.clock.Now())
	})
	return policy, err
}

// Claim issues the caller's certificate for the course when they meet the course's policy.
// created is false when the caller already held a valid certificate. A policy whose exam is no
// longer in the course cannot be met and reads as ErrCertificatesDisabled. The eligibility
// lookups run outside any transaction: each takes its own pool connection.
func (s *Service) Claim(ctx context.Context, p auth.Principal, courseID id.ID) (cert domain.Certificate, created bool, err error) {
	var (
		policy   domain.Policy
		found    bool
		existing domain.Certificate
		hasValid bool
	)
	err = s.tx.RunInTx(ctx, func(r Repository) error {
		var err error
		if policy, found, err = r.FindPolicy(ctx, courseID); err != nil {
			return err
		}
		existing, hasValid, err = r.FindValid(ctx, courseID, p.UserID)
		return err
	})
	if err != nil {
		return domain.Certificate{}, false, err
	}
	if !found || policy.Mode == domain.ModeOff {
		return domain.Certificate{}, false, ErrCertificatesDisabled
	}
	active, err := s.enrollments.IsActivelyEnrolled(ctx, courseID, p.UserID)
	if err != nil {
		return domain.Certificate{}, false, err
	}
	if !active {
		return domain.Certificate{}, false, ErrNotEnrolled
	}
	if hasValid {
		return existing, false, nil
	}
	complete, err := s.progress.IsComplete(ctx, courseID, p.UserID)
	if err != nil {
		return domain.Certificate{}, false, err
	}
	if !complete {
		return domain.Certificate{}, false, ErrProgressIncomplete
	}
	if policy.Mode == domain.ModeCompletionAndExam {
		inCourse, err := s.exams.ExamInCourse(ctx, courseID, policy.ExamID)
		if err != nil {
			return domain.Certificate{}, false, err
		}
		if !inCourse {
			return domain.Certificate{}, false, ErrCertificatesDisabled
		}
		passed, err := s.exams.HasPassed(ctx, courseID, p.UserID, policy.ExamID)
		if err != nil {
			return domain.Certificate{}, false, err
		}
		if !passed {
			return domain.Certificate{}, false, ErrExamNotPassed
		}
	}
	name, err := s.directory.StudentName(ctx, p.UserID)
	if err != nil {
		return domain.Certificate{}, false, err
	}
	title, err := s.directory.CourseTitle(ctx, courseID)
	if err != nil {
		return domain.Certificate{}, false, err
	}
	issued := domain.NewCertificate(s.ids.New(), domain.NewCode(), p.UserID, courseID, name, title, s.clock.Now())
	err = s.tx.RunInTx(ctx, func(r Repository) error {
		inserted, err := r.Insert(ctx, issued)
		if err != nil {
			return err
		}
		if inserted {
			cert, created = issued, true
			return nil
		}
		// A concurrent claim won; return its certificate.
		var ok bool
		cert, ok, err = r.FindValid(ctx, courseID, p.UserID)
		if err == nil && !ok {
			err = errors.New("certificate insert conflicted but no valid certificate exists")
		}
		return err
	})
	return cert, created, err
}

// GetMine returns the caller's valid certificate for the course, or ErrNotFound.
func (s *Service) GetMine(ctx context.Context, p auth.Principal, courseID id.ID) (domain.Certificate, error) {
	var (
		cert  domain.Certificate
		found bool
	)
	err := s.tx.RunInTx(ctx, func(r Repository) error {
		var err error
		cert, found, err = r.FindValid(ctx, courseID, p.UserID)
		return err
	})
	if err == nil && !found {
		err = ErrNotFound
	}
	return cert, err
}

// ListMine returns every certificate of the caller, revoked ones included, newest first.
func (s *Service) ListMine(ctx context.Context, p auth.Principal) ([]domain.Certificate, error) {
	var out []domain.Certificate
	err := s.tx.RunInTx(ctx, func(r Repository) error {
		var err error
		out, err = r.ListByUser(ctx, p.UserID)
		return err
	})
	return out, err
}

// Verify returns the certificate with the given code, revoked or not. A malformed code is
// ErrNotFound without a lookup.
func (s *Service) Verify(ctx context.Context, code string) (domain.Certificate, error) {
	if !domain.ValidCode(code) {
		return domain.Certificate{}, ErrNotFound
	}
	var (
		cert  domain.Certificate
		found bool
	)
	err := s.tx.RunInTx(ctx, func(r Repository) error {
		var err error
		cert, found, err = r.FindByCode(ctx, code)
		return err
	})
	if err == nil && !found {
		err = ErrNotFound
	}
	return cert, err
}

// RevokeForRefund revokes the user's valid certificate for the course if it was issued by
// refundedAt. A certificate issued later was earned again after the refund and stays valid. It
// is idempotent and does nothing when there is none.
func (s *Service) RevokeForRefund(ctx context.Context, userID, courseID id.ID, refundedAt time.Time) error {
	return s.tx.RunInTx(ctx, func(r Repository) error {
		return r.RevokeValid(ctx, courseID, userID, refundedAt, s.clock.Now())
	})
}
