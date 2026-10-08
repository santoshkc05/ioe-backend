package postgres

import (
	"context"

	"github.com/santoshkc2200/ioe-backend/internal/blog/adapters/postgres/sqlcgen"
	"github.com/santoshkc2200/ioe-backend/internal/blog/app"
	"github.com/santoshkc2200/ioe-backend/internal/blog/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/clock"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

type slugs struct {
	q     *sqlcgen.Queries
	clock clock.Clock
}

// Reserve inserts with ON CONFLICT DO NOTHING, then reads the owner, so a taken slug never
// aborts the transaction and the caller can try the next suffix. A concurrent reservation
// of the same slug waits on the primary key and then sees the winner.
func (r slugs) Reserve(ctx context.Context, s domain.Slug, postID id.ID) error {
	if err := r.q.ReservePostSlug(ctx, sqlcgen.ReservePostSlugParams{Slug: s.String(), PostID: int64(postID), CreatedAt: r.clock.Now()}); err != nil {
		return err
	}
	owner, err := r.q.GetPostSlugOwner(ctx, s.String())
	if err != nil {
		return err
	}
	if id.ID(owner) != postID {
		return app.ErrSlugTaken
	}
	return nil
}

func (r slugs) Resolve(ctx context.Context, slug string) (id.ID, error) {
	owner, err := r.q.GetPostSlugOwner(ctx, slug)
	if err != nil {
		return 0, notFound(err)
	}
	return id.ID(owner), nil
}
