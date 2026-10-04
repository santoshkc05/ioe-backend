package contentblocks

import (
	"errors"
	"testing"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

func TestEncodeDecodePayloadRoundTripsAllFiveKinds(t *testing.T) {
	text, _ := NewTextBody("intro")
	video, _ := NewVideoRef("https://cdn.example/video.m3u8", 90*time.Second)
	managedVideo, _ := NewMediaVideoRef(id.ID(77), 30*time.Second)
	quizID := id.ID(99)
	imageRef, _ := NewImageRef("https://example.test/diagram.png", 0, "a diagram", "caption", "wide", "center", 90, 800, 600)
	deck, _ := NewFlashcardDeck("recall", "Deck", []Flashcard{{Front: "front", Back: "back", ClientCardID: "card-1"}})

	quizBlock, err := NewQuizBlock(3, "c3", 0, quizID)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name  string
		block Block
	}{
		{"text", NewTextBlock(1, "c1", 0, text)},
		{"video-url", NewVideoBlock(2, "c2", 0, video)},
		{"video-managed", NewVideoBlock(2, "c2", 0, managedVideo)},
		{"quiz", quizBlock},
		{"image", NewImageBlock(4, "c4", 0, imageRef)},
		{"flashcard", NewFlashcardBlock(5, "c5", 0, deck)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := EncodePayload(tc.block)
			if err != nil {
				t.Fatalf("EncodePayload: %v", err)
			}
			decoded, err := DecodePayload(tc.block.Type(), raw)
			if err != nil {
				t.Fatalf("DecodePayload: %v", err)
			}
			decoded = decoded.WithIdentity(tc.block.ID(), tc.block.ClientBlockID(), tc.block.Position())
			if decoded.Type() != tc.block.Type() {
				t.Fatalf("type = %v, want %v", decoded.Type(), tc.block.Type())
			}
			switch tc.block.Type() {
			case BlockTypeText:
				got, _ := decoded.Text()
				want, _ := tc.block.Text()
				if got != want {
					t.Fatalf("text = %+v, want %+v", got, want)
				}
			case BlockTypeVideo:
				got, _ := decoded.Video()
				want, _ := tc.block.Video()
				if got.URL() != want.URL() || got.MediaAssetID() != want.MediaAssetID() || got.Duration() != want.Duration() {
					t.Fatalf("video = %+v, want %+v", got, want)
				}
			case BlockTypeQuiz:
				if decoded.QuizID() != tc.block.QuizID() {
					t.Fatalf("quiz id = %v, want %v", decoded.QuizID(), tc.block.QuizID())
				}
			case BlockTypeImage:
				got, _ := decoded.Image()
				want, _ := tc.block.Image()
				if got.URL() != want.URL() || got.Alt() != want.Alt() || got.Layout() != want.Layout() {
					t.Fatalf("image = %+v, want %+v", got, want)
				}
			case BlockTypeFlashcard:
				got, _ := decoded.Deck()
				want, _ := tc.block.Deck()
				if got.Mode() != want.Mode() || len(got.Cards()) != len(want.Cards()) {
					t.Fatalf("deck = %+v, want %+v", got, want)
				}
			}
		})
	}
}

func TestDecodePayloadRejectsUnknownKind(t *testing.T) {
	if _, err := DecodePayload(BlockType("bogus"), []byte(`{}`)); !errors.Is(err, ErrInvalidBlockType) {
		t.Fatalf("error = %v, want ErrInvalidBlockType", err)
	}
}
