package domain

import (
	"errors"
	"time"
)

var (
	ErrInvalidQuestion = errors.New("invalid question")
	ErrInvalidQuiz     = errors.New("invalid quiz")
	ErrInvalidExam     = errors.New("invalid exam")
	ErrInvalidAnswer   = errors.New("invalid answer")

	ErrAttemptSubmitted = errors.New("attempt is already submitted")
	ErrAttemptExpired   = errors.New("attempt deadline has passed")

	ErrRevealAttemptOpen = errors.New("attempt is still open")
	ErrRevealDisabled    = errors.New("answers are never revealed for this exam")
	ErrRevealNotYet      = errors.New("answers are not revealed yet")
)

// RevealNotYetError is ErrRevealNotYet with the time answers are revealed.
type RevealNotYetError struct{ At time.Time }

func (e *RevealNotYetError) Error() string { return ErrRevealNotYet.Error() }
func (e *RevealNotYetError) Unwrap() error { return ErrRevealNotYet }
