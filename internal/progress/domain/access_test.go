package domain_test

import (
	"errors"
	"testing"

	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
	"github.com/santoshkc2200/ioe-backend/internal/progress/domain"
)

var (
	owner   = auth.Principal{UserID: 100, Role: auth.RoleInstructor}
	admin   = auth.Principal{UserID: 1, Role: auth.RoleRootAdmin}
	student = auth.Principal{UserID: 200, Role: auth.RoleStudent}
	other   = auth.Principal{UserID: 300, Role: auth.RoleInstructor}

	published = domain.CourseFacts{Published: true, OwnerID: 100, LectureIDs: []id.ID{50, 51}}
	hidden    = domain.CourseFacts{Published: false, OwnerID: 100, LectureIDs: []id.ID{50, 51}}
)

func check(t *testing.T, err, want error) {
	t.Helper()
	if (want == nil && err != nil) || !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
}

func TestAuthorizeRecord(t *testing.T) {
	cases := []struct {
		name    string
		p       auth.Principal
		c       domain.CourseFacts
		lecture id.ID
		target  id.ID
		want    error
	}{
		{"self published lecture in course", student, published, 51, student.UserID, nil},
		{"self lecture not in course", student, published, 99, student.UserID, domain.ErrLectureNotFound},
		{"self unpublished", student, hidden, 50, student.UserID, domain.ErrCourseHidden},
		{"student for another user", student, published, 50, other.UserID, domain.ErrForbidden},
		{"owner for a student", owner, published, 50, student.UserID, domain.ErrForbidden},
		{"admin for a student", admin, published, 50, student.UserID, domain.ErrForbidden},
		{"owner for self on unpublished", owner, hidden, 50, owner.UserID, domain.ErrCourseHidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			check(t, domain.AuthorizeRecord(tc.p, tc.c, tc.lecture, tc.target), tc.want)
		})
	}
}

func TestAuthorizeReadCourse(t *testing.T) {
	cases := []struct {
		name   string
		p      auth.Principal
		c      domain.CourseFacts
		target id.ID
		want   error
	}{
		{"self published", student, published, student.UserID, nil},
		{"self unpublished", student, hidden, student.UserID, nil},
		{"owner reads student", owner, published, student.UserID, nil},
		{"owner reads student on unpublished", owner, hidden, student.UserID, nil},
		{"admin reads student", admin, hidden, student.UserID, nil},
		{"stranger on published", other, published, student.UserID, domain.ErrForbidden},
		{"stranger on unpublished", other, hidden, student.UserID, domain.ErrCourseHidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			check(t, domain.AuthorizeReadCourse(tc.p, tc.c, tc.target), tc.want)
		})
	}
}

func TestAuthorizeReadUser(t *testing.T) {
	check(t, domain.AuthorizeReadUser(student, student.UserID), nil)
	check(t, domain.AuthorizeReadUser(admin, student.UserID), nil)
	check(t, domain.AuthorizeReadUser(owner, student.UserID), domain.ErrForbidden)
	check(t, domain.AuthorizeReadUser(other, student.UserID), domain.ErrForbidden)
}
