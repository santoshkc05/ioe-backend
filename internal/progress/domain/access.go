package domain

import (
	"slices"

	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

// CourseFacts is what progress knows about a course, supplied by the application layer.
type CourseFacts struct {
	Published  bool
	OwnerID    id.ID
	LectureIDs []id.ID
}

// IsManagedBy reports whether p owns the course or is a root admin.
func (c CourseFacts) IsManagedBy(p auth.Principal) bool {
	return p.Role == auth.RoleRootAdmin || p.UserID == c.OwnerID
}

// AuthorizeRecord decides whether p may record userID's progress on lectureID. Only the
// user themselves records, and only on a lecture of a published course.
func AuthorizeRecord(p auth.Principal, c CourseFacts, lectureID, userID id.ID) error {
	if p.UserID != userID {
		return ErrForbidden
	}
	if !c.Published {
		return ErrCourseHidden
	}
	if !slices.Contains(c.LectureIDs, lectureID) {
		return ErrLectureNotFound
	}
	return nil
}

// AuthorizeReadCourse decides whether p may read userID's progress in the course.
// Non-managers never learn that an unpublished course exists.
func AuthorizeReadCourse(p auth.Principal, c CourseFacts, userID id.ID) error {
	if p.UserID == userID || c.IsManagedBy(p) {
		return nil
	}
	if !c.Published {
		return ErrCourseHidden
	}
	return ErrForbidden
}

// AuthorizeReadUser decides whether p may read userID's progress across courses.
func AuthorizeReadUser(p auth.Principal, userID id.ID) error {
	if p.UserID == userID || p.Role == auth.RoleRootAdmin {
		return nil
	}
	return ErrForbidden
}
