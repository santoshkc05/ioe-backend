package app

import "errors"

var (
	ErrNotFound               = errors.New("not found")
	ErrDuplicate              = errors.New("enrollment already exists")
	ErrConcurrentModification = errors.New("concurrent modification")
	ErrInvalidInput           = errors.New("invalid input")
)
