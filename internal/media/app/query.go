package app

import (
	"context"

	"github.com/santoshkc2200/ioe-backend/internal/media/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

// AssetQuery answers which assets belong to a course. It needs no media service, so
// content validation works when media uploads are disabled.
type AssetQuery struct{ assets AssetRepository }

func NewAssetQuery(assets AssetRepository) *AssetQuery { return &AssetQuery{assets: assets} }

// Kinds returns the kinds of the given IDs that belong to courseID; others are absent.
func (q *AssetQuery) Kinds(ctx context.Context, courseID id.ID, ids []id.ID) (map[id.ID]domain.Kind, error) {
	return q.assets.KindsInCourse(ctx, courseID, ids)
}
