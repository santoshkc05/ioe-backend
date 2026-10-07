package domain_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/santoshkc2200/ioe-backend/internal/blog/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

func TestSlugFromTitle(t *testing.T) {
	cases := map[string]string{
		"  Hello, World!  ":       "hello-world",
		"a---b   c":               "a-b-c",
		"Go 1.27 released":        "go-127-released",
		"日本語のタイトル":                "post-42",
		"Hi":                      "post-42",
		strings.Repeat("ab ", 60): strings.TrimSuffix(strings.Repeat("ab-", 32), "-"),
	}
	for title, want := range cases {
		if got := domain.SlugFromTitle(title, id.ID(42)).String(); got != want {
			t.Errorf("SlugFromTitle(%q) = %q, want %q", title, got, want)
		}
	}
}

func TestNewSlug(t *testing.T) {
	s, err := domain.NewSlug("  Valid-Slug123 ")
	if err != nil || s.String() != "valid-slug123" {
		t.Fatalf("got %q, %v", s.String(), err)
	}
	for _, bad := range []string{"", "has space", "-lead", "trail-", "a--b", "ünï", strings.Repeat("a", 97)} {
		if _, err := domain.NewSlug(bad); !errors.Is(err, domain.ErrInvalidSlug) {
			t.Errorf("NewSlug(%q) = %v, want ErrInvalidSlug", bad, err)
		}
	}
}

func TestSlugWithSuffixStaysWithinLimit(t *testing.T) {
	s, _ := domain.NewSlug(strings.Repeat("a", 96))
	got := s.WithSuffix(12).String()
	if len(got) != 96 || !strings.HasSuffix(got, "-12") {
		t.Fatalf("got %q (%d)", got, len(got))
	}
	if _, err := domain.NewSlug(got); err != nil {
		t.Fatalf("suffixed slug invalid: %v", err)
	}
}
