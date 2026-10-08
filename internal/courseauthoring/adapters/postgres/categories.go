package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/adapters/postgres/sqlcgen"
	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/app"
	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

type categories struct {
	tx pgx.Tx
	q  *sqlcgen.Queries
}

// categoryConflict maps a unique violation on name or slug to app.ErrCategoryExists.
func categoryConflict(err error) error {
	if pgCode(err) == uniqueViolation {
		return app.ErrCategoryExists
	}
	return err
}

func (r categories) Insert(ctx context.Context, c domain.Category) error {
	sp, err := r.tx.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = sp.Rollback(ctx) }()
	if err := r.q.WithTx(sp).InsertCategory(ctx, sqlcgen.InsertCategoryParams{
		ID: int64(c.ID), Name: c.Name, Slug: c.Slug, CreatedAt: c.CreatedAt,
	}); err != nil {
		return categoryConflict(err)
	}
	return sp.Commit(ctx)
}

func (r categories) Update(ctx context.Context, c domain.Category) error {
	sp, err := r.tx.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = sp.Rollback(ctx) }()
	n, err := r.q.WithTx(sp).UpdateCategory(ctx, sqlcgen.UpdateCategoryParams{ID: int64(c.ID), Name: c.Name, Slug: c.Slug})
	if err != nil {
		return categoryConflict(err)
	}
	if n == 0 {
		return app.ErrNotFound
	}
	return sp.Commit(ctx)
}

func (r categories) Delete(ctx context.Context, categoryID id.ID) error {
	n, err := r.q.DeleteCategory(ctx, int64(categoryID))
	if err != nil {
		return err
	}
	if n == 0 {
		return app.ErrNotFound
	}
	return nil
}

func (r categories) Get(ctx context.Context, categoryID id.ID) (domain.Category, error) {
	row, err := r.q.GetCategory(ctx, int64(categoryID))
	if err != nil {
		return domain.Category{}, notFound(err)
	}
	return domain.Category{ID: id.ID(row.ID), Name: row.Name, Slug: row.Slug, CreatedAt: row.CreatedAt.UTC()}, nil
}

func (r categories) List(ctx context.Context) ([]app.CategoryWithCount, error) {
	rows, err := r.q.ListCategoriesWithCounts(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]app.CategoryWithCount, len(rows))
	for i, row := range rows {
		out[i] = app.CategoryWithCount{
			Category:    domain.Category{ID: id.ID(row.ID), Name: row.Name, Slug: row.Slug, CreatedAt: row.CreatedAt.UTC()},
			CourseCount: int(row.CourseCount),
		}
	}
	return out, nil
}

func (r categories) ExistAll(ctx context.Context, ids []id.ID) (bool, error) {
	if len(ids) == 0 {
		return true, nil
	}
	n, err := r.q.CountCategories(ctx, int64s(ids))
	if err != nil {
		return false, err
	}
	return int(n) == len(ids), nil
}

func int64s(ids []id.ID) []int64 {
	out := make([]int64, len(ids))
	for i, v := range ids {
		out[i] = int64(v)
	}
	return out
}

// categoryRef builds a hydrated course-category link from a query row.
func categoryRef(categoryID int64, name, slug string) domain.CategoryRef {
	return domain.CategoryRef{ID: id.ID(categoryID), Name: name, Slug: slug}
}
