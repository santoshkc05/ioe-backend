//go:build integration

package postgres_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
	"github.com/santoshkc2200/ioe-backend/internal/platform/postgres/pgtest"
	"github.com/santoshkc2200/ioe-backend/internal/progress/adapters/postgres"
	"github.com/santoshkc2200/ioe-backend/internal/progress/app"
	"github.com/santoshkc2200/ioe-backend/internal/progress/domain"
)

var ctx = context.Background()

type fixture struct {
	pool *pgxpool.Pool
	tx   *postgres.TxRunner
	now  time.Time
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	pool := pgtest.New(t)
	return fixture{pool: pool, tx: postgres.NewTxRunner(pool), now: time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)}
}

func (f fixture) record(t *testing.T, courseID, userID, lectureID id.ID, state domain.LectureState, pos int64, at time.Time) {
	t.Helper()
	lp, err := domain.NewLectureProgress(lectureID, state, pos, at)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.tx.RunInTx(ctx, func(r app.Repository) error { return r.RecordLecture(ctx, courseID, userID, lp) }); err != nil {
		t.Fatal(err)
	}
}

func (f fixture) course(t *testing.T, courseID, userID id.ID) (domain.CourseProgress, bool) {
	t.Helper()
	var (
		cp    domain.CourseProgress
		found bool
	)
	if err := f.tx.RunInTx(ctx, func(r app.Repository) error {
		var err error
		cp, found, err = r.FindCourse(ctx, courseID, userID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return cp, found
}

func TestRecordAndFindCourse(t *testing.T) {
	f := newFixture(t)
	if _, found := f.course(t, 10, 20); found {
		t.Fatal("found progress before any write")
	}
	f.record(t, 10, 20, 51, domain.LectureStateInProgress, 1200, f.now)
	f.record(t, 10, 20, 50, domain.LectureStateCompleted, 0, f.now.Add(time.Minute))

	cp, found := f.course(t, 10, 20)
	if !found || cp.CourseID != 10 || cp.UserID != 20 || cp.LastLectureID != 50 || !cp.UpdatedAt.Equal(f.now.Add(time.Minute)) {
		t.Fatalf("cp=%+v found=%v", cp, found)
	}
	if len(cp.Lectures) != 2 || cp.Lectures[0].LectureID != 50 || cp.Lectures[1].PositionMs != 1200 || cp.Lectures[1].State != domain.LectureStateInProgress {
		t.Fatalf("lectures=%+v", cp.Lectures)
	}
	if !slices.Equal(cp.CompletedLectureIDs(), []id.ID{50}) {
		t.Fatalf("completed=%v", cp.CompletedLectureIDs())
	}
}

func TestCompletedNeverRegresses(t *testing.T) {
	f := newFixture(t)
	f.record(t, 10, 20, 50, domain.LectureStateCompleted, 9000, f.now)
	f.record(t, 10, 20, 51, domain.LectureStateInProgress, 10, f.now.Add(time.Minute))
	// A stale in_progress write for the completed lecture, e.g. a replayed offline queue.
	f.record(t, 10, 20, 50, domain.LectureStateInProgress, 400, f.now.Add(2*time.Minute))

	cp, _ := f.course(t, 10, 20)
	if cp.Lectures[0].State != domain.LectureStateCompleted || cp.Lectures[0].PositionMs != 9000 || !cp.Lectures[0].UpdatedAt.Equal(f.now) {
		t.Fatalf("lecture 50 regressed: %+v", cp.Lectures[0])
	}
	if cp.LastLectureID != 51 || !cp.UpdatedAt.Equal(f.now.Add(time.Minute)) {
		t.Fatalf("rejected write moved the course: %+v", cp)
	}

	// completed overwrites in_progress, and completed overwrites completed.
	f.record(t, 10, 20, 51, domain.LectureStateCompleted, 20, f.now.Add(3*time.Minute))
	f.record(t, 10, 20, 50, domain.LectureStateCompleted, 9500, f.now.Add(4*time.Minute))
	cp, _ = f.course(t, 10, 20)
	if !slices.Equal(cp.CompletedLectureIDs(), []id.ID{50, 51}) || cp.Lectures[0].PositionMs != 9500 || cp.LastLectureID != 50 {
		t.Fatalf("cp=%+v", cp)
	}
}

func TestFindByUserAndActivity(t *testing.T) {
	f := newFixture(t)
	f.record(t, 10, 20, 50, domain.LectureStateCompleted, 0, f.now)
	f.record(t, 10, 20, 51, domain.LectureStateInProgress, 5, f.now.Add(time.Hour))
	f.record(t, 11, 20, 60, domain.LectureStateInProgress, 7, f.now.Add(24*time.Hour))
	f.record(t, 10, 21, 50, domain.LectureStateCompleted, 0, f.now) // another user

	var (
		courses []domain.CourseProgress
		days    []domain.ActivityDay
	)
	if err := f.tx.RunInTx(ctx, func(r app.Repository) error {
		var err error
		if courses, err = r.FindByUser(ctx, 20); err != nil {
			return err
		}
		days, err = r.ActivityByUser(ctx, 20)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(courses) != 2 || courses[0].CourseID != 11 || len(courses[0].Lectures) != 1 || courses[1].CourseID != 10 || len(courses[1].Lectures) != 2 {
		t.Fatalf("courses=%+v", courses)
	}
	want := []domain.ActivityDay{
		{Date: time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC), LectureCount: 1},
		{Date: time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC), LectureCount: 2},
	}
	if !slices.EqualFunc(days, want, func(a, b domain.ActivityDay) bool { return a.Date.Equal(b.Date) && a.LectureCount == b.LectureCount }) {
		t.Fatalf("days=%+v", days)
	}
}

func TestActivityUsesUTCDateWhateverTheSessionTimeZone(t *testing.T) {
	f := newFixture(t)
	if _, err := f.pool.Exec(ctx, "ALTER ROLE CURRENT_USER SET timezone = 'Asia/Kathmandu'"); err != nil {
		t.Fatal(err)
	}
	f.pool.Reset() // new connections pick up the role's time zone
	// 23:30 UTC on Oct 5 is 05:15 on Oct 6 in Kathmandu.
	f.record(t, 10, 20, 50, domain.LectureStateCompleted, 0, time.Date(2026, 10, 5, 23, 30, 0, 0, time.UTC))
	var days []domain.ActivityDay
	if err := f.tx.RunInTx(ctx, func(r app.Repository) error {
		var err error
		days, err = r.ActivityByUser(ctx, 20)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(days) != 1 || !days[0].Date.Equal(time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("days=%+v", days)
	}
}
