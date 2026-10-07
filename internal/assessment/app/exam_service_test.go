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

func examInput(title ...string) app.ExamInput {
	t := "Final"
	if len(title) > 0 {
		t = title[0]
	}
	return app.ExamInput{
		ExamSettings: app.ExamSettings{Title: t, PassMark: 50, RevealPolicy: "after_attempt"},
		Questions: []app.QuestionInput{
			{Prompt: "2+2?", Type: "single_choice", Explanation: "arith", Points: 2, ReferenceLectureID: "50",
				Options: []app.OptionInput{{Label: "4", IsCorrect: true}, {Label: "5"}}},
			{Prompt: "Primes?", Type: "multiple_choice", Options: []app.OptionInput{
				{Label: "2", IsCorrect: true}, {Label: "3", IsCorrect: true}, {Label: "4"}}},
		},
	}
}

func newQuestionInput(prompt string) app.QuestionInput {
	return app.QuestionInput{
		Prompt: prompt,
		Type:   "single_choice",
		Points: 1,
		Options: []app.OptionInput{
			{Label: "a", IsCorrect: true},
			{Label: "b"},
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
	e, err := f.exams.Create(ctx, owner, course, in)
	if err != nil {
		t.Fatal(err)
	}
	return e
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
	e, err := f.exams.Create(ctx, admin, course, examInput())
	if err != nil {
		t.Fatal(err)
	}
	if e.Status != domain.ExamDraft || e.CourseID != course || e.Title != "Final" || !e.CreatedAt.Equal(t0) ||
		e.Questions[0].Points != 2 || e.Questions[1].Points != 1 || e.Questions[0].ReferenceLectureID != 50 ||
		e.Questions[0].ID.IsZero() || e.Questions[1].Options[2].ID.IsZero() || e.Revision != 1 {
		t.Fatalf("created %+v", e)
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
	got, err := f.exams.ListAuthoring(ctx, owner, course, 0)
	if err != nil || len(got) != 2 || got[0].ID != a.ID || got[1].ID != b.ID {
		t.Fatalf("list = %v, %v", got, err)
	}
	f.addAttempt(t, a, 900, false, domain.ExamAnswer{QuestionID: a.Questions[0].ID, OptionIDs: []id.ID{a.Questions[0].Options[0].ID}})
	e, err := f.exams.GetAuthoring(ctx, admin, a.ID)
	if err != nil || e.ID != a.ID {
		t.Fatalf("get = %+v, %v", e, err)
	}
	if _, err := f.exams.ListAuthoring(ctx, student, course, 0); !errors.Is(err, app.ErrForbidden) {
		t.Fatalf("student list: %v", err)
	}
	if _, err := f.exams.GetAuthoring(ctx, student, a.ID); !errors.Is(err, app.ErrForbidden) {
		t.Fatalf("student get: %v", err)
	}
	if _, err := f.exams.GetAuthoring(ctx, owner, 424242); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
	f.access.archived[course] = true
	if _, err := f.exams.ListAuthoring(ctx, owner, course, 0); err != nil {
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
	saved, err := f.exams.Save(ctx, owner, e.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	got := f.store.exams[e.ID]
	if got.Title != "Renamed" || got.Position != 3 || got.Status != domain.ExamPublished || !got.CreatedAt.Equal(t0) ||
		!got.UpdatedAt.Equal(t0.Add(time.Hour)) || len(got.Questions) != 3 || got.Questions[0].ID != e.Questions[0].ID ||
		saved.Title != "Renamed" || saved.Revision != 3 {
		t.Fatalf("saved %+v", got)
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

func TestSaveSettings(t *testing.T) {
	f := newFixture(t)
	e := f.createExam(t, examInput())
	in := app.ExamSettingsInput{
		ExamSettings: app.ExamSettings{Title: "Midterm", PassMark: 70, TimeLimitSeconds: intPtr(600), RevealPolicy: "never"},
		Points:       map[string]int{e.Questions[1].ID.String(): 5},
	}
	saved, err := f.exams.SaveSettings(ctx, owner, e.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	got := f.store.exams[e.ID]
	if got.Title != "Midterm" || got.PassMark != 70 || got.TimeLimit != 10*time.Minute || got.RevealPolicy != domain.RevealNever ||
		got.Questions[0].Points != 2 || got.Questions[1].Points != 5 || got.Questions[0].Prompt != "2+2?" || saved.Title != "Midterm" {
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
	cp, err := f.exams.Duplicate(ctx, owner, src.ID)
	if err != nil {
		t.Fatal(err)
	}
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
	if cp, _ := f.exams.Duplicate(ctx, owner, short.ID); cp.Title != "Final (copy)" {
		t.Fatalf("title = %q", cp.Title)
	}
}

func TestDeleteExam(t *testing.T) {
	f := newFixture(t)
	e := f.createExam(t, examInput())
	if err := f.exams.Delete(ctx, student, e.ID); !errors.Is(err, app.ErrForbidden) {
		t.Fatalf("student: %v", err)
	}
	empty := f.createExam(t, examInput())
	if err := f.exams.Delete(ctx, owner, empty.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.store.exams[empty.ID]; ok {
		t.Fatal("not deleted")
	}
}

func TestExamPublishWaitsForCoursePublish(t *testing.T) {
	f := newFixture(t)
	e, _ := f.exams.Create(ctx, owner, course, examInput("Final"))
	f.goLive(t, course) // exam pinned as draft
	if err := f.exams.Publish(ctx, owner, e.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.exams.List(ctx, student, course); len(got) != 0 {
		t.Fatalf("draft-pinned exam visible: %+v", got)
	}
	f.goLive(t, course)
	if got, _ := f.exams.List(ctx, student, course); len(got) != 1 {
		t.Fatalf("published exam missing after republish")
	}
}

func TestAttemptGradedAgainstItsRevision(t *testing.T) {
	f := newFixture(t)
	e, _ := f.exams.Create(ctx, owner, course, examInput("Final"))
	_ = f.exams.Publish(ctx, owner, e.ID)
	f.goLive(t, course)
	a, err := f.exams.Start(ctx, student, e.ID)
	if err != nil {
		t.Fatal(err)
	}
	// The draft swaps every question; the open attempt keeps revision 2's questions.
	in := examInput("Final")
	in.Questions = []app.QuestionInput{newQuestionInput("new?")}
	if _, err := f.exams.Save(ctx, owner, e.ID, in); err != nil {
		t.Fatalf("edit with an open attempt: %v", err)
	}
	f.goLive(t, course)
	q := a.Exam.Questions[0]
	if err := f.exams.SaveAnswer(ctx, student, a.Attempt.ID, app.AnswerInput{QuestionID: q.ID.String(),
		OptionIDs: []string{q.Options[0].ID.String()}}); err != nil {
		t.Fatalf("answer old revision's question: %v", err)
	}
	done, err := f.exams.Submit(ctx, student, a.Attempt.ID)
	if err != nil || done.Exam.Revision != a.Exam.Revision {
		t.Fatalf("graded against %d, want %d (%v)", done.Exam.Revision, a.Exam.Revision, err)
	}
}

func TestDraftDeadlineExtensionDoesNotMoveOpenAttempt(t *testing.T) {
	f := newFixture(t)
	in := examInput("Timed")
	limit := 600
	in.TimeLimitSeconds = &limit
	e, _ := f.exams.Create(ctx, owner, course, in)
	_ = f.exams.Publish(ctx, owner, e.ID)
	f.goLive(t, course)
	a, _ := f.exams.Start(ctx, student, e.ID)
	longer := 1200
	in.TimeLimitSeconds = &longer
	_, _ = f.exams.Save(ctx, owner, e.ID, in)
	f.goLive(t, course)
	got, _ := f.exams.GetAttempt(ctx, student, a.Attempt.ID)
	if d := got.Attempt.Deadline(got.Exam); !d.Equal(a.Attempt.StartedAt.Add(600 * time.Second)) {
		t.Fatalf("deadline = %v, want start + 10m", d)
	}
}

func TestDeleteExamWithAttemptsKeepsThem(t *testing.T) {
	f := newFixture(t)
	e, _ := f.exams.Create(ctx, owner, course, examInput("Final"))
	_ = f.exams.Publish(ctx, owner, e.ID)
	f.goLive(t, course)
	a, _ := f.exams.Start(ctx, student, e.ID)
	if err := f.exams.Delete(ctx, owner, e.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := f.exams.GetAttempt(ctx, student, a.Attempt.ID); err != nil {
		t.Fatalf("attempt after delete: %v", err)
	}
}
