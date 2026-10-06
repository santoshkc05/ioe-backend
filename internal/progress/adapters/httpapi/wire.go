package httpapi

import (
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
	"github.com/santoshkc2200/ioe-backend/internal/progress/domain"
)

type recordRequest struct {
	State      string `json:"state"`
	PositionMs int64  `json:"position_ms"`
}

type lectureProgressWire struct {
	LectureID  id.ID     `json:"lecture_id"`
	State      string    `json:"state"`
	PositionMs int64     `json:"position_ms"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type courseProgressWire struct {
	CourseID            id.ID                 `json:"course_id"`
	UserID              id.ID                 `json:"user_id"`
	LastLectureID       *id.ID                `json:"last_lecture_id,omitempty"`
	CompletedLectureIDs []id.ID               `json:"completed_lecture_ids"`
	Lectures            []lectureProgressWire `json:"lectures"`
	UpdatedAt           *time.Time            `json:"updated_at,omitempty"`
}

type activityDayWire struct {
	Date         string `json:"date"`
	LectureCount int    `json:"lecture_count"`
}

type userProgressWire struct {
	UserID       id.ID                `json:"user_id"`
	Courses      []courseProgressWire `json:"courses"`
	ActivityDays []activityDayWire    `json:"activity_days"`
}

func toCourseWire(p domain.CourseProgress) courseProgressWire {
	w := courseProgressWire{
		CourseID: p.CourseID, UserID: p.UserID,
		CompletedLectureIDs: p.CompletedLectureIDs(),
		Lectures:            make([]lectureProgressWire, len(p.Lectures)),
	}
	if !p.LastLectureID.IsZero() {
		last := p.LastLectureID
		w.LastLectureID = &last
	}
	if !p.UpdatedAt.IsZero() {
		at := p.UpdatedAt
		w.UpdatedAt = &at
	}
	for i, l := range p.Lectures {
		w.Lectures[i] = lectureProgressWire{LectureID: l.LectureID, State: string(l.State), PositionMs: l.PositionMs, UpdatedAt: l.UpdatedAt}
	}
	return w
}

func toUserWire(userID id.ID, courses []domain.CourseProgress, days []domain.ActivityDay) userProgressWire {
	w := userProgressWire{UserID: userID, Courses: make([]courseProgressWire, len(courses)), ActivityDays: make([]activityDayWire, len(days))}
	for i, c := range courses {
		w.Courses[i] = toCourseWire(c)
	}
	for i, d := range days {
		w.ActivityDays[i] = activityDayWire{Date: d.Date.UTC().Format(time.DateOnly), LectureCount: d.LectureCount}
	}
	return w
}
