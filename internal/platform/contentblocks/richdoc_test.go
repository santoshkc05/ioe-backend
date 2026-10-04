package contentblocks

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func doc(t *testing.T, s string) json.RawMessage {
	t.Helper()
	if !json.Valid([]byte(s)) {
		t.Fatalf("test fixture is not valid JSON: %s", s)
	}
	return json.RawMessage(s)
}

func TestNewSanitizedRichDoc_AcceptsAllowlistedNodesAndMarks(t *testing.T) {
	raw := doc(t, `{"type":"doc","content":[
		{"type":"paragraph","content":[
			{"type":"text","text":"hello","marks":[{"type":"bold"}]}
		]}
	]}`)
	d, err := NewSanitizedRichDoc(raw)
	if err != nil {
		t.Fatalf("NewSanitizedRichDoc: %v", err)
	}
	if d.IsZero() {
		t.Fatal("expected a non-zero RichDoc")
	}
}

func TestNewSanitizedRichDoc_RejectsUnknownNode(t *testing.T) {
	raw := doc(t, `{"type":"doc","content":[{"type":"script","content":[]}]}`)
	if _, err := NewSanitizedRichDoc(raw); !errors.Is(err, ErrInvalidRichDocNode) {
		t.Fatalf("expected ErrInvalidRichDocNode, got %v", err)
	}
}

func TestNewSanitizedRichDoc_RejectsUnknownMark(t *testing.T) {
	raw := doc(t, `{"type":"doc","content":[
		{"type":"paragraph","content":[
			{"type":"text","text":"x","marks":[{"type":"onclick"}]}
		]}
	]}`)
	if _, err := NewSanitizedRichDoc(raw); !errors.Is(err, ErrInvalidRichDocMark) {
		t.Fatalf("expected ErrInvalidRichDocMark, got %v", err)
	}
}

func TestNewSanitizedRichDoc_RejectsNonDocRoot(t *testing.T) {
	raw := doc(t, `{"type":"paragraph","content":[]}`)
	if _, err := NewSanitizedRichDoc(raw); !errors.Is(err, ErrInvalidRichDocNode) {
		t.Fatalf("expected ErrInvalidRichDocNode, got %v", err)
	}
}

func TestNewSanitizedRichDoc_RejectsEmpty(t *testing.T) {
	if _, err := NewSanitizedRichDoc(nil); !errors.Is(err, ErrEmptyRichDoc) {
		t.Fatalf("expected ErrEmptyRichDoc, got %v", err)
	}
	if _, err := NewSanitizedRichDoc(doc(t, `{"type":"doc","content":[]}`)); !errors.Is(err, ErrEmptyRichDoc) {
		t.Fatalf("expected ErrEmptyRichDoc for an empty doc, got %v", err)
	}
}

func TestNewSanitizedRichDoc_RejectsOversize(t *testing.T) {
	big := strings.Repeat("a", MaxRichDocSize)
	raw := doc(t, `{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"`+big+`"}]}]}`)
	if _, err := NewSanitizedRichDoc(raw); !errors.Is(err, ErrRichDocTooLarge) {
		t.Fatalf("expected ErrRichDocTooLarge, got %v", err)
	}
}

func TestNewRichDoc_SkipsAllowlist(t *testing.T) {
	// Persisted content decodes without re-running the allowlist, mirroring
	// how NewTextBody decodes what NewSanitizedTextBody wrote.
	raw := doc(t, `{"type":"doc","content":[{"type":"legacyNode","content":[]}]}`)
	if _, err := NewRichDoc(raw); err != nil {
		t.Fatalf("NewRichDoc on persisted content: %v", err)
	}
}

func TestNewSanitizedRichDoc_URLValidation(t *testing.T) {
	tests := []struct {
		name    string
		fixture string
		wantErr error
	}{
		{
			name: "javascript href rejected",
			fixture: `{"type":"doc","content":[{"type":"paragraph","content":[
				{"type":"text","text":"click me","marks":[{"type":"link","attrs":{"href":"javascript:alert(1)"}}]}
			]}]}`,
			wantErr: ErrInvalidRichDocMark,
		},
		{
			name: "data text/html src rejected",
			fixture: `{"type":"doc","content":[
				{"type":"image","attrs":{"src":"data:text/html;base64,PHNjcmlwdD5hbGVydCgxKTwvc2NyaXB0Pg=="}}
			]}`,
			wantErr: ErrInvalidRichDocNode,
		},
		{
			name: "vbscript href rejected",
			fixture: `{"type":"doc","content":[{"type":"paragraph","content":[
				{"type":"text","text":"click me","marks":[{"type":"link","attrs":{"href":"vbscript:msgbox(1)"}}]}
			]}]}`,
			wantErr: ErrInvalidRichDocMark,
		},
		{
			name: "safe https href accepted",
			fixture: `{"type":"doc","content":[{"type":"paragraph","content":[
				{"type":"text","text":"website","marks":[{"type":"link","attrs":{"href":"https://example.com"}}]}
			]}]}`,
			wantErr: nil,
		},
		{
			name: "relative href rejected by existing isSafeHref",
			fixture: `{"type":"doc","content":[{"type":"paragraph","content":[
				{"type":"text","text":"about","marks":[{"type":"link","attrs":{"href":"/about"}}]}
			]}]}`,
			wantErr: ErrInvalidRichDocMark,
		},
		{
			name: "image addressed by media asset id accepted without src",
			fixture: `{"type":"doc","content":[
				{"type":"image","attrs":{"mediaAssetId":"9001","alt":"diagram","width":420,"align":"center"}}
			]}`,
			wantErr: nil,
		},
		{
			name: "image with neither src nor media asset id rejected",
			fixture: `{"type":"doc","content":[
				{"type":"image","attrs":{"alt":"nothing to render"}}
			]}`,
			wantErr: ErrInvalidRichDocNode,
		},
		{
			name: "unsafe src still rejected when a media asset id is absent",
			fixture: `{"type":"doc","content":[
				{"type":"image","attrs":{"src":"javascript:alert(1)"}}
			]}`,
			wantErr: ErrInvalidRichDocNode,
		},
		{
			name: "data image/png src rejected by existing isSafeHref",
			fixture: `{"type":"doc","content":[
				{"type":"image","attrs":{"src":"data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="}}
			]}`,
			wantErr: ErrInvalidRichDocNode,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewSanitizedRichDoc(doc(t, tt.fixture))
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
			} else {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("got error %v, want %v", err, tt.wantErr)
				}
			}
		})
	}
}

func TestRichDocImageAssetIDs(t *testing.T) {
	d, err := NewSanitizedRichDoc(doc(t, `{"type":"doc","content":[
		{"type":"image","attrs":{"mediaAssetId":"11"}},
		{"type":"paragraph","content":[{"type":"text","text":"between"}]},
		{"type":"image","attrs":{"mediaAssetId":"12"}},
		{"type":"image","attrs":{"mediaAssetId":"11"}},
		{"type":"image","attrs":{"src":"https://cdn.example.com/a.png"}}
	]}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := d.ImageAssetIDs()
	want := []string{"11", "12"}
	if len(got) != len(want) {
		t.Fatalf("ImageAssetIDs() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ImageAssetIDs() = %v, want %v", got, want)
		}
	}
}
