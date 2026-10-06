package app

import "errors"

var (
	ErrNotFound           = errors.New("not found")
	ErrForbidden          = errors.New("forbidden")
	ErrInvalidInput       = errors.New("invalid input")
	ErrCourseNotEditable  = errors.New("course is not editable")
	ErrEnrollmentRequired = errors.New("an active enrollment is required")
)
