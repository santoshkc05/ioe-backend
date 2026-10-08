package postgres

import (
	"strings"
	"unicode"
)

// searchTerms splits search text into websearch text and a prefix term, so the word being
// typed last also matches longer words ("lea" finds "learning"). The last word becomes the
// prefix term only when it is all letters and digits: anything else could carry websearch
// syntax (quotes, a leading "-") or to_tsquery syntax, so the text is then used whole.
func searchTerms(q string) (head, prefix string) {
	q = strings.TrimSpace(q)
	i := strings.LastIndexFunc(q, unicode.IsSpace)
	last := q[i+1:]
	if last == "" || strings.IndexFunc(last, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) >= 0 {
		return q, ""
	}
	return strings.TrimSpace(q[:i+1]), strings.ToLower(last)
}
