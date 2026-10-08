// Package tags normalizes the free-form labels that contexts attach to content.
package tags

import (
	"errors"
	"regexp"
	"strings"
)

// Max is the most tags one item carries.
const Max = 10

const maxLen = 32

var (
	ErrInvalid = errors.New("tags must be 1-32 lowercase letters, digits and single hyphens")
	ErrTooMany = errors.New("at most 10 tags")
)

var kebab = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// New trims and lowercases raw and validates it.
func New(raw string) (string, error) {
	v := strings.ToLower(strings.TrimSpace(raw))
	if len(v) > maxLen || !kebab.MatchString(v) {
		return "", ErrInvalid
	}
	return v, nil
}

// NewList normalizes each tag and drops duplicates, keeping first-occurrence order. It never
// returns a nil slice on success.
func NewList(raw []string) ([]string, error) {
	out := make([]string, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, r := range raw {
		t, err := New(r)
		if err != nil {
			return nil, err
		}
		if _, dup := seen[t]; dup {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	if len(out) > Max {
		return nil, ErrTooMany
	}
	return out, nil
}
