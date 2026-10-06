package domain_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/assessment/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

var t0 = time.Date(2026, 10, 6, 9, 0, 0, 0, time.UTC)

func opts(correct ...bool) []domain.Option {
	out := make([]domain.Option, len(correct))
	for i, c := range correct {
		out[i] = domain.Option{ID: id.ID(1000 + i), Label: " option ", IsCorrect: c}
	}
	return out
}

func TestNewQuestionAccepts(t *testing.T) {
	cases := map[string]struct {
		kind    domain.QuestionType
		options []domain.Option
	}{
		"single":     {domain.QuestionSingleChoice, opts(false, true, false)},
		"multiple":   {domain.QuestionMultipleChoice, opts(true, true, false)},
		"true_false": {domain.QuestionTrueFalse, opts(true, false)},
	}
	for name, tc := range cases {
		q, err := domain.NewQuestion(1, "  Why?  ", tc.kind, " because ", 2, 50, tc.options)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if q.Prompt != "Why?" || q.Explanation != "because" || q.Points != 2 || q.ReferenceLectureID != 50 || q.Options[0].Label != "option" {
			t.Fatalf("%s: %+v", name, q)
		}
	}
}

func TestNewQuestionRejects(t *testing.T) {
	long := func(n int) string { return strings.Repeat("é", n) }
	tooMany := make([]bool, 11)
	tooMany[0] = true
	cases := map[string]func() (domain.Question, error){
		"empty prompt": func() (domain.Question, error) {
			return domain.NewQuestion(1, "  ", domain.QuestionSingleChoice, "", 1, 0, opts(true, false))
		},
		"long prompt": func() (domain.Question, error) {
			return domain.NewQuestion(1, long(5001), domain.QuestionSingleChoice, "", 1, 0, opts(true, false))
		},
		"long explanation": func() (domain.Question, error) {
			return domain.NewQuestion(1, "p", domain.QuestionSingleChoice, long(5001), 1, 0, opts(true, false))
		},
		"unknown type": func() (domain.Question, error) {
			return domain.NewQuestion(1, "p", "essay", "", 1, 0, opts(true, false))
		},
		"zero points": func() (domain.Question, error) {
			return domain.NewQuestion(1, "p", domain.QuestionSingleChoice, "", 0, 0, opts(true, false))
		},
		"one option": func() (domain.Question, error) {
			return domain.NewQuestion(1, "p", domain.QuestionSingleChoice, "", 1, 0, opts(true))
		},
		"eleven options": func() (domain.Question, error) {
			return domain.NewQuestion(1, "p", domain.QuestionMultipleChoice, "", 1, 0, opts(tooMany...))
		},
		"blank label": func() (domain.Question, error) {
			return domain.NewQuestion(1, "p", domain.QuestionSingleChoice, "", 1, 0, []domain.Option{{ID: 1, Label: " ", IsCorrect: true}, {ID: 2, Label: "b"}})
		},
		"long label": func() (domain.Question, error) {
			return domain.NewQuestion(1, "p", domain.QuestionSingleChoice, "", 1, 0, []domain.Option{{ID: 1, Label: long(1001), IsCorrect: true}, {ID: 2, Label: "b"}})
		},
		"no correct": func() (domain.Question, error) {
			return domain.NewQuestion(1, "p", domain.QuestionMultipleChoice, "", 1, 0, opts(false, false))
		},
		"single two right": func() (domain.Question, error) {
			return domain.NewQuestion(1, "p", domain.QuestionSingleChoice, "", 1, 0, opts(true, true))
		},
		"true_false three": func() (domain.Question, error) {
			return domain.NewQuestion(1, "p", domain.QuestionTrueFalse, "", 1, 0, opts(true, false, false))
		},
		"true_false 2 right": func() (domain.Question, error) {
			return domain.NewQuestion(1, "p", domain.QuestionTrueFalse, "", 1, 0, opts(true, true))
		},
	}
	for name, build := range cases {
		if _, err := build(); !errors.Is(err, domain.ErrInvalidQuestion) {
			t.Fatalf("%s: err = %v", name, err)
		}
	}
	// Character count, not bytes: 5,000 two-byte runes are allowed.
	if _, err := domain.NewQuestion(1, long(5000), domain.QuestionSingleChoice, "", 1, 0, opts(true, false)); err != nil {
		t.Fatalf("5000 runes: %v", err)
	}
}

func question(t *testing.T, qid id.ID, optionIDs ...id.ID) domain.Question {
	t.Helper()
	options := make([]domain.Option, len(optionIDs))
	for i, o := range optionIDs {
		options[i] = domain.Option{ID: o, Label: "o", IsCorrect: i == 0}
	}
	q, err := domain.NewQuestion(qid, "p", domain.QuestionSingleChoice, "", 1, 0, options)
	if err != nil {
		t.Fatal(err)
	}
	return q
}

func TestNewQuiz(t *testing.T) {
	qs := []domain.Question{question(t, 1, 11, 12), question(t, 2, 21, 22)}
	q, err := domain.NewQuiz(7, 10, 50, 0, qs, t0, t0)
	if err != nil || q.ID != 7 || q.CourseID != 10 || q.LectureID != 50 || len(q.Questions) != 2 {
		t.Fatalf("q=%+v err=%v", q, err)
	}
	if got := q.IDs(); len(got) != 6 {
		t.Fatalf("IDs = %v", got)
	}
	bad := map[string][]domain.Question{
		"no questions":            nil,
		"duplicate question id":   {question(t, 1, 11, 12), question(t, 1, 21, 22)},
		"duplicate option id":     {question(t, 1, 11, 12), question(t, 2, 12, 22)},
		"option id = question id": {question(t, 1, 11, 12), question(t, 2, 1, 22)},
	}
	for name, qs := range bad {
		if _, err := domain.NewQuiz(7, 10, 50, 0, qs, t0, t0); !errors.Is(err, domain.ErrInvalidQuiz) {
			t.Fatalf("%s: err = %v", name, err)
		}
	}
	if _, err := domain.NewQuiz(7, 10, 50, -1, []domain.Question{question(t, 1, 11, 12)}, t0, t0); !errors.Is(err, domain.ErrInvalidQuiz) {
		t.Fatalf("negative position: %v", err)
	}
	if _, err := domain.NewQuiz(7, 10, 50, domain.MaxPosition+1, []domain.Question{question(t, 1, 11, 12)}, t0, t0); !errors.Is(err, domain.ErrInvalidQuiz) {
		t.Fatalf("position above max: %v", err)
	}
	many := make([]domain.Question, domain.MaxQuestions+1)
	for i := range many {
		many[i] = question(t, id.ID(10000+i*3), id.ID(10001+i*3), id.ID(10002+i*3))
	}
	if _, err := domain.NewQuiz(7, 10, 50, 0, many, t0, t0); !errors.Is(err, domain.ErrInvalidQuiz) {
		t.Fatalf("too many: %v", err)
	}
}

func TestCheckAnswers(t *testing.T) {
	q, err := domain.NewQuiz(7, 10, 50, 0, []domain.Question{question(t, 1, 11, 12), question(t, 2, 21, 22)}, t0, t0)
	if err != nil {
		t.Fatal(err)
	}
	ok := [][]domain.Answer{
		nil,
		{{QuestionID: 1, OptionIDs: []id.ID{11}}},
		{{QuestionID: 1, OptionIDs: []id.ID{12}}, {QuestionID: 2, OptionIDs: nil}},
	}
	for i, a := range ok {
		if err := q.CheckAnswers(a); err != nil {
			t.Fatalf("ok %d: %v", i, err)
		}
	}
	bad := map[string][]domain.Answer{
		"unknown question":  {{QuestionID: 3, OptionIDs: []id.ID{11}}},
		"option of other q": {{QuestionID: 1, OptionIDs: []id.ID{21}}},
		"question twice":    {{QuestionID: 1, OptionIDs: []id.ID{11}}, {QuestionID: 1, OptionIDs: []id.ID{12}}},
		"option twice":      {{QuestionID: 1, OptionIDs: []id.ID{11, 11}}},
	}
	for name, a := range bad {
		if err := q.CheckAnswers(a); !errors.Is(err, domain.ErrInvalidAnswer) {
			t.Fatalf("%s: err = %v", name, err)
		}
	}
}
