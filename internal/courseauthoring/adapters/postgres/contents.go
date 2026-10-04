package postgres

import (
	"context"
	"encoding/json"

	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/adapters/postgres/sqlcgen"
	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/app"
	"github.com/santoshkc2200/ioe-backend/internal/platform/clock"
	"github.com/santoshkc2200/ioe-backend/internal/platform/contentblocks"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

type contents struct {
	q     *sqlcgen.Queries
	clock clock.Clock
}

func (r contents) FindLecture(ctx context.Context, courseID, lectureID id.ID) (app.LectureHeader, error) {
	row, err := r.q.GetLectureHeader(ctx, sqlcgen.GetLectureHeaderParams{CourseID: int64(courseID), ID: int64(lectureID)})
	if err != nil {
		return app.LectureHeader{}, notFound(err)
	}
	return app.LectureHeader{LectureID: id.ID(row.ID), CourseID: id.ID(row.CourseID), Title: row.Title,
		FreePreview: row.FreePreview, ContentRevision: row.ContentRevision}, nil
}

func (r contents) FindLectureForUpdate(ctx context.Context, courseID, lectureID id.ID) (app.LectureHeader, error) {
	row, err := r.q.GetLectureHeaderForUpdate(ctx, sqlcgen.GetLectureHeaderForUpdateParams{CourseID: int64(courseID), ID: int64(lectureID)})
	if err != nil {
		return app.LectureHeader{}, notFound(err)
	}
	return app.LectureHeader{LectureID: id.ID(row.ID), CourseID: id.ID(row.CourseID), Title: row.Title,
		FreePreview: row.FreePreview, ContentRevision: row.ContentRevision}, nil
}

func (r contents) ListBlocks(ctx context.Context, lectureID id.ID) ([]contentblocks.Block, error) {
	rows, err := r.q.ListLectureBlocks(ctx, int64(lectureID))
	if err != nil {
		return nil, err
	}
	out := make([]contentblocks.Block, 0, len(rows))
	for _, row := range rows {
		b, err := contentblocks.DecodePayload(contentblocks.BlockType(row.Kind), row.Payload)
		if err != nil {
			return nil, err
		}
		out = append(out, b.WithIdentity(id.ID(row.ID), row.ClientBlockID, int(row.Position)))
	}
	return out, nil
}

type upsertBlockElem struct {
	ID            int64           `json:"id"`
	ClientBlockID string          `json:"client_block_id"`
	Kind          string          `json:"kind"`
	Position      int             `json:"position"`
	Payload       json.RawMessage `json:"payload"`
}

func (r contents) upsert(ctx context.Context, courseID, lectureID id.ID, blocks []contentblocks.Block) error {
	if len(blocks) == 0 {
		return nil
	}
	elems := make([]upsertBlockElem, len(blocks))
	for i, b := range blocks {
		payload, err := contentblocks.EncodePayload(b)
		if err != nil {
			return err
		}
		elems[i] = upsertBlockElem{ID: int64(b.ID()), ClientBlockID: b.ClientBlockID(), Kind: string(b.Type()), Position: b.Position(), Payload: payload}
	}
	raw, err := json.Marshal(elems)
	if err != nil {
		return err
	}
	return r.q.UpsertLectureBlocks(ctx, sqlcgen.UpsertLectureBlocksParams{
		CourseID: int64(courseID), LectureID: int64(lectureID), Now: r.clock.Now(), Blocks: json.RawMessage(raw),
	})
}

// ApplyPatch bumps the revision with a compare-and-swap first, so a stale writer
// changes nothing; then deletes, upserts and reorders.
func (r contents) ApplyPatch(ctx context.Context, courseID, lectureID id.ID, baseRevision int64, plan app.BlockWritePlan) (int64, error) {
	now := r.clock.Now()
	n, err := r.q.BumpLectureContentRevision(ctx, sqlcgen.BumpLectureContentRevisionParams{ID: int64(lectureID), ContentRevision: baseRevision, UpdatedAt: now})
	if err != nil {
		return 0, err
	}
	if n == 0 {
		return 0, app.ErrConcurrentModification
	}
	if len(plan.Deletes) > 0 {
		if err := r.q.DeleteLectureBlocksByClientIDs(ctx, sqlcgen.DeleteLectureBlocksByClientIDsParams{LectureID: int64(lectureID), ClientBlockIds: plan.Deletes}); err != nil {
			return 0, err
		}
	}
	if err := r.upsert(ctx, courseID, lectureID, plan.Upserts); err != nil {
		return 0, err
	}
	if len(plan.Order) > 0 {
		if err := r.q.ApplyLectureBlockOrder(ctx, sqlcgen.ApplyLectureBlockOrderParams{LectureID: int64(lectureID), Now: now, ClientBlockIds: plan.Order}); err != nil {
			return 0, err
		}
	}
	return baseRevision + 1, nil
}

// ReplaceBlocks swaps the whole block list and bumps the revision unconditionally,
// so any client holding the old revision conflicts on its next patch.
func (r contents) ReplaceBlocks(ctx context.Context, courseID, lectureID id.ID, blocks []contentblocks.Block) (int64, error) {
	if err := r.q.DeleteLectureBlocks(ctx, int64(lectureID)); err != nil {
		return 0, err
	}
	if err := r.upsert(ctx, courseID, lectureID, blocks); err != nil {
		return 0, err
	}
	return r.q.ForceBumpLectureContentRevision(ctx, sqlcgen.ForceBumpLectureContentRevisionParams{ID: int64(lectureID), UpdatedAt: r.clock.Now()})
}
