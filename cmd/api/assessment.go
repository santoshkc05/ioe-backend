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

func (a assessmentCourseAccess) CanManageLecture(ctx context.Context, p auth.Principal, courseID, lectureID id.ID) error {
	return toAssessmentError(a.courses.CheckLectureManage(ctx, p, courseID, lectureID))
}

func (a assessmentCourseAccess) CanReadLecture(ctx context.Context, p auth.Principal, courseID, lectureID id.ID) error {
	return toAssessmentError(a.contents.CheckLectureRead(ctx, p, courseID, lectureID))
}

func (a assessmentCourseAccess) CanManageCourse(ctx context.Context, p auth.Principal, courseID id.ID) error {
	return toAssessmentError(a.courses.CheckManage(ctx, p, courseID))
}

func (a assessmentCourseAccess) CanReadAsManager(ctx context.Context, p auth.Principal, courseID id.ID) error {
	return toAssessmentError(a.courses.CheckManagerRead(ctx, p, courseID))
}

func (a assessmentCourseAccess) CanReadCourse(ctx context.Context, p auth.Principal, courseID id.ID) error {
	_, err := a.courses.Get(ctx, p, courseID)
	return toAssessmentError(err)
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
