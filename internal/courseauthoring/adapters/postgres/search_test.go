package postgres

import "testing"

func TestSearchTerms(t *testing.T) {
	cases := []struct{ q, head, prefix string }{
		{"go", "", "go"},
		{"Learning Go", "Learning", "go"},
		{"  web   dev  ", "web", "dev"},
		{"go!", "go!", ""},
		{`"learning go"`, `"learning go"`, ""},
		{`"unterminated`, `"unterminated`, ""},
		{"rust -unsafe", "rust -unsafe", ""},
		{"c++", "c++", ""},
		{"café", "", "café"},
		{"Go 127", "Go", "127"},
		{"", "", ""},
	}
	for _, c := range cases {
		head, prefix := searchTerms(c.q)
		if head != c.head || prefix != c.prefix {
			t.Errorf("searchTerms(%q) = %q, %q; want %q, %q", c.q, head, prefix, c.head, c.prefix)
		}
	}
}
