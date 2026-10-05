// Package domain holds the enrollment model.
package domain

import "errors"

var (
	ErrInvalidReason      = errors.New("reason must be at most 500 characters")
	ErrCourseHidden       = errors.New("course not found")
	ErrForbidden          = errors.New("forbidden")
	ErrPaymentRequired    = errors.New("payment required")
	ErrCourseNotPublished = errors.New("course is not published")
)
