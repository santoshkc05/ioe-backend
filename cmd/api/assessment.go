package main

import (
	"context"
	"errors"
	"log/slog"

	assessmenthttp "github.com/santoshkc2200/ioe-backend/internal/assessment/adapters/httpapi"
	assessmentpg "github.com/santoshkc2200/ioe-backend/internal/assessment/adapters/postgres"
	assessmentapp "github.com/santoshkc2200/ioe-backend/internal/assessment/app"
	courseauthoringapp "github.com/santoshkc2200/ioe-backend/internal/courseauthoring/app"
	courseauthoringdomain "github.com/santoshkc2200/ioe-backend/internal/courseauthoring/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/clock"
	"github.com/santoshkc2200/ioe-backend/internal/platform/httpserver"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

// assessmentCourseAccess lets assessment ask course authoring who may manage or read a lecture.
type assessmentCourseAccess struct {
	courses  *courseauthoringapp.CourseService
	contents *courseauthoringapp.ContentService
}

func (a assessmentCourseAccess) BeginEdit(ctx context.Context, p auth.Principal, courseID, lectureID id.ID) error {
	return toAssessmentError(a.courses.BeginAssessmentEdit(ctx, p, courseID, lectureID))
}

func (a assessmentCourseAccess) CanReadLecture(ctx context.Context, p auth.Principal, courseID, lectureID id.ID) error {
	return toAssessmentError(a.contents.CheckLectureRead(ctx, p, courseID, lectureID))
}

func (a assessmentCourseAccess) CanReadAsManager(ctx context.Context, p auth.Principal, courseID id.ID) error {
	return toAssessmentError(a.courses.CheckManagerRead(ctx, p, courseID))
}

func (a assessmentCourseAccess) CanReadCourse(ctx context.Context, p auth.Principal, courseID id.ID) error {
	_, err := a.courses.Get(ctx, p, courseID)
	return toAssessmentError(err)
}

func (a assessmentCourseAccess) LivePins(ctx context.Context, courseID id.ID) (assessmentapp.Pins, bool, error) {
	pins, live, err := a.courses.LivePins(ctx, courseID)
	return toAssessmentPins(pins), live, toAssessmentError(err)
}

func (a assessmentCourseAccess) VersionPins(ctx context.Context, p auth.Principal, courseID id.ID, number int) (assessmentapp.Pins, error) {
	pins, err := a.courses.VersionPins(ctx, p, courseID, number)
	return toAssessmentPins(pins), toAssessmentError(err)
}

func toAssessmentPins(pins []courseauthoringdomain.AssessmentPin) assessmentapp.Pins {
	out := make(assessmentapp.Pins, len(pins))
	for _, p := range pins {
		out[assessmentapp.Ref{Kind: assessmentapp.Kind(p.Kind), ID: p.ID}] = p.Revision
	}
	return out
}

func toAssessmentError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, courseauthoringapp.ErrNotFound):
		return assessmentapp.ErrNotFound
	case errors.Is(err, courseauthoringapp.ErrForbidden):
		return assessmentapp.ErrForbidden
	case errors.Is(err, courseauthoringapp.ErrEnrollmentRequired):
		return assessmentapp.ErrEnrollmentRequired
	case errors.Is(err, courseauthoringdomain.ErrCourseNotEditable):
		return assessmentapp.ErrCourseNotEditable
	}
	return err
}

func registerAssessment(r *httpserver.Router, tx *assessmentpg.TxRunner, courses *courseauthoringapp.CourseService, contents *courseauthoringapp.ContentService, enrollments assessmentapp.EnrollmentQuery, ids *id.Generator, clk clock.Clock, requireAuth httpserver.Middleware, logger *slog.Logger) {
	access := assessmentCourseAccess{courses: courses, contents: contents}
	quizzes := assessmentapp.NewQuizService(tx, access, enrollments, ids, clk)
	exams := assessmentapp.NewExamService(tx, access, enrollments, ids, clk)
	assessmenthttp.New(quizzes, exams, assessmenthttp.Config{RequireAuth: requireAuth, Logger: logger}).Register(r)
}

// assessmentHeads lets course authoring pin the current quiz and exam revisions.
type assessmentHeads struct {
	query *assessmentapp.AssessmentQuery
}

func (a assessmentHeads) Heads(ctx context.Context, courseID id.ID) ([]courseauthoringdomain.AssessmentPin, error) {
	heads, err := a.query.Heads(ctx, courseID)
	if err != nil {
		return nil, err
	}
	out := make([]courseauthoringdomain.AssessmentPin, len(heads))
	for i, h := range heads {
		out[i] = courseauthoringdomain.AssessmentPin{Kind: courseauthoringdomain.AssessmentKind(h.Ref.Kind), ID: h.Ref.ID, Revision: h.Revision}
	}
	return out, nil
}
