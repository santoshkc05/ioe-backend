package app

import (
	"errors"
	"testing"

	"github.com/santoshkc2200/ioe-backend/internal/platform/contentblocks"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

func testGen(t *testing.T) *id.Generator {
	t.Helper()
	g, err := id.NewGenerator(0)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func TestBuildContentLegacy(t *testing.T) {
	c, err := buildContent(testGen(t), nil, LegacyContent{TextBody: "<p>hi</p>", VideoURL: "https://v.example.com/a.mp4", VideoDurationMs: 5000})
	if err != nil || !c.HasText() || !c.HasVideo() || len(c.Blocks()) != 2 {
		t.Fatalf("%v %v", c, err)
	}
	empty, err := buildContent(testGen(t), nil, LegacyContent{})
	if err != nil || len(empty.Blocks()) != 0 {
		t.Fatalf("empty: %v %v", empty, err)
	}
}

func TestBuildContentBlocksAreExclusive(t *testing.T) {
	c, err := buildContent(testGen(t), []BlockInput{{ClientBlockID: "a", Type: "text", Body: "<p>x</p>"}}, LegacyContent{VideoURL: "https://v.example.com/a.mp4"})
	if err != nil || c.HasVideo() || len(c.Blocks()) != 1 {
		t.Fatalf("%v %v", c, err)
	}
}

func TestBuildBlockRejectsBadReferences(t *testing.T) {
	for _, in := range []BlockInput{
		{ClientBlockID: "q", Type: "quiz", QuizID: "abc"},
		{ClientBlockID: "v", Type: "video", MediaAssetID: "-5"},
		{ClientBlockID: "x", Type: "nope"},
	} {
		if _, err := buildBlock(1, in.ClientBlockID, 0, in); err == nil {
			t.Fatalf("%+v accepted", in)
		}
	}
	b, err := buildBlock(1, "q", 0, BlockInput{ClientBlockID: "q", Type: "quiz", QuizID: "1840396745219883008"})
	if err != nil || b.Type() != contentblocks.BlockTypeQuiz || b.QuizID() != 1840396745219883008 {
		t.Fatalf("%v %v", b, err)
	}
	if _, err := buildBlock(1, "q", 0, BlockInput{Type: "quiz", QuizID: "x"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput", err)
	}
}
