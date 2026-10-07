package domain

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

const maxSlugLen = 96

// kebab is the shape shared by slugs and tags.
var kebab = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

var nonSlugRun = regexp.MustCompile(`[^a-z0-9]+`)

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
	v := strings.Trim(nonSlugRun.ReplaceAllString(strings.ToLower(removeDots(title)), "-"), "-")
	if len(v) > maxSlugLen {
		v = strings.TrimRight(v[:maxSlugLen], "-")
	}
	if len(v) < 3 {
		return Slug{value: "post-" + postID.String()}
	}
	return Slug{value: v}
}

// removeDots keeps "1.27" as "127" rather than "1-27".
func removeDots(s string) string { return strings.ReplaceAll(s, ".", "") }

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
