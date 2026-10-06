// Package postgres implements media asset persistence on the media schema.
package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/santoshkc2200/ioe-backend/internal/media/adapters/postgres/sqlcgen"
	"github.com/santoshkc2200/ioe-backend/internal/media/app"
	"github.com/santoshkc2200/ioe-backend/internal/media/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

// Assets implements app.AssetRepository. Each method is one statement on the pool.
type Assets struct{ q *sqlcgen.Queries }

var _ app.AssetRepository = (*Assets)(nil)

func New(pool *pgxpool.Pool) *Assets { return &Assets{q: sqlcgen.New(pool)} }

func (r *Assets) Insert(ctx context.Context, a domain.Asset) error {
	return r.q.InsertAsset(ctx, sqlcgen.InsertAssetParams{
		ID: int64(a.ID), CourseID: int64(a.CourseID), Kind: string(a.Kind),
		CreatedBy: int64(a.CreatedBy), CreatedAt: a.CreatedAt,
	})
}

func (r *Assets) Find(ctx context.Context, assetID id.ID) (domain.Asset, error) {
	row, err := r.q.GetAsset(ctx, int64(assetID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Asset{}, app.ErrNotFound
	}
	if err != nil {
		return domain.Asset{}, err
	}
	return domain.Asset{
		ID: id.ID(row.ID), CourseID: id.ID(row.CourseID), Kind: domain.Kind(row.Kind),
		CreatedBy: id.ID(row.CreatedBy), CreatedAt: row.CreatedAt.UTC(),
	}, nil
}

func (r *Assets) Delete(ctx context.Context, assetID id.ID) error {
	return r.q.DeleteAsset(ctx, int64(assetID))
}

func (r *Assets) KindsInCourse(ctx context.Context, courseID id.ID, ids []id.ID) (map[id.ID]domain.Kind, error) {
	out := make(map[id.ID]domain.Kind, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	raw := make([]int64, len(ids))
	for i, v := range ids {
		raw[i] = int64(v)
	}
	rows, err := r.q.ListAssetKindsInCourse(ctx, sqlcgen.ListAssetKindsInCourseParams{CourseID: int64(courseID), Ids: raw})
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		out[id.ID(row.ID)] = domain.Kind(row.Kind)
	}
	return out, nil
}
