package domain_test

import (
	"testing"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/contentblocks"
)

func TestLectureContentFlags(t *testing.T) {
	body, err := contentblocks.NewSanitizedTextBody("<p>hi</p>")
	if err != nil {
		t.Fatal(err)
	}
	video, err := contentblocks.NewVideoRef("https://v.example.com/a.mp4", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	c, err := domain.NewLectureContent([]contentblocks.Block{
		contentblocks.NewTextBlock(1, "a", 0, body),
		contentblocks.NewVideoBlock(2, "b", 1, video),
	})
	if err != nil || !c.HasText() || !c.HasVideo() || len(c.Blocks()) != 2 {
		t.Fatalf("%v %v", c, err)
	}
	if _, err := domain.NewLectureContent([]contentblocks.Block{
		contentblocks.NewTextBlock(1, "a", 0, body),
		contentblocks.NewTextBlock(2, "a", 1, body),
	}); err == nil {
		t.Fatal("duplicate client_block_id accepted")
	}
}
