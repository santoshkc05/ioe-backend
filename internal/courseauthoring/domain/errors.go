// Package domain holds the course authoring model.
package domain

import "errors"

var (
	ErrInvalidPrice            = errors.New("price must not be negative")
	ErrUnsupportedCurrency     = errors.New("only NPR prices are supported")
	ErrInvalidLevel            = errors.New("level must be empty, beginner, intermediate or advanced")
	ErrInvalidThumbnailURL     = errors.New("thumbnail_url must be empty or an absolute http(s) URL")
	ErrDuplicateSectionTitle   = errors.New("section title already used in this course")
	ErrSectionNotFound         = errors.New("section not found")
	ErrLectureNotFound         = errors.New("lecture not found")
	ErrInvalidLectureOrder     = errors.New("lecture order must list every lecture exactly once")
	ErrCourseHasNoLectures     = errors.New("course has no lectures")
	ErrCourseNotEditable       = errors.New("course is archived or awaiting a review decision")
	ErrInvalidStatusTransition = errors.New("invalid status transition")
	ErrApprovalRequired        = errors.New("course must be approved before publishing")
	ErrReviewNoteRequired      = errors.New("note is required")
	ErrReviewNoteTooLong       = errors.New("note is too long")
	ErrInvalidCategoryName     = errors.New("category name must be 1-60 characters")
	ErrTooManyCategories       = errors.New("a course has at most 3 categories")
	ErrInvalidTag              = errors.New("tags must be 1-32 lowercase letters, digits and single hyphens")
	ErrTooManyTags             = errors.New("a course has at most 10 tags")
)
