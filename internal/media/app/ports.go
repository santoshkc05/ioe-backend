// Package app contains the media use cases and the ports they depend on.
package app

import (
	"context"

	"github.com/santoshkc2200/ioe-backend/internal/media/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

// AssetRepository stores asset ownership. Each call is its own statement.
type AssetRepository interface {
	Insert(ctx context.Context, a domain.Asset) error
	// Find returns ErrNotFound when no row exists.
	Find(ctx context.Context, assetID id.ID) (domain.Asset, error)
	// Delete is a no-op when no row exists.
	Delete(ctx context.Context, assetID id.ID) error
	// KindsInCourse returns the kinds of the given IDs that belong to courseID.
	KindsInCourse(ctx context.Context, courseID id.ID, ids []id.ID) (map[id.ID]domain.Kind, error)
}

// CourseAccess is backed by courseauthoring.
type CourseAccess interface {
	// CanManage returns nil when p owns the course or is a root admin and the course is not
	// archived; ErrNotFound when the course is not visible to p; ErrForbidden or
	// ErrCourseNotEditable otherwise.
	CanManage(ctx context.Context, p auth.Principal, courseID id.ID) error
	// CanReadLectureAsset applies the lecture content gate (ErrNotFound,
	// ErrEnrollmentRequired) and returns ErrNotFound when the lecture's blocks do not
	// reference assetID.
	CanReadLectureAsset(ctx context.Context, p auth.Principal, courseID, lectureID, assetID id.ID) error
}
