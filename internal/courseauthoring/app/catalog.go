package app

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
	"github.com/santoshkc2200/ioe-backend/internal/platform/tags"
)

// MaxCatalogLimit is the largest catalog page.
const MaxCatalogLimit = 50

// MaxSearchRunes is the longest search text.
const MaxSearchRunes = 200

type PriceFilter string

const (
	PriceAny  PriceFilter = ""
	PriceFree PriceFilter = "free"
	PricePaid PriceFilter = "paid"
)

// CatalogQuery selects one page of published courses. After is zero for the first page.
type CatalogQuery struct {
	Level     string // empty means any level
	Price     PriceFilter
	Q         string // search text; empty means no search and newest-first order
	Category  string // category slug; empty means any category
	Tag       string // empty means any tag
	Limit     int
	After     id.ID
	AfterRank float32 // the previous page's last rank; used only with Q
}

// CourseSummary is a published course without its outline. Rank is the search relevance; it
// is zero without search text.
type CourseSummary struct {
	ID           id.ID
	OwnerID      id.ID
	Title        string
	Description  string
	Level        string
	ThumbnailURL string
	Price        domain.Price
	Categories   []domain.CategoryRef
	Tags         []string
	LectureCount int
	SectionCount int
	Rank         float32
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// CatalogPage is one page of the catalog. Next is zero on the last page; NextRank is the rank
// of the row Next names when the query had search text.
type CatalogPage struct {
	Courses  []CourseSummary
	Next     id.ID
	NextRank float32
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
	if utf8.RuneCountInString(q.Q) > MaxSearchRunes {
		return fmt.Errorf("%w: q must be at most %d characters", ErrInvalidInput, MaxSearchRunes)
	}
	return nil
}

// ListPublished returns one page of published courses: newest first, or by relevance when q.Q
// is set. It takes no principal because published courses are public. A tag that no course
// could carry and an unknown category slug match nothing.
func (s *CourseService) ListPublished(ctx context.Context, q CatalogQuery) (CatalogPage, error) {
	q.Q = strings.TrimSpace(q.Q)
	q.Category = strings.ToLower(strings.TrimSpace(q.Category))
	if err := q.validate(); err != nil {
		return CatalogPage{}, err
	}
	if q.Tag != "" {
		t, err := tags.New(q.Tag)
		if err != nil {
			return CatalogPage{}, nil
		}
		q.Tag = t
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
		last := page.Courses[q.Limit-1]
		page.Next, page.NextRank = last.ID, last.Rank
	}
	return page, nil
}
