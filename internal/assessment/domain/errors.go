package domain

import "errors"

var (
	ErrInvalidQuestion = errors.New("invalid question")
	ErrInvalidQuiz     = errors.New("invalid quiz")
	ErrInvalidAnswer   = errors.New("invalid answer")
)
