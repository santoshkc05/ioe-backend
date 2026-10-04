package contentblocks

import (
	"errors"
	"strings"
	"testing"
)

func TestSanitizeHTML_AllowsAllowlistedMarkup(t *testing.T) {
	cases := []string{
		"",
		"plain text, no markup at all",
		`<p>Hello <strong>world</strong></p>`,
		`<p class="text-center">Centered</p>`,
		`<span class="text-lg">Big</span>`,
		`<span class="text-red-500">Red</span>`,
		`<ul><li>one</li><li>two</li></ul>`,
		`<table><tr><td colspan="2">cell</td></tr></table>`,
		`<a href="https://example.com">link</a>`,
		`<a href="mailto:a@b.com">mail</a>`,
		`<li data-type="taskItem" data-checked="true"><input type="checkbox" checked="checked"></li>`,
		`<span data-latex="x^2" data-display="true"></span>`,
	}
	for _, in := range cases {
		out, err := SanitizeHTML(in)
		if err != nil {
			t.Errorf("SanitizeHTML(%q) unexpected error: %v", in, err)
			continue
		}
		if out == "" && in != "" {
			t.Errorf("SanitizeHTML(%q) produced empty output", in)
		}
	}
}

func TestSanitizeHTML_RejectsUnsafeContent(t *testing.T) {
	cases := []string{
		`<script>alert(1)</script>`,
		`<img src=x onerror="alert(1)">`,
		`<p style="color:red">styled</p>`,
		`<a href="javascript:alert(1)">bad</a>`,
		`<span class="not-in-allowlist">x</span>`,
		`<p class="text-lg">wrong tag for font size</p>`,
		`<!-- comment -->`,
		`<div>not an allowed tag</div>`,
	}
	for _, in := range cases {
		_, err := SanitizeHTML(in)
		if !errors.Is(err, ErrUnsafeContent) {
			t.Errorf("SanitizeHTML(%q) = err %v, want ErrUnsafeContent", in, err)
		}
	}
}

func TestSanitizeHTML_ForcesNoopenerOnBlankTarget(t *testing.T) {
	out, err := SanitizeHTML(`<a href="https://example.com" target="_blank" rel="opener">link</a>`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := `rel="noopener noreferrer"`; !contains(out, want) {
		t.Errorf("SanitizeHTML output = %q, want to contain %q", out, want)
	}
	if contains(out, `rel="opener"`) {
		t.Errorf("SanitizeHTML output = %q, still contains the client-supplied rel", out)
	}
}

func TestNewSanitizedTextBody_RejectsStructurallyUnsafeContent(t *testing.T) {
	if _, err := NewSanitizedTextBody(`<script>alert(1)</script>`); !errors.Is(err, ErrUnsafeContent) {
		t.Fatalf("got %v, want ErrUnsafeContent", err)
	}
	body, err := NewSanitizedTextBody(`<p>safe</p>`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if body.IsZero() {
		t.Fatal("expected a non-zero TextBody")
	}
}

func TestNewSanitizedFlashcardDeck_SanitizesEachCard(t *testing.T) {
	_, err := NewSanitizedFlashcardDeck("steps", "deck", []Flashcard{
		{Front: `<script>alert(1)</script>`, Back: "back"},
	})
	if !errors.Is(err, ErrUnsafeContent) {
		t.Fatalf("got %v, want ErrUnsafeContent", err)
	}
	deck, err := NewSanitizedFlashcardDeck("steps", "deck", []Flashcard{
		{Front: "<p>front</p>", Back: "<p>back</p>"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(deck.Cards()) != 1 {
		t.Fatalf("expected 1 card, got %d", len(deck.Cards()))
	}
}

func TestSanitizedTextBodyStripsScriptAndHandlers(t *testing.T) {
	for _, raw := range []string{
		`<p>hi</p><script>alert(1)</script>`,
		`<p><img src=x onerror="alert(1)">hi</p>`,
		`<p><a href="javascript:alert(1)">x</a></p>`,
	} {
		body, err := NewSanitizedTextBody(raw)
		if err != nil {
			if !errors.Is(err, ErrUnsafeContent) {
				t.Fatalf("%q: err = %v", raw, err)
			}
			continue
		}
		s := strings.ToLower(body.String())
		if strings.Contains(s, "<script") || strings.Contains(s, "onerror") || strings.Contains(s, "javascript:") {
			t.Fatalf("%q survived as %q", raw, body.String())
		}
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (func() bool {
		for i := 0; i+len(substr) <= len(s); i++ {
			if s[i:i+len(substr)] == substr {
				return true
			}
		}
		return false
	})()
}
