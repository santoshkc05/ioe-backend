package domain_test

import (
	"errors"
	"testing"

	"github.com/santoshkc2200/ioe-backend/internal/media/domain"
)

func TestParseKind(t *testing.T) {
	for _, s := range []string{"video", "image"} {
		if k, err := domain.ParseKind(s); err != nil || string(k) != s {
			t.Fatalf("ParseKind(%q) = %q, %v", s, k, err)
		}
	}
	for _, s := range []string{"", "VIDEO", "thumbnail", "audio"} {
		if _, err := domain.ParseKind(s); !errors.Is(err, domain.ErrInvalidKind) {
			t.Fatalf("ParseKind(%q) err = %v", s, err)
		}
	}
}

func TestValidContentType(t *testing.T) {
	cases := []struct {
		kind domain.Kind
		ct   string
		want bool
	}{
		{domain.KindVideo, "video/mp4", true},
		{domain.KindVideo, "video/quicktime", true},
		{domain.KindVideo, "Video/MP4; codecs=avc1", true},
		{domain.KindVideo, "image/png", false},
		{domain.KindVideo, "video/", false},
		{domain.KindImage, "image/jpeg", true},
		{domain.KindImage, "image/png", true},
		{domain.KindImage, "image/webp", true},
		{domain.KindImage, "image/gif", false},
		{domain.KindImage, "video/mp4", false},
		{domain.KindImage, "", false},
		{domain.KindImage, "not a type", false},
	}
	for _, c := range cases {
		if got := domain.ValidContentType(c.kind, c.ct); got != c.want {
			t.Fatalf("ValidContentType(%s, %q) = %v", c.kind, c.ct, got)
		}
	}
}
