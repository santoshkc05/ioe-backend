package domain

import (
	"errors"

	"github.com/santoshkc2200/ioe-backend/internal/platform/tags"
)

// MaxTags is the most tags a post carries.
const MaxTags = tags.Max

// NewTag trims and lowercases raw and validates it.
func NewTag(raw string) (string, error) {
	t, err := tags.New(raw)
	if err != nil {
		return "", ErrInvalidTag
	}
	return t, nil
}

// NewTags normalizes each tag and drops duplicates, keeping first occurrence order.
func NewTags(raw []string) ([]string, error) {
	out, err := tags.NewList(raw)
	switch {
	case errors.Is(err, tags.ErrTooMany):
		return nil, ErrTooManyTags
	case err != nil:
		return nil, ErrInvalidTag
	}
	return out, nil
}
