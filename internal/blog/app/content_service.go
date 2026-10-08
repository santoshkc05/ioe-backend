package app

import (
	"context"
	"fmt"

	"github.com/santoshkc2200/ioe-backend/internal/blog/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/contentblocks"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

const (
	maxBlocks          = 200
	maxPatchOrderLen   = 500
	maxPatchUpsertsLen = 200
	maxPatchDeletesLen = 500
)

// ContentService reads and writes a post's draft blocks.
type ContentService struct {
	tx  TxRunner
	ids *id.Generator
}

func NewContentService(tx TxRunner, ids *id.Generator) *ContentService {
	return &ContentService{tx: tx, ids: ids}
}

// ContentView is a post's draft content.
type ContentView struct {
	PostID          id.ID
	ContentRevision int64
	Blocks          []contentblocks.Block
}

type PatchInput struct {
	BaseRevision *int64
	Order        []string
	Upserts      []BlockInput
	Deletes      []string
}

func readContent(ctx context.Context, r Repos, h ContentHeader) (ContentView, error) {
	blocks, err := r.Contents.ListBlocks(ctx, h.PostID)
	return ContentView{PostID: h.PostID, ContentRevision: h.ContentRevision, Blocks: blocks}, err
}

func (s *ContentService) Get(ctx context.Context, p auth.Principal, postID id.ID) (ContentView, error) {
	var v ContentView
	err := s.tx.RunInTx(ctx, func(r Repos) error {
		if _, err := loadManaged(ctx, r, p, postID); err != nil {
			return err
		}
		h, err := r.Contents.FindHeader(ctx, postID)
		if err != nil {
			return err
		}
		v, err = readContent(ctx, r, h)
		return err
	})
	return v, err
}

// lockEditable locks the post row first, then checks the caller manages a non-archived
// post. Locking first means an archive that commits meanwhile is seen.
func lockEditable(ctx context.Context, r Repos, p auth.Principal, postID id.ID) (ContentHeader, error) {
	h, err := r.Contents.FindHeaderForUpdate(ctx, postID)
	if err != nil {
		return ContentHeader{}, err
	}
	post, err := loadManaged(ctx, r, p, postID)
	if err != nil {
		return ContentHeader{}, err
	}
	if post.Status == domain.StatusArchived {
		return ContentHeader{}, domain.ErrPostArchived
	}
	return h, nil
}

func validContent(blocks []contentblocks.Block) error {
	if _, err := domain.NewContent(blocks); err != nil {
		if domain.IsPolicyError(err) {
			return err
		}
		return fmt.Errorf("%w: %w", ErrInvalidInput, err)
	}
	return nil
}

// Replace swaps the whole block list and returns the new revision.
func (s *ContentService) Replace(ctx context.Context, p auth.Principal, postID id.ID, in []BlockInput) (int64, error) {
	if len(in) > maxBlocks {
		return 0, ErrPatchTooLarge
	}
	blocks := make([]contentblocks.Block, 0, len(in))
	for i, b := range in {
		block, err := buildBlock(s.ids.New(), i, b)
		if err != nil {
			return 0, err
		}
		blocks = append(blocks, block)
	}
	if err := validContent(blocks); err != nil {
		return 0, err
	}
	var rev int64
	err := s.tx.RunInTx(ctx, func(r Repos) error {
		if _, err := lockEditable(ctx, r, p, postID); err != nil {
			return err
		}
		var err error
		rev, err = r.Contents.ReplaceBlocks(ctx, postID, blocks)
		return err
	})
	return rev, err
}

// Patch applies an incremental diff against base_revision and returns the new revision.
func (s *ContentService) Patch(ctx context.Context, p auth.Principal, postID id.ID, in PatchInput) (int64, error) {
	if in.BaseRevision == nil {
		return 0, ErrRevisionRequired
	}
	if len(in.Order) > maxPatchOrderLen || len(in.Upserts) > maxPatchUpsertsLen || len(in.Deletes) > maxPatchDeletesLen {
		return 0, ErrPatchTooLarge
	}
	upsertIDs := make([]string, len(in.Upserts))
	upserts := make(map[string]BlockInput, len(in.Upserts))
	for i, u := range in.Upserts {
		if err := contentblocks.ValidateClientBlockID(u.ClientBlockID); err != nil {
			return 0, fmt.Errorf("%w: %w", ErrInvalidInput, err)
		}
		upsertIDs[i] = u.ClientBlockID
		upserts[u.ClientBlockID] = u
	}
	if err := contentblocks.CheckPatchLists(in.Order, upsertIDs, in.Deletes); err != nil {
		return 0, err
	}
	var rev int64
	err := s.tx.RunInTx(ctx, func(r Repos) error {
		h, err := lockEditable(ctx, r, p, postID)
		if err != nil {
			return err
		}
		if h.ContentRevision != *in.BaseRevision {
			current, err := readContent(ctx, r, h)
			if err != nil {
				return err
			}
			return &RevisionConflictError{Current: current}
		}
		existing, err := r.Contents.ListBlocks(ctx, postID)
		if err != nil {
			return err
		}
		byClient := make(map[string]contentblocks.Block, len(existing))
		existingIDs := make([]string, 0, len(existing))
		for _, b := range existing {
			byClient[b.ClientBlockID()] = b
			existingIDs = append(existingIDs, b.ClientBlockID())
		}
		if err := contentblocks.CheckBlockSet(existingIDs, in.Order, upsertIDs, in.Deletes); err != nil {
			return err
		}
		resulting := make([]contentblocks.Block, 0, len(in.Order))
		var changed []contentblocks.Block
		for i, cid := range in.Order {
			input, isUpsert := upserts[cid]
			if !isUpsert {
				b := byClient[cid]
				resulting = append(resulting, b.WithIdentity(b.ID(), cid, i))
				continue
			}
			blockID := s.ids.New()
			if prev, ok := byClient[cid]; ok {
				blockID = prev.ID() // edits keep the server ID for life
			}
			b, err := buildBlock(blockID, i, input)
			if err != nil {
				return err
			}
			resulting = append(resulting, b)
			changed = append(changed, b)
		}
		if len(resulting) > maxBlocks {
			return ErrPatchTooLarge
		}
		if err := validContent(resulting); err != nil {
			return err
		}
		rev, err = r.Contents.ApplyPatch(ctx, postID, *in.BaseRevision,
			BlockWritePlan{Upserts: changed, Deletes: in.Deletes, Order: in.Order})
		return err
	})
	return rev, err
}
