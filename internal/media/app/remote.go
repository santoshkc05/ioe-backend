package app

import (
	"context"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/media/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

const (
	// DisplayVariant is the only image rendition requested and served.
	DisplayVariant = "display"
	// PosterRendition is the media service's JPEG poster for a video.
	PosterRendition = "poster"
	// StatusReady is the media service status of a playable asset.
	StatusReady = "ready"
)

type RemoteCreate struct {
	Kind           domain.Kind
	ContentType    string
	Filename       string
	SizeBytes      int64
	OwnerID        id.ID
	CourseID       id.ID
	IdempotencyKey string
}

type UploadPart struct {
	PartNumber int
	URL        string
}

// Upload carries either UploadURL (a single PUT) or PartSize and PartURLs (multipart).
type Upload struct {
	AssetID   id.ID
	UploadID  string
	UploadURL string
	PartSize  int64
	PartURLs  []UploadPart
	ExpiresAt time.Time
}

type CompletedPart struct {
	PartNumber int
	ETag       string
}

// RemoteAsset is the media service's view of an asset.
type RemoteAsset struct {
	ID              id.ID
	Status          string
	ProgressPercent int
	DurationMs      int64
	Width           int
	Height          int
	ErrorMessage    string
	UpdatedAt       time.Time
}

type Rendition struct {
	Name   string
	URL    string
	Width  int
	Height int
}

// Delivery is a grant of short-lived URLs. URL is the signed HLS manifest for a video.
// Every URL is absolute.
type Delivery struct {
	URL        string
	ExpiresAt  time.Time
	Renditions []Rendition
}

// Remote is the media service. Errors wrap one of the ErrRemote* sentinels.
type Remote interface {
	Create(ctx context.Context, in RemoteCreate) (Upload, error)
	PresignParts(ctx context.Context, assetID id.ID, partNumbers []int) ([]UploadPart, error)
	Complete(ctx context.Context, assetID id.ID, parts []CompletedPart) (RemoteAsset, error)
	Get(ctx context.Context, assetID id.ID) (RemoteAsset, error)
	Delete(ctx context.Context, assetID id.ID) error
	Delivery(ctx context.Context, assetID id.ID) (Delivery, error)
}
