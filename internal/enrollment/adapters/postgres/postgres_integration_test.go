//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/santoshkc2200/ioe-backend/internal/enrollment/adapters/postgres"
	"github.com/santoshkc2200/ioe-backend/internal/enrollment/app"
	"github.com/santoshkc2200/ioe-backend/internal/enrollment/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/clock"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
	"github.com/santoshkc2200/ioe-backend/internal/platform/postgres/pgtest"
)

var ctx = context.Background()

type fixture struct {
	pool *pgxpool.Pool
	tx   *postgres.TxRunner
	ids  *id.Generator
	now  time.Time
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	ids, err := id.NewGenerator(0)
	if err != nil {
		t.Fatal(err)
	}
	pool := pgtest.New(t)
	return fixture{pool: pool, tx: postgres.NewTxRunner(pool), ids: ids, now: time.Now().UTC().Truncate(time.Microsecond)}
}

func (f fixture) outboxCount(t *testing.T, topic string) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(ctx, "SELECT count(*) FROM platform.outbox_messages WHERE payload->>'destination_topic' = $1", topic).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestRoundTripAndVersionConflict(t *testing.T) {
	f := newFixture(t)
	e, ev := domain.NewEnrollment(f.ids.New(), 10, 20, f.now)
	err := f.tx.RunInTx(ctx, func(r app.Repos) error {
		if err := r.Enrollments.Insert(ctx, &e); err != nil {
			return err
		}
		return r.Events.Publish(ctx, ev)
	})
	if err != nil || e.Version != 1 {
		t.Fatalf("insert: v=%d err=%v", e.Version, err)
	}

	stale := e // still at version 1
	_, _ = e.Cancel("bye", f.now.Add(time.Minute))
	if err := f.tx.RunInTx(ctx, func(r app.Repos) error { return r.Enrollments.Update(ctx, &e) }); err != nil || e.Version != 2 {
		t.Fatalf("update: v=%d err=%v", e.Version, err)
	}
	if err := f.tx.RunInTx(ctx, func(r app.Repos) error { return r.Enrollments.Update(ctx, &stale) }); !errors.Is(err, app.ErrConcurrentModification) {
		t.Fatalf("stale update err = %v", err)
	}

	var got domain.Enrollment
	var found bool
	if err := f.tx.RunInTx(ctx, func(r app.Repos) error {
		var err error
		got, found, err = r.Enrollments.FindByCourseAndUser(ctx, 10, 20)
		return err
	}); err != nil || !found {
		t.Fatalf("find: found=%v err=%v", found, err)
	}
	if got != e {
		t.Fatalf("round trip = %+v, want %+v", got, e)
	}
	if n := f.outboxCount(t, "enrollment.enrollment.activated"); n != 1 {
		t.Fatalf("activated events = %d", n)
	}
}

func TestFindMissing(t *testing.T) {
	f := newFixture(t)
	if err := f.tx.RunInTx(ctx, func(r app.Repos) error {
		_, found, err := r.Enrollments.FindByCourseAndUser(ctx, 1, 2)
		if found {
			t.Fatal("found missing enrollment")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestInsertDuplicate(t *testing.T) {
	f := newFixture(t)
	a, _ := domain.NewEnrollment(f.ids.New(), 10, 20, f.now)
	b, _ := domain.NewEnrollment(f.ids.New(), 10, 20, f.now)
	if err := f.tx.RunInTx(ctx, func(r app.Repos) error { return r.Enrollments.Insert(ctx, &a) }); err != nil {
		t.Fatal(err)
	}
	if err := f.tx.RunInTx(ctx, func(r app.Repos) error { return r.Enrollments.Insert(ctx, &b) }); !errors.Is(err, app.ErrDuplicate) {
		t.Fatalf("err = %v", err)
	}
}

func TestCanceledConsistencyConstraint(t *testing.T) {
	f := newFixture(t)
	_, err := f.pool.Exec(ctx, `INSERT INTO enrollment.enrollments (id, course_id, user_id, status, enrolled_at, version)
		VALUES (1, 10, 20, 'canceled', now(), 1)`)
	if err == nil {
		t.Fatal("canceled row without canceled_at accepted")
	}
}

func TestListActive(t *testing.T) {
	f := newFixture(t)
	seed := func(course, user id.ID, at time.Time, cancel bool) {
		e, _ := domain.NewEnrollment(f.ids.New(), course, user, at)
		if cancel {
			_, _ = e.Cancel("", at)
		}
		if err := f.tx.RunInTx(ctx, func(r app.Repos) error { return r.Enrollments.Insert(ctx, &e) }); err != nil {
			t.Fatal(err)
		}
	}
	seed(10, 201, f.now, false)
	seed(10, 202, f.now.Add(time.Second), true)
	seed(10, 203, f.now.Add(2*time.Second), false)
	seed(11, 201, f.now.Add(3*time.Second), false)

	if err := f.tx.RunInTx(ctx, func(r app.Repos) error {
		page, total, err := r.Enrollments.ListActiveByCourse(ctx, 10, 1, 1)
		if err != nil {
			return err
		}
		if total != 2 || len(page) != 1 || page[0].UserID != 203 {
			t.Fatalf("page=%+v total=%d", page, total)
		}
		mine, err := r.Enrollments.ListActiveByUser(ctx, 201)
		if err != nil {
			return err
		}
		if len(mine) != 2 || mine[0].CourseID != 11 {
			t.Fatalf("by user = %+v", mine)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

type oneCourse struct{}

func (oneCourse) CourseFacts(context.Context, id.ID) (domain.CourseFacts, error) {
	return domain.CourseFacts{Published: true, Free: true, OwnerID: 1}, nil
}

func TestConcurrentEnrollCreatesOneRow(t *testing.T) {
	f := newFixture(t)
	svc := app.NewService(f.tx, oneCourse{}, f.ids, clock.System{})
	student := auth.Principal{UserID: 20, Role: auth.RoleStudent}
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, errs[i] = svc.Enroll(ctx, student, 10, student.UserID)
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatalf("enroll err = %v", err)
		}
	}
	var rows int
	if err := f.pool.QueryRow(ctx, "SELECT count(*) FROM enrollment.enrollments").Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("rows = %d", rows)
	}
	if n := f.outboxCount(t, "enrollment.enrollment.activated"); n != 1 {
		t.Fatalf("activated events = %d", n)
	}
	ok, err := app.NewAccessQuery(f.tx).IsActivelyEnrolled(ctx, 10, 20)
	if err != nil || !ok {
		t.Fatalf("access: %v %v", ok, err)
	}
}
