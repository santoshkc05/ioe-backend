package app

import (
	"context"
	"fmt"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

// MaxCatalogLimit is the largest catalog page.
const MaxCatalogLimit = 50

type PriceFilter string

const (
	PriceAny  PriceFilter = ""
	PriceFree PriceFilter = "free"
	PricePaid PriceFilter = "paid"
)

// CatalogQuery selects one page of published courses. After is zero for the first page.
type CatalogQuery struct {
	Level string // empty means any level
	Price PriceFilter
	Limit int
	After id.ID
}

// CourseSummary is a published course without its outline.
type CourseSummary struct {
	ID           id.ID
	OwnerID      id.ID
	Title        string
	Description  string
	Level        string
	ThumbnailURL string
	Price        domain.Price
	LectureCount int
	SectionCount int
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// CatalogPage is one page of the catalog. Next is zero on the last page.
type CatalogPage struct {
	Courses []CourseSummary
	Next    id.ID
}

func (q CatalogQuery) validate() error {
	if q.Limit < 1 || q.Limit > MaxCatalogLimit {
		return fmt.Errorf("%w: limit must be between 1 and %d", ErrInvalidInput, MaxCatalogLimit)
	}
	switch q.Level {
	case "", "beginner", "intermediate", "advanced":
	default:
		return fmt.Errorf("%w: level must be beginner, intermediate or advanced", ErrInvalidInput)
	}
	switch q.Price {
	case PriceAny, PriceFree, PricePaid:
	default:
		return fmt.Errorf("%w: price must be free or paid", ErrInvalidInput)
	}
	return nil
}

// ListPublished returns one page of published courses, newest first. It takes no principal
// because published courses are public.
func (s *CourseService) ListPublished(ctx context.Context, q CatalogQuery) (CatalogPage, error) {
	if err := q.validate(); err != nil {
		return CatalogPage{}, err
	}
	probe := q
	probe.Limit++ // one extra row tells whether another page exists
	var rows []CourseSummary
	err := s.tx.RunInTx(ctx, func(r Repos) error {
		var err error
		rows, err = r.Courses.ListPublished(ctx, probe)
		return err
	})
	if err != nil {
		return CatalogPage{}, err
	}
	page := CatalogPage{Courses: rows}
	if len(rows) > q.Limit {
		page.Courses = rows[:q.Limit]
		page.Next = page.Courses[q.Limit-1].ID
	}
	return page, nil
}
