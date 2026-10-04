package contentblocks

import (
	"strings"

	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

// BlockType is the discriminator for one kind of content block.
type BlockType string

const (
	BlockTypeText      BlockType = "text"
	BlockTypeVideo     BlockType = "video"
	BlockTypeQuiz      BlockType = "quiz"
	BlockTypeImage     BlockType = "image"
	BlockTypeFlashcard BlockType = "flashcard"
)

// ValidKinds returns every block kind this package knows how to validate and
// encode, in the same order as the `kind` CHECK constraint each host
// context's migration declares. A test in this package asserts the two stay
// in sync; adding a sixth kind means updating both deliberately rather than
// having them silently diverge.
func ValidKinds() []BlockType {
	return []BlockType{BlockTypeText, BlockTypeVideo, BlockTypeQuiz, BlockTypeImage, BlockTypeFlashcard}
}

// MaxClientBlockIDLen bounds the column and, more importantly, bounds what a
// client can make the server store per block.
const MaxClientBlockIDLen = 64

// Block is one ordered piece of content: exactly one of its content fields
// is set, selected by kind. id and position are identity/ordering concerns
// a repository fills in on read (see WithIdentity); clientBlockID is the
// client-minted identity that survives a re-read (ask A19).
type Block struct {
	id            id.ID
	clientBlockID string
	kind          BlockType
	position      int
	text          *TextBody
	video         *VideoRef
	quizID        id.ID
	image         *ImageRef
	deck          *FlashcardDeck
}

func NewTextBlock(id id.ID, clientBlockID string, position int, body TextBody) Block {
	return Block{id: id, clientBlockID: clientBlockID, kind: BlockTypeText, position: position, text: &body}
}

func NewVideoBlock(id id.ID, clientBlockID string, position int, video VideoRef) Block {
	return Block{id: id, clientBlockID: clientBlockID, kind: BlockTypeVideo, position: position, video: &video}
}

func NewQuizBlock(id id.ID, clientBlockID string, position int, quizID id.ID) (Block, error) {
	if quizID.IsZero() {
		return Block{}, ErrInvalidQuizReference
	}
	return Block{id: id, clientBlockID: clientBlockID, kind: BlockTypeQuiz, position: position, quizID: quizID}, nil
}

func NewImageBlock(id id.ID, clientBlockID string, position int, ref ImageRef) Block {
	return Block{id: id, clientBlockID: clientBlockID, kind: BlockTypeImage, position: position, image: &ref}
}

func NewFlashcardBlock(id id.ID, clientBlockID string, position int, deck FlashcardDeck) Block {
	return Block{id: id, clientBlockID: clientBlockID, kind: BlockTypeFlashcard, position: position, deck: &deck}
}

func (b Block) ID() id.ID             { return b.id }
func (b Block) ClientBlockID() string { return b.clientBlockID }
func (b Block) Type() BlockType       { return b.kind }
func (b Block) Position() int         { return b.position }
func (b Block) QuizID() id.ID         { return b.quizID }
func (b Block) Text() (TextBody, bool) {
	if b.text == nil {
		return TextBody{}, false
	}
	return *b.text, true
}
func (b Block) Video() (VideoRef, bool) {
	if b.video == nil {
		return VideoRef{}, false
	}
	return *b.video, true
}
func (b Block) Image() (ImageRef, bool) {
	if b.image == nil {
		return ImageRef{}, false
	}
	return *b.image, true
}
func (b Block) Deck() (FlashcardDeck, bool) {
	if b.deck == nil {
		return FlashcardDeck{}, false
	}
	return *b.deck, true
}

// WithIdentity returns a copy of b with its row-owned fields (id, client
// block id, position) replaced. Repositories use this to stamp identity
// onto a Block decoded from a payload by DecodePayload, which knows nothing
// about rows - see the package doc comment.
func (b Block) WithIdentity(id id.ID, clientBlockID string, position int) Block {
	b.id = id
	b.clientBlockID = clientBlockID
	b.position = position
	return b
}

// ValidateClientBlockID enforces ask A19's identity rule: non-empty after
// trimming, bounded length, and no control characters. Deliberately not a
// shape requirement (e.g. UUID) - a decimal-snowflake backfill and the wire
// protocol both treat the field as opaque.
func ValidateClientBlockID(id string) error {
	if strings.TrimSpace(id) == "" || len(id) > MaxClientBlockIDLen {
		return ErrInvalidClientBlockID
	}
	for _, r := range id {
		if r < 0x20 || r == 0x7f {
			return ErrInvalidClientBlockID
		}
	}
	return nil
}

// ValidateBlocks copies blocks, reassigns position to match slice order,
// validates every block's client_block_id (non-empty, bounded, no
// duplicates within the set) and validates that each block's kind-specific
// content is actually present. It is the one place "is this an ordered,
// well-formed set of blocks" is decided, so a host context's own content
// wrapper (courseauthoring's LectureContent, blog's PostContent) can build
// on it without re-implementing the rule.
func ValidateBlocks(blocks []Block) ([]Block, error) {
	copyBlocks := make([]Block, len(blocks))
	seenClientIDs := make(map[string]struct{}, len(blocks))
	for i, block := range blocks {
		block.position = i
		if err := ValidateClientBlockID(block.clientBlockID); err != nil {
			return nil, err
		}
		if _, dup := seenClientIDs[block.clientBlockID]; dup {
			return nil, ErrDuplicateClientBlockID
		}
		seenClientIDs[block.clientBlockID] = struct{}{}
		switch block.kind {
		case BlockTypeText:
			if block.text == nil {
				return nil, ErrEmptyTextContent
			}
		case BlockTypeVideo:
			if block.video == nil {
				return nil, ErrEmptyVideoURL
			}
		case BlockTypeQuiz:
			if block.quizID.IsZero() {
				return nil, ErrInvalidQuizReference
			}
		case BlockTypeImage:
			if block.image == nil {
				return nil, ErrEmptyImageRef
			}
		case BlockTypeFlashcard:
			if block.deck == nil {
				return nil, ErrInvalidFlashcardDeck
			}
		default:
			return nil, ErrInvalidBlockType
		}
		copyBlocks[i] = block
	}
	return copyBlocks, nil
}
