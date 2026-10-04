package contentblocks

import "encoding/json"

// MaxRichDocSize bounds a single mobile page body. It matches
// maxTextBodySize: a mobile page is a short-form summary, and one megabyte
// of ProseMirror JSON is already far more than that shape needs.
const MaxRichDocSize = 1 << 20

// allowedRichDocNodes is the ProseMirror node allowlist for mobile page
// bodies. It mirrors the extension set the editor is configured with
// (packages/content-editor/src/editor/richtext), so a document the editor
// can produce is a document this validator accepts. Anything else is
// rejected at the write boundary rather than filtered silently: a caller
// sending an unknown node has a bug, and hiding it makes that bug harder
// to find than returning an error does.
var allowedRichDocNodes = map[string]struct{}{
	"doc": {}, "paragraph": {}, "text": {}, "heading": {},
	"bulletList": {}, "orderedList": {}, "listItem": {},
	"blockquote": {}, "codeBlock": {}, "horizontalRule": {}, "hardBreak": {},
	"image": {}, "table": {}, "tableRow": {}, "tableCell": {}, "tableHeader": {},
	"taskList": {}, "taskItem": {},
}

// allowedRichDocMarks is the mark allowlist. `link` is permitted as a mark
// type here; its href attribute (and image's src attribute) is validated
// through isSafeHref during NewSanitizedRichDoc.
var allowedRichDocMarks = map[string]struct{}{
	"bold": {}, "italic": {}, "strike": {}, "code": {}, "underline": {},
	"link": {}, "highlight": {}, "subscript": {}, "superscript": {},
}

// RichDoc is a validated tiptap/ProseMirror JSON document. Mobile page
// bodies are stored as JSON rather than the HTML TextBody carries, because
// the Android and iOS clients consume the same API and cannot render HTML
// without shipping a parser and a mapping to native views.
type RichDoc struct {
	value json.RawMessage
}

func (d RichDoc) JSON() json.RawMessage { return d.value }
func (d RichDoc) IsZero() bool          { return len(d.value) == 0 }

type richDocNodeAttrs struct {
	Src string `json:"src"`
	// MediaAssetID is how an uploaded image is referenced. The resolved URL
	// is a short-lived presign (media's `GET /v1/media/assets/{id}/image`),
	// so persisting one in `src` would store a link that expires; an image
	// block carries the asset id and the reader resolves it at render time,
	// exactly as the lecture image block already does.
	MediaAssetID string `json:"mediaAssetId"`
}

type richDocMarkAttrs struct {
	Href string `json:"href"`
}

type richDocNode struct {
	Type    string            `json:"type"`
	Attrs   richDocNodeAttrs  `json:"attrs"`
	Content []richDocNode     `json:"content"`
	Marks   []richDocMarkType `json:"marks"`
}

type richDocMarkType struct {
	Type  string           `json:"type"`
	Attrs richDocMarkAttrs `json:"attrs"`
}

// ImageAssetIDs reports every media asset id referenced by an image node in
// the document, in document order and without duplicates. Write paths use it
// to validate the references before the document is stored, the way
// CourseService validates an image block's asset id.
func (d RichDoc) ImageAssetIDs() []string {
	var root richDocNode
	if err := json.Unmarshal(d.value, &root); err != nil {
		return nil
	}
	seen := map[string]struct{}{}
	var out []string
	var walk func(n richDocNode)
	walk = func(n richDocNode) {
		if n.Type == "image" && n.Attrs.MediaAssetID != "" {
			if _, dup := seen[n.Attrs.MediaAssetID]; !dup {
				seen[n.Attrs.MediaAssetID] = struct{}{}
				out = append(out, n.Attrs.MediaAssetID)
			}
		}
		for _, child := range n.Content {
			walk(child)
		}
	}
	walk(root)
	return out
}

// NewRichDoc validates shape and size only. It is the decode path for
// content that is already persisted, exactly as NewTextBody is for
// TextBody: re-running the allowlist here would make an allowlist change
// retroactively unreadable.
func NewRichDoc(raw json.RawMessage) (RichDoc, error) {
	if len(raw) == 0 || !json.Valid(raw) {
		return RichDoc{}, ErrEmptyRichDoc
	}
	if len(raw) > MaxRichDocSize {
		return RichDoc{}, ErrRichDocTooLarge
	}
	return RichDoc{value: raw}, nil
}

// NewSanitizedRichDoc is the write-time counterpart: it walks the document
// and rejects any node or mark outside the allowlist before delegating to
// NewRichDoc. Every caller building a RichDoc from request input uses this.
func NewSanitizedRichDoc(raw json.RawMessage) (RichDoc, error) {
	if len(raw) == 0 || !json.Valid(raw) {
		return RichDoc{}, ErrEmptyRichDoc
	}
	if len(raw) > MaxRichDocSize {
		return RichDoc{}, ErrRichDocTooLarge
	}
	var root richDocNode
	if err := json.Unmarshal(raw, &root); err != nil {
		return RichDoc{}, ErrEmptyRichDoc
	}
	if root.Type != "doc" {
		return RichDoc{}, ErrInvalidRichDocNode
	}
	if len(root.Content) == 0 {
		return RichDoc{}, ErrEmptyRichDoc
	}
	if err := validateRichDocNode(root); err != nil {
		return RichDoc{}, err
	}
	return RichDoc{value: raw}, nil
}

func validateRichDocNode(n richDocNode) error {
	if _, ok := allowedRichDocNodes[n.Type]; !ok {
		return ErrInvalidRichDocNode
	}
	// An image is addressed either by asset id (uploaded through media) or
	// by an external href. Requiring a safe `src` unconditionally would
	// reject every uploaded image, whose `src` is resolved client-side.
	if n.Type == "image" && n.Attrs.MediaAssetID == "" && !isSafeHref(n.Attrs.Src) {
		return ErrInvalidRichDocNode
	}
	for _, m := range n.Marks {
		if _, ok := allowedRichDocMarks[m.Type]; !ok {
			return ErrInvalidRichDocMark
		}
		if m.Type == "link" && !isSafeHref(m.Attrs.Href) {
			return ErrInvalidRichDocMark
		}
	}
	for _, child := range n.Content {
		if err := validateRichDocNode(child); err != nil {
			return err
		}
	}
	return nil
}
