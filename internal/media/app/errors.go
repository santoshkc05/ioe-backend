package app

import "errors"

var (
	ErrNotFound           = errors.New("not found")
	ErrForbidden          = errors.New("forbidden")
	ErrInvalidInput       = errors.New("invalid input")
	ErrCourseNotEditable  = errors.New("course not editable")
	ErrEnrollmentRequired = errors.New("enrollment required")

	ErrRemoteInvalid     = errors.New("media service rejected the request")
	ErrRemoteTooLarge    = errors.New("media service rejected the upload size")
	ErrRemoteNotFound    = errors.New("media service has no such asset")
	ErrRemoteConflict    = errors.New("media service asset is in the wrong state")
	ErrRemoteUnavailable = errors.New("media service unavailable")
)
