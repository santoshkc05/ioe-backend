package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/blog/adapters/postgres/sqlcgen"
	"github.com/santoshkc2200/ioe-backend/internal/blog/app"
	"github.com/santoshkc2200/ioe-backend/internal/blog/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/contentblocks"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

type posts struct{ q *sqlcgen.Queries }

func (r posts) FindByID(ctx context.Context, postID id.ID) (domain.Post, error) {
	ps, err := r.load(ctx, []int64{int64(postID)})
	if err != nil {
		return domain.Post{}, err
	}
	if len(ps) == 0 {
		return domain.Post{}, app.ErrNotFound
	}
	return ps[0], nil
}

func (r posts) ListByAuthor(ctx context.Context, authorID id.ID) ([]domain.Post, error) {
	ids, err := r.q.ListPostIDsByAuthor(ctx, int64(authorID))
	if err != nil {
		return nil, err
	}
	return r.load(ctx, ids) // both queries order by id DESC
}

func (r posts) load(ctx context.Context, ids []int64) ([]domain.Post, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := r.q.ListPostsByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Post, 0, len(rows))
	for _, row := range rows {
		d, err := details(row.Title, row.Summary, row.CoverUrl, row.Tags)
		if err != nil {
			return nil, fmt.Errorf("post %d: %w", row.ID, err)
		}
		slug, err := domain.NewSlug(row.Slug)
		if err != nil {
			return nil, fmt.Errorf("post %d slug: %w", row.ID, err)
		}
		p := domain.Post{ID: id.ID(row.ID), AuthorID: id.ID(row.AuthorID), Details: d, Slug: slug,
			Status: domain.Status(row.Status), LastVersion: int(row.LastVersion),
			FirstPublishedAt: timeOrZero(row.FirstPublishedAt), Version: row.Version,
			CreatedAt: row.CreatedAt.UTC(), UpdatedAt: row.UpdatedAt.UTC()}
		if row.LiveVersion != nil {
			p.Live = domain.LiveVersion{Number: int(*row.LiveVersion), PublishedAt: timeOrZero(row.LivePublishedAt)}
		}
		out = append(out, p)
	}
	return out, nil
}

// details rebuilds stored details without re-validating them: stored rows passed
// validation when written, and a later rule change must not make them unreadable.
func details(title, summary, coverURL string, tags []string) (domain.Details, error) {
	t, err := contentblocks.NewTitle(title)
	if err != nil {
		return domain.Details{}, err
	}
	if tags == nil {
		tags = []string{}
	}
	return domain.Details{Title: t, Summary: summary, CoverURL: coverURL, Tags: tags}, nil
}

func (r posts) Insert(ctx context.Context, p *domain.Post) error {
	if err := r.q.InsertPost(ctx, sqlcgen.InsertPostParams{
		ID: int64(p.ID), AuthorID: int64(p.AuthorID), Title: p.Details.Title.String(), Summary: p.Details.Summary,
		CoverUrl: p.Details.CoverURL, Tags: nonNil(p.Details.Tags), Slug: p.Slug.String(), Status: string(p.Status),
		CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
	}); err != nil {
		return err
	}
	p.Version = 1
	return nil
}

func (r posts) Update(ctx context.Context, p *domain.Post) error {
	n, err := r.q.UpdatePost(ctx, sqlcgen.UpdatePostParams{
		ID: int64(p.ID), Version: p.Version, Title: p.Details.Title.String(), Summary: p.Details.Summary,
		CoverUrl: p.Details.CoverURL, Tags: nonNil(p.Details.Tags), Slug: p.Slug.String(), Status: string(p.Status),
		LastVersion: int32(p.LastVersion), //nolint:gosec // one per publish, far below int32
		LiveVersion: optionalNumber(p.Live.Number), FirstPublishedAt: optionalTime(p.FirstPublishedAt),
		UpdatedAt: p.UpdatedAt,
	})
	if err != nil {
		return err
	}
	if n == 0 {
		return app.ErrConcurrentModification
	}
	p.Version++
	return nil
}

func (r posts) InsertVersion(ctx context.Context, p *domain.Post, minutes int, publishedBy id.ID) error {
	pid, number := int64(p.ID), int32(p.Live.Number) //nolint:gosec // one per publish
	if err := r.q.InsertPostVersion(ctx, sqlcgen.InsertPostVersionParams{
		PostID: pid, Number: number, Title: p.Details.Title.String(), Summary: p.Details.Summary,
		CoverUrl: p.Details.CoverURL, Tags: nonNil(p.Details.Tags),
		ReadingTimeMinutes: int32(minutes), //nolint:gosec // minutes of reading, tiny
		PublishedBy:        int64(publishedBy), PublishedAt: p.Live.PublishedAt,
	}); err != nil {
		return err
	}
	return r.q.CopyPostVersionBlocks(ctx, sqlcgen.CopyPostVersionBlocksParams{PostID: pid, Number: number})
}

func (r posts) FindVersion(ctx context.Context, postID id.ID, number int) (app.VersionView, error) {
	if number < 1 {
		return app.VersionView{}, app.ErrNotFound
	}
	row, err := r.q.GetPostVersion(ctx, sqlcgen.GetPostVersionParams{PostID: int64(postID), Number: int32(number)}) //nolint:gosec // checked by caller range
	if err != nil {
		return app.VersionView{}, notFound(err)
	}
	d, err := details(row.Title, row.Summary, row.CoverUrl, row.Tags)
	if err != nil {
		return app.VersionView{}, fmt.Errorf("post %d version %d: %w", postID, number, err)
	}
	return app.VersionView{Number: int(row.Number), Details: d, ReadingMinutes: int(row.ReadingTimeMinutes),
		PublishedBy: id.ID(row.PublishedBy), PublishedAt: row.PublishedAt.UTC()}, nil
}

func (r posts) ListVersions(ctx context.Context, postID id.ID) ([]app.VersionSummary, error) {
	rows, err := r.q.ListPostVersions(ctx, int64(postID))
	if err != nil {
		return nil, err
	}
	out := make([]app.VersionSummary, 0, len(rows))
	for _, row := range rows {
		out = append(out, app.VersionSummary{Number: int(row.Number), PublishedBy: id.ID(row.PublishedBy), PublishedAt: row.PublishedAt.UTC()})
	}
	return out, nil
}

func nonNil(tags []string) []string {
	if tags == nil {
		return []string{}
	}
	return tags
}

func optionalNumber(n int) *int32 {
	if n == 0 {
		return nil
	}
	v := int32(n) //nolint:gosec // one per publish
	return &v
}

func optionalTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func timeOrZero(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return t.UTC()
}
