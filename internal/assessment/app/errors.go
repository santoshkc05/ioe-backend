package app

import (
	"errors"
	"time"
)

var (
	ErrNotFound           = errors.New("not found")
	ErrForbidden          = errors.New("forbidden")
	ErrInvalidInput       = errors.New("invalid input")
	ErrCourseNotEditable  = errors.New("course is not editable")
	ErrEnrollmentRequired = errors.New("an active enrollment is required")

	ErrExamNotOpen       = errors.New("exam is not open yet")
	ErrExamClosed        = errors.New("exam is closed")
	ErrOpenAttemptExists = errors.New("an open attempt already exists")
	ErrRetakesNotAllowed = errors.New("retakes are not allowed")
)

// WindowError is ErrExamNotOpen or ErrExamClosed with the time the window opens or closed.
type WindowError struct {
	Err error
	At  time.Time
}

func (e *WindowError) Error() string { return e.Err.Error() }
func (e *WindowError) Unwrap() error { return e.Err }
