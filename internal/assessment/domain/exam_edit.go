package domain

import (
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

// Locks describes the attempts that constrain edits to an exam.
type Locks struct {
	OpenAttempts        int
	SubmittedAttempts   int
	AnsweredQuestionIDs map[id.ID]struct{} // questions with a non-empty answer in any attempt
}

// EditViolation wraps one of the ErrEdit errors and names the question and option at fault
// when there is one.
type EditViolation struct {
	Err        error
	QuestionID id.ID // zero when not applicable
	OptionID   id.ID // zero when not applicable
}

func (v *EditViolation) Error() string {
	s := v.Err.Error()
	if !v.QuestionID.IsZero() {
		s += ": question " + v.QuestionID.String()
	}
	if !v.OptionID.IsZero() {
		s += ", option " + v.OptionID.String()
	}
	return s
}

func (v *EditViolation) Unwrap() error { return v.Err }

// CheckExamEdit returns the first way replacing cur with next would break existing attempts,
// or nil. Wording, points, question order, pass mark, retakes, reveal tightening, and deadline
// extensions are always allowed.
func CheckExamEdit(cur, next Exam, locks Locks) error {
	open, submitted := locks.OpenAttempts > 0, locks.SubmittedAttempts > 0
	if open && shortensDeadline(cur, next) {
		return &EditViolation{Err: ErrEditWouldTruncateAttempt}
	}
	if submitted && next.RevealPolicy.strictness() < cur.RevealPolicy.strictness() {
		return &EditViolation{Err: ErrEditKeyFrozen}
	}
	curByID := make(map[id.ID]Question, len(cur.Questions))
	for _, q := range cur.Questions {
		curByID[q.ID] = q
	}
	kept := make(map[id.ID]struct{}, len(next.Questions))
	for _, q := range next.Questions {
		old, ok := curByID[q.ID]
		if !ok {
			if open {
				return &EditViolation{Err: ErrEditAddDuringAttempt, QuestionID: q.ID}
			}
			continue
		}
		kept[q.ID] = struct{}{}
		if open || submitted {
			if v := keyChange(old, q); v != nil {
				return v
			}
		}
	}
	for _, q := range cur.Questions {
		if _, ok := kept[q.ID]; ok {
			continue
		}
		if _, answered := locks.AnsweredQuestionIDs[q.ID]; answered {
			return &EditViolation{Err: ErrEditQuestionAnswered, QuestionID: q.ID}
		}
		if open {
			return &EditViolation{Err: ErrEditAddDuringAttempt, QuestionID: q.ID}
		}
	}
	return nil
}

// shortensDeadline reports whether next could end some attempt earlier than cur would:
// an earlier or newly set close time, or a shorter or newly set time limit.
func shortensDeadline(cur, next Exam) bool {
	if next.ClosesAt != nil && (cur.ClosesAt == nil || next.ClosesAt.Before(*cur.ClosesAt)) {
		return true
	}
	return next.TimeLimit > 0 && (cur.TimeLimit == 0 || next.TimeLimit < cur.TimeLimit)
}

// keyChange reports a change to a question's type, option set, or option correctness.
func keyChange(old, next Question) *EditViolation {
	if old.Type != next.Type {
		return &EditViolation{Err: ErrEditKeyFrozen, QuestionID: old.ID}
	}
	oldCorrect := make(map[id.ID]bool, len(old.Options))
	for _, o := range old.Options {
		oldCorrect[o.ID] = o.IsCorrect
	}
	seen := make(map[id.ID]struct{}, len(next.Options))
	for _, o := range next.Options {
		was, ok := oldCorrect[o.ID]
		if !ok || was != o.IsCorrect {
			return &EditViolation{Err: ErrEditKeyFrozen, QuestionID: old.ID, OptionID: o.ID}
		}
		seen[o.ID] = struct{}{}
	}
	for _, o := range old.Options {
		if _, ok := seen[o.ID]; !ok {
			return &EditViolation{Err: ErrEditKeyFrozen, QuestionID: old.ID, OptionID: o.ID}
		}
	}
	return nil
}
