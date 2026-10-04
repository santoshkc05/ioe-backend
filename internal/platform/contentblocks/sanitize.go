package contentblocks

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/microcosm-cc/bluemonday"
	"golang.org/x/net/html"
)

// SanitizeHTML enforces the write-time HTML allowlist ask B11
// (docs/blog-backend-phase2-blog-authoring.md §2.6) mirrors on the server
// what ioe-frontend's DOMPurify pass (packages/course-core/src/richtext/
// sanitize.ts) already does in the browser. Client-side sanitization
// protects a reader only when the writer ran the same code, which an HTTP
// client that skips the editor does not - and a blog turns that gap into a
// stored-XSS path reaching anonymous visitors (phase 4).
//
// It is deliberately NOT called from DecodePayload or any other read path:
// content already accepted and persisted before this sanitizer shipped must
// keep loading exactly as it did, so this is wired only at the write
// boundary - see NewSanitizedTextBody/NewSanitizedFlashcardDeck below and
// their callers in each host context's application layer.
//
// Where the input contains anything outside the allowlist, SanitizeHTML
// rejects with ErrUnsafeContent naming the offending tag/attribute instead
// of silently storing a stripped body - an author who pasted from Word
// should be told, not surprised later (§2.6).
func SanitizeHTML(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return raw, nil
	}
	if what, unsafe := firstUnsafeConstruct(raw); unsafe {
		return "", fmt.Errorf("%w: %s", ErrUnsafeContent, what)
	}
	return forceNoopenerOnBlankTargets(sanitizePolicy.Sanitize(raw)), nil
}

// allowedTags/allowedAttrs mirror sanitize.ts's DOMPurify configuration:
// the tag and attribute allowlists from §2.6.
var allowedTags = map[string]bool{
	"p": true, "strong": true, "em": true, "s": true, "u": true, "mark": true,
	"span": true, "br": true, "h1": true, "h2": true, "h3": true, "h4": true,
	"h5": true, "h6": true, "blockquote": true, "pre": true, "code": true,
	"hr": true, "ul": true, "ol": true, "li": true, "a": true, "sup": true,
	"sub": true, "table": true, "tr": true, "th": true, "td": true,
	"label": true, "input": true,
}

var allowedAttrs = map[string]bool{
	"class": true, "href": true, "target": true, "rel": true,
	"colspan": true, "rowspan": true, "data-type": true, "data-checked": true,
	"type": true, "checked": true, "data-latex": true, "data-display": true,
}

// blockTagsWithAlign is the "block tags only text-align classes" half of
// §2.6's closed per-tag class allowlist. span's half (font-size/text-colour)
// is spanClassAllowed below; every other tag may carry no class at all.
var blockTagsWithAlign = map[string]bool{
	"p": true, "h1": true, "h2": true, "h3": true, "h4": true, "h5": true,
	"h6": true, "blockquote": true, "li": true, "td": true, "th": true, "pre": true,
}

// fontSizeClassRe/textColorClassRe/textAlignClassRe are this package's
// mirror of the frontend's closed class allowlist. They are regexes over a
// Tailwind-style token space rather than a literal enumerated set, which is
// this repository's best-effort reconstruction of sanitize.ts's allowlist -
// ioe-frontend is a separate repository not checked out alongside this
// one, so the two allowlists cannot literally share a fixture file the way
// §2.6 asks for ("a test in each repository asserting the list against a
// shared fixture"). sanitize_test.go's TestSanitizerAllowlistFixture is this
// repository's half of that fixture; the frontend side must be added
// there and kept in sync by hand until the two repos share one.
var (
	fontSizeClassRe  = regexp.MustCompile(`^text-(?:xs|sm|base|lg|xl|2xl|3xl|4xl|5xl|6xl)$`)
	textColorClassRe = regexp.MustCompile(`^text-(?:black|white|(?:slate|gray|zinc|neutral|stone|red|orange|amber|yellow|lime|green|emerald|teal|cyan|sky|blue|indigo|violet|purple|fuchsia|pink|rose)-(?:50|100|200|300|400|500|600|700|800|900|950))$`)
	textAlignClassRe = regexp.MustCompile(`^text-(?:left|center|right|justify)$`)
	// safeClassAttrRe is the defense-in-depth bound bluemonday's own pass
	// applies to any class value that reaches it - alphanumeric/hyphen
	// tokens only, so no quote or attribute-boundary characters ever survive
	// even if firstUnsafeConstruct's tokenizer walk had a gap. The precise
	// per-tag business rule is enforced by firstUnsafeConstruct, not here.
	safeClassAttrRe = regexp.MustCompile(`^[a-z0-9-]+(?:\s+[a-z0-9-]+)*$`)
)

func spanClassAllowed(class string) bool {
	return fontSizeClassRe.MatchString(class) || textColorClassRe.MatchString(class)
}

func blockClassAllowed(class string) bool {
	return textAlignClassRe.MatchString(class)
}

func isSafeHref(href string) bool {
	lower := strings.ToLower(strings.TrimSpace(href))
	return strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "mailto:")
}

// firstUnsafeConstruct walks raw with the same HTML tokenizer bluemonday
// uses underneath (golang.org/x/net/html) and reports the first tag,
// attribute, class or href that falls outside §2.6's allowlist, so
// SanitizeHTML can reject rather than silently modify. Returns ("", false)
// when raw is already fully inside the allowlist.
func firstUnsafeConstruct(raw string) (string, bool) {
	z := html.NewTokenizer(strings.NewReader(raw))
	for {
		switch z.Next() {
		case html.ErrorToken:
			return "", false
		case html.CommentToken:
			return "comment", true
		case html.DoctypeToken:
			return "doctype", true
		case html.StartTagToken, html.SelfClosingTagToken:
			token := z.Token()
			name := strings.ToLower(token.Data)
			if !allowedTags[name] {
				return name, true
			}
			for _, attr := range token.Attr {
				attrName := strings.ToLower(attr.Key)
				if !allowedAttrs[attrName] {
					return name + "[" + attrName + "]", true
				}
				switch attrName {
				case "class":
					for _, cls := range strings.Fields(attr.Val) {
						switch {
						case name == "span" && spanClassAllowed(cls):
						case blockTagsWithAlign[name] && blockClassAllowed(cls):
						default:
							return name + " class", true
						}
					}
				case "href":
					if name != "a" || !isSafeHref(attr.Val) {
						return "a href", true
					}
				case "target":
					if name != "a" || attr.Val != "_blank" {
						return "a target", true
					}
				}
			}
		}
	}
}

// sanitizePolicy is bluemonday's defense-in-depth pass, run after
// firstUnsafeConstruct has already rejected anything outside the allowlist:
// it re-serializes the (already-validated) markup safely rather than acting
// as the primary gate.
var sanitizePolicy = newSanitizePolicy()

func newSanitizePolicy() *bluemonday.Policy {
	p := bluemonday.NewPolicy()
	tags := make([]string, 0, len(allowedTags))
	for t := range allowedTags {
		tags = append(tags, t)
	}
	p.AllowElements(tags...)
	p.AllowAttrs("colspan", "rowspan").OnElements("td", "th")
	p.AllowAttrs("data-type", "data-checked").Globally()
	p.AllowAttrs("data-latex", "data-display").OnElements("span")
	p.AllowAttrs("type", "checked").OnElements("input")
	p.AllowAttrs("href").OnElements("a")
	p.AllowURLSchemes("http", "https", "mailto")
	p.AllowAttrs("target").OnElements("a")
	p.AllowAttrs("rel").OnElements("a")
	p.AllowAttrs("class").Matching(safeClassAttrRe).Globally()
	return p
}

var aTagRe = regexp.MustCompile(`(?is)<a\s+[^>]*>`)
var relAttrRe = regexp.MustCompile(`(?i)\s+rel="[^"]*"`)

// forceNoopenerOnBlankTargets implements §2.6's "target=_blank forces
// rel=noopener noreferrer" rule: a reverse tabnabbing guard applied
// unconditionally, regardless of whatever rel value (if any) the client
// sent. Runs after bluemonday.Sanitize(), on already-allowlisted markup.
func forceNoopenerOnBlankTargets(sanitized string) string {
	return aTagRe.ReplaceAllStringFunc(sanitized, func(tag string) string {
		if !strings.Contains(tag, `target="_blank"`) {
			return tag
		}
		stripped := relAttrRe.ReplaceAllString(tag, "")
		return strings.TrimSuffix(stripped, ">") + ` rel="noopener noreferrer">`
	})
}
