package httpapi

import (
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/certificate/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

type policyRequest struct {
	Mode   string `json:"mode"`
	ExamID *id.ID `json:"exam_id"`
}

type policyWire struct {
	Mode   string `json:"mode"`
	ExamID *id.ID `json:"exam_id,omitempty"`
}

// certificateWire is a certificate as its owner sees it.
type certificateWire struct {
	ID          id.ID     `json:"id"`
	Code        string    `json:"code"`
	CourseID    id.ID     `json:"course_id"`
	CourseTitle string    `json:"course_title"`
	StudentName string    `json:"student_name"`
	IssuedAt    time.Time `json:"issued_at"`
	Status      string    `json:"status"`
}

type certificateListWire struct {
	Items []certificateWire `json:"items"`
}

// verifyWire is what anyone holding a code sees. It carries no ids.
type verifyWire struct {
	Code        string    `json:"code"`
	StudentName string    `json:"student_name"`
	CourseTitle string    `json:"course_title"`
	IssuedAt    time.Time `json:"issued_at"`
	Status      string    `json:"status"`
}

func status(c domain.Certificate) string {
	if c.Revoked() {
		return "revoked"
	}
	return "valid"
}

func toPolicyWire(p domain.Policy) policyWire {
	w := policyWire{Mode: string(p.Mode)}
	if !p.ExamID.IsZero() {
		exam := p.ExamID
		w.ExamID = &exam
	}
	return w
}

func toCertificateWire(c domain.Certificate) certificateWire {
	return certificateWire{
		ID: c.ID, Code: c.Code, CourseID: c.CourseID, CourseTitle: c.CourseTitle,
		StudentName: c.StudentName, IssuedAt: c.IssuedAt, Status: status(c),
	}
}

func toListWire(cs []domain.Certificate) certificateListWire {
	w := certificateListWire{Items: make([]certificateWire, len(cs))}
	for i, c := range cs {
		w.Items[i] = toCertificateWire(c)
	}
	return w
}

func toVerifyWire(c domain.Certificate) verifyWire {
	return verifyWire{Code: c.Code, StudentName: c.StudentName, CourseTitle: c.CourseTitle, IssuedAt: c.IssuedAt, Status: status(c)}
}
