package app

import "errors"

var (
	ErrNotFound               = errors.New("not found")
	ErrForbidden              = errors.New("forbidden")
	ErrEnrollmentRequired     = errors.New("enrollment required")
	ErrConcurrentModification = errors.New("concurrent modification")
	ErrInvalidInput           = errors.New("invalid input")
	ErrRevisionRequired       = errors.New("base_revision is required")
	ErrPatchTooLarge          = errors.New("patch exceeds a size limit")
	ErrBlockSetMismatch       = errors.New("order, upserts and deletes do not match the lecture's blocks")
	ErrOrderDeleteOverlap     = errors.New("a block cannot appear in both order and deletes")
	ErrDuplicateClientBlockID = errors.New("duplicate client block id")
	ErrInvalidMediaReference  = errors.New("invalid media reference")
)

// RevisionConflictError is returned when base_revision is stale. Current is the
// lecture's content at the time of the conflict.
type RevisionConflictError struct{ Current LectureContentView }

func (e *RevisionConflictError) Error() string { return "lecture content revision conflict" }
