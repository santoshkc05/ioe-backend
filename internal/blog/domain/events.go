package domain

import (
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

// Event is a domain event written to the outbox under its EventName.
type Event interface {
	EventName() string
}

type PostPublished struct {
	PostID     id.ID     `json:"post_id"`
	AuthorID   id.ID     `json:"author_id"`
	Version    int       `json:"version"`
	Slug       string    `json:"slug"`
	OccurredAt time.Time `json:"occurred_at"`
}

func (PostPublished) EventName() string { return "blog.post.published" }

// PostUnpublished is raised when a live post is unpublished or archived.
type PostUnpublished struct {
	PostID     id.ID     `json:"post_id"`
	ActorID    id.ID     `json:"actor_id"`
	OccurredAt time.Time `json:"occurred_at"`
}

func (PostUnpublished) EventName() string { return "blog.post.unpublished" }
