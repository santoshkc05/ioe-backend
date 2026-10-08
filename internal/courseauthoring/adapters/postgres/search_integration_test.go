//go:build integration

package postgres_test

import (
	"slices"
	"testing"

	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/app"
	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/contentblocks"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

// seedLive publishes a course with the given details and classification.
func (f fixture) seedLive(t *testing.T, title, description, level string, cats []domain.Category, tagList []string) domain.Course {
	t.Helper()
	c := f.seedCourse(t)
	ttl, err := contentblocks.NewTitle(title)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.UpdateDetails(ttl, description, level, "", f.now); err != nil {
		t.Fatal(err)
	}
	if err := f.classify(t, &c, cats, tagList); err != nil {
		t.Fatal(err)
	}
	f.publish(t, &c)
	return c
}

func TestCatalogSearchRanksAndPrefixes(t *testing.T) {
	f := newFixture(t)
	titled := f.seedLive(t, "Learning Go", "an introduction", "", nil, nil)
	described := f.seedLive(t, "Cooking", "we go shopping first", "", nil, nil)
	tagged := f.seedLive(t, "Systems", "low level", "", nil, []string{"go"})
	f.seedLive(t, "Rust", "ownership", "", nil, nil)

	got := f.listPublished(t, app.CatalogQuery{Limit: 10, Q: "go"})
	if ids := summaryIDs(got); !slices.Equal(ids, []id.ID{titled.ID, tagged.ID, described.ID}) {
		t.Fatalf("q=go = %v (ranks %v)", ids, ranks(got))
	}
	if !(got[0].Rank > got[1].Rank && got[1].Rank > got[2].Rank) {
		t.Fatalf("ranks not descending: %v", ranks(got))
	}
	if ids := summaryIDs(f.listPublished(t, app.CatalogQuery{Limit: 10, Q: "lea"})); !slices.Equal(ids, []id.ID{titled.ID}) {
		t.Fatalf("prefix = %v", ids)
	}
	if ids := summaryIDs(f.listPublished(t, app.CatalogQuery{Limit: 10, Q: `"learning go"`})); !slices.Equal(ids, []id.ID{titled.ID}) {
		t.Fatalf("phrase = %v", ids)
	}
	if got := f.listPublished(t, app.CatalogQuery{Limit: 10, Q: "golang"}); len(got) != 0 {
		t.Fatalf("golang = %v", summaryIDs(got))
	}
	if got := f.listPublished(t, app.CatalogQuery{Limit: 10}); got[0].Rank != 0 {
		t.Fatalf("rank without q = %v", got[0].Rank)
	}
}

func TestCatalogSearchOddInput(t *testing.T) {
	f := newFixture(t)
	f.seedLive(t, "Learning Go", "intro", "", nil, nil)
	for _, q := range []string{"go!", `"unterminated`, "c++", "!!!", "a & b | c", "go:*", `\`, "-"} {
		got := f.listPublished(t, app.CatalogQuery{Limit: 10, Q: q})
		if q == "go!" && len(got) != 1 {
			t.Errorf("%q = %v", q, summaryIDs(got))
		}
	}
}

func TestCatalogFiltersByCategoryAndTagFromLiveVersion(t *testing.T) {
	f := newFixture(t)
	web := f.seedCategory(t, "Web")
	classified := f.seedLive(t, "Go", "d", "beginner", []domain.Category{web}, []string{"go", "web"})
	plain := f.seedLive(t, "Go", "d", "beginner", nil, nil)

	got := f.listPublished(t, app.CatalogQuery{Limit: 10, Category: "web"})
	if ids := summaryIDs(got); !slices.Equal(ids, []id.ID{classified.ID}) {
		t.Fatalf("category = %v", ids)
	}
	if !slices.Equal(slugs(got[0].Categories), []string{"web"}) || got[0].Categories[0].Name != "Web" || !slices.Equal(got[0].Tags, []string{"go", "web"}) {
		t.Fatalf("summary = %+v", got[0])
	}
	if ids := summaryIDs(f.listPublished(t, app.CatalogQuery{Limit: 10, Tag: "go", Level: "beginner"})); !slices.Equal(ids, []id.ID{classified.ID}) {
		t.Fatalf("tag+level = %v", ids)
	}
	if got := f.listPublished(t, app.CatalogQuery{Limit: 10, Tag: "go", Level: "advanced"}); len(got) != 0 {
		t.Fatalf("tag+advanced = %v", summaryIDs(got))
	}
	all := f.listPublished(t, app.CatalogQuery{Limit: 10})
	if len(all) != 2 || all[0].ID != plain.ID || all[0].Tags == nil || len(all[0].Tags) != 0 || len(all[0].Categories) != 0 {
		t.Fatalf("plain summary = %+v", all[0])
	}

	// A working-copy change is invisible until republished.
	c := classified
	if err := f.classify(t, &c, nil, nil); err != nil {
		t.Fatal(err)
	}
	if ids := summaryIDs(f.listPublished(t, app.CatalogQuery{Limit: 10, Tag: "go"})); !slices.Equal(ids, []id.ID{classified.ID}) {
		t.Fatalf("after draft edit = %v", ids)
	}
	f.publish(t, &c)
	if got := f.listPublished(t, app.CatalogQuery{Limit: 10, Tag: "go"}); len(got) != 0 {
		t.Fatalf("after republish = %v", summaryIDs(got))
	}
}

func TestCatalogSearchKeysetWalk(t *testing.T) {
	f := newFixture(t)
	want := map[id.ID]bool{}
	for range 3 {
		want[f.seedLive(t, "Go", "x", "", nil, nil).ID] = true // equal ranks
	}
	for range 3 {
		want[f.seedLive(t, "Other", "go", "", nil, nil).ID] = true // equal, lower ranks
	}
	f.seedLive(t, "Rust", "x", "", nil, nil)

	var walked []app.CourseSummary
	q := app.CatalogQuery{Limit: 2, Q: "go"}
	for range 10 {
		page := f.listPublished(t, q)
		walked = append(walked, page...)
		if len(page) < q.Limit {
			break
		}
		last := page[len(page)-1]
		q.After, q.AfterRank = last.ID, last.Rank
	}
	if len(walked) != len(want) {
		t.Fatalf("walked %d rows, want %d: %v", len(walked), len(want), summaryIDs(walked))
	}
	for i, s := range walked {
		if !want[s.ID] {
			t.Fatalf("unexpected or repeated %v", s.ID)
		}
		delete(want, s.ID)
		if i > 0 && (s.Rank > walked[i-1].Rank || (s.Rank == walked[i-1].Rank && s.ID > walked[i-1].ID)) {
			t.Fatalf("out of order at %d: %v", i, summaryIDs(walked))
		}
	}
}

func ranks(ss []app.CourseSummary) []float32 {
	out := make([]float32, len(ss))
	for i, s := range ss {
		out[i] = s.Rank
	}
	return out
}
