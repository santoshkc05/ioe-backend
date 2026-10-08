// Package app contains the course authoring use cases and the ports they depend on.
package app

import (
	"context"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/contentblocks"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

// CourseRepository stores the Course aggregate. FindByID returns ErrNotFound;
// Update returns ErrConcurrentModification when c.Version is stale and increments
// c.Version on success. Insert sets c.Version to 1. ListPublished returns up to q.Limit
// published courses matching the filters and, when q.Q is set, the search text, from their
// live versions. Without q.Q they are ordered by ID descending and start below q.After; with
// q.Q they are ordered by rank then ID, descending, and start below (q.AfterRank, q.After).
// Any start when q.After is zero. ListInReview returns in_review courses, longest waiting
// first. LockForUpdate locks the course row until the transaction ends; it returns ErrNotFound
// when the course does not exist. ListReviews returns a course's review trail oldest first.
//
// FindVersion returns the course as published version number: details, sections and
// lectures from the snapshot, identity, Live and CreatedAt from the course, UpdatedAt the
// publish time, Status published. It returns ErrNotFound when the version does not exist.
// ListVersions returns a course's versions, newest first. InsertVersion snapshots the
// stored working copy (sections, lectures and blocks) and c's details as version
// c.Live.Number.
type CourseRepository interface {
	FindByID(ctx context.Context, courseID id.ID) (domain.Course, error)
	FindVersion(ctx context.Context, courseID id.ID, number int) (domain.Course, error)
	ListVersions(ctx context.Context, courseID id.ID) ([]VersionSummary, error)
	ListByOwner(ctx context.Context, ownerID id.ID) ([]domain.Course, error)
	ListPublished(ctx context.Context, q CatalogQuery) ([]CourseSummary, error)
	ListInReview(ctx context.Context) ([]domain.Course, error)
	LockForUpdate(ctx context.Context, courseID id.ID) error
	InsertVersion(ctx context.Context, c *domain.Course, publishedBy id.ID) error
	Insert(ctx context.Context, c *domain.Course) error
	Update(ctx context.Context, c *domain.Course) error
	InsertReview(ctx context.Context, r domain.Review) error
	ListReviews(ctx context.Context, courseID id.ID) ([]domain.Review, error)
	// ReplaceSubmittedPins replaces the pins captured at the course's last submit.
	ReplaceSubmittedPins(ctx context.Context, courseID id.ID, pins []domain.AssessmentPin) error
	ListSubmittedPins(ctx context.Context, courseID id.ID) ([]domain.AssessmentPin, error)
	InsertVersionPins(ctx context.Context, courseID id.ID, number int, pins []domain.AssessmentPin) error
	// ListVersionPins returns version number's pins, by kind then ID; empty when none.
	ListVersionPins(ctx context.Context, courseID id.ID, number int) ([]domain.AssessmentPin, error)
}

// VersionSummary describes one published version without its content.
type VersionSummary struct {
	Number      int
	PublishedBy id.ID
	PublishedAt time.Time
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
// when the lecture does not exist in courseID. FindVersionLecture and ListVersionBlocks read
// published version number of courseID; the live header's ContentRevision is 0. ApplyPatch returns
// ErrConcurrentModification when baseRevision is stale.
type LectureContentRepository interface {
	FindLecture(ctx context.Context, courseID, lectureID id.ID) (LectureHeader, error)
	FindLectureForUpdate(ctx context.Context, courseID, lectureID id.ID) (LectureHeader, error)
	ListBlocks(ctx context.Context, lectureID id.ID) ([]contentblocks.Block, error)
	FindVersionLecture(ctx context.Context, courseID id.ID, number int, lectureID id.ID) (LectureHeader, error)
	ListVersionBlocks(ctx context.Context, courseID id.ID, number int, lectureID id.ID) ([]contentblocks.Block, error)
	ApplyPatch(ctx context.Context, courseID, lectureID id.ID, baseRevision int64, plan BlockWritePlan) (int64, error)
	ReplaceBlocks(ctx context.Context, courseID, lectureID id.ID, blocks []contentblocks.Block) (int64, error)
}

// EventPublisher records events in the current transaction.
type EventPublisher interface {
	Publish(ctx context.Context, events ...domain.Event) error
}

// CategoryRepository stores categories. Insert and Update return ErrCategoryExists when the
// name (ignoring case) or the slug is taken. Update, Delete and Get return ErrNotFound for an
// unknown category. Delete also removes the category from every course and published version.
// List orders by name and counts, per category, the courses whose live version has it.
// ExistAll reports whether every ID names a category; it is true for no IDs.
type CategoryRepository interface {
	Insert(ctx context.Context, c domain.Category) error
	Update(ctx context.Context, c domain.Category) error
	Delete(ctx context.Context, categoryID id.ID) error
	Get(ctx context.Context, categoryID id.ID) (domain.Category, error)
	List(ctx context.Context) ([]CategoryWithCount, error)
	ExistAll(ctx context.Context, ids []id.ID) (bool, error)
}

// CategoryWithCount is a category and how many live courses are filed under it.
type CategoryWithCount struct {
	domain.Category
	CourseCount int
}

// Repos are bound to one transaction.
type Repos struct {
	Courses    CourseRepository
	Contents   LectureContentRepository
	Events     EventPublisher
	Categories CategoryRepository
}

// TxRunner commits when fn returns nil and rolls back otherwise.
type TxRunner interface {
	RunInTx(ctx context.Context, fn func(Repos) error) error
}

// EnrollmentQuery reports whether a user may read a course's non-preview lectures.
type EnrollmentQuery interface {
	IsActivelyEnrolled(ctx context.Context, courseID, userID id.ID) (bool, error)
}
