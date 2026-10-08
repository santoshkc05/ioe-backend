package app

import (
	"context"

	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/clock"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

// CategoryService manages the course category list. Root admins write it; anyone reads it.
type CategoryService struct {
	tx    TxRunner
	ids   *id.Generator
	clock clock.Clock
}

func NewCategoryService(tx TxRunner, ids *id.Generator, c clock.Clock) *CategoryService {
	return &CategoryService{tx: tx, ids: ids, clock: c}
}

func (s *CategoryService) Create(ctx context.Context, p auth.Principal, name string) (domain.Category, error) {
	if p.Role != auth.RoleRootAdmin {
		return domain.Category{}, ErrForbidden
	}
	c, err := domain.NewCategory(s.ids.New(), name, s.clock.Now())
	if err != nil {
		return domain.Category{}, err
	}
	if err := s.tx.RunInTx(ctx, func(r Repos) error { return r.Categories.Insert(ctx, c) }); err != nil {
		return domain.Category{}, err
	}
	return c, nil
}

func (s *CategoryService) Rename(ctx context.Context, p auth.Principal, categoryID id.ID, name string) (domain.Category, error) {
	if p.Role != auth.RoleRootAdmin {
		return domain.Category{}, ErrForbidden
	}
	var out domain.Category
	err := s.tx.RunInTx(ctx, func(r Repos) error {
		c, err := r.Categories.Get(ctx, categoryID)
		if err != nil {
			return err
		}
		if err := c.Rename(name); err != nil {
			return err
		}
		if err := r.Categories.Update(ctx, c); err != nil {
			return err
		}
		out = c
		return nil
	})
	return out, err
}

// Delete removes the category from the list and from every course and published version.
func (s *CategoryService) Delete(ctx context.Context, p auth.Principal, categoryID id.ID) error {
	if p.Role != auth.RoleRootAdmin {
		return ErrForbidden
	}
	return s.tx.RunInTx(ctx, func(r Repos) error { return r.Categories.Delete(ctx, categoryID) })
}

// List returns every category by name with its live course count. It takes no principal
// because the list is public.
func (s *CategoryService) List(ctx context.Context) ([]CategoryWithCount, error) {
	var out []CategoryWithCount
	err := s.tx.RunInTx(ctx, func(r Repos) error {
		var err error
		out, err = r.Categories.List(ctx)
		return err
	})
	return out, err
}
