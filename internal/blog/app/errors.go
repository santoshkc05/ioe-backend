package app

import "errors"

var (
	ErrNotFound               = errors.New("not found")
	ErrForbidden              = errors.New("forbidden")
	ErrConcurrentModification = errors.New("concurrent modification")
	ErrSlugTaken              = errors.New("slug is taken")
	ErrInvalidInput           = errors.New("invalid input")
	ErrRevisionRequired       = errors.New("base_revision is required")
	ErrPatchTooLarge          = errors.New("patch exceeds a size limit")
)

// RevisionConflictError is returned when base_revision is stale. Current is the post's
// draft content at the time of the conflict.
type RevisionConflictError struct{ Current ContentView }

func (e *RevisionConflictError) Error() string { return "post content revision conflict" }
