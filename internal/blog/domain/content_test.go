package domain_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/santoshkc2200/ioe-backend/internal/blog/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/contentblocks"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

func text(t *testing.T, cid, html string) contentblocks.Block {
	t.Helper()
	body, err := contentblocks.NewSanitizedTextBody(html)
	if err != nil {
		t.Fatal(err)
	}
	return contentblocks.NewTextBlock(id.ID(len(cid)+1), cid, 0, body)
}

func TestNewContentPolicy(t *testing.T) {
	img, err := contentblocks.NewImageRef("https://cdn.example.com/a.png", 0, "alt", "", "inline", "center", 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	ok := []contentblocks.Block{text(t, "t1", "<p>hi</p>"), contentblocks.NewImageBlock(2, "i1", 1, img)}
	if _, err := domain.NewContent(ok); err != nil {
		t.Fatalf("text+image: %v", err)
	}
	httpImg, _ := contentblocks.NewImageRef("http://cdn.example.com/a.png", 0, "", "", "inline", "center", 0, 0, 0)
	if _, err := domain.NewContent([]contentblocks.Block{contentblocks.NewImageBlock(3, "i2", 0, httpImg)}); !errors.Is(err, domain.ErrInvalidImage) {
		t.Fatalf("http image: %v", err)
	}
	assetImg, _ := contentblocks.NewImageRef("", 777, "", "", "inline", "center", 0, 0, 0)
	if _, err := domain.NewContent([]contentblocks.Block{contentblocks.NewImageBlock(4, "i3", 0, assetImg)}); !errors.Is(err, domain.ErrInvalidImage) {
		t.Fatalf("asset image: %v", err)
	}
	quiz, _ := contentblocks.NewQuizBlock(5, "q1", 0, 9)
	if _, err := domain.NewContent([]contentblocks.Block{quiz}); !errors.Is(err, domain.ErrBlockKindNotAllowed) {
		t.Fatalf("quiz: %v", err)
	}
}

func TestReadingMinutes(t *testing.T) {
	words := func(n int) string { return "<p>" + strings.TrimSpace(strings.Repeat("word ", n)) + "</p>" }
	cases := map[string]struct {
		blocks []contentblocks.Block
		want   int
	}{
		"empty":         {nil, 1},
		"220 words":     {[]contentblocks.Block{text(t, "a", words(220))}, 1},
		"221 words":     {[]contentblocks.Block{text(t, "a", words(221))}, 2},
		"tags stripped": {[]contentblocks.Block{text(t, "a", "<p><strong>one</strong><em>two</em></p>")}, 1},
		"summed":        {[]contentblocks.Block{text(t, "a", words(200)), text(t, "bb", words(200))}, 2},
	}
	for name, tc := range cases {
		if got := domain.ReadingMinutes(tc.blocks); got != tc.want {
			t.Errorf("%s: got %d, want %d", name, got, tc.want)
		}
	}
}
