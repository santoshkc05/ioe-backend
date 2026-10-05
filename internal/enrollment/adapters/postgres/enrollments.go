package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/santoshkc2200/ioe-backend/internal/enrollment/adapters/postgres/sqlcgen"
	"github.com/santoshkc2200/ioe-backend/internal/enrollment/app"
	"github.com/santoshkc2200/ioe-backend/internal/enrollment/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

const uniqueViolation = "23505"

type enrollments struct{ q *sqlcgen.Queries }

func (r enrollments) FindByCourseAndUser(ctx context.Context, courseID, userID id.ID) (domain.Enrollment, bool, error) {
	row, err := r.q.GetEnrollment(ctx, sqlcgen.GetEnrollmentParams{CourseID: int64(courseID), UserID: int64(userID)})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Enrollment{}, false, nil
	}
	if err != nil {
		return domain.Enrollment{}, false, err
	}
	return toDomain(row), true, nil
}

func (r enrollments) ListActiveByCourse(ctx context.Context, courseID id.ID, limit, offset int) ([]domain.Enrollment, int, error) {
	total, err := r.q.CountActiveEnrollmentsByCourse(ctx, int64(courseID))
	if err != nil {
		return nil, 0, err
	}
	rows, err := r.q.ListActiveEnrollmentsByCourse(ctx, sqlcgen.ListActiveEnrollmentsByCourseParams{
		CourseID: int64(courseID), PageLimit: int64(limit), PageOffset: int64(offset),
	})
	if err != nil {
		return nil, 0, err
	}
	return toDomainAll(rows), int(total), nil
}

func (r enrollments) ListActiveByUser(ctx context.Context, userID id.ID) ([]domain.Enrollment, error) {
	rows, err := r.q.ListActiveEnrollmentsByUser(ctx, int64(userID))
	if err != nil {
		return nil, err
	}
	return toDomainAll(rows), nil
}

func (r enrollments) Insert(ctx context.Context, e *domain.Enrollment) error {
	err := r.q.InsertEnrollment(ctx, sqlcgen.InsertEnrollmentParams{
		ID: int64(e.ID), CourseID: int64(e.CourseID), UserID: int64(e.UserID), Status: string(e.Status),
		CancelReason: e.CancelReason, EnrolledAt: e.EnrolledAt, CanceledAt: optionalTime(e.CanceledAt),
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		return app.ErrDuplicate
	}
	if err != nil {
		return err
	}
	e.Version = 1
	return nil
}

func (r enrollments) Update(ctx context.Context, e *domain.Enrollment) error {
	n, err := r.q.UpdateEnrollment(ctx, sqlcgen.UpdateEnrollmentParams{
		ID: int64(e.ID), Version: e.Version, Status: string(e.Status), CancelReason: e.CancelReason,
		EnrolledAt: e.EnrolledAt, CanceledAt: optionalTime(e.CanceledAt),
	})
	if err != nil {
		return err
	}
	if n == 0 {
		return app.ErrConcurrentModification
	}
	e.Version++
	return nil
}

func toDomain(r sqlcgen.EnrollmentEnrollment) domain.Enrollment {
	e := domain.Enrollment{
		ID: id.ID(r.ID), CourseID: id.ID(r.CourseID), UserID: id.ID(r.UserID), Status: domain.Status(r.Status),
		CancelReason: r.CancelReason, EnrolledAt: r.EnrolledAt.UTC(), Version: r.Version,
	}
	if r.CanceledAt != nil {
		e.CanceledAt = r.CanceledAt.UTC()
	}
	return e
}

func toDomainAll(rows []sqlcgen.EnrollmentEnrollment) []domain.Enrollment {
	out := make([]domain.Enrollment, len(rows))
	for i, r := range rows {
		out[i] = toDomain(r)
	}
	return out
}

func optionalTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}
