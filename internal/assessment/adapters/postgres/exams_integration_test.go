//go:build integration

package postgres_test

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/assessment/adapters/postgres"
	"github.com/santoshkc2200/ioe-backend/internal/assessment/app"
	"github.com/santoshkc2200/ioe-backend/internal/assessment/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
	"github.com/santoshkc2200/ioe-backend/internal/platform/postgres/pgtest"
)

// exam reuses quiz's questions: examID*100+1 (options +2 correct, +3) and examID*100+4
// (options +5, +6, both correct).
func exam(t *testing.T, examID, courseID id.ID, position int) domain.Exam {
	t.Helper()
	q := quiz(t, examID, courseID, 50, 0)
	closes := t0.Add(2 * time.Hour)
	e, err := domain.NewExam(domain.Exam{ID: examID, CourseID: courseID, Title: "Final", Description: "d", Position: position,
		Status: domain.ExamDraft, PassMark: 60, TimeLimit: 30 * time.Minute, RetakesAllowed: true, ClosesAt: &closes,
		RevealPolicy: domain.RevealAfterClose, Questions: q.Questions, CreatedAt: t0, UpdatedAt: t0})
	if err != nil {
		t.Fatal(err)
	}
	e.Revision = 1
	return e
}

func runExams(t *testing.T, tx *postgres.TxRunner, fn func(app.ExamRepository) error) {
	t.Helper()
	if err := tx.RunInTx(ctx, func(r app.Repos) error { return fn(r.Exams) }); err != nil {
		t.Fatal(err)
	}
}

func TestExamRoundTripAndOrder(t *testing.T) {
	tx := postgres.NewTxRunner(pgtest.New(t))
	a, b, other := exam(t, 1, 10, 1), exam(t, 2, 10, 0), exam(t, 3, 11, 0)
	b.Status, b.TimeLimit, b.ClosesAt, b.RevealPolicy = domain.ExamPublished, 0, nil, domain.RevealNever
	runExams(t, tx, func(r app.ExamRepository) error {
		for _, e := range []domain.Exam{a, b, other} {
			if err := r.Insert(ctx, e, 100); err != nil {
				return err
			}
		}
		return nil
	})
	runExams(t, tx, func(r app.ExamRepository) error {
		for _, lock := range []app.LockMode{app.LockNone, app.LockUpdate} {
			got, err := r.Find(ctx, 1, lock)
			if err != nil {
				return err
			}
			if got.Title != "Final" || got.Description != "d" || got.Position != 1 || got.Status != domain.ExamDraft || got.PassMark != 60 ||
				got.TimeLimit != 30*time.Minute || !got.RetakesAllowed || got.OpensAt != nil || !got.ClosesAt.Equal(t0.Add(2*time.Hour)) ||
				got.RevealPolicy != domain.RevealAfterClose || len(got.Questions) != 2 || got.Questions[0].Points != 2 ||
				!got.CreatedAt.Equal(t0) {
				t.Fatalf("round trip (lock %d) = %+v", lock, got)
			}
		}
		if got, err := r.Find(ctx, 2, app.LockNone); err != nil || got.TimeLimit != 0 || got.ClosesAt != nil {
			t.Fatalf("untimed = %+v, %v", got, err)
		}
		if _, err := r.Find(ctx, 999, app.LockNone); !errors.Is(err, app.ErrNotFound) {
			t.Fatalf("missing: %v", err)
		}
		all, err := r.ListByCourse(ctx, 10)
		if err != nil || len(all) != 2 || all[0].ID != 2 || all[1].ID != 1 {
			t.Fatalf("list = %+v, %v", all, err)
		}
		if next, err := r.NextPosition(ctx, 10); err != nil || next != 2 {
			t.Fatalf("next = %d, %v", next, err)
		}
		if next, err := r.NextPosition(ctx, 99); err != nil || next != 0 {
			t.Fatalf("empty course next = %d, %v", next, err)
		}
		return nil
	})
	runExams(t, tx, func(r app.ExamRepository) error {
		a2 := a
		a2.Revision = 2
		a2.Title, a2.Status, a2.Questions = "Renamed", domain.ExamPublished, a2.Questions[:1]
		return r.AppendRevision(ctx, a2, 100)
	})
	runExams(t, tx, func(r app.ExamRepository) error {
		got, _ := r.Find(ctx, 1, app.LockNone)
		if got.Title != "Renamed" || got.Status != domain.ExamPublished || len(got.Questions) != 1 {
			t.Fatalf("replaced = %+v", got)
		}
		missing := exam(t, 999, 10, 0)
		missing.Revision = 2
		if err := r.AppendRevision(ctx, missing, 100); !errors.Is(err, app.ErrNotFound) {
			t.Fatalf("replace missing: %v", err)
		}
		return nil
	})
}

func TestExamRevisionsAndSoftDelete(t *testing.T) {
	tx := postgres.NewTxRunner(pgtest.New(t))
	e := exam(t, 1, 10, 0)
	e.Revision = 1
	runExams(t, tx, func(r app.ExamRepository) error { return r.Insert(ctx, e, 100) })
	e2 := e
	e2.Revision, e2.Status, e2.Position = 2, domain.ExamPublished, 4
	runExams(t, tx, func(r app.ExamRepository) error { return r.AppendRevision(ctx, e2, 100) })
	runExams(t, tx, func(r app.ExamRepository) error { return r.Delete(ctx, 1, t0) })
	runExams(t, tx, func(r app.ExamRepository) error {
		if _, err := r.Find(ctx, 1, app.LockNone); !errors.Is(err, app.ErrNotFound) {
			t.Fatalf("find deleted: %v", err)
		}
		got, err := r.FindRevisions(ctx, map[id.ID]int{1: 1})
		if err != nil || len(got) != 1 || got[0].Status != domain.ExamDraft {
			t.Fatalf("revision 1 = %+v, %v", got, err)
		}
		next, err := r.NextPosition(ctx, 10)
		if err != nil || next != 0 {
			t.Fatalf("next position ignores deleted exams: %d, %v", next, err)
		}
		return nil
	})
}

func TestAttemptLifecycle(t *testing.T) {
	tx := postgres.NewTxRunner(pgtest.New(t))
	e := exam(t, 1, 10, 0)
	a := domain.NewExamAttempt(500, e, 200, t0)
	runExams(t, tx, func(r app.ExamRepository) error {
		if err := r.Insert(ctx, e, 100); err != nil {
			return err
		}
		return r.InsertAttempt(ctx, a)
	})
	runExams(t, tx, func(r app.ExamRepository) error {
		if err := r.MergeAnswer(ctx, 500, domain.ExamAnswer{QuestionID: 101, OptionIDs: []id.ID{103}}); err != nil {
			return err
		}
		if err := r.MergeAnswer(ctx, 500, domain.ExamAnswer{QuestionID: 101, OptionIDs: []id.ID{102}}); err != nil {
			return err
		}
		return r.MergeAnswer(ctx, 500, domain.ExamAnswer{QuestionID: 104, OptionIDs: nil})
	})
	runExams(t, tx, func(r app.ExamRepository) error {
		got, err := r.FindOpenAttempt(ctx, 1, 200)
		if err != nil {
			return err
		}
		if got.ID != 500 || !got.Open() || len(got.Answers) != 2 || got.Answers[0].QuestionID != 101 ||
			got.Answers[0].OptionIDs[0] != 102 || got.Answers[1].OptionIDs == nil || len(got.Answers[1].OptionIDs) != 0 {
			t.Fatalf("open attempt = %+v", got)
		}
		if done, err := r.HasSubmitted(ctx, 1, 200); err != nil || done {
			t.Fatalf("has submitted = %v, %v", done, err)
		}
		got, err = r.FindAttempt(ctx, 500, true)
		if err != nil {
			return err
		}
		got.Grade(e, t0.Add(time.Minute))
		return r.SaveResult(ctx, got)
	})
	runExams(t, tx, func(r app.ExamRepository) error {
		got, err := r.FindAttempt(ctx, 500, false)
		if err != nil {
			return err
		}
		if got.Open() || *got.Score != 66 || !*got.Passed || !got.SubmittedAt.Equal(t0.Add(time.Minute)) || got.AutoSubmitted ||
			len(got.Answers) != 2 || !*got.Answers[0].IsCorrect || got.Answers[0].PointsPossible != 2 || *got.Answers[1].PointsAwarded != 0 {
			t.Fatalf("graded = %+v", got)
		}
		if err := r.MergeAnswer(ctx, 500, domain.ExamAnswer{QuestionID: 101, OptionIDs: []id.ID{103}}); !errors.Is(err, domain.ErrAttemptSubmitted) {
			t.Fatalf("merge after submit: %v", err)
		}
		if err := r.SaveResult(ctx, got); !errors.Is(err, domain.ErrAttemptSubmitted) {
			t.Fatalf("save twice: %v", err)
		}
		if _, err := r.FindOpenAttempt(ctx, 1, 200); !errors.Is(err, app.ErrNotFound) {
			t.Fatalf("open after submit: %v", err)
		}
		if done, err := r.HasSubmitted(ctx, 1, 200); err != nil || !done {
			t.Fatalf("has submitted = %v, %v", done, err)
		}
		if _, err := r.FindAttempt(ctx, 999, false); !errors.Is(err, app.ErrNotFound) {
			t.Fatalf("missing attempt: %v", err)
		}
		second := domain.NewExamAttempt(501, e, 200, t0.Add(time.Hour))
		if err := r.InsertAttempt(ctx, second); err != nil {
			return err
		}
		list, err := r.ListAttempts(ctx, 1)
		if err != nil || len(list) != 2 || list[0].ID != 500 || list[1].ID != 501 {
			t.Fatalf("list = %+v, %v", list, err)
		}
		mine, err := r.ListUserAttempts(ctx, 10, 200)
		if err != nil || len(mine) != 2 {
			t.Fatalf("user list = %+v, %v", mine, err)
		}
		return nil
	})
	runExams(t, tx, func(r app.ExamRepository) error {
		e2 := exam(t, 2, 10, 1)
		if err := r.Insert(ctx, e2, 100); err != nil {
			return err
		}
		if err := r.Delete(ctx, 2, t0); err != nil {
			return err
		}
		if _, err := r.Find(ctx, 2, app.LockNone); !errors.Is(err, app.ErrNotFound) {
			t.Fatalf("deleted exam: %v", err)
		}
		return r.Delete(ctx, 2, t0)
	})
}

func TestConcurrentStartsCreateOneAttempt(t *testing.T) {
	tx := postgres.NewTxRunner(pgtest.New(t))
	e := exam(t, 1, 10, 0)
	runExams(t, tx, func(r app.ExamRepository) error { return r.Insert(ctx, e, 100) })
	const n = 8
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = tx.RunInTx(ctx, func(r app.Repos) error {
				return r.Exams.InsertAttempt(ctx, domain.NewExamAttempt(id.ID(600+i), e, 200, t0))
			})
		}()
	}
	wg.Wait()
	created := 0
	for i, err := range errs {
		switch {
		case err == nil:
			created++
		case !errors.Is(err, app.ErrOpenAttemptExists):
			t.Fatalf("start %d: %v", i, err)
		}
	}
	if created != 1 {
		t.Fatalf("created %d attempts", created)
	}
}

func TestConcurrentAnswersKeepBoth(t *testing.T) {
	tx := postgres.NewTxRunner(pgtest.New(t))
	e := exam(t, 1, 10, 0)
	runExams(t, tx, func(r app.ExamRepository) error {
		if err := r.Insert(ctx, e, 100); err != nil {
			return err
		}
		return r.InsertAttempt(ctx, domain.NewExamAttempt(500, e, 200, t0))
	})
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, ans := range []domain.ExamAnswer{{QuestionID: 101, OptionIDs: []id.ID{102}}, {QuestionID: 104, OptionIDs: []id.ID{105, 106}}} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = tx.RunInTx(ctx, func(r app.Repos) error { return r.Exams.MergeAnswer(ctx, 500, ans) })
		}()
	}
	wg.Wait()
	if errs[0] != nil || errs[1] != nil {
		t.Fatalf("errs = %v", errs)
	}
	runExams(t, tx, func(r app.ExamRepository) error {
		got, err := r.FindAttempt(ctx, 500, false)
		if err == nil && len(got.Answers) != 2 {
			t.Fatalf("answers = %+v", got.Answers)
		}
		return err
	})
}
