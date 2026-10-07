package contentblocks_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/santoshkc2200/ioe-backend/internal/platform/contentblocks"
)

func TestCheckPatchLists(t *testing.T) {
	cases := map[string]struct {
		order, upserts, deletes []string
		want                    error
	}{
		"ok":               {[]string{"a", "b"}, []string{"b"}, []string{"c"}, nil},
		"dup upsert":       {[]string{"a"}, []string{"a", "a"}, nil, contentblocks.ErrDuplicateClientBlockID},
		"dup order":        {[]string{"a", "a"}, nil, nil, contentblocks.ErrDuplicateClientBlockID},
		"dup delete":       {nil, nil, []string{"x", "x"}, contentblocks.ErrDuplicateClientBlockID},
		"order and delete": {[]string{"a"}, nil, []string{"a"}, contentblocks.ErrOrderDeleteOverlap},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := contentblocks.CheckPatchLists(tc.order, tc.upserts, tc.deletes)
			if !errors.Is(err, tc.want) || (tc.want == nil && err != nil) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}

func TestCheckBlockSet(t *testing.T) {
	existing := []string{"a", "b"}
	if err := contentblocks.CheckBlockSet(existing, []string{"b", "c"}, []string{"c"}, []string{"a"}); err != nil {
		t.Fatalf("valid set: %v", err)
	}
	// Deleting an ID the server does not have is a no-op.
	if err := contentblocks.CheckBlockSet(existing, []string{"a", "b"}, nil, []string{"zzz"}); err != nil {
		t.Fatalf("unknown delete: %v", err)
	}
	err := contentblocks.CheckBlockSet(existing, []string{"a"}, nil, nil)
	if !errors.Is(err, contentblocks.ErrBlockSetMismatch) || !strings.Contains(err.Error(), "[b]") {
		t.Fatalf("missing b: %v", err)
	}
}
