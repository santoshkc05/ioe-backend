package app

import (
	"errors"

	"github.com/santoshkc2200/ioe-backend/internal/platform/contentblocks"
)

var (
	ErrNotFound               = errors.New("not found")
	ErrForbidden              = errors.New("forbidden")
	ErrEnrollmentRequired     = errors.New("enrollment required")
	ErrConcurrentModification = errors.New("concurrent modification")
	ErrInvalidInput           = errors.New("invalid input")
	ErrRevisionRequired       = errors.New("base_revision is required")
	ErrPatchTooLarge          = errors.New("patch exceeds a size limit")
	ErrBlockSetMismatch       = contentblocks.ErrBlockSetMismatch
	ErrOrderDeleteOverlap     = contentblocks.ErrOrderDeleteOverlap
	ErrDuplicateClientBlockID = contentblocks.ErrDuplicateClientBlockID
	ErrInvalidMediaReference  = errors.New("invalid media reference")
	ErrInvalidQuizReference   = errors.New("invalid quiz reference")
	ErrCategoryExists         = errors.New("category name or slug already exists")
	ErrUnknownCategory        = errors.New("unknown category")
)

// RevisionConflictError is returned when base_revision is stale. Current is the
// lecture's content at the time of the conflict.
type RevisionConflictError struct{ Current LectureContentView }

func (e *RevisionConflictError) Error() string { return "lecture content revision conflict" }
