package domain

import (
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

// ExamAnswer is a student's choice for one question. Grading fills the last three fields with a
// snapshot, so later edits to the exam never rewrite a recorded result.
type ExamAnswer struct {
	QuestionID     id.ID
	OptionIDs      []id.ID
	IsCorrect      *bool
	PointsPossible int
	PointsAwarded  *int
}

type ExamAttempt struct {
	ID            id.ID
	ExamID        id.ID
	CourseID      id.ID
	UserID        id.ID
	StartedAt     time.Time
	SubmittedAt   *time.Time
	Score         *int
	Passed        *bool
	AutoSubmitted bool
	Answers       []ExamAnswer
}

func NewExamAttempt(attemptID id.ID, e Exam, userID id.ID, startedAt time.Time) ExamAttempt {
	return ExamAttempt{ID: attemptID, ExamID: e.ID, CourseID: e.CourseID, UserID: userID,
		StartedAt: startedAt.UTC(), Answers: []ExamAnswer{}}
}

func (a ExamAttempt) Open() bool { return a.SubmittedAt == nil }

// Deadline is the earlier of StartedAt plus the exam's time limit and the exam's close time, or
// nil when neither applies. It follows the current exam, so extending either extends the attempt.
func (a ExamAttempt) Deadline(e Exam) *time.Time {
	var d *time.Time
	if e.TimeLimit > 0 {
		t := a.StartedAt.Add(e.TimeLimit)
		d = &t
	}
	if e.ClosesAt != nil && (d == nil || e.ClosesAt.Before(*d)) {
		t := *e.ClosesAt
		d = &t
	}
	return d
}

// CheckWritable allows answers and submission while the attempt is open and now is not past
// its deadline.
func (a ExamAttempt) CheckWritable(e Exam, now time.Time) error {
	if !a.Open() {
		return ErrAttemptSubmitted
	}
	if d := a.Deadline(e); d != nil && now.After(*d) {
		return ErrAttemptExpired
	}
	return nil
}

// Grade scores the attempt against e as of at and closes it. Every exam question gets an
// answer, in exam order; unanswered questions are wrong. A question is correct only when the
// chosen options are exactly its correct options. Grade does not check writability.
func (a *ExamAttempt) Grade(e Exam, at time.Time) {
	chosen := make(map[id.ID][]id.ID, len(a.Answers))
	for _, ans := range a.Answers {
		chosen[ans.QuestionID] = ans.OptionIDs
	}
	graded := make([]ExamAnswer, len(e.Questions))
	earned, possible := 0, 0
	for i, q := range e.Questions {
		picked := chosen[q.ID]
		if picked == nil {
			picked = []id.ID{}
		}
		correct := sameSet(picked, q.CorrectOptionIDs())
		awarded := 0
		if correct {
			awarded = q.Points
		}
		graded[i] = ExamAnswer{QuestionID: q.ID, OptionIDs: picked, IsCorrect: &correct,
			PointsPossible: q.Points, PointsAwarded: &awarded}
		earned += awarded
		possible += q.Points
	}
	score := 0
	if possible > 0 {
		score = earned * 100 / possible
	}
	passed := score >= e.PassMark
	at = at.UTC()
	a.Answers, a.Score, a.Passed, a.SubmittedAt = graded, &score, &passed, &at
}

// Settle grades an open attempt as of its deadline once now is past it, marks it
// auto-submitted, and reports whether it changed anything.
func (a *ExamAttempt) Settle(e Exam, now time.Time) bool {
	if !a.Open() {
		return false
	}
	d := a.Deadline(e)
	if d == nil || !now.After(*d) {
		return false
	}
	a.Grade(e, *d)
	a.AutoSubmitted = true
	return true
}

// CheckReveal applies the exam's reveal policy for the attempt's owner.
func (a ExamAttempt) CheckReveal(e Exam, now time.Time) error {
	switch {
	case a.Open():
		return ErrRevealAttemptOpen
	case e.RevealPolicy == RevealNever:
		return ErrRevealDisabled
	case e.RevealPolicy == RevealAfterClose && (e.ClosesAt == nil || now.Before(*e.ClosesAt)):
		at := time.Time{}
		if e.ClosesAt != nil {
			at = *e.ClosesAt
		}
		return &RevealNotYetError{At: at}
	}
	return nil
}

// sameSet reports whether picked, which has no repeats, holds exactly the IDs in want.
func sameSet(picked, want []id.ID) bool {
	if len(picked) != len(want) {
		return false
	}
	set := make(map[id.ID]struct{}, len(want))
	for _, v := range want {
		set[v] = struct{}{}
	}
	for _, v := range picked {
		if _, ok := set[v]; !ok {
			return false
		}
	}
	return true
}
