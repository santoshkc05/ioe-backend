package app_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/assessment/app"
	"github.com/santoshkc2200/ioe-backend/internal/assessment/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

func examInput() app.ExamInput {
	return app.ExamInput{
		ExamSettings: app.ExamSettings{Title: "Final", PassMark: 50, RevealPolicy: "after_attempt"},
		Questions: []app.QuestionInput{
			{Prompt: "2+2?", Type: "single_choice", Explanation: "arith", Points: 2, ReferenceLectureID: "50",
				Options: []app.OptionInput{{Label: "4", IsCorrect: true}, {Label: "5"}}},
			{Prompt: "Primes?", Type: "multiple_choice", Options: []app.OptionInput{
				{Label: "2", IsCorrect: true}, {Label: "3", IsCorrect: true}, {Label: "4"}}},
		},
	}
}

// inputOf turns a stored exam back into a save input that keeps every ID.
func inputOf(e domain.Exam) app.ExamInput {
	in := app.ExamInput{Position: e.Position, ExamSettings: app.ExamSettings{
		Title: e.Title, Description: e.Description, PassMark: e.PassMark, RetakesAllowed: e.RetakesAllowed,
		OpensAt: e.OpensAt, ClosesAt: e.ClosesAt, RevealPolicy: string(e.RevealPolicy)}}
	if e.TimeLimit > 0 {
		secs := int(e.TimeLimit / time.Second)
		in.TimeLimitSeconds = &secs
	}
	for _, q := range e.Questions {
		qi := app.QuestionInput{ID: q.ID.String(), Prompt: q.Prompt, Type: string(q.Type), Explanation: q.Explanation, Points: q.Points}
		for _, o := range q.Options {
			qi.Options = append(qi.Options, app.OptionInput{ID: o.ID.String(), Label: o.Label, IsCorrect: o.IsCorrect})
		}
		in.Questions = append(in.Questions, qi)
	}
	return in
}

func (f fixture) createExam(t *testing.T, in app.ExamInput) domain.Exam {
	t.Helper()
	d, err := f.exams.Create(ctx, owner, course, in)
	if err != nil {
		t.Fatal(err)
	}
	return d.Exam
}

// addAttempt stores an attempt directly, graded at t0 when submitted is true.
func (f fixture) addAttempt(t *testing.T, e domain.Exam, attemptID id.ID, submitted bool, answers ...domain.ExamAnswer) domain.ExamAttempt {
	t.Helper()
	a := domain.NewExamAttempt(attemptID, e, student.UserID, t0)
	a.Answers = answers
	if submitted {
		a.Grade(e, t0)
	}
	f.store.examAttempts[a.ID] = a
	return a
}

func intPtr(v int) *int { return &v }

func TestCreateExam(t *testing.T) {
	f := newFixture(t)
	d, err := f.exams.Create(ctx, admin, course, examInput())
	if err != nil {
		t.Fatal(err)
	}
	e := d.Exam
	if e.Status != domain.ExamDraft || e.CourseID != course || e.Title != "Final" || !e.CreatedAt.Equal(t0) ||
		e.Questions[0].Points != 2 || e.Questions[1].Points != 1 || e.Questions[0].ReferenceLectureID != 50 ||
		e.Questions[0].ID.IsZero() || e.Questions[1].Options[2].ID.IsZero() {
		t.Fatalf("created %+v", e)
	}
	if d.Locks.OpenAttempts != 0 || len(d.Locks.AnsweredQuestionIDs) != 0 {
		t.Fatalf("locks = %+v", d.Locks)
	}
	if _, ok := f.store.exams[e.ID]; !ok {
		t.Fatal("not stored")
	}
}

func TestCreateExamRejects(t *testing.T) {
	withID := examInput()
	withID.Questions[0].ID = "123"
	badMark := examInput()
	badMark.PassMark = 101
	zeroLimit := examInput()
	zeroLimit.TimeLimitSeconds = intPtr(0)
	longLimit := examInput()
	longLimit.TimeLimitSeconds = intPtr(86401)
	badReveal := examInput()
	badReveal.RevealPolicy = "during_attempt"
	cases := map[string]struct {
		courseID id.ID
		in       app.ExamInput
		want     error
	}{
		"supplied id": {course, withID, app.ErrInvalidInput},
		"pass mark":   {course, badMark, domain.ErrInvalidExam},
		"zero limit":  {course, zeroLimit, app.ErrInvalidInput},
		"long limit":  {course, longLimit, app.ErrInvalidInput},
		"reveal":      {course, badReveal, domain.ErrInvalidExam},
		"archived":    {archived, examInput(), app.ErrCourseNotEditable},
		"unknown":     {424242, examInput(), app.ErrNotFound},
	}
	for name, tc := range cases {
		f := newFixture(t)
		if _, err := f.exams.Create(ctx, owner, tc.courseID, tc.in); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v", name, err)
		}
		if len(f.store.exams) != 0 {
			t.Errorf("%s: stored %d exams", name, len(f.store.exams))
		}
	}
	if _, err := newFixture(t).exams.Create(ctx, student, course, examInput()); !errors.Is(err, app.ErrForbidden) {
		t.Fatalf("student: %v", err)
	}
	if _, err := newFixture(t).exams.Create(ctx, owner, hidden, examInput()); err != nil {
		t.Fatalf("manager on unpublished course: %v", err)
	}
}

func TestListAndGetAuthoring(t *testing.T) {
	f := newFixture(t)
	second := examInput()
	second.Position = 1
	b := f.createExam(t, second)
	a := f.createExam(t, examInput())
	got, err := f.exams.ListAuthoring(ctx, owner, course)
	if err != nil || len(got) != 2 || got[0].ID != a.ID || got[1].ID != b.ID {
		t.Fatalf("list = %v, %v", got, err)
	}
	f.addAttempt(t, a, 900, false, domain.ExamAnswer{QuestionID: a.Questions[0].ID, OptionIDs: []id.ID{a.Questions[0].Options[0].ID}})
	d, err := f.exams.GetAuthoring(ctx, admin, a.ID)
	if err != nil || d.Exam.ID != a.ID || d.Locks.OpenAttempts != 1 || len(d.Locks.AnsweredQuestionIDs) != 1 {
		t.Fatalf("get = %+v, %v", d, err)
	}
	if _, err := f.exams.ListAuthoring(ctx, student, course); !errors.Is(err, app.ErrForbidden) {
		t.Fatalf("student list: %v", err)
	}
	if _, err := f.exams.GetAuthoring(ctx, student, a.ID); !errors.Is(err, app.ErrForbidden) {
		t.Fatalf("student get: %v", err)
	}
	if _, err := f.exams.GetAuthoring(ctx, owner, 424242); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
	f.access.archived[course] = true
	if _, err := f.exams.ListAuthoring(ctx, owner, course); err != nil {
		t.Fatalf("archived course read: %v", err)
	}
}

func TestSaveExam(t *testing.T) {
	f := newFixture(t)
	e := f.createExam(t, examInput())
	_ = f.exams.Publish(ctx, owner, e.ID)
	f.clock.now = t0.Add(time.Hour)
	in := inputOf(e)
	in.Title, in.Position = "Renamed", 3
	in.Questions = append(in.Questions, app.QuestionInput{Prompt: "New?", Type: "true_false",
		Options: []app.OptionInput{{Label: "yes", IsCorrect: true}, {Label: "no"}}})
	f.store.locks = nil
	d, err := f.exams.Save(ctx, owner, e.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	got := f.store.exams[e.ID]
	if got.Title != "Renamed" || got.Position != 3 || got.Status != domain.ExamPublished || !got.CreatedAt.Equal(t0) ||
		!got.UpdatedAt.Equal(t0.Add(time.Hour)) || len(got.Questions) != 3 || got.Questions[0].ID != e.Questions[0].ID ||
		d.Exam.Title != "Renamed" {
		t.Fatalf("saved %+v", got)
	}
	if len(f.store.locks) == 0 || f.store.locks[len(f.store.locks)-1] != app.LockUpdate {
		t.Fatalf("locks = %v", f.store.locks)
	}

	foreign := inputOf(e)
	foreign.Questions[0].ID = "999"
	if _, err := f.exams.Save(ctx, owner, e.ID, foreign); !errors.Is(err, app.ErrInvalidInput) {
		t.Fatalf("foreign id: %v", err)
	}
	if _, err := f.exams.Save(ctx, student, e.ID, in); !errors.Is(err, app.ErrForbidden) {
		t.Fatalf("student: %v", err)
	}
}

func TestSaveExamGuardsAttempts(t *testing.T) {
	f := newFixture(t)
	e := f.createExam(t, examInput())
	f.addAttempt(t, e, 900, true)
	flipped := inputOf(e)
	flipped.Questions[0].Options[0].IsCorrect, flipped.Questions[0].Options[1].IsCorrect = false, true
	_, err := f.exams.Save(ctx, owner, e.ID, flipped)
	var v *domain.EditViolation
	if !errors.Is(err, domain.ErrEditKeyFrozen) || !errors.As(err, &v) || v.QuestionID != e.Questions[0].ID {
		t.Fatalf("key change: %v", err)
	}
	if f.store.exams[e.ID].Questions[0].Options[0].IsCorrect != true {
		t.Fatal("refused edit was stored")
	}
	reworded := inputOf(e)
	reworded.Questions[0].Prompt = "What is 2+2?"
	if _, err := f.exams.Save(ctx, owner, e.ID, reworded); err != nil {
		t.Fatalf("rewording: %v", err)
	}
}

func TestSaveSettings(t *testing.T) {
	f := newFixture(t)
	e := f.createExam(t, examInput())
	in := app.ExamSettingsInput{
		ExamSettings: app.ExamSettings{Title: "Midterm", PassMark: 70, TimeLimitSeconds: intPtr(600), RevealPolicy: "never"},
		Points:       map[string]int{e.Questions[1].ID.String(): 5},
	}
	d, err := f.exams.SaveSettings(ctx, owner, e.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	got := f.store.exams[e.ID]
	if got.Title != "Midterm" || got.PassMark != 70 || got.TimeLimit != 10*time.Minute || got.RevealPolicy != domain.RevealNever ||
		got.Questions[0].Points != 2 || got.Questions[1].Points != 5 || got.Questions[0].Prompt != "2+2?" || d.Exam.Title != "Midterm" {
		t.Fatalf("saved %+v", got)
	}
	for name, points := range map[string]map[string]int{
		"unknown question": {"424242": 3},
		"malformed id":     {"x": 3},
		"zero points":      {e.Questions[0].ID.String(): 0},
	} {
		in.Points = points
		if _, err := f.exams.SaveSettings(ctx, owner, e.ID, in); !errors.Is(err, app.ErrInvalidInput) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestReorderExams(t *testing.T) {
	f := newFixture(t)
	a, b := f.createExam(t, examInput()), f.createExam(t, examInput())
	if err := f.exams.Reorder(ctx, owner, course, []id.ID{b.ID, a.ID}); err != nil {
		t.Fatal(err)
	}
	if f.store.exams[b.ID].Position != 0 || f.store.exams[a.ID].Position != 1 {
		t.Fatalf("positions a=%d b=%d", f.store.exams[a.ID].Position, f.store.exams[b.ID].Position)
	}
	for name, ids := range map[string][]id.ID{
		"missing":   {a.ID},
		"duplicate": {a.ID, a.ID, b.ID},
		"foreign":   {a.ID, b.ID, 424242},
	} {
		if err := f.exams.Reorder(ctx, owner, course, ids); !errors.Is(err, app.ErrInvalidInput) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := f.exams.Reorder(ctx, student, course, []id.ID{a.ID, b.ID}); !errors.Is(err, app.ErrForbidden) {
		t.Fatalf("student: %v", err)
	}
}

func TestPublishUnpublish(t *testing.T) {
	f := newFixture(t)
	e := f.createExam(t, examInput())
	for _, step := range []struct {
		fn   func() error
		want domain.ExamStatus
	}{
		{func() error { return f.exams.Publish(ctx, owner, e.ID) }, domain.ExamPublished},
		{func() error { return f.exams.Publish(ctx, owner, e.ID) }, domain.ExamPublished},
		{func() error { return f.exams.Unpublish(ctx, admin, e.ID) }, domain.ExamDraft},
	} {
		if err := step.fn(); err != nil || f.store.exams[e.ID].Status != step.want {
			t.Fatalf("status = %s, err = %v", f.store.exams[e.ID].Status, err)
		}
	}
	if err := f.exams.Publish(ctx, student, e.ID); !errors.Is(err, app.ErrForbidden) {
		t.Fatalf("student: %v", err)
	}
}

func TestDuplicateExam(t *testing.T) {
	f := newFixture(t)
	long := examInput()
	long.Title = strings.Repeat("t", domain.MaxTitleLen)
	long.Position = 4
	src := f.createExam(t, long)
	_ = f.exams.Publish(ctx, owner, src.ID)
	d, err := f.exams.Duplicate(ctx, owner, src.ID)
	if err != nil {
		t.Fatal(err)
	}
	cp := d.Exam
	if cp.ID == src.ID || cp.Status != domain.ExamDraft || cp.Position != 5 || len([]rune(cp.Title)) != domain.MaxTitleLen ||
		len(cp.Questions) != 2 || cp.Questions[0].Prompt != "2+2?" || !cp.Questions[0].Options[0].IsCorrect {
		t.Fatalf("copy %+v", cp)
	}
	srcIDs := src.IDs()
	for v := range cp.IDs() {
		if _, reused := srcIDs[v]; reused {
			t.Fatalf("copy reuses id %s", v)
		}
	}
	if f.store.exams[src.ID].Questions[0].Options[0].ID != src.Questions[0].Options[0].ID {
		t.Fatal("source changed")
	}
	short := f.createExam(t, examInput())
	if d, _ := f.exams.Duplicate(ctx, owner, short.ID); d.Exam.Title != "Final (copy)" {
		t.Fatalf("title = %q", d.Exam.Title)
	}
}

func TestDeleteExam(t *testing.T) {
	f := newFixture(t)
	e := f.createExam(t, examInput())
	if err := f.exams.Delete(ctx, student, e.ID); !errors.Is(err, app.ErrForbidden) {
		t.Fatalf("student: %v", err)
	}
	f.addAttempt(t, e, 900, true)
	if err := f.exams.Delete(ctx, owner, e.ID); !errors.Is(err, app.ErrExamHasAttempts) {
		t.Fatalf("with attempts: %v", err)
	}
	empty := f.createExam(t, examInput())
	if err := f.exams.Delete(ctx, owner, empty.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.store.exams[empty.ID]; ok {
		t.Fatal("not deleted")
	}
}

func TestSaveExamSettlesExpiredAttempts(t *testing.T) {
	f := newFixture(t)
	in := examInput()
	in.TimeLimitSeconds = intPtr(30)
	e := f.createExam(t, in)
	_ = f.exams.Publish(ctx, owner, e.ID)
	att := f.addAttempt(t, e, 900, false)

	f.clock.now = t0.Add(time.Minute)

	editInput := inputOf(e)
	editInput.Questions = append(editInput.Questions, app.QuestionInput{
		Prompt:  "Question 3?",
		Type:    "true_false",
		Options: []app.OptionInput{{Label: "yes", IsCorrect: true}, {Label: "no"}},
	})
	d, err := f.exams.Save(ctx, owner, e.ID, editInput)
	if err != nil {
		t.Fatalf("save should succeed after settling expired attempt, got: %v", err)
	}
	if d.Locks.OpenAttempts != 0 || d.Locks.SubmittedAttempts != 1 {
		t.Fatalf("locks = %+v; want 0 open, 1 submitted", d.Locks)
	}
	if !f.store.examAttempts[att.ID].AutoSubmitted {
		t.Fatal("attempt should be settled as auto-submitted")
	}
}
