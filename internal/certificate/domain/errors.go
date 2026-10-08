// Package domain holds the certificate model.
package domain

import "errors"

var (
	ErrInvalidMode     = errors.New("mode must be off, completion or completion_and_exam")
	ErrExamRequired    = errors.New("completion_and_exam requires exam_id")
	ErrExamNotAllowed  = errors.New("exam_id is only allowed with completion_and_exam")
	ErrExamNotInCourse = errors.New("exam does not belong to the course")
)
