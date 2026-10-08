package domain

import (
	"errors"
	"regexp"
	"strings"

	"github.com/santoshkc2200/ioe-backend/internal/platform/contentblocks"
)

const wordsPerMinute = 220

var htmlTag = regexp.MustCompile(`<[^>]*>`)

// NewContent validates blocks with contentblocks and the blog policy: text and https
// images only. It returns the blocks renumbered by slice order.
func NewContent(blocks []contentblocks.Block) ([]contentblocks.Block, error) {
	valid, err := contentblocks.ValidateBlocks(blocks)
	if err != nil {
		return nil, err
	}
	for _, b := range valid {
		switch b.Type() {
		case contentblocks.BlockTypeText:
		case contentblocks.BlockTypeImage:
			img, _ := b.Image()
			if !img.MediaAssetID().IsZero() || !isHTTPSURL(img.URL(), maxCoverURLLen) {
				return nil, ErrInvalidImage
			}
		default:
			return nil, ErrBlockKindNotAllowed
		}
	}
	return valid, nil
}

// ReadingMinutes estimates reading time from text blocks: 220 words per minute, rounded
// up, at least 1.
func ReadingMinutes(blocks []contentblocks.Block) int {
	words := 0
	for _, b := range blocks {
		if body, ok := b.Text(); ok {
			words += len(strings.Fields(htmlTag.ReplaceAllString(body.String(), " ")))
		}
	}
	return max((words+wordsPerMinute-1)/wordsPerMinute, 1)
}

// IsPolicyError reports whether err is a blog block-policy rejection rather than a
// generic contentblocks validation failure.
func IsPolicyError(err error) bool {
	return errors.Is(err, ErrBlockKindNotAllowed) || errors.Is(err, ErrInvalidImage)
}
