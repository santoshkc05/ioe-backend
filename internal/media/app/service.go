package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/media/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/clock"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

const (
	maxPartsPerRequest = 1000
	maxPartNumber      = 10000
)

// AssetService implements upload, status and playback use cases.
type AssetService struct {
	assets  AssetRepository
	remote  Remote
	courses CourseAccess
	ids     *id.Generator
	clock   clock.Clock
}

func NewAssetService(assets AssetRepository, remote Remote, courses CourseAccess, ids *id.Generator, clk clock.Clock) *AssetService {
	return &AssetService{assets: assets, remote: remote, courses: courses, ids: ids, clock: clk}
}

type UploadInput struct {
	Kind        string
	ContentType string
	Filename    string
	SizeBytes   int64
}

// AssetView is an asset's ownership row with the media service's current state.
type AssetView struct {
	Asset  domain.Asset
	Remote RemoteAsset
}

// Playback carries URLs only when Status is ready. URL is the signed HLS manifest for a
// video and the display rendition for an image.
type Playback struct {
	ID         id.ID
	Kind       domain.Kind
	Status     string
	URL        string
	PosterURL  string
	DurationMs int64
	Width      int
	Height     int
	ExpiresAt  time.Time
}

func (s *AssetService) CreateUpload(ctx context.Context, p auth.Principal, courseID id.ID, in UploadInput) (Upload, error) {
	kind, err := domain.ParseKind(in.Kind)
	if err != nil {
		return Upload{}, fmt.Errorf("%w: %w", ErrInvalidInput, err)
	}
	if !domain.ValidContentType(kind, in.ContentType) {
		return Upload{}, fmt.Errorf("%w: content_type %q is not accepted for %s", ErrInvalidInput, in.ContentType, kind)
	}
	if strings.TrimSpace(in.Filename) == "" {
		return Upload{}, fmt.Errorf("%w: filename is required", ErrInvalidInput)
	}
	if in.SizeBytes <= 0 {
		return Upload{}, fmt.Errorf("%w: size_bytes must be positive", ErrInvalidInput)
	}
	if err := s.courses.CanManage(ctx, p, courseID); err != nil {
		return Upload{}, err
	}
	up, err := s.remote.Create(ctx, RemoteCreate{
		Kind: kind, ContentType: in.ContentType, Filename: in.Filename, SizeBytes: in.SizeBytes,
		OwnerID: p.UserID, CourseID: courseID, IdempotencyKey: "ioe:upload:" + s.ids.New().String(),
	})
	if err != nil {
		return Upload{}, err
	}
	// A failed insert orphans the remote asset; the media service fails abandoned uploads itself.
	err = s.assets.Insert(ctx, domain.Asset{ID: up.AssetID, CourseID: courseID, Kind: kind, CreatedBy: p.UserID, CreatedAt: s.clock.Now()})
	if err != nil {
		return Upload{}, err
	}
	return up, nil
}

func (s *AssetService) PresignParts(ctx context.Context, p auth.Principal, assetID id.ID, partNumbers []int) ([]UploadPart, error) {
	if len(partNumbers) == 0 || len(partNumbers) > maxPartsPerRequest {
		return nil, fmt.Errorf("%w: part_numbers must hold 1 to %d entries", ErrInvalidInput, maxPartsPerRequest)
	}
	for _, n := range partNumbers {
		if n < 1 || n > maxPartNumber {
			return nil, fmt.Errorf("%w: part numbers must be between 1 and %d", ErrInvalidInput, maxPartNumber)
		}
	}
	if _, err := s.managed(ctx, p, assetID); err != nil {
		return nil, err
	}
	return s.remote.PresignParts(ctx, assetID, partNumbers)
}

func (s *AssetService) Complete(ctx context.Context, p auth.Principal, assetID id.ID, parts []CompletedPart) (AssetView, error) {
	a, err := s.managed(ctx, p, assetID)
	if err != nil {
		return AssetView{}, err
	}
	r, err := s.remote.Complete(ctx, assetID, parts)
	if err != nil {
		return AssetView{}, err
	}
	return AssetView{Asset: a, Remote: r}, nil
}

func (s *AssetService) Status(ctx context.Context, p auth.Principal, assetID id.ID) (AssetView, error) {
	a, err := s.managed(ctx, p, assetID)
	if err != nil {
		return AssetView{}, err
	}
	r, err := s.remote.Get(ctx, assetID)
	if err != nil {
		return AssetView{}, err
	}
	return AssetView{Asset: a, Remote: r}, nil
}

// Delete removes an asset no lecture of the working copy or live version uses: the remote asset
// first, then the row. A block added concurrently can slip past the check; course submit
// re-checks references, so the live version never points at a deleted asset.
func (s *AssetService) Delete(ctx context.Context, p auth.Principal, assetID id.ID) error {
	a, err := s.managed(ctx, p, assetID)
	if err != nil {
		return err
	}
	lectures, err := s.courses.AssetUsage(ctx, a.CourseID, assetID)
	if err != nil {
		return err
	}
	if len(lectures) > 0 {
		return &InUseError{LectureIDs: lectures}
	}
	if err := s.remote.Delete(ctx, assetID); err != nil && !errors.Is(err, ErrRemoteNotFound) {
		return err
	}
	return s.assets.Delete(ctx, assetID)
}

func (s *AssetService) Resolve(ctx context.Context, p auth.Principal, courseID, lectureID, assetID id.ID) (Playback, error) {
	a, err := s.assets.Find(ctx, assetID)
	if err != nil {
		return Playback{}, err
	}
	if a.CourseID != courseID {
		return Playback{}, ErrNotFound
	}
	if err := s.courses.CanReadLectureAsset(ctx, p, courseID, lectureID, assetID); err != nil {
		return Playback{}, err
	}
	r, err := s.remote.Get(ctx, assetID)
	if err != nil {
		return Playback{}, err
	}
	pb := Playback{ID: a.ID, Kind: a.Kind, Status: r.Status}
	if r.Status != StatusReady {
		return pb, nil
	}
	d, err := s.remote.Delivery(ctx, assetID)
	if err != nil {
		return Playback{}, err
	}
	pb.ExpiresAt = d.ExpiresAt
	switch a.Kind {
	case domain.KindVideo:
		if d.URL == "" {
			return Playback{}, fmt.Errorf("%w: ready video %s has no manifest URL", ErrRemoteUnavailable, assetID)
		}
		pb.URL, pb.DurationMs, pb.Width, pb.Height = d.URL, r.DurationMs, r.Width, r.Height
		if poster, ok := rendition(d, PosterRendition); ok {
			pb.PosterURL = poster.URL
		}
	case domain.KindImage:
		display, ok := rendition(d, DisplayVariant)
		if !ok || display.URL == "" {
			return Playback{}, fmt.Errorf("%w: ready image %s has no %s rendition", ErrRemoteUnavailable, assetID, DisplayVariant)
		}
		pb.URL, pb.Width, pb.Height = display.URL, display.Width, display.Height
	}
	return pb, nil
}

// managed loads the row and checks the caller manages its course. A caller who cannot
// manage it gets ErrNotFound so asset IDs cannot be probed.
func (s *AssetService) managed(ctx context.Context, p auth.Principal, assetID id.ID) (domain.Asset, error) {
	a, err := s.assets.Find(ctx, assetID)
	if err != nil {
		return domain.Asset{}, err
	}
	if err := s.courses.CanManage(ctx, p, a.CourseID); err != nil {
		if errors.Is(err, ErrForbidden) {
			return domain.Asset{}, ErrNotFound
		}
		return domain.Asset{}, err
	}
	return a, nil
}

func rendition(d Delivery, name string) (Rendition, bool) {
	for _, r := range d.Renditions {
		if r.Name == name {
			return r, true
		}
	}
	return Rendition{}, false
}
