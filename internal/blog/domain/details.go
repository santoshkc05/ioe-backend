package domain

import (
	"net/url"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/santoshkc2200/ioe-backend/internal/platform/contentblocks"
)

const (
	maxSummaryRunes = 300
	maxCoverURLLen  = 2048
)

// Details are a post's editable, versioned fields.
type Details struct {
	Title    contentblocks.Title
	Summary  string
	CoverURL string
	Tags     []string
}

func NewDetails(title, summary, coverURL string, tags []string) (Details, error) {
	t, err := contentblocks.NewTitle(title)
	if err != nil {
		return Details{}, err
	}
	summary = strings.TrimSpace(summary)
	if utf8.RuneCountInString(summary) > maxSummaryRunes {
		return Details{}, ErrInvalidSummary
	}
	coverURL = strings.TrimSpace(coverURL)
	if coverURL != "" && !isHTTPSURL(coverURL, maxCoverURLLen) {
		return Details{}, ErrInvalidCoverURL
	}
	normalized, err := NewTags(tags)
	if err != nil {
		return Details{}, err
	}
	return Details{Title: t, Summary: summary, CoverURL: coverURL, Tags: normalized}, nil
}

func isHTTPSURL(raw string, maxLen int) bool {
	if len(raw) > maxLen {
		return false
	}
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host != ""
}

func (d Details) clone() Details {
	d.Tags = slices.Clone(d.Tags)
	return d
}
