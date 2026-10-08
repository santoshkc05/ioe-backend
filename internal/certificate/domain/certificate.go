package domain

import (
	"crypto/rand"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

// Mode is what earns a student a certificate in a course.
type Mode string

const (
	ModeOff               Mode = "off"
	ModeCompletion        Mode = "completion"
	ModeCompletionAndExam Mode = "completion_and_exam"
)

func (m Mode) Valid() bool {
	return m == ModeOff || m == ModeCompletion || m == ModeCompletionAndExam
}

// Policy is one course's certificate rule. ExamID is the zero ID unless Mode is
// ModeCompletionAndExam.
type Policy struct {
	CourseID id.ID
	Mode     Mode
	ExamID   id.ID
}

func NewPolicy(courseID id.ID, mode Mode, examID id.ID) (Policy, error) {
	if !mode.Valid() {
		return Policy{}, ErrInvalidMode
	}
	if mode == ModeCompletionAndExam && examID.IsZero() {
		return Policy{}, ErrExamRequired
	}
	if mode != ModeCompletionAndExam && !examID.IsZero() {
		return Policy{}, ErrExamNotAllowed
	}
	return Policy{CourseID: courseID, Mode: mode, ExamID: examID}, nil
}

// Certificate is one user's certificate for one course. StudentName and CourseTitle are
// snapshots taken at issue time. RevokedAt is the zero time while the certificate is valid.
type Certificate struct {
	ID          id.ID
	Code        string
	UserID      id.ID
	CourseID    id.ID
	StudentName string
	CourseTitle string
	IssuedAt    time.Time
	RevokedAt   time.Time
}

func NewCertificate(certID id.ID, code string, userID, courseID id.ID, studentName, courseTitle string, now time.Time) Certificate {
	return Certificate{
		ID: certID, Code: code, UserID: userID, CourseID: courseID,
		StudentName: studentName, CourseTitle: courseTitle, IssuedAt: now.UTC(),
	}
}

func (c Certificate) Revoked() bool { return !c.RevokedAt.IsZero() }

// CodeLen is the length of a certificate code: 26 base32 characters, 130 bits.
const CodeLen = 26

// NewCode returns an unguessable URL-safe code.
func NewCode() string { return rand.Text() }

// ValidCode reports whether s has the shape NewCode produces. Callers use it to reject
// malformed lookups before touching storage.
func ValidCode(s string) bool {
	if len(s) != CodeLen {
		return false
	}
	for i := range len(s) {
		c := s[i]
		if (c < 'A' || c > 'Z') && (c < '2' || c > '7') {
			return false
		}
	}
	return true
}
