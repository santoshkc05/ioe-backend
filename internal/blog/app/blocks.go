package app

import (
	"fmt"

	"github.com/santoshkc2200/ioe-backend/internal/platform/contentblocks"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

// BlockInput is the wire-level shape of one text or image block.
type BlockInput struct {
	ClientBlockID string
	Type          string
	Body          string
	URL           string
	Alt           string
	Caption       string
	Layout        string
	Alignment     string
	Rotation      int
	Width         int
	Height        int
}

// buildBlock constructs a text or image block. Other kinds are rejected later by
// domain.NewContent; here they are invalid input because blog has no wire shape for them.
func buildBlock(blockID id.ID, position int, in BlockInput) (contentblocks.Block, error) {
	if err := contentblocks.ValidateClientBlockID(in.ClientBlockID); err != nil {
		return contentblocks.Block{}, fmt.Errorf("%w: %w", ErrInvalidInput, err)
	}
	switch contentblocks.BlockType(in.Type) {
	case contentblocks.BlockTypeText:
		body, err := contentblocks.NewSanitizedTextBody(in.Body)
		if err != nil {
			return contentblocks.Block{}, fmt.Errorf("%w: %w", ErrInvalidInput, err)
		}
		return contentblocks.NewTextBlock(blockID, in.ClientBlockID, position, body), nil
	case contentblocks.BlockTypeImage:
		ref, err := contentblocks.NewImageRef(in.URL, 0, in.Alt, in.Caption, in.Layout, in.Alignment, in.Rotation, in.Width, in.Height)
		if err != nil {
			return contentblocks.Block{}, fmt.Errorf("%w: %w", ErrInvalidInput, err)
		}
		return contentblocks.NewImageBlock(blockID, in.ClientBlockID, position, ref), nil
	default:
		return contentblocks.Block{}, fmt.Errorf("%w: block type must be text or image, got %q", ErrInvalidInput, in.Type)
	}
}
