package domain

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
	"github.com/santoshkc2200/ioe-backend/internal/platform/slug"
)

const maxSlugLen = 96

// kebab is the slug shape.
var kebab = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// Slug is a post's URL path segment. Every slug a post ever had stays reserved for it.
type Slug struct{ value string }

// NewSlug validates a slug an author typed, after trimming and lowercasing.
func NewSlug(raw string) (Slug, error) {
	v := strings.ToLower(strings.TrimSpace(raw))
	if len(v) > maxSlugLen || !kebab.MatchString(v) {
		return Slug{}, ErrInvalidSlug
	}
	return Slug{value: v}, nil
}

// SlugFromTitle derives a slug from a title. Titles with fewer than 3 usable ASCII
// characters (a Nepali or Japanese title, for example) fall back to "post-<id>": no
// transliteration is attempted, and the author can set a slug explicitly.
func SlugFromTitle(title string, postID id.ID) Slug {
	v := slug.From(title, maxSlugLen)
	if len(v) < 3 {
		return Slug{value: "post-" + postID.String()}
	}
	return Slug{value: v}
}

// WithSuffix appends "-n", truncating the base so the result stays within the limit.
func (s Slug) WithSuffix(n int) Slug {
	suffix := "-" + strconv.Itoa(n)
	base := s.value
	if len(base)+len(suffix) > maxSlugLen {
		base = strings.TrimRight(base[:maxSlugLen-len(suffix)], "-")
	}
	return Slug{value: base + suffix}
}

func (s Slug) String() string { return s.value }
