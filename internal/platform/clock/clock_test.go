package clock_test

import (
	"testing"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/platform/clock"
)

func TestFakeAdvance(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c := clock.NewFake(start)
	c.Advance(time.Hour)
	if got := c.Now(); !got.Equal(start.Add(time.Hour)) {
		t.Fatalf("Now = %v", got)
	}
}

func TestSystemIsUTC(t *testing.T) {
	if loc := (clock.System{}).Now().Location(); loc != time.UTC {
		t.Fatalf("location = %v", loc)
	}
}
