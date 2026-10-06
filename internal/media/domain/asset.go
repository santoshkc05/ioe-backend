// Package domain holds the media asset ownership model.
package domain

import (
	"errors"
	"mime"
	"strings"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

// Kind is what an asset holds.
type Kind string

const (
	KindVideo Kind = "video"
	KindImage Kind = "image"
)

var ErrInvalidKind = errors.New("kind must be video or image")

func ParseKind(s string) (Kind, error) {
	switch Kind(s) {
	case KindVideo, KindImage:
		return Kind(s), nil
	}
	return "", ErrInvalidKind
}

// ValidContentType reports whether contentType is acceptable for an upload of kind k.
// The media service re-validates the bytes.
func ValidContentType(k Kind, contentType string) bool {
	mt, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return false
	}
	switch k {
	case KindVideo:
		return strings.HasPrefix(mt, "video/") && len(mt) > len("video/")
	case KindImage:
		return mt == "image/jpeg" || mt == "image/png" || mt == "image/webp"
	}
	return false
}

// Asset records which course owns a media-service asset. ID is the media service's asset ID.
type Asset struct {
	ID        id.ID
	CourseID  id.ID
	Kind      Kind
	CreatedBy id.ID
	CreatedAt time.Time
}
