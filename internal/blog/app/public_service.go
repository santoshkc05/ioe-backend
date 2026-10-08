package app

import (
	"context"
	"fmt"
	"slices"

	"github.com/santoshkc2200/ioe-backend/internal/blog/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/contentblocks"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

const (
	DefaultListLimit  = 20
	MaxListLimit      = 50
	DefaultIndexLimit = 100
	MaxIndexLimit     = 500
)

// PublicService serves live posts to anyone.
type PublicService struct {
	tx    TxRunner
	users UserDirectory
}

func NewPublicService(tx TxRunner, users UserDirectory) *PublicService {
	return &PublicService{tx: tx, users: users}
}

// Card is a live post with its author's display name.
type Card struct {
	LivePost
	AuthorName string
}

type CardPage struct {
	Items []Card
	Next  *Cursor // nil on the last page
}

// PublicPost is a live post with its blocks. RequestedSlug differs from Slug when the
// reader used an old slug; the client redirects to Slug.
type PublicPost struct {
	Card
	RequestedSlug string
	Blocks        []contentblocks.Block
}

type IndexPage struct {
	Items []IndexEntry
	Next  *Cursor
}

func pageLimit(limit, def, maxLimit int) (int, error) {
	if limit == 0 {
		return def, nil
	}
	if limit < 1 || limit > maxLimit {
		return 0, fmt.Errorf("%w: limit must be between 1 and %d", ErrInvalidInput, maxLimit)
	}
	return limit, nil
}

func (s *PublicService) List(ctx context.Context, tag string, after *Cursor, limit int) (CardPage, error) {
	limit, err := pageLimit(limit, DefaultListLimit, MaxListLimit)
	if err != nil {
		return CardPage{}, err
	}
	if tag != "" {
		if tag, err = domain.NewTag(tag); err != nil {
			return CardPage{}, fmt.Errorf("%w: %w", ErrInvalidInput, err)
		}
	}
	var rows []LivePost
	if err := s.tx.RunInTx(ctx, func(r Repos) error {
		var err error
		rows, err = r.Public.ListLive(ctx, tag, after, limit+1)
		return err
	}); err != nil {
		return CardPage{}, err
	}
	var page CardPage
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[limit-1]
		page.Next = &Cursor{FirstPublishedAt: last.FirstPublishedAt, PostID: last.PostID}
	}
	page.Items, err = s.cards(ctx, rows)
	return page, err
}

// cards looks author names up outside any transaction so a read never holds two pool
// connections.
func (s *PublicService) cards(ctx context.Context, rows []LivePost) ([]Card, error) {
	ids := make([]id.ID, 0, len(rows))
	for _, r := range rows {
		if !slices.Contains(ids, r.AuthorID) {
			ids = append(ids, r.AuthorID)
		}
	}
	names, err := s.users.Names(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]Card, len(rows))
	for i, r := range rows {
		out[i] = Card{LivePost: r, AuthorName: names[r.AuthorID]}
	}
	return out, nil
}

// GetBySlug resolves a current or former slug to the post's live version. Unknown, draft
// and archived posts are indistinguishable: all ErrNotFound.
func (s *PublicService) GetBySlug(ctx context.Context, raw string) (PublicPost, error) {
	slug, err := domain.NewSlug(raw)
	if err != nil {
		return PublicPost{}, ErrNotFound
	}
	var out PublicPost
	err = s.tx.RunInTx(ctx, func(r Repos) error {
		postID, err := r.Slugs.Resolve(ctx, slug.String())
		if err != nil {
			return err
		}
		live, err := r.Public.FindLive(ctx, postID)
		if err != nil {
			return err
		}
		blocks, err := r.Contents.ListVersionBlocks(ctx, postID, live.Number)
		out = PublicPost{Card: Card{LivePost: live}, RequestedSlug: slug.String(), Blocks: blocks}
		return err
	})
	if err != nil {
		return PublicPost{}, err
	}
	cards, err := s.cards(ctx, []LivePost{out.LivePost})
	if err != nil {
		return PublicPost{}, err
	}
	out.Card = cards[0]
	return out, nil
}

func (s *PublicService) Tags(ctx context.Context) ([]TagCount, error) {
	var out []TagCount
	err := s.tx.RunInTx(ctx, func(r Repos) error {
		var err error
		out, err = r.Public.ListTags(ctx)
		return err
	})
	return out, err
}

func (s *PublicService) Index(ctx context.Context, after *Cursor, limit int) (IndexPage, error) {
	limit, err := pageLimit(limit, DefaultIndexLimit, MaxIndexLimit)
	if err != nil {
		return IndexPage{}, err
	}
	var page IndexPage
	err = s.tx.RunInTx(ctx, func(r Repos) error {
		rows, err := r.Public.ListIndex(ctx, after, limit+1)
		if err != nil {
			return err
		}
		if len(rows) > limit {
			rows = rows[:limit]
			last := rows[limit-1]
			page.Next = &Cursor{FirstPublishedAt: last.FirstPublishedAt, PostID: last.PostID}
		}
		page.Items = rows
		return nil
	})
	return page, err
}
