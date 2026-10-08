package app

import (
	"context"
	"fmt"

	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/clock"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
	"github.com/santoshkc2200/ioe-backend/internal/progress/domain"
)

// Service implements the progress use cases.
type Service struct {
	tx          TxRunner
	courses     CourseCatalog
	enrollments EnrollmentQuery
	clock       clock.Clock
}

func NewService(tx TxRunner, courses CourseCatalog, enrollments EnrollmentQuery, c clock.Clock) *Service {
	return &Service{tx: tx, courses: courses, enrollments: enrollments, clock: c}
}

// RecordLecture stores userID's progress on a lecture. The course and enrollment lookups
// run before the transaction opens: each takes its own pool connection, and holding a
// transaction open across them would hold two connections per write.
func (s *Service) RecordLecture(ctx context.Context, p auth.Principal, courseID, lectureID, userID id.ID, state domain.LectureState, positionMs int64) error {
	lp, err := domain.NewLectureProgress(lectureID, state, positionMs, s.clock.Now())
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidInput, err)
	}
	c, err := s.courses.CourseFacts(ctx, courseID)
	if err != nil {
		return err
	}
	if err := domain.AuthorizeRecord(p, c, lectureID, userID); err != nil {
		return err
	}
	active, err := s.enrollments.IsActivelyEnrolled(ctx, courseID, userID)
	if err != nil {
		return err
	}
	if !active {
		return ErrEnrollmentRequired
	}
	return s.tx.RunInTx(ctx, func(r Repository) error {
		return r.RecordLecture(ctx, courseID, userID, lp)
	})
}

// CourseProgress returns userID's progress in a course, empty when nothing was recorded.
// Users read their own progress without a course lookup, so it survives archiving.
func (s *Service) CourseProgress(ctx context.Context, p auth.Principal, courseID, userID id.ID) (domain.CourseProgress, error) {
	if p.UserID != userID {
		c, err := s.courses.CourseFacts(ctx, courseID)
		if err != nil {
			return domain.CourseProgress{}, err
		}
		if err := domain.AuthorizeReadCourse(p, c, userID); err != nil {
			return domain.CourseProgress{}, err
		}
	}
	var (
		cp    domain.CourseProgress
		found bool
	)
	err := s.tx.RunInTx(ctx, func(r Repository) error {
		var err error
		cp, found, err = r.FindCourse(ctx, courseID, userID)
		return err
	})
	if err != nil {
		return domain.CourseProgress{}, err
	}
	if !found {
		return domain.CourseProgress{CourseID: courseID, UserID: userID}, nil
	}
	return cp, nil
}

// IsComplete reports whether userID completed every lecture of the course. It applies no
// authorization: callers decide who may ask. ErrNotFound when the course does not exist.
func (s *Service) IsComplete(ctx context.Context, courseID, userID id.ID) (bool, error) {
	c, err := s.courses.CourseFacts(ctx, courseID)
	if err != nil {
		return false, err
	}
	var (
		cp    domain.CourseProgress
		found bool
	)
	err = s.tx.RunInTx(ctx, func(r Repository) error {
		var err error
		cp, found, err = r.FindCourse(ctx, courseID, userID)
		return err
	})
	if err != nil || !found {
		return false, err
	}
	return cp.CompletedAll(c.LectureIDs), nil
}

// UserProgress returns userID's progress in every course and their daily activity.
func (s *Service) UserProgress(ctx context.Context, p auth.Principal, userID id.ID) ([]domain.CourseProgress, []domain.ActivityDay, error) {
	if err := domain.AuthorizeReadUser(p, userID); err != nil {
		return nil, nil, err
	}
	var (
		courses []domain.CourseProgress
		days    []domain.ActivityDay
	)
	err := s.tx.RunInTx(ctx, func(r Repository) error {
		var err error
		if courses, err = r.FindByUser(ctx, userID); err != nil {
			return err
		}
		days, err = r.ActivityByUser(ctx, userID)
		return err
	})
	if err != nil {
		return nil, nil, err
	}
	return courses, days, nil
}
