// Package app contains the course authoring use cases and the ports they depend on.
package app

import (
	"context"

	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/contentblocks"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

// CourseRepository stores the Course aggregate. FindByID returns ErrNotFound;
// Update returns ErrConcurrentModification when c.Version is stale and increments
// c.Version on success. Insert sets c.Version to 1.
type CourseRepository interface {
	FindByID(ctx context.Context, courseID id.ID) (domain.Course, error)
	ListByOwner(ctx context.Context, ownerID id.ID) ([]domain.Course, error)
	Insert(ctx context.Context, c *domain.Course) error
	Update(ctx context.Context, c *domain.Course) error
}

// LectureHeader is a lecture's identity and content revision without its blocks.
type LectureHeader struct {
	LectureID       id.ID
	CourseID        id.ID
	Title           string
	FreePreview     bool
	ContentRevision int64
}

// BlockWritePlan is a validated content diff. Order is the full desired order of client block IDs.
type BlockWritePlan struct {
	Upserts []contentblocks.Block
	Deletes []string
	Order   []string
}

// LectureContentRepository reads and writes one lecture's blocks. Find* return ErrNotFound
// when the lecture does not exist in courseID. ApplyPatch returns ErrConcurrentModification
// when baseRevision is stale.
type LectureContentRepository interface {
	FindLecture(ctx context.Context, courseID, lectureID id.ID) (LectureHeader, error)
	FindLectureForUpdate(ctx context.Context, courseID, lectureID id.ID) (LectureHeader, error)
	ListBlocks(ctx context.Context, lectureID id.ID) ([]contentblocks.Block, error)
	ApplyPatch(ctx context.Context, courseID, lectureID id.ID, baseRevision int64, plan BlockWritePlan) (int64, error)
	ReplaceBlocks(ctx context.Context, courseID, lectureID id.ID, blocks []contentblocks.Block) (int64, error)
}

// EventPublisher records events in the current transaction.
type EventPublisher interface {
	Publish(ctx context.Context, events ...domain.Event) error
}

// Repos are bound to one transaction.
type Repos struct {
	Courses  CourseRepository
	Contents LectureContentRepository
	Events   EventPublisher
}

// TxRunner commits when fn returns nil and rolls back otherwise.
type TxRunner interface {
	RunInTx(ctx context.Context, fn func(Repos) error) error
}

// EnrollmentQuery reports whether a user may read a course's non-preview lectures.
type EnrollmentQuery interface {
	IsActivelyEnrolled(ctx context.Context, courseID, userID id.ID) (bool, error)
}

// LectureContentView is a lecture's full content.
//
// TEMPORARY: Task 8 moves this to content_service.go; it lives here so the
// package compiles until then.
type LectureContentView struct {
	LectureID       id.ID
	CourseID        id.ID
	Title           string
	FreePreview     bool
	ContentRevision int64
	Blocks          []contentblocks.Block
}
