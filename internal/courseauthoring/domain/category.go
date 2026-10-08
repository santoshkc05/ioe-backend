package domain

import (
	"strings"
	"time"
	"unicode/utf8"

	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
	"github.com/santoshkc2200/ioe-backend/internal/platform/slug"
)

// MaxCategoryNameRunes is the longest category name.
const MaxCategoryNameRunes = 60

const maxCategorySlugLen = 64

// Category is an entry in the root-admin-managed list courses are filed under.
type Category struct {
	ID        id.ID
	Name      string
	Slug      string
	CreatedAt time.Time
}

// CategoryRef is a course's link to a category. Writes use only ID; reads hydrate Name and
// Slug from the category.
type CategoryRef struct {
	ID   id.ID
	Name string
	Slug string
}

func NewCategory(categoryID id.ID, name string, now time.Time) (Category, error) {
	c := Category{ID: categoryID, CreatedAt: now}
	if err := c.Rename(name); err != nil {
		return Category{}, err
	}
	return c, nil
}

// Rename sets the trimmed name and re-derives the slug. A name with fewer than 2 usable ASCII
// characters gets the slug "category-<id>".
func (c *Category) Rename(raw string) error {
	name := strings.TrimSpace(raw)
	if name == "" || utf8.RuneCountInString(name) > MaxCategoryNameRunes {
		return ErrInvalidCategoryName
	}
	s := slug.From(name, maxCategorySlugLen)
	if len(s) < 2 {
		s = "category-" + c.ID.String()
	}
	c.Name, c.Slug = name, s
	return nil
}
