package domain_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/domain"
)

func TestNewCategory(t *testing.T) {
	now := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	c, err := domain.NewCategory(42, "  Web Development ", now)
	if err != nil || c.ID != 42 || c.Name != "Web Development" || c.Slug != "web-development" || !c.CreatedAt.Equal(now) {
		t.Fatalf("NewCategory = %+v, %v", c, err)
	}
	for name, want := range map[string]string{"C++": "category-42", "日本語": "category-42", "Go": "go"} {
		c, err := domain.NewCategory(42, name, now)
		if err != nil || c.Slug != want {
			t.Errorf("%q: slug = %q, %v, want %q", name, c.Slug, err, want)
		}
	}
	for _, bad := range []string{"", "   ", strings.Repeat("x", domain.MaxCategoryNameRunes+1)} {
		if _, err := domain.NewCategory(42, bad, now); !errors.Is(err, domain.ErrInvalidCategoryName) {
			t.Errorf("%q: err = %v", bad, err)
		}
	}
	if _, err := domain.NewCategory(42, strings.Repeat("é", domain.MaxCategoryNameRunes), now); err != nil {
		t.Errorf("60 runes: %v", err)
	}
}

func TestCategoryRename(t *testing.T) {
	c, _ := domain.NewCategory(7, "Web", time.Now())
	if err := c.Rename("Data Science"); err != nil || c.Name != "Data Science" || c.Slug != "data-science" {
		t.Fatalf("Rename = %+v, %v", c, err)
	}
	if err := c.Rename(" "); !errors.Is(err, domain.ErrInvalidCategoryName) || c.Name != "Data Science" {
		t.Fatalf("bad rename = %+v, %v", c, err)
	}
}
