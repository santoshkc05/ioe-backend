package httpapi

import (
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/enrollment/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

type enrollmentWire struct {
	ID         id.ID      `json:"id"`
	CourseID   id.ID      `json:"course_id"`
	UserID     id.ID      `json:"user_id"`
	Status     string     `json:"status"`
	EnrolledAt time.Time  `json:"enrolled_at"`
	CanceledAt *time.Time `json:"canceled_at,omitempty"`
}

type enrollmentPageWire struct {
	Enrollments []enrollmentWire `json:"enrollments"`
	Total       int              `json:"total"`
}

type cancelRequest struct {
	Reason string `json:"reason"`
}

func toWire(e domain.Enrollment) enrollmentWire {
	w := enrollmentWire{ID: e.ID, CourseID: e.CourseID, UserID: e.UserID, Status: string(e.Status), EnrolledAt: e.EnrolledAt}
	if !e.CanceledAt.IsZero() {
		at := e.CanceledAt
		w.CanceledAt = &at
	}
	return w
}

func toWires(es []domain.Enrollment) []enrollmentWire {
	out := make([]enrollmentWire, len(es))
	for i, e := range es {
		out[i] = toWire(e)
	}
	return out
}
