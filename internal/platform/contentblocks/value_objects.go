package contentblocks

import (
	"strings"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

const (
	maxTitleLength     = 200
	maxTextBodySize    = 1 << 20 // 1 MiB of Markdown per text block.
	maxImageAltLen     = 500
	maxImageCaptionLen = 500
)

// Title is a validated, trimmed non-empty string used for any titled piece
// of content - a course, a lecture, a section, a blog post.
type Title struct {
	value string
}

func NewTitle(raw string) (Title, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return Title{}, ErrEmptyTitle
	}
	if len(trimmed) > maxTitleLength {
		return Title{}, ErrTitleTooLong
	}
	return Title{value: trimmed}, nil
}

func (t Title) String() string { return t.value }
func (t Title) IsZero() bool   { return t.value == "" }

// TextBody is a validated, non-empty block of text. It is intentionally a
// distinct type from a plain string so an empty/whitespace body can't sneak
// past validation.
type TextBody struct {
	value string
}

func NewTextBody(raw string) (TextBody, error) {
	if strings.TrimSpace(raw) == "" {
		return TextBody{}, ErrEmptyTextContent
	}
	if len(raw) > maxTextBodySize {
		return TextBody{}, ErrTextContentTooLarge
	}
	// Markdown is source content, not a display label: preserve it byte for
	// byte so fenced code blocks and intentional leading/trailing whitespace
	// round-trip through the editor without normalization.
	return TextBody{value: raw}, nil
}

func (b TextBody) String() string { return b.value }
func (b TextBody) IsZero() bool   { return b.value == "" }

// NewSanitizedTextBody is NewTextBody's write-time counterpart (ask B11,
// docs/blog-backend-phase2-blog-authoring.md §2.6): it runs the HTML
// allowlist in sanitize.go before validating size/non-emptiness, so a
// caller building a TextBody from untrusted request input gets both
// checks. NewTextBody itself is unchanged and stays the one used to decode
// already-persisted content (DecodePayload) - re-validating a row written
// before this sanitizer existed must never make it fail to load.
func NewSanitizedTextBody(raw string) (TextBody, error) {
	clean, err := SanitizeHTML(raw)
	if err != nil {
		return TextBody{}, err
	}
	return NewTextBody(clean)
}

// VideoRef points either at a legacy external URL or at a managed media
// asset. Delivery details remain owned by media and are resolved by each
// context's own read model rather than persisted into block content.
type VideoRef struct {
	url          string
	mediaAssetID id.ID
	duration     time.Duration
}

func NewVideoRef(url string, duration time.Duration) (VideoRef, error) {
	trimmed := strings.TrimSpace(url)
	if trimmed == "" {
		return VideoRef{}, ErrEmptyVideoURL
	}
	if duration < 0 {
		return VideoRef{}, ErrInvalidVideoDuration
	}
	return VideoRef{url: trimmed, duration: duration}, nil
}

func NewMediaVideoRef(mediaAssetID id.ID, duration time.Duration) (VideoRef, error) {
	if mediaAssetID.IsZero() {
		return VideoRef{}, ErrEmptyVideoURL
	}
	if duration < 0 {
		return VideoRef{}, ErrInvalidVideoDuration
	}
	return VideoRef{mediaAssetID: mediaAssetID, duration: duration}, nil
}

func (v VideoRef) URL() string             { return v.url }
func (v VideoRef) MediaAssetID() id.ID     { return v.mediaAssetID }
func (v VideoRef) Duration() time.Duration { return v.duration }
func (v VideoRef) IsZero() bool            { return v.url == "" && v.mediaAssetID.IsZero() }

// ImageRef points either at an external image URL or at a managed media
// asset (mutually exclusive, mirroring VideoRef). width/height are
// client-measured natural pixels, persisted so the reader can reserve
// layout space before the URL resolves.
type ImageRef struct {
	url           string
	mediaAssetID  id.ID
	alt           string
	caption       string
	layout        string // "inline" | "wide"
	alignment     string // "left" | "center" | "right"
	rotation      int    // 0-359 whole degrees clockwise
	width, height int
}

func NewImageRef(url string, mediaAssetID id.ID, alt, caption, layout, alignment string, rotation, width, height int) (ImageRef, error) {
	trimmedURL := strings.TrimSpace(url)
	hasURL := trimmedURL != ""
	hasAsset := !mediaAssetID.IsZero()
	if !hasURL && !hasAsset {
		return ImageRef{}, ErrEmptyImageRef
	}
	if hasURL && hasAsset {
		return ImageRef{}, ErrAmbiguousImageRef
	}
	// Empty alt is valid here: the publish gate is where it's enforced by
	// each host context, because rejecting a save would stop an author
	// mid-draft.
	if len(alt) > maxImageAltLen {
		return ImageRef{}, ErrImageAltTooLong
	}
	if len(caption) > maxImageCaptionLen {
		return ImageRef{}, ErrImageCaptionTooLong
	}
	if layout == "" {
		layout = "inline"
	}
	if layout != "inline" && layout != "wide" {
		return ImageRef{}, ErrInvalidImageLayout
	}
	if alignment == "" {
		alignment = "left"
	}
	if alignment != "left" && alignment != "center" && alignment != "right" {
		return ImageRef{}, ErrInvalidImageAlignment
	}
	if rotation < 0 || rotation > 359 {
		return ImageRef{}, ErrInvalidImageRotation
	}
	if width < 0 || height < 0 {
		return ImageRef{}, ErrInvalidImageDimensions
	}
	return ImageRef{url: trimmedURL, mediaAssetID: mediaAssetID, alt: alt, caption: caption, layout: layout, alignment: alignment, rotation: rotation, width: width, height: height}, nil
}

func (i ImageRef) URL() string         { return i.url }
func (i ImageRef) MediaAssetID() id.ID { return i.mediaAssetID }
func (i ImageRef) Alt() string         { return i.alt }
func (i ImageRef) Caption() string     { return i.caption }
func (i ImageRef) Layout() string      { return i.layout }
func (i ImageRef) Alignment() string   { return i.alignment }
func (i ImageRef) Rotation() int       { return i.rotation }
func (i ImageRef) Width() int          { return i.width }
func (i ImageRef) Height() int         { return i.height }
func (i ImageRef) IsZero() bool        { return i.url == "" && i.mediaAssetID.IsZero() }

const (
	maxFlashcardTitleLen = 200
	maxFlashcardFrontLen = 500
	maxFlashcardBackLen  = 2000
	maxFlashcardHintLen  = 200
	minFlashcardCards    = 1
	maxFlashcardCards    = 40
	maxFlashcardDeckSize = 256 << 10 // total payload guard so a pathological deck can't bloat a read.
)

// Flashcard is one card in a FlashcardDeck: front, back, an optional hint,
// and an optional per-card diagram. front/back are Markdown, preserved byte
// for byte for the reason NewTextBody documents: fenced code and
// deliberate whitespace must round-trip.
type Flashcard struct {
	Front        string
	Back         string
	Hint         string
	MediaAssetID id.ID
	Alignment    string
	Rotation     int
	Width        int
	Height       int
	// ClientCardID is an opaque, client-minted identity for one card in a
	// deck (mirrors Block.clientBlockID). Cards have no rows of their own -
	// this exists purely so the frontend's React keys and content hashes
	// survive a re-read; blank is valid, and no uniqueness is enforced.
	ClientCardID string
}

// FlashcardDeck is an ordered deck of cards, carried inline in a flashcard
// block's payload rather than as a separate referenced resource: a deck
// bears no attempts, no grade, and no other reader, unlike a quiz.
type FlashcardDeck struct {
	mode  string // "steps" | "recall"
	title string
	cards []Flashcard
}

func NewFlashcardDeck(mode, title string, cards []Flashcard) (FlashcardDeck, error) {
	if mode == "" {
		mode = "steps"
	}
	if mode != "steps" && mode != "recall" {
		return FlashcardDeck{}, ErrInvalidFlashcardMode
	}
	if len(title) > maxFlashcardTitleLen {
		return FlashcardDeck{}, ErrInvalidFlashcardDeck
	}
	if len(cards) < minFlashcardCards || len(cards) > maxFlashcardCards {
		return FlashcardDeck{}, ErrInvalidFlashcardDeck
	}
	size := len(title)
	copyCards := make([]Flashcard, len(cards))
	for i, card := range cards {
		if strings.TrimSpace(card.Front) == "" || len(card.Front) > maxFlashcardFrontLen {
			return FlashcardDeck{}, ErrInvalidFlashcard
		}
		if strings.TrimSpace(card.Back) == "" || len(card.Back) > maxFlashcardBackLen {
			return FlashcardDeck{}, ErrInvalidFlashcard
		}
		if len(card.Hint) > maxFlashcardHintLen {
			return FlashcardDeck{}, ErrInvalidFlashcard
		}
		size += len(card.Front) + len(card.Back) + len(card.Hint)
		copyCards[i] = card
	}
	if size > maxFlashcardDeckSize {
		return FlashcardDeck{}, ErrFlashcardDeckTooLarge
	}
	return FlashcardDeck{mode: mode, title: title, cards: copyCards}, nil
}

func (d FlashcardDeck) Mode() string       { return d.mode }
func (d FlashcardDeck) Title() string      { return d.title }
func (d FlashcardDeck) Cards() []Flashcard { return append([]Flashcard(nil), d.cards...) }
func (d FlashcardDeck) IsZero() bool       { return len(d.cards) == 0 }

// NewSanitizedFlashcardDeck is NewFlashcardDeck's write-time counterpart
// (ask B11, §2.6): each card's Front/Back/Hint passes through the same HTML
// allowlist NewSanitizedTextBody applies, before the deck's own validation
// runs. Like NewSanitizedTextBody, NewFlashcardDeck itself is unchanged and
// stays the one DecodePayload uses to read already-persisted decks.
func NewSanitizedFlashcardDeck(mode, title string, cards []Flashcard) (FlashcardDeck, error) {
	clean := make([]Flashcard, len(cards))
	for i, card := range cards {
		front, err := SanitizeHTML(card.Front)
		if err != nil {
			return FlashcardDeck{}, err
		}
		back, err := SanitizeHTML(card.Back)
		if err != nil {
			return FlashcardDeck{}, err
		}
		hint, err := SanitizeHTML(card.Hint)
		if err != nil {
			return FlashcardDeck{}, err
		}
		card.Front, card.Back, card.Hint = front, back, hint
		clean[i] = card
	}
	return NewFlashcardDeck(mode, title, clean)
}
