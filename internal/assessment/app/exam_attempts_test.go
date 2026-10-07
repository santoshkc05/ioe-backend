package app_test

import (
	"errors"
	"testing"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/assessment/app"
	"github.com/santoshkc2200/ioe-backend/internal/assessment/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

// publishedExam creates and publishes an exam from in.
func (f fixture) publishedExam(t *testing.T, in app.ExamInput) domain.Exam {
	t.Helper()
	e := f.createExam(t, in)
	if err := f.exams.Publish(ctx, owner, e.ID); err != nil {
		t.Fatal(err)
	}
	f.goLive(t, course)
	return f.store.exams[e.ID]
}

func (f fixture) start(t *testing.T, e domain.Exam) domain.ExamAttempt {
	t.Helper()
	d, err := f.exams.Start(ctx, student, e.ID)
	if err != nil {
		t.Fatal(err)
	}
	return d.Attempt
}

// answer picks the given option indexes of question qi.
func answer(e domain.Exam, qi int, options ...int) app.AnswerInput {
	q := e.Questions[qi]
	in := app.AnswerInput{QuestionID: q.ID.String(), OptionIDs: []string{}}
	for _, o := range options {
		in.OptionIDs = append(in.OptionIDs, q.Options[o].ID.String())
	}
	return in
}

func timed(minutes int) app.ExamInput {
	in := examInput()
	in.TimeLimitSeconds = intPtr(minutes * 60)
	in.RetakesAllowed = true
	return in
}

func TestStudentListAndGet(t *testing.T) {
	f := newFixture(t)
	draft := f.createExam(t, examInput())
	pub := f.publishedExam(t, examInput())
	later := examInput()
	later.OpensAt = ptrTime(t0.Add(time.Hour))
	notOpen := f.publishedExam(t, later)

	f.addAttempt(t, pub, 900, true, domain.ExamAnswer{QuestionID: pub.Questions[0].ID, OptionIDs: []id.ID{pub.Questions[0].Options[0].ID}})
	second := f.addAttempt(t, pub, 901, false)
	second.StartedAt = t0.Add(time.Minute)
	f.store.examAttempts[second.ID] = second

	got, err := f.exams.List(ctx, student, course)
	if err != nil || len(got) != 2 {
		t.Fatalf("list = %+v, %v", got, err)
	}
	byID := map[id.ID]app.StudentExam{got[0].Exam.ID: got[0], got[1].Exam.ID: got[1]}
	s := byID[pub.ID]
	if s.Availability != domain.AvailabilityOpen || s.AttemptCount != 2 || s.OpenAttemptID != 901 || *s.BestScore != 66 || !*s.BestPassed {
		t.Fatalf("summary = %+v", s)
	}
	if n := byID[notOpen.ID]; n.Availability != domain.AvailabilityNotOpen || n.BestScore != nil || n.AttemptCount != 0 {
		t.Fatalf("not open = %+v", n)
	}
	if _, listed := byID[draft.ID]; listed {
		t.Fatal("draft listed")
	}

	if e, err := f.exams.Get(ctx, student, pub.ID); err != nil || e.ID != pub.ID {
		t.Fatalf("get = %v", err)
	}
	if _, err := f.exams.Get(ctx, student, notOpen.ID); !errors.Is(err, app.ErrExamNotOpen) {
		t.Fatalf("not open student get: %v", err)
	}
	past := examInput()
	past.ClosesAt = ptrTime(t0.Add(-time.Hour))
	closed := f.publishedExam(t, past)
	if e, err := f.exams.Get(ctx, student, closed.ID); err != nil || e.ID != closed.ID {
		t.Fatalf("closed student get: %v", err)
	}
	if _, err := f.exams.Get(ctx, student, draft.ID); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("draft get: %v", err)
	}
	if _, err := f.exams.Get(ctx, stranger, pub.ID); !errors.Is(err, app.ErrEnrollmentRequired) {
		t.Fatalf("unenrolled get: %v", err)
	}
	if _, err := f.exams.List(ctx, stranger, course); !errors.Is(err, app.ErrEnrollmentRequired) {
		t.Fatalf("unenrolled list: %v", err)
	}
	if _, err := f.exams.List(ctx, student, hidden); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("unpublished course: %v", err)
	}
}

func ptrTime(v time.Time) *time.Time { return &v }

func TestStart(t *testing.T) {
	f := newFixture(t)
	e := f.publishedExam(t, timed(30))
	d, err := f.exams.Start(ctx, student, e.ID)
	if err != nil {
		t.Fatal(err)
	}
	a := d.Attempt
	if a.UserID != student.UserID || a.ExamID != e.ID || a.CourseID != course || !a.StartedAt.Equal(t0) ||
		!a.Deadline(d.Exam).Equal(t0.Add(30*time.Minute)) {
		t.Fatalf("attempt %+v", a)
	}
	if _, err := f.exams.Start(ctx, student, e.ID); !errors.Is(err, app.ErrOpenAttemptExists) {
		t.Fatalf("second start: %v", err)
	}
	// Once the open attempt expires, starting settles it and opens a new one.
	f.clock.now = t0.Add(31 * time.Minute)
	if d, err := f.exams.Start(ctx, student, e.ID); err != nil || d.Attempt.ID == a.ID {
		t.Fatalf("restart: %v", err)
	}
	if old := f.store.examAttempts[a.ID]; !old.AutoSubmitted || !old.SubmittedAt.Equal(t0.Add(30*time.Minute)) {
		t.Fatalf("stale attempt %+v", old)
	}
}

func TestStartRejects(t *testing.T) {
	f := newFixture(t)
	draft := f.createExam(t, examInput())
	window := examInput()
	window.OpensAt, window.ClosesAt = ptrTime(t0.Add(time.Hour)), ptrTime(t0.Add(2*time.Hour))
	windowed := f.publishedExam(t, window)
	once := f.publishedExam(t, examInput())
	f.addAttempt(t, once, 900, true)

	if _, err := f.exams.Start(ctx, student, draft.ID); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("draft: %v", err)
	}
	var w *app.WindowError
	if _, err := f.exams.Start(ctx, student, windowed.ID); !errors.Is(err, app.ErrExamNotOpen) || !errors.As(err, &w) || !w.At.Equal(t0.Add(time.Hour)) {
		t.Fatalf("not open: %v", err)
	}
	f.clock.now = t0.Add(2 * time.Hour)
	if _, err := f.exams.Start(ctx, student, windowed.ID); !errors.Is(err, app.ErrExamClosed) || !errors.As(err, &w) || !w.At.Equal(t0.Add(2*time.Hour)) {
		t.Fatalf("closed: %v", err)
	}
	if _, err := f.exams.Start(ctx, student, once.ID); !errors.Is(err, app.ErrRetakesNotAllowed) {
		t.Fatalf("retake: %v", err)
	}
	if _, err := f.exams.Start(ctx, stranger, once.ID); !errors.Is(err, app.ErrEnrollmentRequired) {
		t.Fatalf("unenrolled: %v", err)
	}
	if _, err := f.exams.Start(ctx, student, 424242); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
}

func TestSaveAnswerAndSubmit(t *testing.T) {
	f := newFixture(t)
	e := f.publishedExam(t, examInput())
	a := f.start(t, e)
	for _, in := range []app.AnswerInput{answer(e, 0, 1), answer(e, 0, 0), answer(e, 1, 0, 1)} {
		if err := f.exams.SaveAnswer(ctx, student, a.ID, in); err != nil {
			t.Fatal(err)
		}
	}
	if got := f.store.examAttempts[a.ID].Answers; len(got) != 2 {
		t.Fatalf("answers = %+v", got)
	}
	for name, in := range map[string]app.AnswerInput{
		"two single picks": answer(e, 0, 0, 1),
		"unknown question": {QuestionID: "424242", OptionIDs: []string{}},
		"malformed option": {QuestionID: e.Questions[0].ID.String(), OptionIDs: []string{"x"}},
	} {
		if err := f.exams.SaveAnswer(ctx, student, a.ID, in); !errors.Is(err, app.ErrInvalidInput) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := f.exams.SaveAnswer(ctx, stranger, a.ID, answer(e, 0, 0)); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("other user save: %v", err)
	}
	if _, err := f.exams.Submit(ctx, stranger, a.ID); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("other user submit: %v", err)
	}
	f.clock.now = t0.Add(time.Minute)
	d, err := f.exams.Submit(ctx, student, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if *d.Attempt.Score != 100 || !*d.Attempt.Passed || !d.Attempt.SubmittedAt.Equal(t0.Add(time.Minute)) || d.Attempt.AutoSubmitted {
		t.Fatalf("graded %+v", d.Attempt)
	}
	if _, err := f.exams.Submit(ctx, student, a.ID); !errors.Is(err, domain.ErrAttemptSubmitted) {
		t.Fatalf("resubmit: %v", err)
	}
	if err := f.exams.SaveAnswer(ctx, student, a.ID, answer(e, 0, 1)); !errors.Is(err, domain.ErrAttemptSubmitted) {
		t.Fatalf("save after submit: %v", err)
	}
}

func TestExpiredAttempt(t *testing.T) {
	f := newFixture(t)
	e := f.publishedExam(t, timed(30))
	a := f.start(t, e)
	_ = f.exams.SaveAnswer(ctx, student, a.ID, answer(e, 0, 0))
	f.clock.now = t0.Add(31 * time.Minute)
	if err := f.exams.SaveAnswer(ctx, student, a.ID, answer(e, 1, 0)); !errors.Is(err, domain.ErrAttemptExpired) {
		t.Fatalf("late save: %v", err)
	}
	if _, err := f.exams.Submit(ctx, student, a.ID); !errors.Is(err, domain.ErrAttemptExpired) {
		t.Fatalf("late submit: %v", err)
	}
	d, err := f.exams.GetAttempt(ctx, student, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !d.Attempt.AutoSubmitted || !d.Attempt.SubmittedAt.Equal(t0.Add(30*time.Minute)) || *d.Attempt.Score != 66 {
		t.Fatalf("settled %+v", d.Attempt)
	}
	if stored := f.store.examAttempts[a.ID]; stored.Open() {
		t.Fatal("settlement not stored")
	}
	if _, err := f.exams.GetAttempt(ctx, stranger, a.ID); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("other user: %v", err)
	}
}

func TestReview(t *testing.T) {
	f := newFixture(t)
	in := examInput()
	in.RetakesAllowed = true
	e := f.publishedExam(t, in)
	a := f.start(t, e)
	if _, err := f.exams.Review(ctx, student, a.ID); !errors.Is(err, domain.ErrRevealAttemptOpen) {
		t.Fatalf("open, owner: %v", err)
	}
	if _, err := f.exams.Review(ctx, owner, a.ID); !errors.Is(err, domain.ErrRevealAttemptOpen) {
		t.Fatalf("open, manager: %v", err)
	}
	subDetail, err := f.exams.Submit(ctx, student, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !subDetail.RevealPermitted {
		t.Fatal("expected RevealPermitted for after_attempt on Submit")
	}
	getDetail, err := f.exams.GetAttempt(ctx, student, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !getDetail.RevealPermitted {
		t.Fatal("expected RevealPermitted for after_attempt on GetAttempt")
	}
	if d, err := f.exams.Review(ctx, student, a.ID); err != nil || d.Attempt.ID != a.ID || !d.RevealPermitted {
		t.Fatalf("after_attempt: %v", err)
	}
	if _, err := f.exams.Review(ctx, stranger, a.ID); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("stranger: %v", err)
	}
	if _, err := f.exams.Review(ctx, student, 424242); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}

	neverIn := examInput()
	neverIn.RetakesAllowed = true
	neverIn.RevealPolicy = "never"
	neverExam := f.publishedExam(t, neverIn)
	neverAttempt := f.start(t, neverExam)
	_, _ = f.exams.Submit(ctx, student, neverAttempt.ID)
	if getNever, err := f.exams.GetAttempt(ctx, student, neverAttempt.ID); err != nil || getNever.RevealPermitted {
		t.Fatalf("never on GetAttempt: err=%v, RevealPermitted=%v", err, getNever.RevealPermitted)
	}
	if _, err := f.exams.Review(ctx, student, neverAttempt.ID); !errors.Is(err, domain.ErrRevealDisabled) {
		t.Fatalf("never: %v", err)
	}
	if _, err := f.exams.Review(ctx, owner, neverAttempt.ID); err != nil {
		t.Fatalf("manager under never: %v", err)
	}

	closing := f.publishedExam(t, func() app.ExamInput {
		in := examInput()
		in.ClosesAt, in.RevealPolicy = ptrTime(t0.Add(time.Hour)), "after_close"
		return in
	}())
	b := f.start(t, closing)
	bSub, err := f.exams.Submit(ctx, student, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if bSub.RevealPermitted {
		t.Fatal("expected RevealPermitted=false for after_close on Submit before close")
	}
	if bGet, err := f.exams.GetAttempt(ctx, student, b.ID); err != nil || bGet.RevealPermitted {
		t.Fatalf("before close on GetAttempt: err=%v, RevealPermitted=%v", err, bGet.RevealPermitted)
	}
	var notYet *domain.RevealNotYetError
	if _, err := f.exams.Review(ctx, student, b.ID); !errors.As(err, &notYet) || !notYet.At.Equal(t0.Add(time.Hour)) {
		t.Fatalf("before close: %v", err)
	}
	f.clock.now = t0.Add(time.Hour)
	if bGetAfter, err := f.exams.GetAttempt(ctx, student, b.ID); err != nil || !bGetAfter.RevealPermitted {
		t.Fatalf("after close on GetAttempt: err=%v, RevealPermitted=%v", err, bGetAfter.RevealPermitted)
	}
	if _, err := f.exams.Review(ctx, student, b.ID); err != nil {
		t.Fatalf("after close: %v", err)
	}
}

func TestListAttempts(t *testing.T) {
	f := newFixture(t)
	e := f.publishedExam(t, timed(30))
	a := f.start(t, e)
	f.clock.now = t0.Add(time.Hour)
	got, err := f.exams.ListAttempts(ctx, owner, e.ID)
	if err != nil || len(got) != 1 || got[0].ID != a.ID || !got[0].AutoSubmitted {
		t.Fatalf("list = %+v, %v", got, err)
	}
	if _, err := f.exams.ListAttempts(ctx, student, e.ID); !errors.Is(err, app.ErrForbidden) {
		t.Fatalf("student: %v", err)
	}
}

func TestPointsEditKeepsRecordedScore(t *testing.T) {
	f := newFixture(t)
	in := examInput()
	in.RetakesAllowed = true
	e := f.publishedExam(t, in)
	a := f.start(t, e)
	_ = f.exams.SaveAnswer(ctx, student, a.ID, answer(e, 0, 0))
	if _, err := f.exams.Submit(ctx, student, a.ID); err != nil {
		t.Fatal(err)
	}
	settings := app.ExamSettingsInput{
		ExamSettings: app.ExamSettings{Title: "Final", PassMark: 50, RetakesAllowed: true, RevealPolicy: "after_attempt"},
		Points:       map[string]int{e.Questions[1].ID.String(): 10},
	}
	if _, err := f.exams.SaveSettings(ctx, owner, e.ID, settings); err != nil {
		t.Fatal(err)
	}
	d, err := f.exams.GetAttempt(ctx, student, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if *d.Attempt.Score != 66 || d.Attempt.Answers[1].PointsPossible != 1 {
		t.Fatalf("after points edit %+v", d.Attempt)
	}
}

func TestUnpublishKeepsOpenAttemptUsable(t *testing.T) {
	f := newFixture(t)
	e := f.publishedExam(t, examInput())
	a := f.start(t, e)
	if err := f.exams.Unpublish(ctx, owner, e.ID); err != nil {
		t.Fatal(err)
	}
	f.goLive(t, course)
	if err := f.exams.SaveAnswer(ctx, student, a.ID, answer(e, 0, 0)); err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, err := f.exams.Submit(ctx, student, a.ID); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if _, err := f.exams.Start(ctx, student, e.ID); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("start after unpublish: %v", err)
	}
}

func TestSettlePreservesConcurrentAnswer(t *testing.T) {
	f := newFixture(t)
	e := f.publishedExam(t, timed(30))
	a := f.start(t, e)

	if err := f.exams.SaveAnswer(ctx, student, a.ID, answer(e, 0, 0)); err != nil {
		t.Fatal(err)
	}

	f.clock.now = t0.Add(31 * time.Minute)

	q1Ans := domain.ExamAnswer{
		QuestionID: e.Questions[1].ID,
		OptionIDs:  []id.ID{e.Questions[1].Options[0].ID, e.Questions[1].Options[1].ID},
	}
	if err := (memExams{f.store}).MergeAnswer(ctx, a.ID, q1Ans); err != nil {
		t.Fatal(err)
	}

	d, err := f.exams.GetAttempt(ctx, student, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !d.Attempt.AutoSubmitted {
		t.Fatal("expected auto-submitted")
	}
	if len(d.Attempt.Answers) != 2 {
		t.Fatalf("expected 2 answers, got %d: %+v", len(d.Attempt.Answers), d.Attempt.Answers)
	}
	if d.Attempt.Score == nil || *d.Attempt.Score != 100 {
		t.Fatalf("score = %v; want 100", d.Attempt.Score)
	}
}
