// Package domain holds the assessment model: questions, quizzes, and quiz attempts.
package domain

import (
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

const (
	MaxPromptLen      = 5000
	MaxExplanationLen = 5000
	MaxOptionLabelLen = 1000
	MinOptions        = 2
	MaxOptions        = 10
	MaxQuestions      = 100
	MaxPosition       = 10000
)

type QuestionType string

const (
	QuestionSingleChoice   QuestionType = "single_choice"
	QuestionMultipleChoice QuestionType = "multiple_choice"
	QuestionTrueFalse      QuestionType = "true_false"
)

func (t QuestionType) valid() bool {
	return t == QuestionSingleChoice || t == QuestionMultipleChoice || t == QuestionTrueFalse
}

type Option struct {
	ID        id.ID
	Label     string
	IsCorrect bool
}

type Question struct {
	ID                 id.ID
	Prompt             string
	Type               QuestionType
	Explanation        string
	Points             int
	ReferenceLectureID id.ID // zero when absent
	Options            []Option
}

// NewQuestion trims text and validates the question's shape and answer key.
func NewQuestion(questionID id.ID, prompt string, kind QuestionType, explanation string, points int, referenceLectureID id.ID, options []Option) (Question, error) {
	prompt, explanation = strings.TrimSpace(prompt), strings.TrimSpace(explanation)
	switch {
	case prompt == "":
		return Question{}, fmt.Errorf("%w: prompt is required", ErrInvalidQuestion)
	case utf8.RuneCountInString(prompt) > MaxPromptLen:
		return Question{}, fmt.Errorf("%w: prompt exceeds %d characters", ErrInvalidQuestion, MaxPromptLen)
	case utf8.RuneCountInString(explanation) > MaxExplanationLen:
		return Question{}, fmt.Errorf("%w: explanation exceeds %d characters", ErrInvalidQuestion, MaxExplanationLen)
	case !kind.valid():
		return Question{}, fmt.Errorf("%w: unknown type %q", ErrInvalidQuestion, kind)
	case points < 1:
		return Question{}, fmt.Errorf("%w: points must be at least 1", ErrInvalidQuestion)
	case len(options) < MinOptions || len(options) > MaxOptions:
		return Question{}, fmt.Errorf("%w: a question has %d to %d options", ErrInvalidQuestion, MinOptions, MaxOptions)
	}
	opts := make([]Option, len(options))
	correct := 0
	for i, o := range options {
		label := strings.TrimSpace(o.Label)
		if label == "" || utf8.RuneCountInString(label) > MaxOptionLabelLen {
			return Question{}, fmt.Errorf("%w: option labels are 1 to %d characters", ErrInvalidQuestion, MaxOptionLabelLen)
		}
		if o.IsCorrect {
			correct++
		}
		opts[i] = Option{ID: o.ID, Label: label, IsCorrect: o.IsCorrect}
	}
	switch {
	case correct == 0:
		return Question{}, fmt.Errorf("%w: at least one option must be correct", ErrInvalidQuestion)
	case kind != QuestionMultipleChoice && correct != 1:
		return Question{}, fmt.Errorf("%w: %s questions have exactly one correct option", ErrInvalidQuestion, kind)
	case kind == QuestionTrueFalse && len(opts) != 2:
		return Question{}, fmt.Errorf("%w: true_false questions have exactly two options", ErrInvalidQuestion)
	}
	return Question{ID: questionID, Prompt: prompt, Type: kind, Explanation: explanation, Points: points,
		ReferenceLectureID: referenceLectureID, Options: opts}, nil
}

// Quiz is an ordered set of questions attached to one lecture.
type Quiz struct {
	ID        id.ID
	CourseID  id.ID
	LectureID id.ID
	Revision  int // set by storage; 0 before the first save
	Position  int
	Questions []Question
	CreatedAt time.Time
	UpdatedAt time.Time
}

// NewQuiz validates the quiz size and that question and option IDs are unique across it.
func NewQuiz(quizID, courseID, lectureID id.ID, position int, questions []Question, createdAt, updatedAt time.Time) (Quiz, error) {
	if position < 0 || position > MaxPosition {
		return Quiz{}, fmt.Errorf("%w: position must be 0 to %d", ErrInvalidQuiz, MaxPosition)
	}
	if len(questions) == 0 || len(questions) > MaxQuestions {
		return Quiz{}, fmt.Errorf("%w: a quiz has 1 to %d questions", ErrInvalidQuiz, MaxQuestions)
	}
	if err := checkUniqueIDs(questions); err != nil {
		return Quiz{}, fmt.Errorf("%w: %w", ErrInvalidQuiz, err)
	}
	return Quiz{ID: quizID, CourseID: courseID, LectureID: lectureID, Position: position,
		Questions: slices.Clone(questions), CreatedAt: createdAt.UTC(), UpdatedAt: updatedAt.UTC()}, nil
}

// IDs returns every question and option ID in the quiz.
func (q Quiz) IDs() map[id.ID]struct{} { return questionIDs(q.Questions) }

type Answer struct {
	QuestionID id.ID
	OptionIDs  []id.ID
}

// CheckAnswers rejects answers that name a question or option outside the quiz, or repeat one.
// Unanswered questions are allowed.
func (q Quiz) CheckAnswers(answers []Answer) error {
	options := make(map[id.ID]map[id.ID]struct{}, len(q.Questions))
	for _, qu := range q.Questions {
		set := make(map[id.ID]struct{}, len(qu.Options))
		for _, o := range qu.Options {
			set[o.ID] = struct{}{}
		}
		options[qu.ID] = set
	}
	answered := make(map[id.ID]struct{}, len(answers))
	for _, a := range answers {
		set, ok := options[a.QuestionID]
		if !ok {
			return fmt.Errorf("%w: question %s is not in the quiz", ErrInvalidAnswer, a.QuestionID)
		}
		if _, dup := answered[a.QuestionID]; dup {
			return fmt.Errorf("%w: question %s is answered twice", ErrInvalidAnswer, a.QuestionID)
		}
		answered[a.QuestionID] = struct{}{}
		picked := make(map[id.ID]struct{}, len(a.OptionIDs))
		for _, o := range a.OptionIDs {
			if _, ok := set[o]; !ok {
				return fmt.Errorf("%w: option %s is not in question %s", ErrInvalidAnswer, o, a.QuestionID)
			}
			if _, dup := picked[o]; dup {
				return fmt.Errorf("%w: option %s is chosen twice", ErrInvalidAnswer, o)
			}
			picked[o] = struct{}{}
		}
	}
	return nil
}

// QuizAttempt is a student's submitted answers. The client grades; the server only stores.
type QuizAttempt struct {
	ID             id.ID
	QuizID         id.ID
	Revision       int // the quiz revision the answers were checked against
	UserID         id.ID
	Answers        []Answer
	IdempotencyKey string // empty when the request had no key
	SubmittedAt    time.Time
}
