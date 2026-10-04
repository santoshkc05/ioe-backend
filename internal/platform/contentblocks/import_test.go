package contentblocks

import (
	"os/exec"
	"strings"
	"testing"
)

// TestImportsNothingFromCourseauthoring is the concrete check behind phase
// 1's whole reason for existing: contentblocks is consumed by
// courseauthoring (and, from phase 2, blog), never the reverse. If this
// ever fails, something in this package started depending on a bounded
// context again, which defeats the extraction.
func TestImportsNothingFromCourseauthoring(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").Output()
	if err != nil {
		t.Skipf("go list unavailable in this environment: %v", err)
	}
	for line := range strings.SplitSeq(string(out), "\n") {
		if strings.Contains(line, "/internal/courseauthoring") || strings.Contains(line, "/internal/blog") {
			t.Fatalf("contentblocks must not depend on a bounded context, found: %s", line)
		}
	}
}
