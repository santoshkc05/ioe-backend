// Package domain holds the learner progress model.
package domain

import "errors"

var (
	ErrInvalidLectureState = errors.New("state must be in_progress or completed")
	ErrNegativePosition    = errors.New("position_ms must not be negative")
	ErrCourseHidden        = errors.New("course not found")
	ErrForbidden           = errors.New("forbidden")
	ErrLectureNotFound     = errors.New("lecture not found")
)
