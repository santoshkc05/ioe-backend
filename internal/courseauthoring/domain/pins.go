package domain

import "github.com/santoshkc2200/ioe-backend/internal/platform/id"

// AssessmentKind is a kind of assessment a course version pins.
type AssessmentKind string

const (
	AssessmentQuiz AssessmentKind = "quiz"
	AssessmentExam AssessmentKind = "exam"
)

// AssessmentPin fixes the revision of one quiz or exam that a course version shows.
type AssessmentPin struct {
	Kind     AssessmentKind `json:"kind"`
	ID       id.ID          `json:"id"`
	Revision int            `json:"revision"`
}
