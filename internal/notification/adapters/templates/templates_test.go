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
