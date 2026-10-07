package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/santoshkc2200/ioe-backend/internal/payment/adapters/postgres/sqlcgen"
	"github.com/santoshkc2200/ioe-backend/internal/payment/app"
	"github.com/santoshkc2200/ioe-backend/internal/payment/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

type purchases struct{ q *sqlcgen.Queries }

func (r purchases) Find(ctx context.Context, purchaseID id.ID) (domain.Purchase, bool, error) {
	row, err := r.q.GetPurchase(ctx, int64(purchaseID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Purchase{}, false, nil
	}
	if err != nil {
		return domain.Purchase{}, false, err
	}
	return toDomain(row), true, nil
}

func (r purchases) CountPaid(ctx context.Context, userID, courseID id.ID) (int, error) {
	n, err := r.q.CountPaidPurchases(ctx, sqlcgen.CountPaidPurchasesParams{UserID: int64(userID), CourseID: int64(courseID)})
	return int(n), err
}

func (r purchases) ListUnsettled(ctx context.Context, pendingBefore time.Time, afterID id.ID, limit int) ([]domain.Purchase, error) {
	rows, err := r.q.ListUnsettledPurchases(ctx, sqlcgen.ListUnsettledPurchasesParams{
		AfterID: int64(afterID), PendingBefore: pendingBefore, PageLimit: int64(limit),
	})
	if err != nil {
		return nil, err
	}
	return toDomainAll(rows), nil
}

func (r purchases) ListByUser(ctx context.Context, userID, before id.ID, limit int) ([]domain.Purchase, error) {
	rows, err := r.q.ListPurchasesByUser(ctx, sqlcgen.ListPurchasesByUserParams{
		UserID: int64(userID), BeforeID: int64(before), PageLimit: int64(limit),
	})
	if err != nil {
		return nil, err
	}
	return toDomainAll(rows), nil
}

func toDomainAll(rows []sqlcgen.PaymentPurchase) []domain.Purchase {
	out := make([]domain.Purchase, len(rows))
	for i, row := range rows {
		out[i] = toDomain(row)
	}
	return out
}

func (r purchases) Insert(ctx context.Context, p *domain.Purchase) error {
	err := r.q.InsertPurchase(ctx, sqlcgen.InsertPurchaseParams{
		ID: int64(p.ID), UserID: int64(p.UserID), CourseID: int64(p.CourseID),
		AmountMinor: p.Price.AmountMinor, Currency: p.Price.Currency, Gateway: p.Gateway,
		GatewayRef: p.GatewayRef, GatewayTxn: p.GatewayTxn, Status: string(p.Status), CreatedAt: p.CreatedAt,
		SettledAt: optionalTime(p.SettledAt), GrantedAt: optionalTime(p.GrantedAt),
		CourseTitle: p.CourseTitle, ManualMethod: optionalString(p.ManualMethod),
		RecordedBy: optionalID(p.RecordedBy), Note: p.Note,
	})
	if err != nil {
		return err
	}
	p.Version = 1
	return nil
}

func (r purchases) Update(ctx context.Context, p *domain.Purchase) error {
	n, err := r.q.UpdatePurchase(ctx, sqlcgen.UpdatePurchaseParams{
		ID: int64(p.ID), Version: p.Version, GatewayTxn: p.GatewayTxn, Status: string(p.Status),
		SettledAt: optionalTime(p.SettledAt), GrantedAt: optionalTime(p.GrantedAt),
	})
	if err != nil {
		return err
	}
	if n == 0 {
		return app.ErrConcurrentModification
	}
	p.Version++
	return nil
}

func toDomain(r sqlcgen.PaymentPurchase) domain.Purchase {
	p := domain.Purchase{
		ID: id.ID(r.ID), UserID: id.ID(r.UserID), CourseID: id.ID(r.CourseID),
		Price:   domain.Money{AmountMinor: r.AmountMinor, Currency: r.Currency},
		Gateway: r.Gateway, GatewayRef: r.GatewayRef, GatewayTxn: r.GatewayTxn,
		Status: domain.Status(r.Status), CreatedAt: r.CreatedAt.UTC(), Version: r.Version,
		CourseTitle: r.CourseTitle, Note: r.Note,
	}
	if r.ManualMethod != nil {
		p.ManualMethod = *r.ManualMethod
	}
	if r.RecordedBy != nil {
		p.RecordedBy = id.ID(*r.RecordedBy)
	}
	if r.SettledAt != nil {
		p.SettledAt = r.SettledAt.UTC()
	}
	if r.GrantedAt != nil {
		p.GrantedAt = r.GrantedAt.UTC()
	}
	return p
}

func optionalTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func optionalString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func optionalID(v id.ID) *int64 {
	if v == 0 {
		return nil
	}
	n := int64(v)
	return &n
}
