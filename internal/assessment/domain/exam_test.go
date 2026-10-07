package domain_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/assessment/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

// examQuestion builds a question whose options are qid*10, qid*10+1, ... with the given correctness.
func examQuestion(t *testing.T, qid id.ID, kind domain.QuestionType, points int, correct ...bool) domain.Question {
	t.Helper()
	options := make([]domain.Option, len(correct))
	for i, c := range correct {
		options[i] = domain.Option{ID: qid*10 + id.ID(i), Label: "o", IsCorrect: c}
	}
	q, err := domain.NewQuestion(qid, "p", kind, "e", points, 0, options)
	if err != nil {
		t.Fatal(err)
	}
	return q
}

// examDraft has questions 1 (single, options 10 correct, 11), 2 (multiple, 20 and 21 correct,
// 22), and 3 (true/false, 30, 31 correct), worth 1, 2, and 3 points.
func examDraft(t *testing.T) domain.Exam {
	t.Helper()
	return domain.Exam{ID: 1, CourseID: 10, Title: " Final ", Description: " d ", Status: domain.ExamDraft,
		PassMark: 50, RevealPolicy: domain.RevealAfterAttempt, CreatedAt: t0, UpdatedAt: t0,
		Questions: []domain.Question{
			examQuestion(t, 1, domain.QuestionSingleChoice, 1, true, false),
			examQuestion(t, 2, domain.QuestionMultipleChoice, 2, true, true, false),
			examQuestion(t, 3, domain.QuestionTrueFalse, 3, false, true),
		}}
}

func mustExam(t *testing.T, e domain.Exam) domain.Exam {
	t.Helper()
	out, err := domain.NewExam(e)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// at returns t0 plus the given minutes.
func at(minutes int) *time.Time {
	v := t0.Add(time.Duration(minutes) * time.Minute)
	return &v
}

func TestNewExamAccepts(t *testing.T) {
	nepal := time.FixedZone("NPT", 5*3600+45*60)
	e := examDraft(t)
	opens := t0.In(nepal)
	e.OpensAt, e.ClosesAt, e.TimeLimit = &opens, at(60), 30*time.Minute
	e.RevealPolicy = domain.RevealAfterClose
	got := mustExam(t, e)
	if got.Title != "Final" || got.Description != "d" || got.OpensAt.Location() != time.UTC || !got.OpensAt.Equal(t0) {
		t.Fatalf("got %+v", got)
	}
	if got.TotalPoints() != 6 {
		t.Fatalf("total = %d", got.TotalPoints())
	}
	if q, ok := got.Question(2); !ok || q.Points != 2 {
		t.Fatalf("question 2 = %+v %v", q, ok)
	}
}

func TestNewExamRejects(t *testing.T) {
	cases := map[string]func(e *domain.Exam){
		"empty title":          func(e *domain.Exam) { e.Title = "  " },
		"long title":           func(e *domain.Exam) { e.Title = strings.Repeat("é", 201) },
		"long description":     func(e *domain.Exam) { e.Description = strings.Repeat("é", 5001) },
		"negative position":    func(e *domain.Exam) { e.Position = -1 },
		"large position":       func(e *domain.Exam) { e.Position = domain.MaxPosition + 1 },
		"unknown status":       func(e *domain.Exam) { e.Status = "archived" },
		"unknown reveal":       func(e *domain.Exam) { e.RevealPolicy = "during_attempt" },
		"negative pass mark":   func(e *domain.Exam) { e.PassMark = -1 },
		"pass mark over 100":   func(e *domain.Exam) { e.PassMark = 101 },
		"negative time limit":  func(e *domain.Exam) { e.TimeLimit = -time.Second },
		"fractional limit":     func(e *domain.Exam) { e.TimeLimit = 1500 * time.Millisecond },
		"limit over 24h":       func(e *domain.Exam) { e.TimeLimit = 24*time.Hour + time.Second },
		"closes before opens":  func(e *domain.Exam) { e.OpensAt, e.ClosesAt = at(60), at(60) },
		"after_close no close": func(e *domain.Exam) { e.RevealPolicy = domain.RevealAfterClose },
		"no questions":         func(e *domain.Exam) { e.Questions = nil },
		"duplicate ids": func(e *domain.Exam) {
			e.Questions[1].ID = e.Questions[0].ID
		},
	}
	for name, mutate := range cases {
		e := examDraft(t)
		mutate(&e)
		if _, err := domain.NewExam(e); !errors.Is(err, domain.ErrInvalidExam) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	e := examDraft(t)
	for len(e.Questions) <= domain.MaxQuestions {
		e.Questions = append(e.Questions, examQuestion(t, id.ID(100+len(e.Questions)), domain.QuestionTrueFalse, 1, true, false))
	}
	if _, err := domain.NewExam(e); !errors.Is(err, domain.ErrInvalidExam) {
		t.Errorf("too many questions: err = %v", err)
	}
}

func TestAvailability(t *testing.T) {
	e := examDraft(t)
	if got := e.Availability(t0); got != domain.AvailabilityOpen {
		t.Fatalf("no window = %s", got)
	}
	e.OpensAt, e.ClosesAt = at(10), at(20)
	for minutes, want := range map[int]domain.Availability{
		9: domain.AvailabilityNotOpen, 10: domain.AvailabilityOpen, 19: domain.AvailabilityOpen, 20: domain.AvailabilityClosed,
	} {
		if got := e.Availability(*at(minutes)); got != want {
			t.Errorf("minute %d = %s, want %s", minutes, got, want)
		}
	}
}

func TestDeadline(t *testing.T) {
	a := domain.ExamAttempt{StartedAt: t0}
	cases := map[string]struct {
		limit time.Duration
		close *time.Time
		want  *time.Time
	}{
		"untimed":        {0, nil, nil},
		"limit only":     {30 * time.Minute, nil, at(30)},
		"close only":     {0, at(45), at(45)},
		"limit earlier":  {30 * time.Minute, at(45), at(30)},
		"close earlier":  {time.Hour, at(45), at(45)},
		"same deadlines": {45 * time.Minute, at(45), at(45)},
	}
	for name, tc := range cases {
		e := examDraft(t)
		e.TimeLimit, e.ClosesAt = tc.limit, tc.close
		got := a.Deadline(e)
		if (got == nil) != (tc.want == nil) || (got != nil && !got.Equal(*tc.want)) {
			t.Errorf("%s: deadline = %v, want %v", name, got, tc.want)
		}
	}
}

func TestCheckWritable(t *testing.T) {
	e := examDraft(t)
	e.TimeLimit = 30 * time.Minute
	a := domain.NewExamAttempt(7, e, 200, t0)
	if err := a.CheckWritable(e, *at(30)); err != nil {
		t.Fatalf("at deadline: %v", err)
	}
	if err := a.CheckWritable(e, at(30).Add(time.Nanosecond)); !errors.Is(err, domain.ErrAttemptExpired) {
		t.Fatalf("after deadline: %v", err)
	}
	a.Grade(e, *at(5))
	if err := a.CheckWritable(e, *at(6)); !errors.Is(err, domain.ErrAttemptSubmitted) {
		t.Fatalf("submitted: %v", err)
	}
}

func TestCheckAnswer(t *testing.T) {
	e := examDraft(t)
	ok := []domain.ExamAnswer{
		{QuestionID: 1, OptionIDs: []id.ID{11}},
		{QuestionID: 2, OptionIDs: []id.ID{20, 22}},
		{QuestionID: 3, OptionIDs: []id.ID{}},
	}
	for _, a := range ok {
		if err := e.CheckAnswer(a); err != nil {
			t.Errorf("%+v: %v", a, err)
		}
	}
	bad := map[string]domain.ExamAnswer{
		"unknown question":  {QuestionID: 9, OptionIDs: []id.ID{10}},
		"foreign option":    {QuestionID: 1, OptionIDs: []id.ID{20}},
		"repeated option":   {QuestionID: 2, OptionIDs: []id.ID{20, 20}},
		"two single picks":  {QuestionID: 1, OptionIDs: []id.ID{10, 11}},
		"two boolean picks": {QuestionID: 3, OptionIDs: []id.ID{30, 31}},
	}
	for name, a := range bad {
		if err := e.CheckAnswer(a); !errors.Is(err, domain.ErrInvalidAnswer) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestGrade(t *testing.T) {
	e := examDraft(t)
	e.PassMark = 17
	a := domain.NewExamAttempt(7, e, 200, t0)
	// Question 1 right, question 2 half right (wrong), question 3 unanswered: 1 of 6 points.
	a.Answers = []domain.ExamAnswer{
		{QuestionID: 2, OptionIDs: []id.ID{20}},
		{QuestionID: 1, OptionIDs: []id.ID{10}},
	}
	a.Grade(e, *at(5))
	if *a.Score != 16 || *a.Passed || !a.SubmittedAt.Equal(*at(5)) || a.Open() {
		t.Fatalf("score=%d passed=%v submitted=%v", *a.Score, *a.Passed, a.SubmittedAt)
	}
	want := []struct {
		q       id.ID
		correct bool
		awarded int
		picked  int
	}{{1, true, 1, 1}, {2, false, 0, 1}, {3, false, 0, 0}}
	if len(a.Answers) != 3 {
		t.Fatalf("answers = %+v", a.Answers)
	}
	for i, w := range want {
		got := a.Answers[i]
		if got.QuestionID != w.q || *got.IsCorrect != w.correct || *got.PointsAwarded != w.awarded ||
			got.PointsPossible != e.Questions[i].Points || len(got.OptionIDs) != w.picked || got.OptionIDs == nil {
			t.Errorf("answer %d = %+v", i, got)
		}
	}

	b := domain.NewExamAttempt(8, e, 200, t0)
	b.Answers = []domain.ExamAnswer{{QuestionID: 1, OptionIDs: []id.ID{10}}, {QuestionID: 2, OptionIDs: []id.ID{21, 20}}}
	e.PassMark = 50
	b.Grade(e, *at(5))
	if *b.Score != 50 || !*b.Passed {
		t.Fatalf("pass mark boundary: score=%d passed=%v", *b.Score, *b.Passed)
	}
}

func TestSettle(t *testing.T) {
	e := examDraft(t)
	e.TimeLimit = 30 * time.Minute
	a := domain.NewExamAttempt(7, e, 200, t0)
	if a.Settle(e, *at(30)) || !a.Open() {
		t.Fatal("settled at the deadline")
	}
	if !a.Settle(e, *at(31)) || a.Open() || !a.AutoSubmitted || !a.SubmittedAt.Equal(*at(30)) || *a.Score != 0 {
		t.Fatalf("after deadline: %+v", a)
	}
	if a.Settle(e, *at(40)) {
		t.Fatal("settled twice")
	}
	untimed := examDraft(t)
	b := domain.NewExamAttempt(8, untimed, 200, t0)
	if b.Settle(untimed, *at(10000)) {
		t.Fatal("settled an untimed attempt")
	}
}

func TestCheckReveal(t *testing.T) {
	e := examDraft(t)
	a := domain.NewExamAttempt(7, e, 200, t0)
	if err := a.CheckReveal(e, t0); !errors.Is(err, domain.ErrRevealAttemptOpen) {
		t.Fatalf("open: %v", err)
	}
	a.Grade(e, t0)
	if err := a.CheckReveal(e, t0); err != nil {
		t.Fatalf("after_attempt: %v", err)
	}
	e.RevealPolicy = domain.RevealNever
	if err := a.CheckReveal(e, t0); !errors.Is(err, domain.ErrRevealDisabled) {
		t.Fatalf("never: %v", err)
	}
	e.RevealPolicy, e.ClosesAt = domain.RevealAfterClose, at(60)
	var notYet *domain.RevealNotYetError
	if err := a.CheckReveal(e, *at(59)); !errors.As(err, &notYet) || !notYet.At.Equal(*at(60)) || !errors.Is(err, domain.ErrRevealNotYet) {
		t.Fatalf("before close: %v", err)
	}
	if err := a.CheckReveal(e, *at(60)); err != nil {
		t.Fatalf("at close: %v", err)
	}
}

func TestNewExamAttemptCopiesRevision(t *testing.T) {
	e := examDraft(t)
	e.Revision = 3
	a := domain.NewExamAttempt(9, e, 200, time.Date(2026, 10, 7, 9, 0, 0, 0, time.UTC))
	if a.Revision != 3 {
		t.Fatalf("revision = %d, want 3", a.Revision)
	}
}
