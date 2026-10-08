package tags_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/santoshkc2200/ioe-backend/internal/platform/tags"
)

func TestNew(t *testing.T) {
	got, err := tags.New("  Web-Dev ")
	if err != nil || got != "web-dev" {
		t.Fatalf("New = %q, %v", got, err)
	}
	for _, bad := range []string{"", "  ", "c++", "a--b", "-a", "a-", "has space", "ünï", strings.Repeat("a", 33)} {
		if _, err := tags.New(bad); !errors.Is(err, tags.ErrInvalid) {
			t.Errorf("New(%q) = %v, want ErrInvalid", bad, err)
		}
	}
}

func TestNewList(t *testing.T) {
	got, err := tags.NewList([]string{"Go", "web", "go", " WEB "})
	if err != nil || !slices.Equal(got, []string{"go", "web"}) {
		t.Fatalf("NewList = %v, %v", got, err)
	}
	if got, err := tags.NewList(nil); err != nil || got == nil || len(got) != 0 {
		t.Fatalf("NewList(nil) = %#v, %v", got, err)
	}
	eleven := []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k"}
	if _, err := tags.NewList(eleven); !errors.Is(err, tags.ErrTooMany) {
		t.Fatalf("eleven = %v", err)
	}
	// Duplicates do not count toward the limit.
	if got, err := tags.NewList(append(eleven[:10:10], "a")); err != nil || len(got) != 10 {
		t.Fatalf("ten plus duplicate = %v, %v", got, err)
	}
	if _, err := tags.NewList([]string{"ok", "c++"}); !errors.Is(err, tags.ErrInvalid) {
		t.Fatalf("invalid = %v", err)
	}
}
