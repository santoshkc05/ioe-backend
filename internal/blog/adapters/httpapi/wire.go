package httpapi

import (
	"encoding/json"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/blog/app"
	"github.com/santoshkc2200/ioe-backend/internal/blog/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/contentblocks"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

type detailsRequest struct {
	Title    string   `json:"title"`
	Summary  string   `json:"summary"`
	CoverURL string   `json:"cover_url"`
	Tags     []string `json:"tags"`
}

func (d detailsRequest) input() app.DetailsInput {
	return app.DetailsInput{Title: d.Title, Summary: d.Summary, CoverURL: d.CoverURL, Tags: d.Tags}
}

type updateDetailsRequest struct {
	detailsRequest
	Version int64 `json:"version"`
}

type setSlugRequest struct {
	Slug string `json:"slug"`
}

// blockRequest has no position: order comes from the list or the patch's order.
type blockRequest struct {
	ClientBlockID string `json:"client_block_id"`
	Type          string `json:"type"`
	Body          string `json:"body"`
	URL           string `json:"url"`
	Alt           string `json:"alt"`
	Caption       string `json:"caption"`
	Layout        string `json:"layout"`
	Alignment     string `json:"alignment"`
	Rotation      int    `json:"rotation"`
	Width         int    `json:"width"`
	Height        int    `json:"height"`
}

type replaceContentRequest struct {
	Blocks []blockRequest `json:"blocks"`
}

type patchContentRequest struct {
	BaseRevision *int64         `json:"base_revision"`
	Order        []string       `json:"order"`
	Upserts      []blockRequest `json:"upserts"`
	Deletes      []string       `json:"deletes"`
}

func toBlockInputs(in []blockRequest) []app.BlockInput {
	out := make([]app.BlockInput, len(in))
	for i, b := range in {
		out[i] = app.BlockInput{ClientBlockID: b.ClientBlockID, Type: b.Type, Body: b.Body, URL: b.URL, Alt: b.Alt,
			Caption: b.Caption, Layout: b.Layout, Alignment: b.Alignment, Rotation: b.Rotation, Width: b.Width, Height: b.Height}
	}
	return out
}

type detailsWire struct {
	Title    string   `json:"title"`
	Summary  string   `json:"summary"`
	CoverURL string   `json:"cover_url"`
	Tags     []string `json:"tags"`
}

func toDetailsWire(d domain.Details) detailsWire {
	tags := d.Tags
	if tags == nil {
		tags = []string{}
	}
	return detailsWire{Title: d.Title.String(), Summary: d.Summary, CoverURL: d.CoverURL, Tags: tags}
}

type postWire struct {
	ID       id.ID `json:"id"`
	AuthorID id.ID `json:"author_id"`
	detailsWire
	Slug             string     `json:"slug"`
	Status           string     `json:"status"`
	LastVersion      int        `json:"last_version"`
	LiveVersion      *int       `json:"live_version"`
	LivePublishedAt  *time.Time `json:"live_published_at"`
	FirstPublishedAt *time.Time `json:"first_published_at"`
	Version          int64      `json:"version"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

func toPostWire(p domain.Post) postWire {
	w := postWire{ID: p.ID, AuthorID: p.AuthorID, detailsWire: toDetailsWire(p.Details), Slug: p.Slug.String(),
		Status: string(p.Status), LastVersion: p.LastVersion, Version: p.Version, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt}
	if p.IsLive() {
		n, at := p.Live.Number, p.Live.PublishedAt
		w.LiveVersion, w.LivePublishedAt = &n, &at
	}
	if !p.FirstPublishedAt.IsZero() {
		at := p.FirstPublishedAt
		w.FirstPublishedAt = &at
	}
	return w
}

type postPageWire struct {
	Posts []postWire `json:"posts"`
}

type blockWire struct {
	ID            id.ID  `json:"id"`
	ClientBlockID string `json:"client_block_id"`
	Type          string `json:"type"`
	Position      int    `json:"position"`
	Body          string `json:"body,omitempty"`
	URL           string `json:"url,omitempty"`
	Alt           string `json:"alt,omitempty"`
	Caption       string `json:"caption,omitempty"`
	Layout        string `json:"layout,omitempty"`
	Alignment     string `json:"alignment,omitempty"`
	Rotation      int    `json:"rotation,omitempty"`
	Width         int    `json:"width,omitempty"`
	Height        int    `json:"height,omitempty"`
}

func toBlockWires(blocks []contentblocks.Block) []blockWire {
	out := make([]blockWire, 0, len(blocks))
	for _, b := range blocks {
		w := blockWire{ID: b.ID(), ClientBlockID: b.ClientBlockID(), Type: string(b.Type()), Position: b.Position()}
		if t, ok := b.Text(); ok {
			w.Body = t.String()
		}
		if img, ok := b.Image(); ok {
			w.URL, w.Alt, w.Caption, w.Layout, w.Alignment = img.URL(), img.Alt(), img.Caption(), img.Layout(), img.Alignment()
			w.Rotation, w.Width, w.Height = img.Rotation(), img.Width(), img.Height()
		}
		out = append(out, w)
	}
	return out
}

type contentWire struct {
	PostID          id.ID       `json:"post_id"`
	ContentRevision int64       `json:"content_revision"`
	Blocks          []blockWire `json:"blocks"`
}

func toContentWire(v app.ContentView) contentWire {
	return contentWire{PostID: v.PostID, ContentRevision: v.ContentRevision, Blocks: toBlockWires(v.Blocks)}
}

// contentConflictExt renders the winner's content as problem+json extension members.
func contentConflictExt(v app.ContentView) map[string]any {
	raw, err := json.Marshal(toContentWire(v))
	if err != nil {
		return nil
	}
	var ext map[string]any
	if err := json.Unmarshal(raw, &ext); err != nil {
		return nil
	}
	return ext
}

type versionSummaryWire struct {
	Number      int       `json:"version_number"`
	PublishedBy id.ID     `json:"published_by"`
	PublishedAt time.Time `json:"published_at"`
}

type versionWire struct {
	versionSummaryWire
	detailsWire
	ReadingTimeMinutes int         `json:"reading_time_minutes"`
	Blocks             []blockWire `json:"blocks"`
}

func toVersionWire(v app.VersionView) versionWire {
	return versionWire{versionSummaryWire: versionSummaryWire{Number: v.Number, PublishedBy: v.PublishedBy, PublishedAt: v.PublishedAt},
		detailsWire: toDetailsWire(v.Details), ReadingTimeMinutes: v.ReadingMinutes, Blocks: toBlockWires(v.Blocks)}
}

type cardWire struct {
	ID         id.ID  `json:"id"`
	AuthorID   id.ID  `json:"author_id"`
	AuthorName string `json:"author_name"`
	Slug       string `json:"slug"`
	detailsWire
	VersionNumber      int       `json:"version_number"`
	ReadingTimeMinutes int       `json:"reading_time_minutes"`
	FirstPublishedAt   time.Time `json:"first_published_at"`
	PublishedAt        time.Time `json:"published_at"`
}

func toCardWire(c app.Card) cardWire {
	return cardWire{ID: c.PostID, AuthorID: c.AuthorID, AuthorName: c.AuthorName, Slug: c.Slug,
		detailsWire: toDetailsWire(c.Details), VersionNumber: c.Number, ReadingTimeMinutes: c.ReadingMinutes,
		FirstPublishedAt: c.FirstPublishedAt, PublishedAt: c.PublishedAt}
}

type cardPageWire struct {
	Items      []cardWire `json:"items"`
	NextCursor *string    `json:"next_cursor"`
}

type publicPostWire struct {
	cardWire
	RequestedSlug string      `json:"requested_slug"`
	Blocks        []blockWire `json:"blocks"`
}

func toPublicPostWire(p app.PublicPost) publicPostWire {
	return publicPostWire{cardWire: toCardWire(p.Card), RequestedSlug: p.RequestedSlug, Blocks: toBlockWires(p.Blocks)}
}

type tagCountWire struct {
	Tag   string `json:"tag"`
	Count int    `json:"count"`
}

type indexEntryWire struct {
	Slug             string    `json:"slug"`
	FirstPublishedAt time.Time `json:"first_published_at"`
	PublishedAt      time.Time `json:"published_at"`
}

type indexPageWire struct {
	Items      []indexEntryWire `json:"items"`
	NextCursor *string          `json:"next_cursor"`
}
