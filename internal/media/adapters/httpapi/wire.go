package httpapi

import (
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/media/app"
	"github.com/santoshkc2200/ioe-backend/internal/media/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

type createUploadRequest struct {
	Kind        string `json:"kind"`
	ContentType string `json:"content_type"`
	Filename    string `json:"filename"`
	SizeBytes   int64  `json:"size_bytes"`
}

type partsRequest struct {
	PartNumbers []int `json:"part_numbers"`
}

type completedPartWire struct {
	PartNumber int    `json:"part_number"`
	ETag       string `json:"etag"`
}

type completeRequest struct {
	Parts []completedPartWire `json:"parts"`
}

type partWire struct {
	PartNumber int    `json:"part_number"`
	URL        string `json:"url"`
}

type uploadWire struct {
	AssetID   id.ID      `json:"asset_id"`
	UploadID  string     `json:"upload_id,omitempty"`
	UploadURL string     `json:"upload_url,omitempty"`
	PartSize  int64      `json:"part_size,omitempty"`
	PartURLs  []partWire `json:"part_urls,omitempty"`
	ExpiresAt time.Time  `json:"expires_at"`
}

type partsWire struct {
	PartURLs []partWire `json:"part_urls"`
}

type assetWire struct {
	ID              id.ID     `json:"id"`
	CourseID        id.ID     `json:"course_id"`
	Kind            string    `json:"kind"`
	Status          string    `json:"status"`
	ProgressPercent int       `json:"progress_percent"`
	DurationMs      int64     `json:"duration_ms,omitempty"`
	Width           int       `json:"width,omitempty"`
	Height          int       `json:"height,omitempty"`
	ErrorMessage    string    `json:"error_message,omitempty"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type playbackWire struct {
	ID          id.ID      `json:"id"`
	Kind        string     `json:"kind"`
	Status      string     `json:"status"`
	PlaybackURL string     `json:"playback_url,omitempty"`
	URL         string     `json:"url,omitempty"`
	PosterURL   string     `json:"poster_url,omitempty"`
	DurationMs  int64      `json:"duration_ms,omitempty"`
	Width       int        `json:"width,omitempty"`
	Height      int        `json:"height,omitempty"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
}

func toParts(in []app.UploadPart) []partWire {
	out := make([]partWire, len(in))
	for i, p := range in {
		out[i] = partWire{PartNumber: p.PartNumber, URL: p.URL}
	}
	return out
}

func toUploadWire(u app.Upload) uploadWire {
	return uploadWire{AssetID: u.AssetID, UploadID: u.UploadID, UploadURL: u.UploadURL,
		PartSize: u.PartSize, PartURLs: toParts(u.PartURLs), ExpiresAt: u.ExpiresAt}
}

func toAssetWire(v app.AssetView) assetWire {
	return assetWire{ID: v.Asset.ID, CourseID: v.Asset.CourseID, Kind: string(v.Asset.Kind),
		Status: v.Remote.Status, ProgressPercent: v.Remote.ProgressPercent, DurationMs: v.Remote.DurationMs,
		Width: v.Remote.Width, Height: v.Remote.Height, ErrorMessage: v.Remote.ErrorMessage, UpdatedAt: v.Remote.UpdatedAt}
}

func toPlaybackWire(p app.Playback) playbackWire {
	w := playbackWire{ID: p.ID, Kind: string(p.Kind), Status: p.Status}
	if p.URL == "" {
		return w
	}
	if p.Kind == domain.KindVideo {
		w.PlaybackURL, w.PosterURL, w.DurationMs = p.URL, p.PosterURL, p.DurationMs
	} else {
		w.URL = p.URL
	}
	w.Width, w.Height = p.Width, p.Height
	at := p.ExpiresAt
	w.ExpiresAt = &at
	return w
}
