package domain_test

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/contentblocks"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

var classifyNow = time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)

func classifiedCourse(t *testing.T) domain.Course {
	t.Helper()
	ttl, _ := contentblocks.NewTitle("Go")
	c := domain.NewCourse(1, 100, ttl, "d", classifyNow)
	lt, _ := contentblocks.NewTitle("L1")
	if err := c.AddLecture(2, lt, false, false, classifyNow); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestSetClassification(t *testing.T) {
	c := classifiedCourse(t)
	if err := c.SetClassification([]id.ID{9, 8, 9}, []string{"Go", "web", "GO"}, classifyNow); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(c.CategoryIDs(), []id.ID{9, 8}) || !slices.Equal(c.Tags, []string{"go", "web"}) {
		t.Fatalf("categories = %v, tags = %v", c.CategoryIDs(), c.Tags)
	}
	if err := c.SetClassification([]id.ID{1, 2, 3, 4}, nil, classifyNow); !errors.Is(err, domain.ErrTooManyCategories) {
		t.Fatalf("four categories = %v", err)
	}
	if err := c.SetClassification(nil, []string{"c++"}, classifyNow); !errors.Is(err, domain.ErrInvalidTag) {
		t.Fatalf("bad tag = %v", err)
	}
	eleven := []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k"}
	if err := c.SetClassification(nil, eleven, classifyNow); !errors.Is(err, domain.ErrTooManyTags) {
		t.Fatalf("eleven tags = %v", err)
	}
	if err := c.SetClassification(nil, nil, classifyNow); err != nil || len(c.Categories) != 0 || c.Tags == nil || len(c.Tags) != 0 {
		t.Fatalf("clear = %v, %+v %#v", err, c.Categories, c.Tags)
	}
}

func TestSetClassificationFollowsEditRules(t *testing.T) {
	c := classifiedCourse(t)
	if err := c.Publish(true, classifyNow); err != nil {
		t.Fatal(err)
	}
	if err := c.SetClassification([]id.ID{9}, nil, classifyNow); err != nil || c.Status != domain.StatusDraft {
		t.Fatalf("published edit = %v, status %s", err, c.Status)
	}
	if err := c.Archive(classifyNow); err != nil {
		t.Fatal(err)
	}
	if err := c.SetClassification(nil, nil, classifyNow); !errors.Is(err, domain.ErrCourseNotEditable) {
		t.Fatalf("archived edit = %v", err)
	}
}

func TestDiscardDraftRestoresClassification(t *testing.T) {
	c := classifiedCourse(t)
	if err := c.SetClassification([]id.ID{9}, []string{"go"}, classifyNow); err != nil {
		t.Fatal(err)
	}
	if err := c.Publish(true, classifyNow); err != nil {
		t.Fatal(err)
	}
	live := c
	if err := c.SetClassification([]id.ID{8}, []string{"rust"}, classifyNow); err != nil {
		t.Fatal(err)
	}
	if err := c.DiscardDraft(live, classifyNow); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(c.CategoryIDs(), []id.ID{9}) || !slices.Equal(c.Tags, []string{"go"}) {
		t.Fatalf("restored categories = %v, tags = %v", c.CategoryIDs(), c.Tags)
	}
}
