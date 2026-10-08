package domain_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/certificate/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

func idOf(n int64) id.ID { return id.ID(n) }

func TestNewPolicy(t *testing.T) {
	cases := []struct {
		name string
		mode domain.Mode
		exam int64
		want error
	}{
		{"off", domain.ModeOff, 0, nil},
		{"completion", domain.ModeCompletion, 0, nil},
		{"completion and exam", domain.ModeCompletionAndExam, 700, nil},
		{"unknown mode", domain.Mode("always"), 0, domain.ErrInvalidMode},
		{"empty mode", domain.Mode(""), 0, domain.ErrInvalidMode},
		{"exam mode without exam", domain.ModeCompletionAndExam, 0, domain.ErrExamRequired},
		{"completion with exam", domain.ModeCompletion, 700, domain.ErrExamNotAllowed},
		{"off with exam", domain.ModeOff, 700, domain.ErrExamNotAllowed},
	}
	for _, c := range cases {
		p, err := domain.NewPolicy(10, c.mode, idOf(c.exam))
		if !errors.Is(err, c.want) {
			t.Errorf("%s: error = %v, want %v", c.name, err, c.want)
			continue
		}
		if err == nil && (p.CourseID != 10 || p.Mode != c.mode || p.ExamID != idOf(c.exam)) {
			t.Errorf("%s: policy = %+v", c.name, p)
		}
	}
}

func TestCodes(t *testing.T) {
	a, b := domain.NewCode(), domain.NewCode()
	if a == b {
		t.Fatal("two codes are equal")
	}
	if len(a) != domain.CodeLen || !domain.ValidCode(a) {
		t.Fatalf("generated code %q is not valid", a)
	}
	for _, bad := range []string{"", a[:25], a + "A", strings.ToLower(a), a[:25] + "1", a[:25] + "=", a[:25] + " ", "../../etc/passwd"} {
		if domain.ValidCode(bad) {
			t.Errorf("ValidCode(%q) = true", bad)
		}
	}
}

func TestNewCertificateIsValidUntilRevoked(t *testing.T) {
	now := time.Date(2026, 10, 8, 9, 0, 0, 0, time.FixedZone("NPT", 5*3600+45*60))
	c := domain.NewCertificate(1, domain.NewCode(), 200, 10, "Asha Rai", "Go", now)
	if c.Revoked() || !c.RevokedAt.IsZero() {
		t.Fatalf("new certificate is revoked: %+v", c)
	}
	if !c.IssuedAt.Equal(now) || c.IssuedAt.Location() != time.UTC {
		t.Fatalf("IssuedAt = %v, want %v in UTC", c.IssuedAt, now)
	}
	c.RevokedAt = now
	if !c.Revoked() {
		t.Fatal("certificate with RevokedAt is not revoked")
	}
}
