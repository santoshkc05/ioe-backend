package domain_test

import (
	"errors"
	"testing"

	"github.com/santoshkc2200/ioe-backend/internal/enrollment/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
)

var (
	owner   = auth.Principal{UserID: 100, Role: auth.RoleInstructor}
	admin   = auth.Principal{UserID: 1, Role: auth.RoleRootAdmin}
	student = auth.Principal{UserID: 200, Role: auth.RoleStudent}
	other   = auth.Principal{UserID: 300, Role: auth.RoleInstructor}

	freePub = domain.CourseFacts{Published: true, Free: true, OwnerID: 100}
	paidPub = domain.CourseFacts{Published: true, Free: false, OwnerID: 100}
	draft   = domain.CourseFacts{Published: false, Free: true, OwnerID: 100}
)

func TestAuthorizeEnroll(t *testing.T) {
	cases := []struct {
		name   string
		p      auth.Principal
		c      domain.CourseFacts
		target auth.Principal
		want   error
	}{
		{"self free published", student, freePub, student, nil},
		{"self paid published", student, paidPub, student, domain.ErrPaymentRequired},
		{"self draft", student, draft, student, domain.ErrCourseHidden},
		{"other user as student", student, freePub, other, domain.ErrForbidden},
		{"other user on draft as non-manager", other, draft, student, domain.ErrCourseHidden},
		{"owner enrolls student in paid", owner, paidPub, student, nil},
		{"admin enrolls student in paid", admin, paidPub, student, nil},
		{"owner enrolls self in paid", owner, paidPub, owner, nil},
		{"owner on draft", owner, draft, student, domain.ErrCourseNotPublished},
		{"admin on draft", admin, draft, student, domain.ErrCourseNotPublished},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := domain.AuthorizeEnroll(tc.p, tc.c, tc.target.UserID); !errors.Is(err, tc.want) || (tc.want == nil && err != nil) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestAuthorizeManage(t *testing.T) {
	cases := []struct {
		name string
		p    auth.Principal
		c    domain.CourseFacts
		want error
	}{
		{"owner", owner, paidPub, nil},
		{"owner draft", owner, draft, nil},
		{"admin", admin, draft, nil},
		{"stranger published", other, paidPub, domain.ErrForbidden},
		{"student published", student, freePub, domain.ErrForbidden},
		{"stranger draft", other, draft, domain.ErrCourseHidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := domain.AuthorizeManage(tc.p, tc.c); !errors.Is(err, tc.want) || (tc.want == nil && err != nil) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestAuthorizeListUser(t *testing.T) {
	if err := domain.AuthorizeListUser(student, student.UserID); err != nil {
		t.Fatalf("self: %v", err)
	}
	if err := domain.AuthorizeListUser(admin, student.UserID); err != nil {
		t.Fatalf("admin: %v", err)
	}
	if err := domain.AuthorizeListUser(owner, student.UserID); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("instructor: %v", err)
	}
}
