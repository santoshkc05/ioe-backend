package httpapi

import (
	"encoding/json"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/app"
	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/contentblocks"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

type priceWire struct {
	AmountMinor int64  `json:"amount_minor"`
	Currency    string `json:"currency"`
}

type sectionWire struct {
	ID    id.ID  `json:"id"`
	Title string `json:"title"`
	Order int    `json:"order"`
}

type lectureWire struct {
	ID          id.ID  `json:"id"`
	SectionID   *id.ID `json:"section_id,omitempty"`
	Title       string `json:"title"`
	HasText     bool   `json:"has_text"`
	HasVideo    bool   `json:"has_video"`
	FreePreview bool   `json:"free_preview"`
	Order       int    `json:"order"`
}

type courseWire struct {
	ID           id.ID         `json:"id"`
	OwnerID      id.ID         `json:"owner_id"`
	Title        string        `json:"title"`
	Description  string        `json:"description"`
	Status       string        `json:"status"`
	Price        priceWire     `json:"price"`
	IsFree       bool          `json:"is_free"`
	Sections     []sectionWire `json:"sections"`
	Lectures     []lectureWire `json:"lectures"`
	ThumbnailURL string        `json:"thumbnail_url"`
	Level        string        `json:"level"`
	CreatedAt    time.Time     `json:"created_at"`
	UpdatedAt    time.Time     `json:"updated_at"`
}

type coursePageWire struct {
	Courses []courseWire `json:"courses"`
	Total   int          `json:"total"`
}

func toCourseWire(c domain.Course) courseWire {
	w := courseWire{ID: c.ID, OwnerID: c.OwnerID, Title: c.Title.String(), Description: c.Description,
		Status: string(c.Status), Price: priceWire{c.Price.AmountMinor, c.Price.Currency}, IsFree: c.Price.IsFree(),
		Sections: make([]sectionWire, 0, len(c.Sections)), Lectures: make([]lectureWire, 0, len(c.Lectures)),
		ThumbnailURL: c.ThumbnailURL, Level: c.Level, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt}
	for _, s := range c.Sections {
		w.Sections = append(w.Sections, sectionWire{ID: s.ID, Title: s.Title.String(), Order: s.Order})
	}
	for _, l := range c.Lectures {
		lw := lectureWire{ID: l.ID, Title: l.Title.String(), HasText: l.HasText, HasVideo: l.HasVideo, FreePreview: l.FreePreview, Order: l.Order}
		if !l.SectionID.IsZero() {
			sid := l.SectionID
			lw.SectionID = &sid
		}
		w.Lectures = append(w.Lectures, lw)
	}
	return w
}

type createCourseRequest struct {
	OwnerID     *id.ID `json:"owner_id"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

type updateDetailsRequest struct {
	Title        string `json:"title"`
	Description  string `json:"description"`
	ThumbnailURL string `json:"thumbnail_url"`
	Level        string `json:"level"`
}

type setPriceRequest struct {
	AmountMinor int64  `json:"amount_minor"`
	Currency    string `json:"currency"`
}

type titleRequest struct {
	Title string `json:"title"`
}

type addLectureRequest struct {
	Title           string `json:"title"`
	TextBody        string `json:"text_body"`
	VideoURL        string `json:"video_url"`
	VideoDurationMs int64  `json:"video_duration_ms"`
}

type reorderLecturesRequest struct {
	LectureIDs []id.ID `json:"lecture_ids"`
}

type moveLectureRequest struct {
	SectionID string `json:"section_id"` // "" means unsectioned
}

type freePreviewRequest struct {
	FreePreview bool `json:"free_preview"`
}

type replaceContentRequest struct {
	Blocks          []contentBlockRequest `json:"blocks"`
	TextBody        string                `json:"text_body"`
	VideoURL        string                `json:"video_url"`
	VideoDurationMs int64                 `json:"video_duration_ms"`
}

type patchContentRequest struct {
	BaseRevision *int64                `json:"base_revision"`
	Order        []string              `json:"order"`
	Upserts      []contentBlockRequest `json:"upserts"`
	Deletes      []string              `json:"deletes"`
}

// contentBlockRequest deliberately has no position field: DecodeJSON rejects unknown
// fields, and order comes only from the patch's order list.
type contentBlockRequest struct {
	ClientBlockID string               `json:"client_block_id"`
	Type          string               `json:"type"`
	Body          string               `json:"body"`
	URL           string               `json:"url"`
	DurationMs    int64                `json:"duration_ms"`
	QuizID        string               `json:"quiz_id"`
	MediaAssetID  string               `json:"media_asset_id"`
	Alt           string               `json:"alt"`
	Caption       string               `json:"caption"`
	Layout        string               `json:"layout"`
	Alignment     string               `json:"alignment"`
	Rotation      int                  `json:"rotation"`
	Width         int                  `json:"width"`
	Height        int                  `json:"height"`
	Mode          string               `json:"mode"`
	Title         string               `json:"title"`
	Cards         []flashcardCardInput `json:"cards"`
}

type flashcardCardInput struct {
	ClientCardID string `json:"client_card_id"`
	Front        string `json:"front"`
	Back         string `json:"back"`
	Hint         string `json:"hint"`
	MediaAssetID string `json:"media_asset_id"`
	Alignment    string `json:"alignment"`
	Rotation     int    `json:"rotation"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
}

// toContentBlockInputs returns nil for an absent list and a non-nil empty slice for [].
func toContentBlockInputs(blocks []contentBlockRequest) []app.BlockInput {
	if blocks == nil {
		return nil
	}
	inputs := make([]app.BlockInput, len(blocks))
	for i, b := range blocks {
		cards := make([]app.FlashcardCardInput, len(b.Cards))
		for j, c := range b.Cards {
			cards[j] = app.FlashcardCardInput{ClientCardID: c.ClientCardID, Front: c.Front, Back: c.Back, Hint: c.Hint,
				MediaAssetID: c.MediaAssetID, Alignment: c.Alignment, Rotation: c.Rotation, Width: c.Width, Height: c.Height}
		}
		inputs[i] = app.BlockInput{ClientBlockID: b.ClientBlockID, Type: b.Type, Body: b.Body, URL: b.URL,
			DurationMs: b.DurationMs, QuizID: b.QuizID, MediaAssetID: b.MediaAssetID, Alt: b.Alt, Caption: b.Caption,
			Layout: b.Layout, Alignment: b.Alignment, Rotation: b.Rotation, Width: b.Width, Height: b.Height,
			Mode: b.Mode, Title: b.Title, Cards: cards}
	}
	return inputs
}

type flashcardCardWire struct {
	ClientCardID string `json:"client_card_id,omitempty"`
	Front        string `json:"front"`
	Back         string `json:"back"`
	Hint         string `json:"hint,omitempty"`
	MediaAssetID string `json:"media_asset_id,omitempty"`
	Alignment    string `json:"alignment,omitempty"`
	Rotation     int    `json:"rotation,omitempty"`
	Width        int    `json:"width,omitempty"`
	Height       int    `json:"height,omitempty"`
}

type contentBlockWire struct {
	ID            string              `json:"id"`
	ClientBlockID string              `json:"client_block_id"`
	Type          string              `json:"type"`
	Position      int                 `json:"position"`
	Body          string              `json:"body,omitempty"`
	URL           string              `json:"url,omitempty"`
	DurationMs    int64               `json:"duration_ms,omitempty"`
	QuizID        string              `json:"quiz_id,omitempty"`
	MediaAssetID  string              `json:"media_asset_id,omitempty"`
	Alt           string              `json:"alt,omitempty"`
	Caption       string              `json:"caption,omitempty"`
	Layout        string              `json:"layout,omitempty"`
	Alignment     string              `json:"alignment,omitempty"`
	Rotation      int                 `json:"rotation,omitempty"`
	Width         int                 `json:"width,omitempty"`
	Height        int                 `json:"height,omitempty"`
	Mode          string              `json:"mode,omitempty"`
	Title         string              `json:"title,omitempty"`
	Cards         []flashcardCardWire `json:"cards,omitempty"`
}

type lectureContentWire struct {
	LectureID       id.ID              `json:"lecture_id"`
	CourseID        id.ID              `json:"course_id"`
	Title           string             `json:"title"`
	TextBody        string             `json:"text_body"`
	VideoURL        string             `json:"video_url"`
	VideoDurationMs int64              `json:"video_duration_ms"`
	FreePreview     bool               `json:"free_preview"`
	ContentRevision int64              `json:"content_revision"`
	Blocks          []contentBlockWire `json:"blocks"`
}

// optionalID renders a zero ID as "" so omitempty drops it.
func optionalID(v id.ID) string {
	if v.IsZero() {
		return ""
	}
	return v.String()
}

func toContentBlockWire(b contentblocks.Block) contentBlockWire {
	w := contentBlockWire{ID: b.ID().String(), ClientBlockID: b.ClientBlockID(), Type: string(b.Type()),
		Position: b.Position(), QuizID: optionalID(b.QuizID())}
	if text, ok := b.Text(); ok {
		w.Body = text.String()
	}
	if video, ok := b.Video(); ok {
		w.URL = video.URL()
		w.DurationMs = video.Duration().Milliseconds()
		w.MediaAssetID = optionalID(video.MediaAssetID())
	}
	if image, ok := b.Image(); ok {
		w.URL = image.URL()
		w.MediaAssetID = optionalID(image.MediaAssetID())
		w.Alt, w.Caption, w.Layout, w.Alignment = image.Alt(), image.Caption(), image.Layout(), image.Alignment()
		w.Rotation, w.Width, w.Height = image.Rotation(), image.Width(), image.Height()
	}
	if deck, ok := b.Deck(); ok {
		w.Mode, w.Title = deck.Mode(), deck.Title()
		cards := deck.Cards()
		w.Cards = make([]flashcardCardWire, len(cards))
		for i, c := range cards {
			w.Cards[i] = flashcardCardWire{ClientCardID: c.ClientCardID, Front: c.Front, Back: c.Back, Hint: c.Hint,
				MediaAssetID: optionalID(c.MediaAssetID), Alignment: c.Alignment, Rotation: c.Rotation, Width: c.Width, Height: c.Height}
		}
	}
	return w
}

func toLectureContentWire(v app.LectureContentView) lectureContentWire {
	w := lectureContentWire{LectureID: v.LectureID, CourseID: v.CourseID, Title: v.Title, FreePreview: v.FreePreview,
		ContentRevision: v.ContentRevision, Blocks: make([]contentBlockWire, 0, len(v.Blocks))}
	textSeen, videoSeen := false, false
	for _, b := range v.Blocks {
		w.Blocks = append(w.Blocks, toContentBlockWire(b))
		if text, ok := b.Text(); ok && !textSeen {
			w.TextBody, textSeen = text.String(), true
		}
		if video, ok := b.Video(); ok && !videoSeen {
			w.VideoURL, w.VideoDurationMs, videoSeen = video.URL(), video.Duration().Milliseconds(), true
		}
	}
	return w
}

// lectureContentConflictExt renders the winner's content as problem+json extension members.
func lectureContentConflictExt(v app.LectureContentView) map[string]any {
	raw, err := json.Marshal(toLectureContentWire(v))
	if err != nil {
		return nil
	}
	var ext map[string]any
	if err := json.Unmarshal(raw, &ext); err != nil {
		return nil
	}
	return ext
}

type courseSummaryWire struct {
	ID           id.ID     `json:"id"`
	OwnerID      id.ID     `json:"owner_id"`
	Title        string    `json:"title"`
	Description  string    `json:"description"`
	Level        string    `json:"level"`
	ThumbnailURL string    `json:"thumbnail_url"`
	Price        priceWire `json:"price"`
	IsFree       bool      `json:"is_free"`
	LectureCount int       `json:"lecture_count"`
	SectionCount int       `json:"section_count"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type catalogPageWire struct {
	Courses    []courseSummaryWire `json:"courses"`
	NextCursor string              `json:"next_cursor,omitempty"`
}

func toCatalogPageWire(p app.CatalogPage) catalogPageWire {
	w := catalogPageWire{Courses: make([]courseSummaryWire, 0, len(p.Courses))}
	for _, c := range p.Courses {
		w.Courses = append(w.Courses, courseSummaryWire{
			ID: c.ID, OwnerID: c.OwnerID, Title: c.Title, Description: c.Description,
			Level: c.Level, ThumbnailURL: c.ThumbnailURL,
			Price: priceWire{c.Price.AmountMinor, c.Price.Currency}, IsFree: c.Price.IsFree(),
			LectureCount: c.LectureCount, SectionCount: c.SectionCount,
			CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
		})
	}
	if p.Next != 0 {
		w.NextCursor = p.Next.String()
	}
	return w
}
