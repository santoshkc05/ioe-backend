package app_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/assessment/app"
	"github.com/santoshkc2200/ioe-backend/internal/assessment/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

var (
	ctx      = context.Background()
	t0       = time.Date(2026, 10, 6, 9, 0, 0, 0, time.UTC)
	owner    = auth.Principal{UserID: 100, Role: auth.RoleInstructor}
	admin    = auth.Principal{UserID: 1, Role: auth.RoleRootAdmin}
	student  = auth.Principal{UserID: 200, Role: auth.RoleStudent}
	stranger = auth.Principal{UserID: 300, Role: auth.RoleStudent}
)

const (
	course   id.ID = 10 // lectures 50 (locked) and 51 (free preview)
	archived id.ID = 11 // lecture 60
	hidden   id.ID = 12 // unpublished; lecture 70
	locked   id.ID = 50
	free     id.ID = 51
)

type fixture struct {
	svc    *app.QuizService
	query  *app.QuizQuery
	exams  *app.ExamService
	store  *memStore
	clock  *fixedClock
	access *courseAccess
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	store := newMemStore()
	enr := enrollments{{course, student.UserID}: true, {hidden, student.UserID}: true}
	access := &courseAccess{
		t: t, store: store, owner: owner.UserID,
		lectures: map[id.ID]id.ID{locked: course, free: course, 60: archived, 70: hidden},
		archived: map[id.ID]bool{archived: true},
		hidden:   map[id.ID]bool{archived: true, hidden: true},
		free:     map[id.ID]bool{free: true},
		enrolled: enr,
	}
	ids, err := id.NewGenerator(1)
	if err != nil {
		t.Fatal(err)
	}
	clk := &fixedClock{now: t0}
	probe := enrollmentProbe{t: t, store: store, set: enr}
	return fixture{
		svc:   app.NewQuizService(store, access, probe, ids, clk),
		query: app.NewQuizQuery(store),
		exams: app.NewExamService(store, access, probe, ids, clk),
		store: store, clock: clk, access: access,
	}
}

func sampleInput() app.QuizInput {
	return app.QuizInput{Position: 0, Questions: []app.QuestionInput{
		{Prompt: "2+2?", Type: "single_choice", Explanation: "arith", Options: []app.OptionInput{
			{Label: "4", IsCorrect: true}, {Label: "5"}}},
		{Prompt: "Primes?", Type: "multiple_choice", Points: 3, ReferenceLectureID: "51", Options: []app.OptionInput{
			{Label: "2", IsCorrect: true}, {Label: "3", IsCorrect: true}, {Label: "4"}}},
	}}
}

func (f fixture) create(t *testing.T, lectureID id.ID) domain.Quiz {
	t.Helper()
	q, err := f.svc.Create(ctx, owner, course, lectureID, sampleInput())
	if err != nil {
		t.Fatal(err)
	}
	return q
}

func answersFor(q domain.Quiz) []app.AnswerInput {
	return []app.AnswerInput{{QuestionID: q.Questions[0].ID.String(), OptionIDs: []string{q.Questions[0].Options[0].ID.String()}}}
}

func TestCreateAndList(t *testing.T) {
	f := newFixture(t)
	q := f.create(t, locked)
	if q.ID.IsZero() || q.CourseID != course || q.LectureID != locked || !q.CreatedAt.Equal(t0) || len(q.Questions) != 2 {
		t.Fatalf("q=%+v", q)
	}
	if q.Questions[0].Points != 1 || q.Questions[1].Points != 3 || q.Questions[1].ReferenceLectureID != free {
		t.Fatalf("points/ref = %+v", q.Questions)
	}
	if q.Questions[0].ID.IsZero() || q.Questions[0].Options[0].ID.IsZero() {
		t.Fatal("IDs were not generated")
	}
	for _, p := range []auth.Principal{owner, admin, student} {
		got, err := f.svc.List(ctx, p, course, locked)
		if err != nil || len(got) != 1 || got[0].ID != q.ID {
			t.Fatalf("%d: got=%v err=%v", p.UserID, got, err)
		}
	}
	if _, err := f.svc.List(ctx, stranger, course, locked); !errors.Is(err, app.ErrEnrollmentRequired) {
		t.Fatalf("stranger locked: %v", err)
	}
	if got, err := f.svc.List(ctx, stranger, course, free); err != nil || len(got) != 0 {
		t.Fatalf("stranger free: %v %v", got, err)
	}
	if _, err := f.svc.List(ctx, owner, course, 60); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("lecture of other course: %v", err)
	}
}

func TestCreateRejections(t *testing.T) {
	f := newFixture(t)
	withID := sampleInput()
	withID.Questions[0].ID = "123"
	withOptionID := sampleInput()
	withOptionID.Questions[0].Options[0].ID = "123"
	badType := sampleInput()
	badType.Questions[0].Type = "essay"
	badRef := sampleInput()
	badRef.Questions[1].ReferenceLectureID = "abc"
	negPoints := sampleInput()
	negPoints.Questions[0].Points = -1
	empty := app.QuizInput{}
	cases := []struct {
		name    string
		p       auth.Principal
		course  id.ID
		lecture id.ID
		in      app.QuizInput
		want    error
	}{
		{"student", student, course, locked, sampleInput(), app.ErrForbidden},
		{"archived", owner, archived, 60, sampleInput(), app.ErrCourseNotEditable},
		{"lecture of other course", owner, course, 60, sampleInput(), app.ErrNotFound},
		{"question id on create", owner, course, locked, withID, app.ErrInvalidInput},
		{"option id on create", owner, course, locked, withOptionID, app.ErrInvalidInput},
		{"bad type", owner, course, locked, badType, app.ErrInvalidInput},
		{"bad reference", owner, course, locked, badRef, app.ErrInvalidInput},
		{"negative points", owner, course, locked, negPoints, app.ErrInvalidInput},
		{"no questions", owner, course, locked, empty, app.ErrInvalidInput},
	}
	for _, tc := range cases {
		if _, err := f.svc.Create(ctx, tc.p, tc.course, tc.lecture, tc.in); !errors.Is(err, tc.want) {
			t.Fatalf("%s: err = %v, want %v", tc.name, err, tc.want)
		}
	}
	if len(f.store.quizzes) != 0 {
		t.Fatalf("stored %d quizzes", len(f.store.quizzes))
	}
	// Domain errors surface their detail under invalid_input.
	if _, err := f.svc.Create(ctx, owner, course, locked, badType); !strings.Contains(err.Error(), "essay") {
		t.Fatalf("detail missing: %v", err)
	}
}

func TestUpdateKeepsIDsAndCreatedAt(t *testing.T) {
	f := newFixture(t)
	q := f.create(t, locked)
	f.clock.now = t0.Add(time.Hour)
	in := app.QuizInput{Position: 2, Questions: []app.QuestionInput{{
		ID: q.Questions[0].ID.String(), Prompt: "2+2 again?", Type: "single_choice",
		Options: []app.OptionInput{
			{ID: q.Questions[0].Options[1].ID.String(), Label: "5"},
			{ID: q.Questions[0].Options[0].ID.String(), Label: "4", IsCorrect: true},
			{Label: "6"},
		},
	}}}
	got, err := f.svc.Update(ctx, admin, course, locked, q.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != q.ID || got.Position != 2 || !got.CreatedAt.Equal(t0) || !got.UpdatedAt.Equal(t0.Add(time.Hour)) ||
		len(got.Questions) != 1 || got.Questions[0].ID != q.Questions[0].ID || got.Questions[0].Options[1].ID != q.Questions[0].Options[0].ID ||
		got.Questions[0].Options[2].ID.IsZero() {
		t.Fatalf("got=%+v", got)
	}
	if stored := f.store.quizzes[q.ID]; stored.Position != 2 || len(stored.Questions) != 1 {
		t.Fatalf("stored=%+v", stored)
	}
}

func TestUpdateRejectsForeignIDs(t *testing.T) {
	f := newFixture(t)
	a := f.create(t, locked)
	b := f.create(t, locked)
	in := sampleInput()
	in.Questions[0].ID = b.Questions[0].ID.String()
	if _, err := f.svc.Update(ctx, owner, course, locked, a.ID, in); !errors.Is(err, app.ErrInvalidInput) {
		t.Fatalf("foreign question id: %v", err)
	}
	in = sampleInput()
	in.Questions[0].Options[0].ID = b.Questions[0].Options[0].ID.String()
	if _, err := f.svc.Update(ctx, owner, course, locked, a.ID, in); !errors.Is(err, app.ErrInvalidInput) {
		t.Fatalf("foreign option id: %v", err)
	}
}

func TestUpdatePathMismatch(t *testing.T) {
	f := newFixture(t)
	q := f.create(t, locked)
	if _, err := f.svc.Update(ctx, owner, course, free, q.ID, sampleInput()); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("other lecture: %v", err)
	}
	if _, err := f.svc.Update(ctx, owner, course, locked, 999, sampleInput()); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("missing quiz: %v", err)
	}
	if _, err := f.svc.Update(ctx, student, course, locked, q.ID, sampleInput()); !errors.Is(err, app.ErrForbidden) {
		t.Fatalf("student: %v", err)
	}
}

func TestUpdateAfterDeleteIsNotFound(t *testing.T) {
	f := newFixture(t)
	q := f.create(t, locked)
	if err := f.svc.Delete(ctx, owner, q.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Update(ctx, owner, course, locked, q.ID, sampleInput()); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
	if len(f.store.quizzes) != 0 {
		t.Fatal("update resurrected a deleted quiz")
	}
}

func TestDelete(t *testing.T) {
	f := newFixture(t)
	q := f.create(t, locked)
	if _, err := f.svc.RecordAttempt(ctx, student, q.ID, student.UserID, answersFor(q), "k"); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Delete(ctx, student, q.ID); !errors.Is(err, app.ErrForbidden) {
		t.Fatalf("student: %v", err)
	}
	if err := f.svc.Delete(ctx, owner, 999); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
	if err := f.svc.Delete(ctx, owner, q.ID); err != nil {
		t.Fatal(err)
	}
	if len(f.store.quizzes) != 0 || len(f.store.attempts) != 0 {
		t.Fatalf("left quizzes=%d attempts=%d", len(f.store.quizzes), len(f.store.attempts))
	}
}

func TestRecordAttemptIsIdempotent(t *testing.T) {
	f := newFixture(t)
	q := f.create(t, locked)
	first, err := f.svc.RecordAttempt(ctx, student, q.ID, student.UserID, answersFor(q), "key-1")
	if err != nil || first.IsZero() {
		t.Fatalf("first=%v err=%v", first, err)
	}
	again, err := f.svc.RecordAttempt(ctx, student, q.ID, student.UserID, answersFor(q), "key-1")
	if err != nil || again != first {
		t.Fatalf("again=%v first=%v err=%v", again, first, err)
	}
	other, err := f.svc.RecordAttempt(ctx, student, q.ID, student.UserID, nil, "")
	if err != nil || other == first {
		t.Fatalf("keyless=%v err=%v", other, err)
	}
	if len(f.store.attempts) != 2 {
		t.Fatalf("attempts = %d", len(f.store.attempts))
	}
	got := f.store.attempts[0]
	if got.UserID != student.UserID || got.QuizID != q.ID || !got.SubmittedAt.Equal(t0) || len(got.Answers) != 1 {
		t.Fatalf("attempt=%+v", got)
	}
}

func TestRecordAttemptRejections(t *testing.T) {
	f := newFixture(t)
	q := f.create(t, locked)
	freeQuiz := f.create(t, free)
	cases := []struct {
		name    string
		p       auth.Principal
		quiz    id.ID
		user    id.ID
		answers []app.AnswerInput
		key     string
		want    error
	}{
		{"for another user", student, q.ID, stranger.UserID, answersFor(q), "", app.ErrForbidden},
		{"missing quiz", student, 999, student.UserID, nil, "", app.ErrNotFound},
		{"not enrolled", stranger, q.ID, stranger.UserID, answersFor(q), "", app.ErrEnrollmentRequired},
		{"free preview, not enrolled", stranger, freeQuiz.ID, stranger.UserID, answersFor(freeQuiz), "", app.ErrEnrollmentRequired},
		{"owner not enrolled", owner, q.ID, owner.UserID, answersFor(q), "", app.ErrEnrollmentRequired},
		{"foreign question", student, q.ID, student.UserID, answersFor(freeQuiz), "", app.ErrInvalidInput},
		{"malformed question id", student, q.ID, student.UserID, []app.AnswerInput{{QuestionID: "x"}}, "", app.ErrInvalidInput},
		{"long key", student, q.ID, student.UserID, answersFor(q), strings.Repeat("k", 129), app.ErrInvalidInput},
	}
	for _, tc := range cases {
		if _, err := f.svc.RecordAttempt(ctx, tc.p, tc.quiz, tc.user, tc.answers, tc.key); !errors.Is(err, tc.want) {
			t.Fatalf("%s: err = %v, want %v", tc.name, err, tc.want)
		}
	}
	if len(f.store.attempts) != 0 {
		t.Fatalf("attempts = %d", len(f.store.attempts))
	}
}

func TestQuizQueryLectures(t *testing.T) {
	f := newFixture(t)
	a := f.create(t, locked)
	b := f.create(t, free)
	got, err := f.query.Lectures(ctx, course, []id.ID{a.ID, b.ID, 999})
	if err != nil || len(got) != 2 || got[a.ID] != locked || got[b.ID] != free {
		t.Fatalf("got=%v err=%v", got, err)
	}
	if got, err := f.query.Lectures(ctx, archived, []id.ID{a.ID}); err != nil || len(got) != 0 {
		t.Fatalf("other course: %v %v", got, err)
	}
	if got, err := f.query.Lectures(ctx, course, nil); err != nil || len(got) != 0 {
		t.Fatalf("empty: %v %v", got, err)
	}
}
