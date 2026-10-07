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
	MaxTitleLen       = 200
	MaxDescriptionLen = 5000
	MaxTimeLimit      = 24 * time.Hour
)

type ExamStatus string

const (
	ExamDraft     ExamStatus = "draft"
	ExamPublished ExamStatus = "published"
)

type RevealPolicy string

const (
	RevealAfterAttempt RevealPolicy = "after_attempt"
	RevealAfterClose   RevealPolicy = "after_close"
	RevealNever        RevealPolicy = "never"
)

// strictness ranks policies from most revealing (0) to least (2), and -1 for unknown values.
func (p RevealPolicy) strictness() int {
	switch p {
	case RevealAfterAttempt:
		return 0
	case RevealAfterClose:
		return 1
	case RevealNever:
		return 2
	}
	return -1
}

type Availability string

const (
	AvailabilityNotOpen Availability = "not_open"
	AvailabilityOpen    Availability = "open"
	AvailabilityClosed  Availability = "closed"
)

// Exam is a server-graded set of questions in a course, optionally timed and windowed.
type Exam struct {
	ID             id.ID
	CourseID       id.ID
	Revision       int // set by storage; 0 before the first save
	Title          string
	Description    string
	Position       int
	Status         ExamStatus
	PassMark       int           // percent
	TimeLimit      time.Duration // zero means untimed
	RetakesAllowed bool
	OpensAt        *time.Time
	ClosesAt       *time.Time
	RevealPolicy   RevealPolicy
	Questions      []Question
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// NewExam trims e's text, validates it, and returns a copy with UTC times.
func NewExam(e Exam) (Exam, error) {
	e.Title, e.Description = strings.TrimSpace(e.Title), strings.TrimSpace(e.Description)
	switch {
	case e.Title == "" || utf8.RuneCountInString(e.Title) > MaxTitleLen:
		return Exam{}, fmt.Errorf("%w: title is 1 to %d characters", ErrInvalidExam, MaxTitleLen)
	case utf8.RuneCountInString(e.Description) > MaxDescriptionLen:
		return Exam{}, fmt.Errorf("%w: description exceeds %d characters", ErrInvalidExam, MaxDescriptionLen)
	case e.Position < 0 || e.Position > MaxPosition:
		return Exam{}, fmt.Errorf("%w: position must be 0 to %d", ErrInvalidExam, MaxPosition)
	case e.Status != ExamDraft && e.Status != ExamPublished:
		return Exam{}, fmt.Errorf("%w: unknown status %q", ErrInvalidExam, e.Status)
	case e.RevealPolicy.strictness() < 0:
		return Exam{}, fmt.Errorf("%w: unknown reveal policy %q", ErrInvalidExam, e.RevealPolicy)
	case e.PassMark < 0 || e.PassMark > 100:
		return Exam{}, fmt.Errorf("%w: pass mark must be 0 to 100", ErrInvalidExam)
	case e.TimeLimit < 0 || e.TimeLimit > MaxTimeLimit || e.TimeLimit%time.Second != 0:
		return Exam{}, fmt.Errorf("%w: time limit is whole seconds up to %s", ErrInvalidExam, MaxTimeLimit)
	case e.OpensAt != nil && e.ClosesAt != nil && !e.ClosesAt.After(*e.OpensAt):
		return Exam{}, fmt.Errorf("%w: closes_at must be after opens_at", ErrInvalidExam)
	case e.RevealPolicy == RevealAfterClose && e.ClosesAt == nil:
		return Exam{}, fmt.Errorf("%w: after_close needs closes_at", ErrInvalidExam)
	case len(e.Questions) == 0 || len(e.Questions) > MaxQuestions:
		return Exam{}, fmt.Errorf("%w: an exam has 1 to %d questions", ErrInvalidExam, MaxQuestions)
	}
	if err := checkUniqueIDs(e.Questions); err != nil {
		return Exam{}, fmt.Errorf("%w: %w", ErrInvalidExam, err)
	}
	e.OpensAt, e.ClosesAt = utc(e.OpensAt), utc(e.ClosesAt)
	e.Questions = slices.Clone(e.Questions)
	e.CreatedAt, e.UpdatedAt = e.CreatedAt.UTC(), e.UpdatedAt.UTC()
	return e, nil
}

func utc(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

// Availability reports whether attempts may start at now.
func (e Exam) Availability(now time.Time) Availability {
	switch {
	case e.OpensAt != nil && now.Before(*e.OpensAt):
		return AvailabilityNotOpen
	case e.ClosesAt != nil && !now.Before(*e.ClosesAt):
		return AvailabilityClosed
	}
	return AvailabilityOpen
}

func (e Exam) TotalPoints() int {
	total := 0
	for _, q := range e.Questions {
		total += q.Points
	}
	return total
}

// IDs returns every question and option ID in the exam.
func (e Exam) IDs() map[id.ID]struct{} { return questionIDs(e.Questions) }

// Question returns the exam's question with the given ID.
func (e Exam) Question(questionID id.ID) (Question, bool) {
	for _, q := range e.Questions {
		if q.ID == questionID {
			return q, true
		}
	}
	return Question{}, false
}

// CheckAnswer rejects an answer naming a question or option outside the exam, repeating an
// option, or choosing more than one option of a single_choice or true_false question. An empty
// option list clears the answer.
func (e Exam) CheckAnswer(a ExamAnswer) error {
	q, ok := e.Question(a.QuestionID)
	if !ok {
		return fmt.Errorf("%w: question %s is not in the exam", ErrInvalidAnswer, a.QuestionID)
	}
	if q.Type != QuestionMultipleChoice && len(a.OptionIDs) > 1 {
		return fmt.Errorf("%w: %s questions take one option", ErrInvalidAnswer, q.Type)
	}
	valid := make(map[id.ID]struct{}, len(q.Options))
	for _, o := range q.Options {
		valid[o.ID] = struct{}{}
	}
	picked := make(map[id.ID]struct{}, len(a.OptionIDs))
	for _, o := range a.OptionIDs {
		if _, ok := valid[o]; !ok {
			return fmt.Errorf("%w: option %s is not in question %s", ErrInvalidAnswer, o, q.ID)
		}
		if _, dup := picked[o]; dup {
			return fmt.Errorf("%w: option %s is chosen twice", ErrInvalidAnswer, o)
		}
		picked[o] = struct{}{}
	}
	return nil
}
