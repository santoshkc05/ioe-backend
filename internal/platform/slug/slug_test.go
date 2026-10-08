package slug_test

import (
	"strings"
	"testing"

	"github.com/santoshkc2200/ioe-backend/internal/platform/slug"
)

func TestFrom(t *testing.T) {
	cases := map[string]string{
		"  Hello, World!  ": "hello-world",
		"a---b   c":         "a-b-c",
		"Go 1.27 released":  "go-127-released",
		"日本語のタイトル":          "",
		"C++":               "c",
		"Web Development":   "web-development",
	}
	for in, want := range cases {
		if got := slug.From(in, 96); got != want {
			t.Errorf("From(%q) = %q, want %q", in, got, want)
		}
	}
	if got := slug.From(strings.Repeat("ab ", 60), 96); got != strings.TrimSuffix(strings.Repeat("ab-", 32), "-") {
		t.Errorf("truncated = %q", got)
	}
	if got := slug.From("abc def", 4); got != "abc" {
		t.Errorf("no trailing hyphen = %q", got)
	}
}
