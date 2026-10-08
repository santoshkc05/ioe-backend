package app

import "errors"

var (
	ErrNotFound             = errors.New("not found")
	ErrForbidden            = errors.New("forbidden")
	ErrInvalidInput         = errors.New("invalid input")
	ErrCertificatesDisabled = errors.New("certificates are not offered for this course")
	ErrNotEnrolled          = errors.New("an active enrollment is required")
	ErrProgressIncomplete   = errors.New("the course is not complete")
	ErrExamNotPassed        = errors.New("the certificate exam has not been passed")
)
