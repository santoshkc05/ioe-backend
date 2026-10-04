package contentblocks

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

func TestNewTitle_TrimsAndValidates(t *testing.T) {
	title, err := NewTitle("  Hello  ")
	if err != nil {
		t.Fatal(err)
	}
	if title.String() != "Hello" {
		t.Fatalf("title = %q, want %q", title.String(), "Hello")
	}
	if _, err := NewTitle("   "); !errors.Is(err, ErrEmptyTitle) {
		t.Fatalf("blank title error = %v, want ErrEmptyTitle", err)
	}
	if _, err := NewTitle(strings.Repeat("x", maxTitleLength+1)); !errors.Is(err, ErrTitleTooLong) {
		t.Fatalf("oversized title error = %v, want ErrTitleTooLong", err)
	}
}

func TestManagedVideoReferenceDoesNotRequireRawURL(t *testing.T) {
	video, err := NewMediaVideoRef(id.ID(55), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if video.URL() != "" || video.MediaAssetID() != 55 || video.IsZero() {
		t.Fatalf("unexpected managed video: %+v", video)
	}
}

func TestTextBodyHasAbuseSizeLimit(t *testing.T) {
	if _, err := NewTextBody(strings.Repeat("x", maxTextBodySize+1)); !errors.Is(err, ErrTextContentTooLarge) {
		t.Fatalf("oversized body error = %v", err)
	}
}

func TestTextBodyPreservesMarkdownExactly(t *testing.T) {
	raw := "\n  ```go\nfmt.Println(\"hi\")\n```\n"
	body, err := NewTextBody(raw)
	if err != nil {
		t.Fatal(err)
	}
	if body.String() != raw {
		t.Fatalf("markdown changed: got %q want %q", body.String(), raw)
	}
}

func TestImageRefRequiresExactlyOneOfURLOrAsset(t *testing.T) {
	if _, err := NewImageRef("", 0, "", "", "", "", 0, 0, 0); !errors.Is(err, ErrEmptyImageRef) {
		t.Fatalf("empty ref error = %v", err)
	}
	if _, err := NewImageRef("https://example.test/a.png", id.ID(1), "", "", "", "", 0, 0, 0); !errors.Is(err, ErrAmbiguousImageRef) {
		t.Fatalf("ambiguous ref error = %v", err)
	}
	if _, err := NewImageRef("https://example.test/a.png", 0, "", "", "", "", 0, 0, 0); err != nil {
		t.Fatalf("url-only ref should be valid: %v", err)
	}
	if _, err := NewImageRef("", id.ID(1), "", "", "", "", 0, 0, 0); err != nil {
		t.Fatalf("asset-only ref should be valid: %v", err)
	}
}

func TestImageRefLayoutDefaultsToInline(t *testing.T) {
	ref, err := NewImageRef("https://example.test/a.png", 0, "", "", "", "", 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if ref.Layout() != "inline" {
		t.Fatalf("layout = %q, want inline", ref.Layout())
	}
	if _, err := NewImageRef("https://example.test/a.png", 0, "", "", "full-bleed", "", 0, 0, 0); !errors.Is(err, ErrInvalidImageLayout) {
		t.Fatalf("invalid layout error = %v", err)
	}
}

func TestImageRefAlignmentDefaultsToLeftAndRejectsUnknownValues(t *testing.T) {
	ref, err := NewImageRef("https://example.test/a.png", 0, "", "", "", "", 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if ref.Alignment() != "left" {
		t.Fatalf("alignment = %q, want left", ref.Alignment())
	}
	if _, err := NewImageRef("https://example.test/a.png", 0, "", "", "", "justify", 0, 0, 0); !errors.Is(err, ErrInvalidImageAlignment) {
		t.Fatalf("invalid alignment error = %v", err)
	}
}

func TestImageRefRejectsUnknownRotation(t *testing.T) {
	if _, err := NewImageRef("https://example.test/a.png", 0, "", "", "", "", 360, 0, 0); !errors.Is(err, ErrInvalidImageRotation) {
		t.Fatalf("invalid rotation error = %v", err)
	}
}

func TestImageRefAltAndCaptionLengthCaps(t *testing.T) {
	if _, err := NewImageRef("https://example.test/a.png", 0, strings.Repeat("x", maxImageAltLen+1), "", "", "", 0, 0, 0); !errors.Is(err, ErrImageAltTooLong) {
		t.Fatalf("oversized alt error = %v", err)
	}
	if _, err := NewImageRef("https://example.test/a.png", 0, "", strings.Repeat("x", maxImageCaptionLen+1), "", "", 0, 0, 0); !errors.Is(err, ErrImageCaptionTooLong) {
		t.Fatalf("oversized caption error = %v", err)
	}
}

func TestImageRefRejectsNegativeDimensions(t *testing.T) {
	if _, err := NewImageRef("https://example.test/a.png", 0, "", "", "", "", 0, -1, 10); !errors.Is(err, ErrInvalidImageDimensions) {
		t.Fatalf("negative width error = %v", err)
	}
}

func validFlashcard() Flashcard { return Flashcard{Front: "front", Back: "back"} }

func TestFlashcardDeckCardCountBounds(t *testing.T) {
	if _, err := NewFlashcardDeck("steps", "", nil); !errors.Is(err, ErrInvalidFlashcardDeck) {
		t.Fatalf("zero cards error = %v", err)
	}
	tooMany := make([]Flashcard, maxFlashcardCards+1)
	for i := range tooMany {
		tooMany[i] = validFlashcard()
	}
	if _, err := NewFlashcardDeck("steps", "", tooMany); !errors.Is(err, ErrInvalidFlashcardDeck) {
		t.Fatalf("41 cards error = %v", err)
	}
	if _, err := NewFlashcardDeck("steps", "", []Flashcard{validFlashcard()}); err != nil {
		t.Fatalf("1 card should be valid: %v", err)
	}
}

func TestFlashcardDeckModeDefaultsToStepsAndRejectsUnknown(t *testing.T) {
	deck, err := NewFlashcardDeck("", "", []Flashcard{validFlashcard()})
	if err != nil {
		t.Fatal(err)
	}
	if deck.Mode() != "steps" {
		t.Fatalf("mode = %q, want steps", deck.Mode())
	}
	if _, err := NewFlashcardDeck("drill", "", []Flashcard{validFlashcard()}); !errors.Is(err, ErrInvalidFlashcardMode) {
		t.Fatalf("unknown mode error = %v", err)
	}
}

func TestFlashcardFrontBackLengthCaps(t *testing.T) {
	if _, err := NewFlashcardDeck("steps", "", []Flashcard{{Front: "", Back: "back"}}); !errors.Is(err, ErrInvalidFlashcard) {
		t.Fatalf("empty front error = %v", err)
	}
	if _, err := NewFlashcardDeck("steps", "", []Flashcard{{Front: strings.Repeat("x", maxFlashcardFrontLen+1), Back: "back"}}); !errors.Is(err, ErrInvalidFlashcard) {
		t.Fatalf("oversized front error = %v", err)
	}
	if _, err := NewFlashcardDeck("steps", "", []Flashcard{{Front: "front", Back: strings.Repeat("x", maxFlashcardBackLen+1)}}); !errors.Is(err, ErrInvalidFlashcard) {
		t.Fatalf("oversized back error = %v", err)
	}
	if _, err := NewFlashcardDeck("steps", "", []Flashcard{{Front: "front", Back: "back", Hint: strings.Repeat("x", maxFlashcardHintLen+1)}}); !errors.Is(err, ErrInvalidFlashcard) {
		t.Fatalf("oversized hint error = %v", err)
	}
}

func TestFlashcardDeckAtMaximumSizeIsAccepted(t *testing.T) {
	cards := make([]Flashcard, maxFlashcardCards)
	for i := range cards {
		cards[i] = Flashcard{Front: strings.Repeat("f", maxFlashcardFrontLen), Back: strings.Repeat("b", maxFlashcardBackLen), Hint: strings.Repeat("h", maxFlashcardHintLen)}
	}
	if _, err := NewFlashcardDeck("steps", "", cards); err != nil {
		t.Fatalf("40 max-length cards should fit under the size guard: %v", err)
	}
}
