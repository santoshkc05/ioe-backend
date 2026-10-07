package templates_test

import (
	"strings"
	"testing"

	"github.com/santoshkc2200/ioe-backend/internal/notification/adapters/templates"
	"github.com/santoshkc2200/ioe-backend/internal/notification/app"
)

var _ app.Renderer = (*templates.Renderer)(nil)

func render(t *testing.T, name string) (string, string, string) {
	t.Helper()
	r, err := templates.New()
	if err != nil {
		t.Fatal(err)
	}
	subject, text, html, err := r.Welcome(name)
	if err != nil {
		t.Fatal(err)
	}
	return subject, text, html
}

func TestWelcomeWithName(t *testing.T) {
	subject, text, html := render(t, "Alice")
	if subject != "Welcome to IOE" {
		t.Fatalf("subject %q", subject)
	}
	if !strings.HasPrefix(text, "Hello, Alice,\n") {
		t.Fatalf("text %q", text)
	}
	if !strings.Contains(html, "<p>Hello, Alice,</p>") {
		t.Fatalf("html %q", html)
	}
}

func TestWelcomeWithoutNameOmitsIt(t *testing.T) {
	for _, name := range []string{"", "   ", "\t\n"} {
		_, text, html := render(t, name)
		if !strings.HasPrefix(text, "Hello,\n") {
			t.Fatalf("name %q: text %q", name, text)
		}
		if !strings.Contains(html, "<p>Hello,</p>") {
			t.Fatalf("name %q: html %q", name, html)
		}
	}
}

func TestWelcomeEscapesHTMLOnly(t *testing.T) {
	_, text, html := render(t, "<b>Bob</b>")
	if !strings.Contains(html, "Hello, &lt;b&gt;Bob&lt;/b&gt;,") || strings.Contains(html, "<b>") {
		t.Fatalf("html not escaped: %q", html)
	}
	if !strings.Contains(text, "Hello, <b>Bob</b>,") {
		t.Fatalf("text %q", text)
	}
}

func TestWelcomeDoesNotEvaluateNameAsTemplate(t *testing.T) {
	_, text, _ := render(t, "{{.Name}}")
	if !strings.Contains(text, "Hello, {{.Name}},") {
		t.Fatalf("text %q", text)
	}
}

var paidEmail = app.PurchasePaidEmail{Name: "Sita", CourseTitle: "Go", Amount: "NPR 1,500.00", Method: "eSewa",
	PaidOn: "7 October 2026, 10:00 NPT", CourseURL: "https://app.test/courses/11"}

func renderPaid(t *testing.T, e app.PurchasePaidEmail) (string, string, string) {
	t.Helper()
	r, err := templates.New()
	if err != nil {
		t.Fatal(err)
	}
	subject, text, html, err := r.PurchasePaid(e)
	if err != nil {
		t.Fatal(err)
	}
	return subject, text, html
}

func TestPurchasePaid(t *testing.T) {
	subject, text, html := renderPaid(t, paidEmail)
	if subject != "Payment received: Go" {
		t.Fatalf("subject %q", subject)
	}
	for _, want := range []string{"Hello, Sita,", "Go", "NPR 1,500.00", "eSewa", "7 October 2026, 10:00 NPT", "enrolled", "https://app.test/courses/11"} {
		if !strings.Contains(text, want) || !strings.Contains(html, want) {
			t.Fatalf("missing %q\ntext=%s\nhtml=%s", want, text, html)
		}
	}
	if !strings.Contains(html, `<a href="https://app.test/courses/11">`) {
		t.Fatalf("html link: %s", html)
	}
}

func TestPurchasePaidWithoutTitleNameOrLink(t *testing.T) {
	e := paidEmail
	e.CourseTitle, e.Name, e.CourseURL = "", " ", ""
	subject, text, html := renderPaid(t, e)
	if subject != "Payment received" {
		t.Fatalf("subject %q", subject)
	}
	if !strings.HasPrefix(text, "Hello,\n") || strings.Contains(text, "http") || strings.Contains(html, "<a ") {
		t.Fatalf("text=%s html=%s", text, html)
	}
}

func TestPurchasePaidEscapesHTMLOnly(t *testing.T) {
	e := paidEmail
	e.CourseTitle, e.Name = "<b>Go</b>", "<i>Sita</i>"
	subject, text, html := renderPaid(t, e)
	if subject != "Payment received: <b>Go</b>" || !strings.Contains(text, "<b>Go</b>") || !strings.Contains(text, "<i>Sita</i>") {
		t.Fatalf("subject=%q text=%s", subject, text)
	}
	if strings.Contains(html, "<b>") || strings.Contains(html, "<i>") || !strings.Contains(html, "&lt;b&gt;Go&lt;/b&gt;") {
		t.Fatalf("html not escaped: %s", html)
	}
}
