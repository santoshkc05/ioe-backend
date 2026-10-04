package contentblocks

import "errors"

var (
	ErrEmptyTitle             = errors.New("contentblocks: title must not be empty")
	ErrTitleTooLong           = errors.New("contentblocks: title exceeds maximum length")
	ErrEmptyTextContent       = errors.New("contentblocks: text block body must not be empty")
	ErrTextContentTooLarge    = errors.New("contentblocks: text block body exceeds maximum size")
	ErrEmptyVideoURL          = errors.New("contentblocks: video block url must not be empty")
	ErrInvalidVideoDuration   = errors.New("contentblocks: video duration must be non-negative")
	ErrInvalidBlockType       = errors.New("contentblocks: invalid block type")
	ErrInvalidQuizReference   = errors.New("contentblocks: quiz block requires a quiz id")
	ErrEmptyImageRef          = errors.New("contentblocks: image block requires either a url or a media asset id")
	ErrAmbiguousImageRef      = errors.New("contentblocks: image block must use either url or media_asset_id, not both")
	ErrImageAltTooLong        = errors.New("contentblocks: image alt text exceeds maximum length")
	ErrImageCaptionTooLong    = errors.New("contentblocks: image caption exceeds maximum length")
	ErrInvalidImageLayout     = errors.New("contentblocks: image layout must be inline or wide")
	ErrInvalidImageAlignment  = errors.New("contentblocks: image alignment must be left, center, or right")
	ErrInvalidImageRotation   = errors.New("contentblocks: image rotation must be a whole degree from 0 through 359")
	ErrInvalidImageDimensions = errors.New("contentblocks: image width/height must be non-negative")
	ErrInvalidFlashcardDeck   = errors.New("contentblocks: flashcard deck must have between 1 and 40 cards")
	ErrInvalidFlashcardMode   = errors.New("contentblocks: flashcard deck mode must be steps or recall")
	ErrInvalidFlashcard       = errors.New("contentblocks: flashcard front/back/hint is invalid or too long")
	ErrFlashcardDeckTooLarge  = errors.New("contentblocks: flashcard deck payload exceeds the size limit")
	ErrInvalidClientBlockID   = errors.New("contentblocks: client block id is empty, too long, or contains control characters")
	ErrDuplicateClientBlockID = errors.New("contentblocks: two blocks share a client block id")

	// ErrUnsafeContent is returned by NewSanitizedTextBody/NewSanitizedFlashcardDeck
	// (sanitize.go, ask B11) when the input contains a tag, attribute or
	// class outside the write-time HTML allowlist. Returned instead of
	// silently storing a stripped body.
	ErrUnsafeContent = errors.New("contentblocks: content contains disallowed HTML and was rejected rather than silently modified")

	ErrEmptyRichDoc       = errors.New("contentblocks: rich document is empty")
	ErrRichDocTooLarge    = errors.New("contentblocks: rich document exceeds the maximum size")
	ErrInvalidRichDocNode = errors.New("contentblocks: rich document contains a node type that is not allowed")
	ErrInvalidRichDocMark = errors.New("contentblocks: rich document contains a mark type that is not allowed")
)
