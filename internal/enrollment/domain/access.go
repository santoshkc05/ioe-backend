package domain

import (
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

// CourseFacts is what enrollment knows about a course, supplied by the application layer.
type CourseFacts struct {
	Published bool
	Free      bool
	OwnerID   id.ID
}

// IsManagedBy reports whether p owns the course or is a root admin.
func (c CourseFacts) IsManagedBy(p auth.Principal) bool {
	return p.Role == auth.RoleRootAdmin || p.UserID == c.OwnerID
}

// AuthorizeEnroll decides whether p may enroll userID. Non-managers never learn that an
// unpublished course exists.
func AuthorizeEnroll(p auth.Principal, c CourseFacts, userID id.ID) error {
	if c.IsManagedBy(p) {
		if !c.Published {
			return ErrCourseNotPublished
		}
		return nil
	}
	if !c.Published {
		return ErrCourseHidden
	}
	if p.UserID != userID {
		return ErrForbidden
	}
	if !c.Free {
		return ErrPaymentRequired
	}
	return nil
}

// AuthorizeManage decides whether p may cancel another user's enrollment or list the roster.
func AuthorizeManage(p auth.Principal, c CourseFacts) error {
	if c.IsManagedBy(p) {
		return nil
	}
	if !c.Published {
		return ErrCourseHidden
	}
	return ErrForbidden
}

// AuthorizeListUser decides whether p may list userID's enrollments.
func AuthorizeListUser(p auth.Principal, userID id.ID) error {
	if p.UserID == userID || p.Role == auth.RoleRootAdmin {
		return nil
	}
	return ErrForbidden
}
