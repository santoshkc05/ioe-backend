package domain

import "github.com/santoshkc2200/ioe-backend/internal/platform/contentblocks"

// LectureContent is a validated, ordered block list.
type LectureContent struct{ blocks []contentblocks.Block }

// NewLectureContent validates blocks (identity, per-kind rules) and renumbers positions by slice order.
func NewLectureContent(blocks []contentblocks.Block) (LectureContent, error) {
	valid, err := contentblocks.ValidateBlocks(blocks)
	if err != nil {
		return LectureContent{}, err
	}
	return LectureContent{blocks: valid}, nil
}

func (c LectureContent) Blocks() []contentblocks.Block {
	return append([]contentblocks.Block(nil), c.blocks...)
}

func (c LectureContent) HasText() bool  { return c.has(contentblocks.BlockTypeText) }
func (c LectureContent) HasVideo() bool { return c.has(contentblocks.BlockTypeVideo) }

func (c LectureContent) has(kind contentblocks.BlockType) bool {
	for _, b := range c.blocks {
		if b.Type() == kind {
			return true
		}
	}
	return false
}
