package app

import "errors"

var (
	ErrNotFound           = errors.New("not found")
	ErrInvalidInput       = errors.New("invalid input")
	ErrEnrollmentRequired = errors.New("an active enrollment is required")
)
