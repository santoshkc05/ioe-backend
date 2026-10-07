package domain

import "strings"

const (
	maxTagLen = 32
	MaxTags   = 10
)

// NewTag trims and lowercases raw and validates it.
func NewTag(raw string) (string, error) {
	v := strings.ToLower(strings.TrimSpace(raw))
	if len(v) > maxTagLen || !kebab.MatchString(v) {
		return "", ErrInvalidTag
	}
	return v, nil
}

// NewTags normalizes each tag and drops duplicates, keeping first occurrence order.
func NewTags(raw []string) ([]string, error) {
	out := make([]string, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, r := range raw {
		t, err := NewTag(r)
		if err != nil {
			return nil, err
		}
		if _, dup := seen[t]; dup {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	if len(out) > MaxTags {
		return nil, ErrTooManyTags
	}
	return out, nil
}
