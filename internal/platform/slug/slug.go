// Package slug derives URL path segments from human text.
package slug

import (
	"regexp"
	"strings"
)

var nonSlugRun = regexp.MustCompile(`[^a-z0-9]+`)

// From lowercases text, drops dots ("1.27" becomes "127" rather than "1-27"), joins the
// remaining runs of [a-z0-9] with single hyphens and truncates to maxLen bytes without a
// trailing hyphen. No transliteration is attempted, so text with few ASCII letters or digits
// yields an empty or short result; callers choose a fallback.
func From(text string, maxLen int) string {
	v := strings.ToLower(strings.ReplaceAll(text, ".", ""))
	v = strings.Trim(nonSlugRun.ReplaceAllString(v, "-"), "-")
	if len(v) > maxLen {
		v = strings.TrimRight(v[:maxLen], "-")
	}
	return v
}
