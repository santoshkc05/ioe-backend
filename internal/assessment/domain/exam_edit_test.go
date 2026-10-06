package domain_test

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/assessment/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

var (
	noAttempts    = domain.Locks{}
	openAttempt   = domain.Locks{OpenAttempts: 1}
	submittedOnly = domain.Locks{SubmittedAttempts: 1}
	answeredQ3    = domain.Locks{SubmittedAttempts: 1, AnsweredQuestionIDs: map[id.ID]struct{}{3: {}}}
	bothKinds     = domain.Locks{OpenAttempts: 1, SubmittedAttempts: 1}
)

// liveExam closes at minute 120, allows 30 minutes, and reveals after close.
func liveExam(t *testing.T) domain.Exam {
	t.Helper()
	e := examDraft(t)
	e.ClosesAt, e.TimeLimit, e.RevealPolicy = at(120), 30*time.Minute, domain.RevealAfterClose
	return mustExam(t, e)
}

// edited returns a deep copy of e with fn applied.
func edited(e domain.Exam, fn func(e *domain.Exam)) domain.Exam {
	e.Questions = slices.Clone(e.Questions)
	for i := range e.Questions {
		e.Questions[i].Options = slices.Clone(e.Questions[i].Options)
	}
	fn(&e)
	return e
}

func TestCheckExamEditRejects(t *testing.T) {
	cases := map[string]struct {
		locks    domain.Locks
		edit     func(e *domain.Exam)
		want     error
		question id.ID
		option   id.ID
	}{
		"earlier close": {openAttempt, func(e *domain.Exam) { e.ClosesAt = at(60) }, domain.ErrEditWouldTruncateAttempt, 0, 0},
		"shorter limit": {openAttempt, func(e *domain.Exam) { e.TimeLimit = 10 * time.Minute }, domain.ErrEditWouldTruncateAttempt, 0, 0},
		"looser reveal": {submittedOnly, func(e *domain.Exam) { e.RevealPolicy = domain.RevealAfterAttempt }, domain.ErrEditKeyFrozen, 0, 0},
		"add during attempt": {openAttempt, func(e *domain.Exam) {
			e.Questions = append(e.Questions, examQuestion(t, 4, domain.QuestionTrueFalse, 1, true, false))
		}, domain.ErrEditAddDuringAttempt, 4, 0},
		"remove during open": {openAttempt, func(e *domain.Exam) { e.Questions = e.Questions[:2] }, domain.ErrEditAddDuringAttempt, 3, 0},
		"remove answered":    {answeredQ3, func(e *domain.Exam) { e.Questions = e.Questions[:2] }, domain.ErrEditQuestionAnswered, 3, 0},
		"type change":        {submittedOnly, func(e *domain.Exam) { e.Questions[0].Type = domain.QuestionMultipleChoice }, domain.ErrEditKeyFrozen, 1, 0},
		"correctness flip":   {openAttempt, func(e *domain.Exam) { e.Questions[0].Options[1].IsCorrect = true }, domain.ErrEditKeyFrozen, 1, 11},
		"added option": {submittedOnly, func(e *domain.Exam) {
			e.Questions[1].Options = append(e.Questions[1].Options, domain.Option{ID: 23, Label: "x"})
		}, domain.ErrEditKeyFrozen, 2, 23},
		"removed option":       {submittedOnly, func(e *domain.Exam) { e.Questions[1].Options = e.Questions[1].Options[:2] }, domain.ErrEditKeyFrozen, 2, 22},
		"replaced option id":   {submittedOnly, func(e *domain.Exam) { e.Questions[0].Options[1].ID = 19 }, domain.ErrEditKeyFrozen, 1, 19},
		"close set from unset": {openAttempt, func(e *domain.Exam) { e.ClosesAt, e.RevealPolicy = at(500), domain.RevealNever }, nil, 0, 0},
	}
	for name, tc := range cases {
		cur := liveExam(t)
		if name == "close set from unset" {
			// Starting without a close time, setting any close time shortens the deadline.
			cur.ClosesAt, cur.RevealPolicy = nil, domain.RevealNever
			tc.want = domain.ErrEditWouldTruncateAttempt
		}
		err := domain.CheckExamEdit(cur, edited(cur, tc.edit), tc.locks)
		var v *domain.EditViolation
		if !errors.Is(err, tc.want) || !errors.As(err, &v) || v.QuestionID != tc.question || v.OptionID != tc.option {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestCheckExamEditAllows(t *testing.T) {
	cases := map[string]struct {
		locks domain.Locks
		edit  func(e *domain.Exam)
	}{
		"key change without attempts": {noAttempts, func(e *domain.Exam) { e.Questions[0].Options[1].IsCorrect = true }},
		"remove without attempts":     {noAttempts, func(e *domain.Exam) { e.Questions = e.Questions[:1] }},
		"shorten without open":        {submittedOnly, func(e *domain.Exam) { e.ClosesAt, e.TimeLimit = at(60), time.Minute }},
		"add with only submitted": {submittedOnly, func(e *domain.Exam) {
			e.Questions = append(e.Questions, examQuestion(t, 4, domain.QuestionTrueFalse, 1, true, false))
		}},
		"remove unanswered submitted": {answeredQ3, func(e *domain.Exam) { e.Questions = e.Questions[1:] }},
		"extend and clear limits":     {bothKinds, func(e *domain.Exam) { e.ClosesAt, e.TimeLimit = at(500), 0 }},
		"clear close":                 {bothKinds, func(e *domain.Exam) { e.ClosesAt, e.RevealPolicy = nil, domain.RevealNever }},
		"tighten reveal":              {bothKinds, func(e *domain.Exam) { e.RevealPolicy = domain.RevealNever }},
		"wording, points, order": {bothKinds, func(e *domain.Exam) {
			e.Title, e.Description, e.PassMark, e.RetakesAllowed = "New", "new", 90, true
			e.Questions[0].Prompt, e.Questions[0].Explanation, e.Questions[0].Points = "new?", "because", 9
			e.Questions[0].Options[0].Label = "new label"
			e.Questions[0].ReferenceLectureID = 77
			e.Questions[0], e.Questions[2] = e.Questions[2], e.Questions[0]
			e.Questions[1].Options[0], e.Questions[1].Options[1] = e.Questions[1].Options[1], e.Questions[1].Options[0]
		}},
	}
	for name, tc := range cases {
		cur := liveExam(t)
		if err := domain.CheckExamEdit(cur, edited(cur, tc.edit), tc.locks); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestEditViolationMessage(t *testing.T) {
	v := &domain.EditViolation{Err: domain.ErrEditKeyFrozen, QuestionID: 1, OptionID: 11}
	if v.Error() != "the answer key is frozen once an attempt exists: question 1, option 11" {
		t.Fatalf("message = %q", v.Error())
	}
}
