//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/assessment/adapters/postgres"
	"github.com/santoshkc2200/ioe-backend/internal/assessment/app"
	"github.com/santoshkc2200/ioe-backend/internal/assessment/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
	"github.com/santoshkc2200/ioe-backend/internal/platform/postgres/pgtest"
)

var (
	ctx = context.Background()
	t0  = time.Date(2026, 10, 6, 9, 0, 0, 0, time.UTC)
)

func quiz(t *testing.T, quizID, courseID, lectureID id.ID, position int) domain.Quiz {
	t.Helper()
	base := quizID * 100
	q1, err := domain.NewQuestion(base+1, "p1", domain.QuestionSingleChoice, "e", 2, lectureID, []domain.Option{
		{ID: base + 2, Label: "a", IsCorrect: true}, {ID: base + 3, Label: "b"}})
	if err != nil {
		t.Fatal(err)
	}
	q2, err := domain.NewQuestion(base+4, "p2", domain.QuestionMultipleChoice, "", 1, 0, []domain.Option{
		{ID: base + 5, Label: "c", IsCorrect: true}, {ID: base + 6, Label: "d", IsCorrect: true}})
	if err != nil {
		t.Fatal(err)
	}
	q, err := domain.NewQuiz(quizID, courseID, lectureID, position, []domain.Question{q1, q2}, t0, t0)
	if err != nil {
		t.Fatal(err)
	}
	q.Revision = 1
	return q
}

func run(t *testing.T, tx *postgres.TxRunner, fn func(app.QuizRepository) error) {
	t.Helper()
	if err := tx.RunInTx(ctx, func(r app.Repos) error { return fn(r.Quizzes) }); err != nil {
		t.Fatal(err)
	}
}

func TestInsertFindListReplace(t *testing.T) {
	tx := postgres.NewTxRunner(pgtest.New(t))
	a, b, c := quiz(t, 1, 10, 50, 1), quiz(t, 2, 10, 50, 0), quiz(t, 3, 10, 51, 0)
	run(t, tx, func(r app.QuizRepository) error {
		for _, q := range []domain.Quiz{a, b, c} {
			if err := r.Insert(ctx, q, 100); err != nil {
				return err
			}
		}
		return nil
	})
	run(t, tx, func(r app.QuizRepository) error {
		got, err := r.Find(ctx, 1)
		if err != nil {
			return err
		}
		if got.CourseID != 10 || got.LectureID != 50 || got.Position != 1 || !got.CreatedAt.Equal(t0) || len(got.Questions) != 2 ||
			got.Questions[0].Points != 2 || got.Questions[0].ReferenceLectureID != 50 || got.Questions[1].ReferenceLectureID != 0 ||
			got.Questions[0].Explanation != "e" || got.Questions[1].Type != domain.QuestionMultipleChoice ||
			!got.Questions[0].Options[0].IsCorrect || got.Questions[0].Options[1].ID != 103 {
			t.Fatalf("round trip = %+v", got)
		}
		list, err := r.ListByLecture(ctx, 10, 50)
		if err != nil {
			return err
		}
		if len(list) != 2 || list[0].ID != 2 || list[1].ID != 1 {
			t.Fatalf("list = %+v", list)
		}
		if _, err := r.Find(ctx, 999); !errors.Is(err, app.ErrNotFound) {
			t.Fatalf("missing: %v", err)
		}
		return nil
	})
	edited := quiz(t, 1, 10, 50, 5)
	edited.Revision = 2
	edited.Questions = edited.Questions[:1]
	edited.UpdatedAt = t0.Add(time.Hour)
	run(t, tx, func(r app.QuizRepository) error { return r.AppendRevision(ctx, edited, 100) })
	run(t, tx, func(r app.QuizRepository) error {
		got, err := r.Find(ctx, 1)
		if err == nil && (got.Position != 5 || len(got.Questions) != 1 || !got.UpdatedAt.Equal(t0.Add(time.Hour)) || !got.CreatedAt.Equal(t0)) {
			t.Fatalf("replaced = %+v", got)
		}
		return err
	})
}

func TestQuizRevisionsAndSoftDelete(t *testing.T) {
	tx := postgres.NewTxRunner(pgtest.New(t))
	q := quiz(t, 1, 10, 50, 0)
	q.Revision = 1
	run(t, tx, func(r app.QuizRepository) error { return r.Insert(ctx, q, 100) })

	edited := quiz(t, 1, 10, 50, 3)
	edited.Revision = 2
	run(t, tx, func(r app.QuizRepository) error { return r.AppendRevision(ctx, edited, 100) })

	run(t, tx, func(r app.QuizRepository) error {
		stale := edited // still claims revision 2: the head is already 2
		if err := r.AppendRevision(ctx, stale, 100); !errors.Is(err, app.ErrNotFound) {
			t.Fatalf("stale append: %v, want ErrNotFound", err)
		}
		head, err := r.Find(ctx, 1)
		if err != nil || head.Revision != 2 || head.Position != 3 {
			t.Fatalf("head = %+v, %v", head, err)
		}
		old, err := r.FindRevisions(ctx, map[id.ID]int{1: 1})
		if err != nil || len(old) != 1 || old[0].Revision != 1 || old[0].Position != 0 {
			t.Fatalf("revision 1 = %+v, %v", old, err)
		}
		return nil
	})

	run(t, tx, func(r app.QuizRepository) error { return r.Delete(ctx, 1, t0) })
	run(t, tx, func(r app.QuizRepository) error {
		if _, err := r.Find(ctx, 1); !errors.Is(err, app.ErrNotFound) {
			t.Fatalf("find deleted: %v", err)
		}
		if got, _ := r.ListByLecture(ctx, 10, 50); len(got) != 0 {
			t.Fatalf("list shows deleted quiz: %+v", got)
		}
		if got, _ := r.LecturesOf(ctx, 10, []id.ID{1}); len(got) != 0 {
			t.Fatalf("LecturesOf shows deleted quiz: %v", got)
		}
		// Pinned revisions stay readable after delete.
		if got, _ := r.FindRevisions(ctx, map[id.ID]int{1: 2}); len(got) != 1 {
			t.Fatalf("pinned revision of deleted quiz missing")
		}
		heads, err := r.Heads(ctx, 10)
		want := []app.Head{{Ref: app.Ref{Kind: app.KindQuiz, ID: 1}, Revision: 2, Deleted: true}}
		if err != nil || !slices.Equal(heads, want) {
			t.Fatalf("heads = %+v, %v", heads, err)
		}
		return nil
	})

	run(t, tx, func(r app.QuizRepository) error { return r.Undelete(ctx, 1, t0) })
	run(t, tx, func(r app.QuizRepository) error {
		_, err := r.Find(ctx, 1)
		return err
	})
}

func TestQuizAttemptKeepsRevision(t *testing.T) {
	tx := postgres.NewTxRunner(pgtest.New(t))
	q := quiz(t, 2, 10, 50, 0)
	q.Revision = 1
	run(t, tx, func(r app.QuizRepository) error { return r.Insert(ctx, q, 100) })
	a := domain.QuizAttempt{ID: 900, QuizID: 2, Revision: 1, UserID: 200, Answers: []domain.Answer{}, SubmittedAt: t0}
	run(t, tx, func(r app.QuizRepository) error {
		_, err := r.RecordAttempt(ctx, a)
		return err
	})
	a.ID, a.Revision = 901, 7 // no such revision
	err := tx.RunInTx(ctx, func(r app.Repos) error {
		_, err := r.Quizzes.RecordAttempt(ctx, a)
		return err
	})
	if err == nil {
		t.Fatal("attempt against a missing revision was stored")
	}
}

func TestLecturesOf(t *testing.T) {
	tx := postgres.NewTxRunner(pgtest.New(t))
	run(t, tx, func(r app.QuizRepository) error {
		for _, q := range []domain.Quiz{quiz(t, 1, 10, 50, 0), quiz(t, 2, 10, 51, 0), quiz(t, 3, 11, 60, 0)} {
			if err := r.Insert(ctx, q, 100); err != nil {
				return err
			}
		}
		got, err := r.LecturesOf(ctx, 10, []id.ID{1, 2, 3, 999})
		if err != nil {
			return err
		}
		if len(got) != 2 || got[1] != 50 || got[2] != 51 {
			t.Fatalf("got = %v", got)
		}
		return nil
	})
}

func attempt(attemptID id.ID, key string) domain.QuizAttempt {
	return domain.QuizAttempt{ID: attemptID, QuizID: 1, Revision: 1, UserID: 200, IdempotencyKey: key, SubmittedAt: t0,
		Answers: []domain.Answer{{QuestionID: 101, OptionIDs: []id.ID{102}}}}
}

func TestRecordAttempt(t *testing.T) {
	pool := pgtest.New(t)
	tx := postgres.NewTxRunner(pool)
	run(t, tx, func(r app.QuizRepository) error { return r.Insert(ctx, quiz(t, 1, 10, 50, 0), 100) })
	var first, again, keyless1, keyless2 id.ID
	run(t, tx, func(r app.QuizRepository) error {
		var err error
		if first, err = r.RecordAttempt(ctx, attempt(500, "k")); err != nil {
			return err
		}
		if again, err = r.RecordAttempt(ctx, attempt(501, "k")); err != nil {
			return err
		}
		if keyless1, err = r.RecordAttempt(ctx, attempt(502, "")); err != nil {
			return err
		}
		keyless2, err = r.RecordAttempt(ctx, attempt(503, ""))
		return err
	})
	if first != 500 || again != 500 || keyless1 != 502 || keyless2 != 503 {
		t.Fatalf("ids = %v %v %v %v", first, again, keyless1, keyless2)
	}
	var answers string
	if err := pool.QueryRow(ctx, `SELECT answers::text FROM assessment.quiz_attempts WHERE id = 500`).Scan(&answers); err != nil {
		t.Fatal(err)
	}
	if answers != `[{"option_ids": ["102"], "question_id": "101"}]` {
		t.Fatalf("answers = %s", answers)
	}
	run(t, tx, func(r app.QuizRepository) error { return r.Delete(ctx, 1, t0) })
	run(t, tx, func(r app.QuizRepository) error { return r.Delete(ctx, 1, t0) }) // no-op when absent
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM assessment.quiz_attempts`).Scan(&n); err != nil || n != 3 {
		t.Fatalf("attempts left = %d err=%v", n, err)
	}
}

func TestRecordAttemptConcurrentReplay(t *testing.T) {
	pool := pgtest.New(t)
	tx := postgres.NewTxRunner(pool)
	run(t, tx, func(r app.QuizRepository) error { return r.Insert(ctx, quiz(t, 1, 10, 50, 0), 100) })
	const n = 8
	ids := make([]id.ID, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = tx.RunInTx(ctx, func(r app.Repos) error {
				var err error
				ids[i], err = r.Quizzes.RecordAttempt(ctx, attempt(id.ID(600+i), "same"))
				return err
			})
		}()
	}
	wg.Wait()
	for i := range n {
		if errs[i] != nil || ids[i] != ids[0] {
			t.Fatalf("i=%d id=%v first=%v err=%v", i, ids[i], ids[0], errs[i])
		}
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM assessment.quiz_attempts`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("rows = %d err=%v", count, err)
	}
}
