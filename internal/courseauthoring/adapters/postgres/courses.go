package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/adapters/postgres/sqlcgen"
	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/app"
	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/contentblocks"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

type courses struct{ q *sqlcgen.Queries }

func (r courses) FindByID(ctx context.Context, courseID id.ID) (domain.Course, error) {
	cs, err := r.load(ctx, []int64{int64(courseID)})
	if err != nil {
		return domain.Course{}, err
	}
	if len(cs) == 0 {
		return domain.Course{}, app.ErrNotFound
	}
	return cs[0], nil
}

func (r courses) ListByOwner(ctx context.Context, ownerID id.ID) ([]domain.Course, error) {
	ids, err := r.q.ListCourseIDsByOwner(ctx, int64(ownerID))
	if err != nil {
		return nil, err
	}
	cs, err := r.load(ctx, ids)
	if err != nil {
		return nil, err
	}
	// load returns database order; restore newest-first.
	byID := make(map[int64]domain.Course, len(cs))
	for _, c := range cs {
		byID[int64(c.ID)] = c
	}
	out := make([]domain.Course, 0, len(ids))
	for _, cid := range ids {
		out = append(out, byID[cid])
	}
	return out, nil
}

// load hydrates courses with their sections and lectures in three queries.
func (r courses) load(ctx context.Context, courseIDs []int64) ([]domain.Course, error) {
	if len(courseIDs) == 0 {
		return nil, nil
	}
	rows, err := r.q.ListCoursesByIDs(ctx, courseIDs)
	if err != nil {
		return nil, err
	}
	sections, err := r.q.ListSectionsByCourseIDs(ctx, courseIDs)
	if err != nil {
		return nil, err
	}
	lectures, err := r.q.ListLecturesByCourseIDs(ctx, courseIDs)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Course, 0, len(rows))
	index := make(map[int64]int, len(rows))
	for _, row := range rows {
		ttl, err := contentblocks.NewTitle(row.Title)
		if err != nil {
			return nil, fmt.Errorf("course %d title: %w", row.ID, err)
		}
		index[row.ID] = len(out)
		out = append(out, domain.Course{
			ID: id.ID(row.ID), OwnerID: id.ID(row.OwnerID), Title: ttl, Description: row.Description,
			Level: row.Level, ThumbnailURL: row.ThumbnailUrl, Status: domain.Status(row.Status),
			Price:   domain.Price{AmountMinor: row.PriceAmountMinor, Currency: row.PriceCurrency},
			Version: row.Version, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
		})
	}
	for _, s := range sections {
		ttl, err := contentblocks.NewTitle(s.Title)
		if err != nil {
			return nil, fmt.Errorf("section %d title: %w", s.ID, err)
		}
		c := &out[index[s.CourseID]]
		c.Sections = append(c.Sections, domain.Section{ID: id.ID(s.ID), Title: ttl, Order: int(s.SortOrder)})
	}
	for _, l := range lectures {
		ttl, err := contentblocks.NewTitle(l.Title)
		if err != nil {
			return nil, fmt.Errorf("lecture %d title: %w", l.ID, err)
		}
		var sectionID id.ID
		if l.SectionID != nil {
			sectionID = id.ID(*l.SectionID)
		}
		c := &out[index[l.CourseID]]
		c.Lectures = append(c.Lectures, domain.Lecture{ID: id.ID(l.ID), SectionID: sectionID, Title: ttl,
			FreePreview: l.FreePreview, Order: int(l.SortOrder), HasText: l.HasText, HasVideo: l.HasVideo})
	}
	return out, nil
}

func (r courses) Insert(ctx context.Context, c *domain.Course) error {
	if err := r.q.InsertCourse(ctx, sqlcgen.InsertCourseParams{
		ID: int64(c.ID), OwnerID: int64(c.OwnerID), Title: c.Title.String(), Description: c.Description,
		Level: c.Level, ThumbnailUrl: c.ThumbnailURL, Status: string(c.Status),
		PriceAmountMinor: c.Price.AmountMinor, PriceCurrency: c.Price.Currency,
		CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
	}); err != nil {
		return err
	}
	c.Version = 1
	return r.saveChildren(ctx, c)
}

func (r courses) Update(ctx context.Context, c *domain.Course) error {
	n, err := r.q.UpdateCourse(ctx, sqlcgen.UpdateCourseParams{
		ID: int64(c.ID), Version: c.Version, Title: c.Title.String(), Description: c.Description,
		Level: c.Level, ThumbnailUrl: c.ThumbnailURL, Status: string(c.Status),
		PriceAmountMinor: c.Price.AmountMinor, PriceCurrency: c.Price.Currency, UpdatedAt: c.UpdatedAt,
	})
	if err != nil {
		return err
	}
	if n == 0 {
		return app.ErrConcurrentModification
	}
	c.Version++
	return r.saveChildren(ctx, c)
}

// saveChildren upserts sections then lectures, then deletes rows no longer in the
// aggregate. It never touches lecture content or content_revision.
func (r courses) saveChildren(ctx context.Context, c *domain.Course) error {
	sectionIDs := make([]int64, 0, len(c.Sections))
	for _, s := range c.Sections {
		if err := r.q.UpsertSection(ctx, sqlcgen.UpsertSectionParams{
			ID: int64(s.ID), CourseID: int64(c.ID), Title: s.Title.String(),
			SortOrder: int32(s.Order), //nolint:gosec // Order is a slice index, bounded by aggregate size
		}); err != nil {
			return err
		}
		sectionIDs = append(sectionIDs, int64(s.ID))
	}
	lectureIDs := make([]int64, 0, len(c.Lectures))
	for _, l := range c.Lectures {
		var sectionID *int64
		if !l.SectionID.IsZero() {
			v := int64(l.SectionID)
			sectionID = &v
		}
		if err := r.q.UpsertLecture(ctx, sqlcgen.UpsertLectureParams{
			ID: int64(l.ID), CourseID: int64(c.ID), SectionID: sectionID, Title: l.Title.String(),
			FreePreview: l.FreePreview, CreatedAt: c.UpdatedAt,
			SortOrder: int32(l.Order), //nolint:gosec // Order is a slice index, bounded by aggregate size
		}); err != nil {
			return err
		}
		lectureIDs = append(lectureIDs, int64(l.ID))
	}
	if err := r.q.DeleteLecturesNotIn(ctx, sqlcgen.DeleteLecturesNotInParams{CourseID: int64(c.ID), Keep: lectureIDs}); err != nil {
		return err
	}
	return r.q.DeleteSectionsNotIn(ctx, sqlcgen.DeleteSectionsNotInParams{CourseID: int64(c.ID), Keep: sectionIDs})
}

func notFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return app.ErrNotFound
	}
	return err
}
