package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/santoshkc2200/ioe-backend/internal/certificate/adapters/postgres/sqlcgen"
	"github.com/santoshkc2200/ioe-backend/internal/certificate/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

type repo struct{ q *sqlcgen.Queries }

func (r repo) FindPolicy(ctx context.Context, courseID id.ID) (domain.Policy, bool, error) {
	row, err := r.q.GetPolicy(ctx, int64(courseID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Policy{}, false, nil
	}
	if err != nil {
		return domain.Policy{}, false, err
	}
	p := domain.Policy{CourseID: id.ID(row.CourseID), Mode: domain.Mode(row.Mode)}
	if row.ExamID.Valid {
		p.ExamID = id.ID(row.ExamID.Int64)
	}
	return p, true, nil
}

func (r repo) UpsertPolicy(ctx context.Context, p domain.Policy, now time.Time) error {
	return r.q.UpsertPolicy(ctx, sqlcgen.UpsertPolicyParams{
		CourseID:  int64(p.CourseID),
		Mode:      string(p.Mode),
		ExamID:    pgtype.Int8{Int64: int64(p.ExamID), Valid: !p.ExamID.IsZero()},
		UpdatedAt: now.UTC(),
	})
}

func (r repo) FindValid(ctx context.Context, courseID, userID id.ID) (domain.Certificate, bool, error) {
	row, err := r.q.GetValidCertificate(ctx, sqlcgen.GetValidCertificateParams{CourseID: int64(courseID), UserID: int64(userID)})
	return one(row, err)
}

func (r repo) Insert(ctx context.Context, c domain.Certificate) (bool, error) {
	_, err := r.q.InsertCertificate(ctx, sqlcgen.InsertCertificateParams{
		ID: int64(c.ID), Code: c.Code, UserID: int64(c.UserID), CourseID: int64(c.CourseID),
		StudentName: c.StudentName, CourseTitle: c.CourseTitle, IssuedAt: c.IssuedAt.UTC(),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (r repo) FindByCode(ctx context.Context, code string) (domain.Certificate, bool, error) {
	row, err := r.q.GetCertificateByCode(ctx, code)
	return one(row, err)
}

func (r repo) ListByUser(ctx context.Context, userID id.ID) ([]domain.Certificate, error) {
	rows, err := r.q.ListCertificatesByUser(ctx, int64(userID))
	if err != nil {
		return nil, err
	}
	out := make([]domain.Certificate, len(rows))
	for i, row := range rows {
		out[i] = toCertificate(row)
	}
	return out, nil
}

func (r repo) RevokeValid(ctx context.Context, courseID, userID id.ID, now time.Time) error {
	utc := now.UTC()
	return r.q.RevokeValidCertificate(ctx, sqlcgen.RevokeValidCertificateParams{
		CourseID: int64(courseID), UserID: int64(userID), RevokedAt: &utc,
	})
}

func one(row sqlcgen.CertificateCertificate, err error) (domain.Certificate, bool, error) {
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Certificate{}, false, nil
	}
	if err != nil {
		return domain.Certificate{}, false, err
	}
	return toCertificate(row), true, nil
}

func toCertificate(row sqlcgen.CertificateCertificate) domain.Certificate {
	c := domain.Certificate{
		ID: id.ID(row.ID), Code: row.Code, UserID: id.ID(row.UserID), CourseID: id.ID(row.CourseID),
		StudentName: row.StudentName, CourseTitle: row.CourseTitle, IssuedAt: row.IssuedAt.UTC(),
	}
	if row.RevokedAt != nil {
		c.RevokedAt = row.RevokedAt.UTC()
	}
	return c
}
