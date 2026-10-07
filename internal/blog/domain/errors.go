// Package domain holds the blog model: the Post aggregate, its value objects and events.
package domain

import "errors"

var (
	ErrInvalidStatusTransition = errors.New("invalid status transition")
	ErrPostArchived            = errors.New("post is archived")
	ErrEmptyPost               = errors.New("post has no content")
	ErrBlockKindNotAllowed     = errors.New("blog posts allow only text and image blocks")
	ErrInvalidImage            = errors.New("blog images must use an https url and no media_asset_id")
	ErrInvalidSummary          = errors.New("summary must be at most 300 characters")
	ErrInvalidCoverURL         = errors.New("cover_url must be empty or an absolute https URL")
	ErrInvalidSlug             = errors.New("slug must be 1-96 lowercase letters, digits and single hyphens")
	ErrInvalidTag              = errors.New("tags must be 1-32 lowercase letters, digits and single hyphens")
	ErrTooManyTags             = errors.New("a post has at most 10 tags")
)
