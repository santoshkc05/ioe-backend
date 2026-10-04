package app

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/contentblocks"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

// BlockInput is the wire-level shape for one content block. ClientBlockID is
// the durable, client-minted identity for the block; empty when the caller is
// a pre-block client, in which case buildContent mints a server-side id
// rather than rejecting the write.
type BlockInput struct {
	ClientBlockID string
	Type          string
	Body          string
	URL           string
	DurationMs    int64
	QuizID        string
	MediaAssetID  string
	Alt           string
	Caption       string
	Layout        string
	Alignment     string
	Rotation      int
	Width         int
	Height        int
	Mode          string
	Title         string
	Cards         []FlashcardCardInput
}

// FlashcardCardInput is the wire-level shape for one card in a flashcard deck.
type FlashcardCardInput struct {
	ClientCardID string
	Front        string
	Back         string
	Hint         string
	MediaAssetID string
	Alignment    string
	Rotation     int
	Width        int
	Height       int
}

// LegacyContent is the pre-block request shape: optional text body and video URL.
type LegacyContent struct {
	TextBody        string
	VideoURL        string
	VideoDurationMs int64
}

// newServerMintedClientBlockID mints a server-side client block id for a block
// whose caller supplied none (a pre-block client, or the legacy scalar
// text_body/video_url path). The "srv-" prefix exists so a row whose identity
// was minted server-side is recognisable in a support query.
func newServerMintedClientBlockID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		// crypto/rand failing indicates a broken host RNG; there is no
		// sensible fallback that preserves the uniqueness this id exists
		// for, so surface it loudly rather than mint a colliding id.
		panic("courseauthoring: crypto/rand unavailable: " + err.Error())
	}
	return "srv-" + hex.EncodeToString(buf[:])
}

// buildBlock constructs one contentblocks.Block from a wire-level BlockInput.
// Every failure wraps ErrInvalidInput; references (quiz, media asset) are
// stored unchecked.
func buildBlock(blockID id.ID, clientBlockID string, position int, in BlockInput) (contentblocks.Block, error) {
	switch contentblocks.BlockType(in.Type) {
	case contentblocks.BlockTypeText:
		body, err := contentblocks.NewSanitizedTextBody(in.Body)
		if err != nil {
			return contentblocks.Block{}, fmt.Errorf("%w: %w", ErrInvalidInput, err)
		}
		return contentblocks.NewTextBlock(blockID, clientBlockID, position, body), nil
	case contentblocks.BlockTypeVideo:
		var video contentblocks.VideoRef
		var err error
		if in.MediaAssetID != "" {
			if strings.TrimSpace(in.URL) != "" {
				return contentblocks.Block{}, fmt.Errorf("%w: video block must use either url or media_asset_id", ErrInvalidInput)
			}
			assetID, parseErr := id.Parse(in.MediaAssetID)
			if parseErr != nil {
				return contentblocks.Block{}, fmt.Errorf("%w: media_asset_id: %w", ErrInvalidInput, parseErr)
			}
			video, err = contentblocks.NewMediaVideoRef(assetID, time.Duration(in.DurationMs)*time.Millisecond)
		} else {
			video, err = contentblocks.NewVideoRef(in.URL, time.Duration(in.DurationMs)*time.Millisecond)
		}
		if err != nil {
			return contentblocks.Block{}, fmt.Errorf("%w: %w", ErrInvalidInput, err)
		}
		return contentblocks.NewVideoBlock(blockID, clientBlockID, position, video), nil
	case contentblocks.BlockTypeQuiz:
		quizID, err := id.Parse(in.QuizID)
		if err != nil {
			return contentblocks.Block{}, fmt.Errorf("%w: quiz_id: %w", ErrInvalidInput, err)
		}
		block, err := contentblocks.NewQuizBlock(blockID, clientBlockID, position, quizID)
		if err != nil {
			return contentblocks.Block{}, fmt.Errorf("%w: %w", ErrInvalidInput, err)
		}
		return block, nil
	case contentblocks.BlockTypeImage:
		var assetID id.ID
		if in.MediaAssetID != "" {
			if strings.TrimSpace(in.URL) != "" {
				return contentblocks.Block{}, fmt.Errorf("%w: image block must use either url or media_asset_id", ErrInvalidInput)
			}
			parsed, parseErr := id.Parse(in.MediaAssetID)
			if parseErr != nil {
				return contentblocks.Block{}, fmt.Errorf("%w: media_asset_id: %w", ErrInvalidInput, parseErr)
			}
			assetID = parsed
		}
		ref, err := contentblocks.NewImageRef(in.URL, assetID, in.Alt, in.Caption, in.Layout, in.Alignment, in.Rotation, in.Width, in.Height)
		if err != nil {
			return contentblocks.Block{}, fmt.Errorf("%w: %w", ErrInvalidInput, err)
		}
		return contentblocks.NewImageBlock(blockID, clientBlockID, position, ref), nil
	case contentblocks.BlockTypeFlashcard:
		cards := make([]contentblocks.Flashcard, len(in.Cards))
		for i, card := range in.Cards {
			var assetID id.ID
			if card.MediaAssetID != "" {
				parsed, parseErr := id.Parse(card.MediaAssetID)
				if parseErr != nil {
					return contentblocks.Block{}, fmt.Errorf("%w: media_asset_id: %w", ErrInvalidInput, parseErr)
				}
				assetID = parsed
			}
			cards[i] = contentblocks.Flashcard{
				ClientCardID: card.ClientCardID,
				Front:        card.Front, Back: card.Back, Hint: card.Hint, MediaAssetID: assetID,
				Alignment: card.Alignment, Rotation: card.Rotation, Width: card.Width, Height: card.Height,
			}
		}
		deck, err := contentblocks.NewSanitizedFlashcardDeck(in.Mode, in.Title, cards)
		if err != nil {
			return contentblocks.Block{}, fmt.Errorf("%w: %w", ErrInvalidInput, err)
		}
		return contentblocks.NewFlashcardBlock(blockID, clientBlockID, position, deck), nil
	default:
		return contentblocks.Block{}, fmt.Errorf("%w: invalid block type %q", ErrInvalidInput, in.Type)
	}
}

// buildContent constructs a domain.LectureContent from a block list or, when
// in is nil, from the legacy scalar shape. A non-nil block list is exclusive
// and legacy is ignored; otherwise a text block is created from
// legacy.TextBody when non-empty and a video block from legacy.VideoURL when
// non-empty, each with a server-minted client block ID. Block row IDs come
// from ids.
func buildContent(ids *id.Generator, in []BlockInput, legacy LegacyContent) (domain.LectureContent, error) {
	if in != nil {
		blocks := make([]contentblocks.Block, 0, len(in))
		for position, input := range in {
			// A pre-block client (or a request that omitted the field) sends
			// no client_block_id; mint a server-side one so the row-level
			// natural key exists.
			clientBlockID := input.ClientBlockID
			if clientBlockID == "" {
				clientBlockID = newServerMintedClientBlockID()
			}
			block, err := buildBlock(ids.New(), clientBlockID, position, input)
			if err != nil {
				return domain.LectureContent{}, err
			}
			blocks = append(blocks, block)
		}
		content, err := domain.NewLectureContent(blocks)
		if err != nil {
			return domain.LectureContent{}, fmt.Errorf("%w: %w", ErrInvalidInput, err)
		}
		return content, nil
	}
	var blocks []contentblocks.Block
	if strings.TrimSpace(legacy.TextBody) != "" {
		body, err := contentblocks.NewSanitizedTextBody(legacy.TextBody)
		if err != nil {
			return domain.LectureContent{}, fmt.Errorf("%w: %w", ErrInvalidInput, err)
		}
		blocks = append(blocks, contentblocks.NewTextBlock(ids.New(), newServerMintedClientBlockID(), len(blocks), body))
	}
	if vurl := trimmedOrEmpty(legacy.VideoURL); vurl != "" {
		video, err := contentblocks.NewVideoRef(vurl, time.Duration(legacy.VideoDurationMs)*time.Millisecond)
		if err != nil {
			return domain.LectureContent{}, fmt.Errorf("%w: %w", ErrInvalidInput, err)
		}
		blocks = append(blocks, contentblocks.NewVideoBlock(ids.New(), newServerMintedClientBlockID(), len(blocks), video))
	}
	content, err := domain.NewLectureContent(blocks)
	if err != nil {
		return domain.LectureContent{}, fmt.Errorf("%w: %w", ErrInvalidInput, err)
	}
	return content, nil
}

func trimmedOrEmpty(s string) string {
	if s == "" {
		return ""
	}
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t' || s[0] == '\n' || s[0] == '\r') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t' || s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}
