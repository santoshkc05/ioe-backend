package contentblocks

import (
	"encoding/json"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

// payload is the JSONB shape a block's content is persisted as - one struct
// wide enough for every kind, with omitempty keeping each row's actual bytes
// narrow to whichever kind it is. It is the one encoding every host
// context's `..._blocks` table uses, so two contexts can never encode a
// block two different ways.
type payload struct {
	Body         string       `json:"body,omitempty"`
	URL          string       `json:"url,omitempty"`
	DurationMs   int64        `json:"duration_ms,omitempty"`
	QuizID       string       `json:"quiz_id,omitempty"`
	MediaAssetID string       `json:"media_asset_id,omitempty"`
	Alt          string       `json:"alt,omitempty"`
	Caption      string       `json:"caption,omitempty"`
	Layout       string       `json:"layout,omitempty"`
	Alignment    string       `json:"alignment,omitempty"`
	Rotation     int          `json:"rotation,omitempty"`
	Width        int          `json:"width,omitempty"`
	Height       int          `json:"height,omitempty"`
	Deck         *deckPayload `json:"deck,omitempty"`
}

type deckPayload struct {
	Mode  string        `json:"mode"`
	Title string        `json:"title,omitempty"`
	Cards []cardPayload `json:"cards"`
}

type cardPayload struct {
	ClientCardID string `json:"client_card_id,omitempty"`
	Front        string `json:"front"`
	Back         string `json:"back"`
	Hint         string `json:"hint,omitempty"`
	MediaAssetID string `json:"media_asset_id,omitempty"`
	Alignment    string `json:"alignment,omitempty"`
	Rotation     int    `json:"rotation,omitempty"`
	Width        int    `json:"width,omitempty"`
	Height       int    `json:"height,omitempty"`
}

// CanonicalizePayload round-trips a stored payload through the same
// unmarshal/marshal pair EncodePayload uses, so a caller can compare a
// freshly-encoded payload against a previously-stored one for byte
// identity regardless of how the database's jsonb text output reformatted
// it (e.g. a space after ':').
func CanonicalizePayload(raw []byte) ([]byte, error) {
	var p payload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	return json.Marshal(p)
}

// EncodePayload serializes a block's content into the bytes a `..._blocks`
// table's `payload JSONB` column stores. It knows nothing about the row the
// caller will write - id, client_block_id, position, kind are separate
// columns each context's repository maps itself.
func EncodePayload(b Block) ([]byte, error) {
	p := payload{}
	if text, ok := b.Text(); ok {
		p.Body = text.String()
	}
	if video, ok := b.Video(); ok {
		p.URL = video.URL()
		p.DurationMs = video.Duration().Milliseconds()
		if !video.MediaAssetID().IsZero() {
			p.MediaAssetID = video.MediaAssetID().String()
		}
	}
	if b.Type() == BlockTypeQuiz {
		p.QuizID = b.QuizID().String()
	}
	if image, ok := b.Image(); ok {
		p.URL = image.URL()
		if !image.MediaAssetID().IsZero() {
			p.MediaAssetID = image.MediaAssetID().String()
		}
		p.Alt = image.Alt()
		p.Caption = image.Caption()
		p.Layout = image.Layout()
		p.Alignment = image.Alignment()
		p.Rotation = image.Rotation()
		p.Width = image.Width()
		p.Height = image.Height()
	}
	if deck, ok := b.Deck(); ok {
		cards := make([]cardPayload, len(deck.Cards()))
		for i, card := range deck.Cards() {
			mediaAssetID := ""
			if !card.MediaAssetID.IsZero() {
				mediaAssetID = card.MediaAssetID.String()
			}
			cards[i] = cardPayload{
				ClientCardID: card.ClientCardID,
				Front:        card.Front, Back: card.Back, Hint: card.Hint, MediaAssetID: mediaAssetID,
				Alignment: card.Alignment, Rotation: card.Rotation, Width: card.Width, Height: card.Height,
			}
		}
		p.Deck = &deckPayload{Mode: deck.Mode(), Title: deck.Title(), Cards: cards}
	}
	return json.Marshal(p)
}

// DecodePayload decodes one block's payload bytes into a Block for the
// given kind. The returned Block has a zero id, empty client block id and
// zero position - the caller (a repository, which owns row mapping) stamps
// those on with Block.WithIdentity.
func DecodePayload(kind BlockType, raw []byte) (Block, error) {
	var p payload
	if err := json.Unmarshal(raw, &p); err != nil {
		return Block{}, err
	}
	switch kind {
	case BlockTypeText:
		body, err := NewTextBody(p.Body)
		if err != nil {
			return Block{}, err
		}
		return NewTextBlock(0, "", 0, body), nil
	case BlockTypeVideo:
		var video VideoRef
		var err error
		if p.MediaAssetID != "" {
			assetID, parseErr := id.Parse(p.MediaAssetID)
			if parseErr != nil {
				return Block{}, parseErr
			}
			video, err = NewMediaVideoRef(assetID, time.Duration(p.DurationMs)*time.Millisecond)
		} else {
			video, err = NewVideoRef(p.URL, time.Duration(p.DurationMs)*time.Millisecond)
		}
		if err != nil {
			return Block{}, err
		}
		return NewVideoBlock(0, "", 0, video), nil
	case BlockTypeQuiz:
		quizID, err := id.Parse(p.QuizID)
		if err != nil {
			return Block{}, ErrInvalidQuizReference
		}
		return NewQuizBlock(0, "", 0, quizID)
	case BlockTypeImage:
		var assetID id.ID
		if p.MediaAssetID != "" {
			parsed, parseErr := id.Parse(p.MediaAssetID)
			if parseErr != nil {
				return Block{}, parseErr
			}
			assetID = parsed
		}
		ref, err := NewImageRef(p.URL, assetID, p.Alt, p.Caption, p.Layout, p.Alignment, p.Rotation, p.Width, p.Height)
		if err != nil {
			return Block{}, err
		}
		return NewImageBlock(0, "", 0, ref), nil
	case BlockTypeFlashcard:
		if p.Deck == nil {
			return Block{}, ErrInvalidFlashcardDeck
		}
		cards := make([]Flashcard, len(p.Deck.Cards))
		for i, card := range p.Deck.Cards {
			var assetID id.ID
			if card.MediaAssetID != "" {
				parsed, parseErr := id.Parse(card.MediaAssetID)
				if parseErr != nil {
					return Block{}, parseErr
				}
				assetID = parsed
			}
			cards[i] = Flashcard{
				ClientCardID: card.ClientCardID,
				Front:        card.Front, Back: card.Back, Hint: card.Hint, MediaAssetID: assetID,
				Alignment: card.Alignment, Rotation: card.Rotation, Width: card.Width, Height: card.Height,
			}
		}
		deck, err := NewFlashcardDeck(p.Deck.Mode, p.Deck.Title, cards)
		if err != nil {
			return Block{}, err
		}
		return NewFlashcardBlock(0, "", 0, deck), nil
	default:
		return Block{}, ErrInvalidBlockType
	}
}
