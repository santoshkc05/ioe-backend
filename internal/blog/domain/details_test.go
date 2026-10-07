package domain_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/santoshkc2200/ioe-backend/internal/blog/domain"
)

func TestNewDetails(t *testing.T) {
	d, err := domain.NewDetails("Title", "sum", "https://cdn.example.com/a.png", []string{" Go ", "go", "Web-Dev"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(d.Tags, ",") != "go,web-dev" {
		t.Fatalf("tags %v", d.Tags)
	}
	cases := map[string]struct {
		summary, cover string
		tags           []string
		want           error
	}{
		"long summary": {strings.Repeat("é", 301), "", nil, domain.ErrInvalidSummary},
		"http cover":   {"", "http://x.example/a.png", nil, domain.ErrInvalidCoverURL},
		"hostless":     {"", "https:///a.png", nil, domain.ErrInvalidCoverURL},
		"bad tag":      {"", "", []string{"c++"}, domain.ErrInvalidTag},
		"too many":     {"", "", []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k"}, domain.ErrTooManyTags},
	}
	for name, tc := range cases {
		if _, err := domain.NewDetails("T", tc.summary, tc.cover, tc.tags); !errors.Is(err, tc.want) {
			t.Errorf("%s: got %v, want %v", name, err, tc.want)
		}
	}
	if _, err := domain.NewDetails("   ", "", "", nil); err == nil {
		t.Fatal("blank title accepted")
	}
}
