package domain

import (
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/contentblocks"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

type Status string

const (
	StatusDraft            Status = "draft"
	StatusInReview         Status = "in_review"
	StatusApproved         Status = "approved"
	StatusChangesRequested Status = "changes_requested"
	StatusPublished        Status = "published"
	StatusArchived         Status = "archived"
)

type Section struct {
	ID    id.ID
	Title contentblocks.Title
	Order int
}

// Lecture is lecture metadata. HasText/HasVideo are hydrated from stored blocks.
type Lecture struct {
	ID          id.ID
	SectionID   id.ID // zero when unsectioned
	Title       contentblocks.Title
	FreePreview bool
	Order       int
	HasText     bool
	HasVideo    bool
}

// LiveVersion identifies the published snapshot readers are served. Number is 0 when the
// course is not live.
type LiveVersion struct {
	Number      int
	PublishedAt time.Time
}

// Course is the authoring aggregate. It holds structure only; block content is
// stored and versioned per lecture outside the aggregate.
type Course struct {
	ID           id.ID
	OwnerID      id.ID
	Title        contentblocks.Title
	Description  string
	Level        string
	ThumbnailURL string
	Price        Price
	Status       Status
	Sections     []Section   // ordered by Order
	Lectures     []Lecture   // ordered by Order, dense from 0
	SubmittedAt  time.Time   // zero until first submitted for review
	ReviewedAt   time.Time   // zero until first reviewed
	ReviewNote   string      // the latest reviewer note; cleared on submit and approve
	LastVersion  int         // number of the latest published version; 0 before the first publish
	Live         LiveVersion // the version readers are served; zero when not live
	Version      int64       // optimistic-concurrency token; 0 until first insert
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

func NewCourse(courseID, ownerID id.ID, title contentblocks.Title, description string, now time.Time) Course {
	return Course{ID: courseID, OwnerID: ownerID, Title: title, Description: description,
		Status: StatusDraft, CreatedAt: now, UpdatedAt: now}
}

// IsManagedBy reports whether p may edit, publish or archive the course.
func (c *Course) IsManagedBy(p auth.Principal) bool {
	return p.Role == auth.RoleRootAdmin || p.UserID == c.OwnerID
}

// Editable returns ErrCourseNotEditable when the course is archived or frozen for a
// review decision (in_review, approved), so a reviewer never approves content that
// changes underneath them.
func (c *Course) Editable() error {
	switch c.Status {
	case StatusArchived, StatusInReview, StatusApproved:
		return ErrCourseNotEditable
	}
	return nil
}

// BeginEdit is Editable for a change about to be made. Editing a published course reopens
// its working copy as a draft; readers keep the live version until the next publish.
func (c *Course) BeginEdit() error {
	if err := c.Editable(); err != nil {
		return err
	}
	if c.Status == StatusPublished {
		c.Status = StatusDraft
	}
	return nil
}

// IsLive reports whether readers are served a published version.
func (c *Course) IsLive() bool { return c.Live.Number > 0 }

// HasDraftChanges reports whether the working copy differs from the live version.
func (c *Course) HasDraftChanges() bool { return c.IsLive() && c.Status != StatusPublished }

func (c *Course) UpdateDetails(title contentblocks.Title, description, level, thumbnailURL string, now time.Time) error {
	if err := c.BeginEdit(); err != nil {
		return err
	}
	switch level {
	case "", "beginner", "intermediate", "advanced":
	default:
		return ErrInvalidLevel
	}
	if thumbnailURL != "" {
		u, err := url.Parse(thumbnailURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return ErrInvalidThumbnailURL
		}
	}
	c.Title, c.Description, c.Level, c.ThumbnailURL, c.UpdatedAt = title, description, level, thumbnailURL, now
	return nil
}

func (c *Course) SetPrice(p Price, now time.Time) error {
	if err := c.BeginEdit(); err != nil {
		return err
	}
	c.Price, c.UpdatedAt = p, now
	return nil
}

func (c *Course) Section(sectionID id.ID) (Section, bool) {
	i := slices.IndexFunc(c.Sections, func(s Section) bool { return s.ID == sectionID })
	if i < 0 {
		return Section{}, false
	}
	return c.Sections[i], true
}

func (c *Course) Lecture(lectureID id.ID) (Lecture, bool) {
	i := c.lectureIndex(lectureID)
	if i < 0 {
		return Lecture{}, false
	}
	return c.Lectures[i], true
}

func (c *Course) lectureIndex(lectureID id.ID) int {
	return slices.IndexFunc(c.Lectures, func(l Lecture) bool { return l.ID == lectureID })
}

func (c *Course) AddSection(sectionID id.ID, title contentblocks.Title, now time.Time) error {
	if err := c.BeginEdit(); err != nil {
		return err
	}
	if c.hasSectionTitle(title, 0) {
		return ErrDuplicateSectionTitle
	}
	c.Sections = append(c.Sections, Section{ID: sectionID, Title: title, Order: len(c.Sections)})
	c.UpdatedAt = now
	return nil
}

func (c *Course) RenameSection(sectionID id.ID, title contentblocks.Title, now time.Time) error {
	if err := c.BeginEdit(); err != nil {
		return err
	}
	i := slices.IndexFunc(c.Sections, func(s Section) bool { return s.ID == sectionID })
	if i < 0 {
		return ErrSectionNotFound
	}
	if c.hasSectionTitle(title, sectionID) {
		return ErrDuplicateSectionTitle
	}
	c.Sections[i].Title, c.UpdatedAt = title, now
	return nil
}

// RemoveSection deletes the section and moves its lectures to unsectioned.
func (c *Course) RemoveSection(sectionID id.ID, now time.Time) error {
	if err := c.BeginEdit(); err != nil {
		return err
	}
	i := slices.IndexFunc(c.Sections, func(s Section) bool { return s.ID == sectionID })
	if i < 0 {
		return ErrSectionNotFound
	}
	c.Sections = slices.Delete(c.Sections, i, i+1)
	for j := range c.Sections {
		c.Sections[j].Order = j
	}
	for j := range c.Lectures {
		if c.Lectures[j].SectionID == sectionID {
			c.Lectures[j].SectionID = 0
		}
	}
	c.UpdatedAt = now
	return nil
}

func (c *Course) hasSectionTitle(title contentblocks.Title, except id.ID) bool {
	return slices.ContainsFunc(c.Sections, func(s Section) bool { return s.ID != except && s.Title == title })
}

// AddLecture appends an unsectioned lecture. hasText/hasVideo describe its initial content.
func (c *Course) AddLecture(lectureID id.ID, title contentblocks.Title, hasText, hasVideo bool, now time.Time) error {
	if err := c.BeginEdit(); err != nil {
		return err
	}
	c.Lectures = append(c.Lectures, Lecture{ID: lectureID, Title: title, Order: len(c.Lectures), HasText: hasText, HasVideo: hasVideo})
	c.UpdatedAt = now
	return nil
}

func (c *Course) RenameLecture(lectureID id.ID, title contentblocks.Title, now time.Time) error {
	return c.updateLecture(lectureID, now, func(l *Lecture) error { l.Title = title; return nil })
}

func (c *Course) SetLectureFreePreview(lectureID id.ID, freePreview bool, now time.Time) error {
	return c.updateLecture(lectureID, now, func(l *Lecture) error { l.FreePreview = freePreview; return nil })
}

// MoveLectureToSection moves a lecture into sectionID, or to unsectioned when sectionID is zero.
func (c *Course) MoveLectureToSection(lectureID, sectionID id.ID, now time.Time) error {
	if !sectionID.IsZero() {
		if _, ok := c.Section(sectionID); !ok {
			return ErrSectionNotFound
		}
	}
	return c.updateLecture(lectureID, now, func(l *Lecture) error { l.SectionID = sectionID; return nil })
}

func (c *Course) updateLecture(lectureID id.ID, now time.Time, fn func(*Lecture) error) error {
	if err := c.BeginEdit(); err != nil {
		return err
	}
	i := c.lectureIndex(lectureID)
	if i < 0 {
		return ErrLectureNotFound
	}
	if err := fn(&c.Lectures[i]); err != nil {
		return err
	}
	c.UpdatedAt = now
	return nil
}

func (c *Course) RemoveLecture(lectureID id.ID, now time.Time) error {
	if err := c.BeginEdit(); err != nil {
		return err
	}
	i := c.lectureIndex(lectureID)
	if i < 0 {
		return ErrLectureNotFound
	}
	c.Lectures = slices.Delete(c.Lectures, i, i+1)
	c.renumberLectures()
	c.UpdatedAt = now
	return nil
}

// ReorderLectures sets the course-wide lecture order. ordered must name every lecture once.
func (c *Course) ReorderLectures(ordered []id.ID, now time.Time) error {
	if err := c.BeginEdit(); err != nil {
		return err
	}
	if len(ordered) != len(c.Lectures) {
		return ErrInvalidLectureOrder
	}
	next := make([]Lecture, 0, len(ordered))
	seen := make(map[id.ID]struct{}, len(ordered))
	for _, lid := range ordered {
		i := c.lectureIndex(lid)
		if _, dup := seen[lid]; dup || i < 0 {
			return ErrInvalidLectureOrder
		}
		seen[lid] = struct{}{}
		next = append(next, c.Lectures[i])
	}
	c.Lectures = next
	c.renumberLectures()
	c.UpdatedAt = now
	return nil
}

func (c *Course) renumberLectures() {
	for i := range c.Lectures {
		c.Lectures[i].Order = i
	}
}

// Submit sends a draft, or a course sent back with changes requested, to review.
func (c *Course) Submit(reviewID, actorID id.ID, now time.Time) (Review, error) {
	if c.Status != StatusDraft && c.Status != StatusChangesRequested {
		return Review{}, ErrInvalidStatusTransition
	}
	if len(c.Lectures) == 0 {
		return Review{}, ErrCourseHasNoLectures
	}
	c.Status, c.SubmittedAt, c.ReviewNote, c.UpdatedAt = StatusInReview, now, "", now
	return c.review(reviewID, actorID, DecisionSubmitted, "", now), nil
}

// Approve accepts a course under review. note is optional and kept only in the trail.
func (c *Course) Approve(reviewID, reviewerID id.ID, note string, now time.Time) (Review, error) {
	if c.Status != StatusInReview {
		return Review{}, ErrInvalidStatusTransition
	}
	note, err := reviewNote(note, false)
	if err != nil {
		return Review{}, err
	}
	c.Status, c.ReviewedAt, c.ReviewNote, c.UpdatedAt = StatusApproved, now, "", now
	return c.review(reviewID, reviewerID, DecisionApproved, note, now), nil
}

// RequestChanges sends a course under review back to its author with a required note.
func (c *Course) RequestChanges(reviewID, reviewerID id.ID, note string, now time.Time) (Review, error) {
	if c.Status != StatusInReview {
		return Review{}, ErrInvalidStatusTransition
	}
	return c.sendBack(reviewID, reviewerID, DecisionChangesRequested, note, now)
}

// Unpublish takes the live version off the catalog and sends the course back to its
// author with a required note. It must be resubmitted and approved before it goes live again.
func (c *Course) Unpublish(reviewID, reviewerID id.ID, note string, now time.Time) (Review, error) {
	if !c.IsLive() || c.Status == StatusArchived {
		return Review{}, ErrInvalidStatusTransition
	}
	r, err := c.sendBack(reviewID, reviewerID, DecisionUnpublished, note, now)
	if err == nil {
		c.Live = LiveVersion{}
	}
	return r, err
}

func (c *Course) sendBack(reviewID, reviewerID id.ID, d ReviewDecision, note string, now time.Time) (Review, error) {
	note, err := reviewNote(note, true)
	if err != nil {
		return Review{}, err
	}
	c.Status, c.ReviewedAt, c.ReviewNote, c.UpdatedAt = StatusChangesRequested, now, note, now
	return c.review(reviewID, reviewerID, d, note, now), nil
}

func (c *Course) review(reviewID, actorID id.ID, d ReviewDecision, note string, now time.Time) Review {
	return Review{ID: reviewID, CourseID: c.ID, ActorID: actorID, Decision: d, Note: note, CreatedAt: now}
}

func reviewNote(raw string, required bool) (string, error) {
	note := strings.TrimSpace(raw)
	if required && note == "" {
		return "", ErrReviewNoteRequired
	}
	if utf8.RuneCountInString(note) > MaxReviewNoteRunes {
		return "", ErrReviewNoteTooLong
	}
	return note, nil
}

// Publish snapshots the working copy as the next live version. Authors publish only
// approved courses; a reviewer may publish without review.
func (c *Course) Publish(byReviewer bool, now time.Time) error {
	switch c.Status {
	case StatusApproved:
	case StatusDraft, StatusChangesRequested, StatusInReview:
		if !byReviewer {
			if c.Status == StatusInReview {
				return ErrInvalidStatusTransition
			}
			return ErrApprovalRequired
		}
		if len(c.Lectures) == 0 {
			return ErrCourseHasNoLectures
		}
	default:
		return ErrInvalidStatusTransition
	}
	c.LastVersion++
	c.Status, c.Live, c.UpdatedAt = StatusPublished, LiveVersion{Number: c.LastVersion, PublishedAt: now}, now
	return nil
}

// DiscardDraft restores the working copy's details and outline from live, the course's
// live version, and returns it to published. Only a draft or a course with changes
// requested may be discarded; lecture content is restored by the caller.
func (c *Course) DiscardDraft(live Course, now time.Time) error {
	if !c.IsLive() || (c.Status != StatusDraft && c.Status != StatusChangesRequested) {
		return ErrInvalidStatusTransition
	}
	c.Title, c.Description, c.Level, c.ThumbnailURL, c.Price = live.Title, live.Description, live.Level, live.ThumbnailURL, live.Price
	c.Sections = append([]Section(nil), live.Sections...)
	c.Lectures = append([]Lecture(nil), live.Lectures...)
	c.Status, c.ReviewNote, c.UpdatedAt = StatusPublished, "", now
	return nil
}

// Archive is terminal: an archived course rejects every further change.
func (c *Course) Archive(now time.Time) error {
	if c.Status == StatusArchived {
		return ErrInvalidStatusTransition
	}
	c.Status, c.Live, c.UpdatedAt = StatusArchived, LiveVersion{}, now
	return nil
}
