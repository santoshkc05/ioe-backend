package app

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/santoshkc2200/ioe-backend/internal/platform/contentblocks"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

// AssetKind is the kind of an uploaded media asset.
type AssetKind string

const (
	AssetVideo AssetKind = "video"
	AssetImage AssetKind = "image"
)

// AssetCatalog reports the kinds of the given asset IDs that belong to courseID. IDs that do
// not exist or belong to another course are absent from the result.
type AssetCatalog interface {
	Kinds(ctx context.Context, courseID id.ID, ids []id.ID) (map[id.ID]AssetKind, error)
}

type assetRef struct {
	id            id.ID
	kind          AssetKind
	clientBlockID string
}

// inputAssetRefs lists the media assets the given inputs reference. IDs that do not parse are
// skipped; block building rejects them as invalid input.
func inputAssetRefs(in []BlockInput) []assetRef {
	var refs []assetRef
	add := func(raw string, kind AssetKind, clientBlockID string) {
		if raw == "" {
			return
		}
		if v, err := id.Parse(raw); err == nil {
			refs = append(refs, assetRef{id: v, kind: kind, clientBlockID: clientBlockID})
		}
	}
	for _, b := range in {
		switch contentblocks.BlockType(b.Type) {
		case contentblocks.BlockTypeVideo:
			add(b.MediaAssetID, AssetVideo, b.ClientBlockID)
		case contentblocks.BlockTypeImage:
			add(b.MediaAssetID, AssetImage, b.ClientBlockID)
		case contentblocks.BlockTypeFlashcard:
			for _, c := range b.Cards {
				add(c.MediaAssetID, AssetImage, b.ClientBlockID)
			}
		}
	}
	return refs
}

// checkAssetRefs returns refErr when a reference is unknown, foreign, or of the wrong kind,
// and err when the catalog itself failed. Callers consult it before opening a transaction
// and return refErr only after authorizing the caller, so a non-manager learns nothing
// about other courses' assets.
func checkAssetRefs(ctx context.Context, catalog AssetCatalog, courseID id.ID, refs []assetRef) (refErr, err error) {
	if len(refs) == 0 {
		return nil, nil
	}
	ids := make([]id.ID, len(refs))
	for i, r := range refs {
		ids[i] = r.id
	}
	kinds, err := catalog.Kinds(ctx, courseID, ids)
	if err != nil {
		return nil, err
	}
	for _, r := range refs {
		if kinds[r.id] != r.kind {
			return fmt.Errorf("%w: block %q references media asset %s, which is not a %s in this course",
				ErrInvalidMediaReference, r.clientBlockID, r.id, r.kind), nil
		}
	}
	return nil, nil
}

// blocksReference reports whether any block references assetID as a video, an image, or a
// flashcard card image.
func blocksReference(blocks []contentblocks.Block, assetID id.ID) bool {
	for _, b := range blocks {
		if v, ok := b.Video(); ok && v.MediaAssetID() == assetID {
			return true
		}
		if img, ok := b.Image(); ok && img.MediaAssetID() == assetID {
			return true
		}
		if deck, ok := b.Deck(); ok {
			for _, c := range deck.Cards() {
				if c.MediaAssetID == assetID {
					return true
				}
			}
		}
	}
	return false
}

// blockAssetRefs lists the media assets stored blocks reference.
func blockAssetRefs(blocks []contentblocks.Block) []assetRef {
	var refs []assetRef
	for _, b := range blocks {
		cid := b.ClientBlockID()
		if v, ok := b.Video(); ok {
			refs = append(refs, assetRef{id: v.MediaAssetID(), kind: AssetVideo, clientBlockID: cid})
		}
		if img, ok := b.Image(); ok {
			refs = append(refs, assetRef{id: img.MediaAssetID(), kind: AssetImage, clientBlockID: cid})
		}
		if deck, ok := b.Deck(); ok {
			for _, c := range deck.Cards() {
				if !c.MediaAssetID.IsZero() {
					refs = append(refs, assetRef{id: c.MediaAssetID, kind: AssetImage, clientBlockID: cid})
				}
			}
		}
	}
	return refs
}

// AssetUsage returns the lectures, by ID, whose working-copy or live-version blocks reference
// assetID. It applies no authorization: media calls it after authorizing a delete.
func (s *ContentService) AssetUsage(ctx context.Context, courseID, assetID id.ID) ([]id.ID, error) {
	used := map[id.ID]struct{}{}
	err := s.tx.RunInTx(ctx, func(r Repos) error {
		c, err := r.Courses.FindByID(ctx, courseID)
		if err != nil {
			return err
		}
		for _, l := range c.Lectures {
			blocks, err := r.Contents.ListBlocks(ctx, l.ID)
			if err != nil {
				return err
			}
			if blocksReference(blocks, assetID) {
				used[l.ID] = struct{}{}
			}
		}
		if !c.IsLive() {
			return nil
		}
		live, err := r.Courses.FindVersion(ctx, courseID, c.Live.Number)
		if err != nil {
			return err
		}
		for _, l := range live.Lectures {
			blocks, err := r.Contents.ListVersionBlocks(ctx, courseID, c.Live.Number, l.ID)
			if err != nil {
				return err
			}
			if blocksReference(blocks, assetID) {
				used[l.ID] = struct{}{}
			}
		}
		return nil
	})
	return slices.Sorted(maps.Keys(used)), err
}
