package contentblocks

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

func TestValidateBlocksRejectsEmptyClientBlockID(t *testing.T) {
	body, _ := NewTextBody("hello")
	if _, err := ValidateBlocks([]Block{NewTextBlock(1, "", 0, body)}); !errors.Is(err, ErrInvalidClientBlockID) {
		t.Fatalf("error = %v, want ErrInvalidClientBlockID", err)
	}
}

func TestValidateBlocksRejectsWhitespaceOnlyClientBlockID(t *testing.T) {
	body, _ := NewTextBody("hello")
	if _, err := ValidateBlocks([]Block{NewTextBlock(1, "   ", 0, body)}); !errors.Is(err, ErrInvalidClientBlockID) {
		t.Fatalf("error = %v, want ErrInvalidClientBlockID", err)
	}
}

func TestValidateBlocksRejectsClientBlockIDOverMaxLen(t *testing.T) {
	body, _ := NewTextBody("hello")
	tooLong := strings.Repeat("a", MaxClientBlockIDLen+1)
	if _, err := ValidateBlocks([]Block{NewTextBlock(1, tooLong, 0, body)}); !errors.Is(err, ErrInvalidClientBlockID) {
		t.Fatalf("error = %v, want ErrInvalidClientBlockID", err)
	}
}

func TestValidateBlocksAcceptsClientBlockIDAtMaxLen(t *testing.T) {
	body, _ := NewTextBody("hello")
	exact := strings.Repeat("a", MaxClientBlockIDLen)
	if _, err := ValidateBlocks([]Block{NewTextBlock(1, exact, 0, body)}); err != nil {
		t.Fatalf("64-char client block id should be accepted: %v", err)
	}
}

func TestValidateBlocksRejectsControlCharacterInClientBlockID(t *testing.T) {
	body, _ := NewTextBody("hello")
	if _, err := ValidateBlocks([]Block{NewTextBlock(1, "abc\ndef", 0, body)}); !errors.Is(err, ErrInvalidClientBlockID) {
		t.Fatalf("error = %v, want ErrInvalidClientBlockID", err)
	}
	if _, err := ValidateBlocks([]Block{NewTextBlock(1, "abc\tdef", 0, body)}); !errors.Is(err, ErrInvalidClientBlockID) {
		t.Fatalf("error = %v, want ErrInvalidClientBlockID", err)
	}
}

// TestValidateBlocksAcceptsDecimalSnowflakeShape covers courseauthoring's
// migration 0005 backfill shape: client_block_id = id::text, which for
// negative dense legacy ids looks like "-42". Requiring UUID-like shape
// would break these rows.
func TestValidateBlocksAcceptsDecimalSnowflakeShape(t *testing.T) {
	body, _ := NewTextBody("hello")
	if _, err := ValidateBlocks([]Block{NewTextBlock(1, "-42", 0, body)}); err != nil {
		t.Fatalf("decimal snowflake shape should be accepted: %v", err)
	}
	if _, err := ValidateBlocks([]Block{NewTextBlock(1, "729102938475", 0, body)}); err != nil {
		t.Fatalf("decimal snowflake shape should be accepted: %v", err)
	}
}

func TestValidateBlocksRejectsDuplicateClientBlockID(t *testing.T) {
	body, _ := NewTextBody("hello")
	video, _ := NewVideoRef("https://cdn.example/video.m3u8", 0)
	_, err := ValidateBlocks([]Block{
		NewTextBlock(1, "dup", 0, body),
		NewVideoBlock(2, "dup", 1, video),
	})
	if !errors.Is(err, ErrDuplicateClientBlockID) {
		t.Fatalf("error = %v, want ErrDuplicateClientBlockID", err)
	}
}

func TestValidateBlocksReassignsPositionToMatchOrder(t *testing.T) {
	body, _ := NewTextBody("# Introduction")
	video, _ := NewVideoRef("https://cdn.example/video.m3u8", 0*time.Second)
	quiz, err := NewQuizBlock(13, "c3", 2, 99)
	if err != nil {
		t.Fatal(err)
	}
	blocks, err := ValidateBlocks([]Block{NewTextBlock(11, "c1", 9, body), NewVideoBlock(12, "c2", 4, video), quiz})
	if err != nil {
		t.Fatal(err)
	}
	for i, block := range blocks {
		if block.Position() != i {
			t.Fatalf("block %d position = %d", i, block.Position())
		}
	}
}

func TestValidateBlocksRoundTripsAllFiveKinds(t *testing.T) {
	text, _ := NewTextBody("intro")
	video, _ := NewVideoRef("https://cdn.example/video.m3u8", 0)
	quiz, err := NewQuizBlock(3, "c3", 0, id.ID(99))
	if err != nil {
		t.Fatal(err)
	}
	imageRef, err := NewImageRef("https://example.test/diagram.png", 0, "a diagram", "", "wide", "center", 90, 800, 600)
	if err != nil {
		t.Fatal(err)
	}
	deck, err := NewFlashcardDeck("recall", "Deck", []Flashcard{validFlashcard()})
	if err != nil {
		t.Fatal(err)
	}
	blocks, err := ValidateBlocks([]Block{
		NewTextBlock(1, "c1", 4, text),
		NewVideoBlock(2, "c2", 3, video),
		quiz,
		NewImageBlock(4, "c4", 2, imageRef),
		NewFlashcardBlock(5, "c5", 1, deck),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 5 {
		t.Fatalf("block count = %d, want 5", len(blocks))
	}
	if _, ok := blocks[3].Image(); !ok {
		t.Fatal("expected block 3 to carry an image ref")
	}
	if _, ok := blocks[4].Deck(); !ok {
		t.Fatal("expected block 4 to carry a flashcard deck")
	}
}

func TestWithIdentityReplacesRowOwnedFieldsOnly(t *testing.T) {
	body, _ := NewTextBody("hello")
	block := NewTextBlock(0, "", 0, body)
	stamped := block.WithIdentity(id.ID(42), "client-1", 3)
	if stamped.ID() != id.ID(42) || stamped.ClientBlockID() != "client-1" || stamped.Position() != 3 {
		t.Fatalf("unexpected stamped block: %+v", stamped)
	}
	if text, ok := stamped.Text(); !ok || text.String() != "hello" {
		t.Fatalf("content lost after WithIdentity: %+v", stamped)
	}
}

func TestValidKindsMatchesTheKindCheckConstraint(t *testing.T) {
	// migrations/courseauthoring/0003_image_flashcard_blocks.up.sql pins the
	// lecture_blocks.kind CHECK constraint to this literal list, in this
	// order. Adding a sixth kind means updating both deliberately.
	want := []BlockType{"text", "video", "quiz", "image", "flashcard"}
	got := ValidKinds()
	if len(got) != len(want) {
		t.Fatalf("ValidKinds() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ValidKinds()[%d] = %v, want %v", i, got[i], want[i])
		}
	}
}
