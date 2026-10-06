package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
	"github.com/santoshkc2200/ioe-backend/internal/progress/adapters/postgres/sqlcgen"
	"github.com/santoshkc2200/ioe-backend/internal/progress/domain"
)

type progress struct{ q *sqlcgen.Queries }

func (r progress) RecordLecture(ctx context.Context, courseID, userID id.ID, p domain.LectureProgress) error {
	_, err := r.q.UpsertLectureProgress(ctx, sqlcgen.UpsertLectureProgressParams{
		CourseID: int64(courseID), UserID: int64(userID), LectureID: int64(p.LectureID),
		State: string(p.State), PositionMs: p.PositionMs, UpdatedAt: p.UpdatedAt,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// A completed lecture rejected an in_progress write; the course is untouched too.
		return nil
	}
	if err != nil {
		return err
	}
	return r.q.UpsertCourseProgress(ctx, sqlcgen.UpsertCourseProgressParams{
		CourseID: int64(courseID), UserID: int64(userID), LastLectureID: int64(p.LectureID), UpdatedAt: p.UpdatedAt,
	})
}

func (r progress) FindCourse(ctx context.Context, courseID, userID id.ID) (domain.CourseProgress, bool, error) {
	row, err := r.q.GetCourseProgress(ctx, sqlcgen.GetCourseProgressParams{CourseID: int64(courseID), UserID: int64(userID)})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.CourseProgress{}, false, nil
	}
	if err != nil {
		return domain.CourseProgress{}, false, err
	}
	lectures, err := r.q.ListLectureProgressForCourse(ctx, sqlcgen.ListLectureProgressForCourseParams{CourseID: int64(courseID), UserID: int64(userID)})
	if err != nil {
		return domain.CourseProgress{}, false, err
	}
	return toCourse(row, lectures), true, nil
}

func (r progress) FindByUser(ctx context.Context, userID id.ID) ([]domain.CourseProgress, error) {
	rows, err := r.q.ListCourseProgressByUser(ctx, int64(userID))
	if err != nil {
		return nil, err
	}
	lectures, err := r.q.ListLectureProgressByUser(ctx, int64(userID))
	if err != nil {
		return nil, err
	}
	byCourse := make(map[int64][]sqlcgen.ProgressLectureProgress, len(rows))
	for _, l := range lectures {
		byCourse[l.CourseID] = append(byCourse[l.CourseID], l)
	}
	out := make([]domain.CourseProgress, len(rows))
	for i, row := range rows {
		out[i] = toCourse(row, byCourse[row.CourseID])
	}
	return out, nil
}

func (r progress) ActivityByUser(ctx context.Context, userID id.ID) ([]domain.ActivityDay, error) {
	rows, err := r.q.ListActivityDaysByUser(ctx, int64(userID))
	if err != nil {
		return nil, err
	}
	out := make([]domain.ActivityDay, len(rows))
	for i, row := range rows {
		d, err := time.Parse(time.DateOnly, row.Day)
		if err != nil {
			return nil, err
		}
		out[i] = domain.ActivityDay{Date: d, LectureCount: int(row.LectureCount)}
	}
	return out, nil
}

func toCourse(row sqlcgen.ProgressCourseProgress, lectures []sqlcgen.ProgressLectureProgress) domain.CourseProgress {
	cp := domain.CourseProgress{
		CourseID: id.ID(row.CourseID), UserID: id.ID(row.UserID), LastLectureID: id.ID(row.LastLectureID),
		UpdatedAt: row.UpdatedAt.UTC(), Lectures: make([]domain.LectureProgress, len(lectures)),
	}
	for i, l := range lectures {
		cp.Lectures[i] = domain.LectureProgress{
			LectureID: id.ID(l.LectureID), State: domain.LectureState(l.State), PositionMs: l.PositionMs, UpdatedAt: l.UpdatedAt.UTC(),
		}
	}
	return cp
}
