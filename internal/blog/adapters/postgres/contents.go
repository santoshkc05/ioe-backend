package postgres

import (
	"context"
	"encoding/json"

	"github.com/santoshkc2200/ioe-backend/internal/blog/adapters/postgres/sqlcgen"
	"github.com/santoshkc2200/ioe-backend/internal/blog/app"
	"github.com/santoshkc2200/ioe-backend/internal/platform/clock"
	"github.com/santoshkc2200/ioe-backend/internal/platform/contentblocks"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

type contents struct {
	q     *sqlcgen.Queries
	clock clock.Clock
}

func (r contents) FindHeader(ctx context.Context, postID id.ID) (app.ContentHeader, error) {
	row, err := r.q.GetPostContentHeader(ctx, int64(postID))
	if err != nil {
		return app.ContentHeader{}, notFound(err)
	}
	return app.ContentHeader{PostID: id.ID(row.ID), ContentRevision: row.ContentRevision}, nil
}

func (r contents) FindHeaderForUpdate(ctx context.Context, postID id.ID) (app.ContentHeader, error) {
	row, err := r.q.GetPostContentHeaderForUpdate(ctx, int64(postID))
	if err != nil {
		return app.ContentHeader{}, notFound(err)
	}
	return app.ContentHeader{PostID: id.ID(row.ID), ContentRevision: row.ContentRevision}, nil
}

func (r contents) ListBlocks(ctx context.Context, postID id.ID) ([]contentblocks.Block, error) {
	rows, err := r.q.ListPostBlocks(ctx, int64(postID))
	if err != nil {
		return nil, err
	}
	out := make([]contentblocks.Block, 0, len(rows))
	for _, row := range rows {
		b, err := decodeBlock(row.ID, row.ClientBlockID, row.Kind, row.Position, row.Payload)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, nil
}

func (r contents) ListVersionBlocks(ctx context.Context, postID id.ID, number int) ([]contentblocks.Block, error) {
	rows, err := r.q.ListPostVersionBlocks(ctx, sqlcgen.ListPostVersionBlocksParams{PostID: int64(postID), Number: int32(number)}) //nolint:gosec // one per publish
	if err != nil {
		return nil, err
	}
	out := make([]contentblocks.Block, 0, len(rows))
	for _, row := range rows {
		b, err := decodeBlock(row.ID, row.ClientBlockID, row.Kind, row.Position, row.Payload)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, nil
}

func decodeBlock(blockID int64, clientBlockID, kind string, position int32, payload json.RawMessage) (contentblocks.Block, error) {
	b, err := contentblocks.DecodePayload(contentblocks.BlockType(kind), payload)
	if err != nil {
		return contentblocks.Block{}, err
	}
	return b.WithIdentity(id.ID(blockID), clientBlockID, int(position)), nil
}

type upsertBlockElem struct {
	ID            int64           `json:"id"`
	ClientBlockID string          `json:"client_block_id"`
	Kind          string          `json:"kind"`
	Position      int             `json:"position"`
	Payload       json.RawMessage `json:"payload"`
}

func (r contents) upsert(ctx context.Context, postID id.ID, blocks []contentblocks.Block) error {
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
	return r.q.UpsertPostBlocks(ctx, sqlcgen.UpsertPostBlocksParams{PostID: int64(postID), Now: r.clock.Now(), Blocks: json.RawMessage(raw)})
}

// ApplyPatch bumps the revision with a compare-and-swap first, so a stale writer changes
// nothing; then deletes, upserts and reorders.
func (r contents) ApplyPatch(ctx context.Context, postID id.ID, baseRevision int64, plan app.BlockWritePlan) (int64, error) {
	n, err := r.q.BumpPostContentRevision(ctx, sqlcgen.BumpPostContentRevisionParams{ID: int64(postID), ContentRevision: baseRevision})
	if err != nil {
		return 0, err
	}
	if n == 0 {
		return 0, app.ErrConcurrentModification
	}
	if len(plan.Deletes) > 0 {
		if err := r.q.DeletePostBlocksByClientIDs(ctx, sqlcgen.DeletePostBlocksByClientIDsParams{PostID: int64(postID), ClientBlockIds: plan.Deletes}); err != nil {
			return 0, err
		}
	}
	if err := r.upsert(ctx, postID, plan.Upserts); err != nil {
		return 0, err
	}
	if len(plan.Order) > 0 {
		if err := r.q.ApplyPostBlockOrder(ctx, sqlcgen.ApplyPostBlockOrderParams{PostID: int64(postID), Now: r.clock.Now(), ClientBlockIds: plan.Order}); err != nil {
			return 0, err
		}
	}
	return baseRevision + 1, nil
}

func (r contents) ReplaceBlocks(ctx context.Context, postID id.ID, blocks []contentblocks.Block) (int64, error) {
	if err := r.q.DeletePostBlocks(ctx, int64(postID)); err != nil {
		return 0, err
	}
	if err := r.upsert(ctx, postID, blocks); err != nil {
		return 0, err
	}
	return r.q.ForceBumpPostContentRevision(ctx, int64(postID))
}
