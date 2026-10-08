package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/blog/adapters/postgres/sqlcgen"
	"github.com/santoshkc2200/ioe-backend/internal/blog/app"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

type public struct{ q *sqlcgen.Queries }

func cursorArgs(after *app.Cursor) (*time.Time, int64) {
	if after == nil {
		return nil, 0
	}
	at := after.FirstPublishedAt
	return &at, int64(after.PostID)
}

type liveRow struct {
	id, authorID             int64
	slug                     string
	firstPublishedAt         *time.Time
	number                   int32
	title, summary, coverURL string
	tags                     []string
	minutes                  int32
	publishedAt              time.Time
}

func (l liveRow) toLivePost() (app.LivePost, error) {
	d, err := details(l.title, l.summary, l.coverURL, l.tags)
	if err != nil {
		return app.LivePost{}, fmt.Errorf("post %d: %w", l.id, err)
	}
	return app.LivePost{PostID: id.ID(l.id), AuthorID: id.ID(l.authorID), Slug: l.slug, Number: int(l.number),
		Details: d, ReadingMinutes: int(l.minutes), FirstPublishedAt: timeOrZero(l.firstPublishedAt), PublishedAt: l.publishedAt.UTC()}, nil
}

func (r public) ListLive(ctx context.Context, tag string, after *app.Cursor, limit int) ([]app.LivePost, error) {
	at, afterID := cursorArgs(after)
	rows, err := r.q.ListLivePosts(ctx, sqlcgen.ListLivePostsParams{Tag: tag, AfterAt: at, AfterID: afterID,
		RowLimit: int32(limit)}) //nolint:gosec // bounded by MaxListLimit+1
	if err != nil {
		return nil, err
	}
	out := make([]app.LivePost, 0, len(rows))
	for _, row := range rows {
		lp, err := liveRow{row.ID, row.AuthorID, row.Slug, row.FirstPublishedAt, row.Number, row.Title, row.Summary,
			row.CoverUrl, row.Tags, row.ReadingTimeMinutes, row.PublishedAt}.toLivePost()
		if err != nil {
			return nil, err
		}
		out = append(out, lp)
	}
	return out, nil
}

func (r public) FindLive(ctx context.Context, postID id.ID) (app.LivePost, error) {
	row, err := r.q.GetLivePost(ctx, int64(postID))
	if err != nil {
		return app.LivePost{}, notFound(err)
	}
	return liveRow{row.ID, row.AuthorID, row.Slug, row.FirstPublishedAt, row.Number, row.Title, row.Summary,
		row.CoverUrl, row.Tags, row.ReadingTimeMinutes, row.PublishedAt}.toLivePost()
}

func (r public) ListTags(ctx context.Context) ([]app.TagCount, error) {
	rows, err := r.q.ListLiveTags(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]app.TagCount, 0, len(rows))
	for _, row := range rows {
		out = append(out, app.TagCount{Tag: row.Tag, Count: int(row.PostCount)})
	}
	return out, nil
}

func (r public) ListIndex(ctx context.Context, after *app.Cursor, limit int) ([]app.IndexEntry, error) {
	at, afterID := cursorArgs(after)
	rows, err := r.q.ListLiveIndex(ctx, sqlcgen.ListLiveIndexParams{AfterAt: at, AfterID: afterID,
		RowLimit: int32(limit)}) //nolint:gosec // bounded by MaxIndexLimit+1
	if err != nil {
		return nil, err
	}
	out := make([]app.IndexEntry, 0, len(rows))
	for _, row := range rows {
		out = append(out, app.IndexEntry{PostID: id.ID(row.ID), Slug: row.Slug,
			FirstPublishedAt: timeOrZero(row.FirstPublishedAt), PublishedAt: row.PublishedAt.UTC()})
	}
	return out, nil
}
