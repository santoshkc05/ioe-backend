package id_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

func TestParse(t *testing.T) {
	good := map[string]id.ID{"1": 1, "1840396745219883008": 1840396745219883008, "9223372036854775807": 9223372036854775807}
	for s, want := range good {
		got, err := id.Parse(s)
		if err != nil || got != want {
			t.Fatalf("Parse(%q) = %d, %v; want %d", s, got, err, want)
		}
	}
	for _, s := range []string{"", "0", "-1", "+1", " 1", "1 ", "01", "1a", "9223372036854775808", "01920000-0000-7000-8000-000000000001"} {
		if _, err := id.Parse(s); !errors.Is(err, id.ErrInvalid) {
			t.Fatalf("Parse(%q) error = %v, want ErrInvalid", s, err)
		}
	}
}

func TestJSONRoundTripAsString(t *testing.T) {
	type doc struct {
		ID id.ID `json:"id"`
	}
	b, err := json.Marshal(doc{ID: 1840396745219883008})
	if err != nil || string(b) != `{"id":"1840396745219883008"}` {
		t.Fatalf("marshal = %s, %v", b, err)
	}
	var d doc
	if err := json.Unmarshal(b, &d); err != nil || d.ID != 1840396745219883008 {
		t.Fatalf("unmarshal = %d, %v", d.ID, err)
	}
}

func TestUnmarshalRejectsNumbersAndBadStrings(t *testing.T) {
	for _, in := range []string{`123`, `"abc"`, `""`, `null`, `"0"`} {
		var v id.ID
		if err := json.Unmarshal([]byte(in), &v); err == nil {
			t.Fatalf("Unmarshal(%s) succeeded with %d", in, v)
		}
	}
}

func TestGenerator(t *testing.T) {
	for _, n := range []int64{-1, 1024} {
		if _, err := id.NewGenerator(n); err == nil {
			t.Fatalf("NewGenerator(%d) succeeded", n)
		}
	}
	g, err := id.NewGenerator(1023)
	if err != nil {
		t.Fatal(err)
	}
	prev := g.New()
	for range 1000 {
		next := g.New()
		if next <= prev || next.IsZero() {
			t.Fatalf("ids not increasing: %d then %d", prev, next)
		}
		prev = next
	}
}
