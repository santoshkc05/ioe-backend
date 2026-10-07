package app

import (
	"errors"

	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

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
	ErrAssetInUse        = errors.New("asset is in use")
)

// InUseError is ErrAssetInUse with the lectures that reference the asset.
type InUseError struct{ LectureIDs []id.ID }

func (e *InUseError) Error() string { return ErrAssetInUse.Error() }
func (e *InUseError) Unwrap() error { return ErrAssetInUse }
