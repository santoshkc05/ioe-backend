package app

import (
	"cmp"
	"context"
	"fmt"

	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/clock"
	"github.com/santoshkc2200/ioe-backend/internal/platform/contentblocks"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

// CourseService implements course structure, lifecycle and read use cases.
type CourseService struct {
	tx          TxRunner
	ids         *id.Generator
	clock       clock.Clock
	assets      AssetCatalog
	quizzes     QuizCatalog
	assessments AssessmentCatalog
}

func NewCourseService(tx TxRunner, ids *id.Generator, c clock.Clock, assets AssetCatalog, quizzes QuizCatalog, assessments AssessmentCatalog) *CourseService {
	return &CourseService{tx: tx, ids: ids, clock: c, assets: assets, quizzes: quizzes, assessments: assessments}
}

type CreateCourseInput struct {
	OwnerID     id.ID // zero means the caller
	Title       string
	Description string
}

type DetailsInput struct {
	Title, Description, Level, ThumbnailURL string
}

type AddLectureInput struct {
	Title  string
	Blocks []BlockInput
	Legacy LegacyContent
}

func newTitle(raw string) (contentblocks.Title, error) {
	t, err := contentblocks.NewTitle(raw)
	if err != nil {
		return contentblocks.Title{}, fmt.Errorf("%w: %w", ErrInvalidInput, err)
	}
	return t, nil
}

// visible reports whether p may read c at all. Managers read the working copy; everyone
// else reads the live version.
func visible(p auth.Principal, c *domain.Course) bool {
	return c.IsManagedBy(p) || c.IsLive()
}

// readsLive reports whether p reads c's live version: always for non-managers, and for
// managers when they ask for it.
func readsLive(p auth.Principal, c *domain.Course, wantLive bool) bool {
	return wantLive || !c.IsManagedBy(p)
}

// loadManaged returns the course for a write: ErrNotFound when the caller cannot see it,
// ErrForbidden when they can see it but do not manage it.
func loadManaged(ctx context.Context, r Repos, p auth.Principal, courseID id.ID) (domain.Course, error) {
	c, err := r.Courses.FindByID(ctx, courseID)
	if err != nil {
		return domain.Course{}, err
	}
	if !visible(p, &c) {
		return domain.Course{}, ErrNotFound
	}
	if !c.IsManagedBy(p) {
		return domain.Course{}, ErrForbidden
	}
	return c, nil
}

// mutate loads a managed course, applies fn, and saves it in one transaction.
func (s *CourseService) mutate(ctx context.Context, p auth.Principal, courseID id.ID, fn func(r Repos, c *domain.Course) error) (domain.Course, error) {
	var out domain.Course
	err := s.tx.RunInTx(ctx, func(r Repos) error {
		c, err := loadManaged(ctx, r, p, courseID)
		if err != nil {
			return err
		}
		if err := fn(r, &c); err != nil {
			return err
		}
		if err := r.Courses.Update(ctx, &c); err != nil {
			return err
		}
		out = c
		return nil
	})
	return out, err
}

func (s *CourseService) Create(ctx context.Context, p auth.Principal, in CreateCourseInput) (domain.Course, error) {
	if p.Role != auth.RoleInstructor && p.Role != auth.RoleRootAdmin {
		return domain.Course{}, ErrForbidden
	}
	if !in.OwnerID.IsZero() && in.OwnerID != p.UserID {
		return domain.Course{}, ErrForbidden
	}
	title, err := newTitle(in.Title)
	if err != nil {
		return domain.Course{}, err
	}
	c := domain.NewCourse(s.ids.New(), p.UserID, title, in.Description, s.clock.Now())
	err = s.tx.RunInTx(ctx, func(r Repos) error { return r.Courses.Insert(ctx, &c) })
	return c, err
}

// Get returns the working copy to managers and the live version to everyone else.
func (s *CourseService) Get(ctx context.Context, p auth.Principal, courseID id.ID) (domain.Course, error) {
	return s.get(ctx, p, courseID, false)
}

// GetLive returns the live version to anyone who may see the course.
func (s *CourseService) GetLive(ctx context.Context, p auth.Principal, courseID id.ID) (domain.Course, error) {
	return s.get(ctx, p, courseID, true)
}

func (s *CourseService) get(ctx context.Context, p auth.Principal, courseID id.ID, wantLive bool) (domain.Course, error) {
	var c domain.Course
	err := s.tx.RunInTx(ctx, func(r Repos) error {
		var err error
		c, err = r.Courses.FindByID(ctx, courseID)
		if err != nil {
			return err
		}
		if !visible(p, &c) {
			return ErrNotFound
		}
		if readsLive(p, &c, wantLive) {
			if !c.IsLive() {
				return ErrNotFound
			}
			c, err = r.Courses.FindVersion(ctx, courseID, c.Live.Number)
		}
		return err
	})
	return c, err
}

// CourseFacts is what other contexts may know about a course without a principal.
type CourseFacts struct {
	Published  bool
	Title      string
	Free       bool
	Price      domain.Price
	OwnerID    id.ID
	LectureIDs []id.ID // course order; empty, never nil, when the course has no lectures
}

// Facts returns a course's publication, title, price, ownership and lecture facts for internal
// callers, from the live version when the course is live and from the working copy
// otherwise. It applies no authorization and must not be exposed over HTTP.
func (s *CourseService) Facts(ctx context.Context, courseID id.ID) (CourseFacts, error) {
	var f CourseFacts
	err := s.tx.RunInTx(ctx, func(r Repos) error {
		c, err := r.Courses.FindByID(ctx, courseID)
		if err != nil {
			return err
		}
		live := c.IsLive()
		if live {
			if c, err = r.Courses.FindVersion(ctx, courseID, c.Live.Number); err != nil {
				return err
			}
		}
		lectureIDs := make([]id.ID, len(c.Lectures))
		for i, l := range c.Lectures {
			lectureIDs[i] = l.ID
		}
		f = CourseFacts{Published: live, Title: c.Title.String(), Free: c.Price.IsFree(), Price: c.Price, OwnerID: c.OwnerID, LectureIDs: lectureIDs}
		return nil
	})
	return f, err
}

// CheckManage returns nil when p manages the course and it is editable: ErrNotFound when
// p cannot see it, ErrForbidden when p does not manage it, domain.ErrCourseNotEditable when
// archived or awaiting a review decision. For internal callers.
func (s *CourseService) CheckManage(ctx context.Context, p auth.Principal, courseID id.ID) error {
	return s.tx.RunInTx(ctx, func(r Repos) error {
		_, err := loadEditable(ctx, r, p, courseID)
		return err
	})
}

// CheckManagerRead returns nil when p manages the course, archived included: ErrNotFound when p
// cannot see it, ErrForbidden when p does not manage it. For internal callers.
func (s *CourseService) CheckManagerRead(ctx context.Context, p auth.Principal, courseID id.ID) error {
	return s.tx.RunInTx(ctx, func(r Repos) error {
		_, err := loadManaged(ctx, r, p, courseID)
		return err
	})
}

// CheckLectureManage applies CheckManage and returns ErrNotFound when the lecture is not in
// the course. For internal callers.
func (s *CourseService) CheckLectureManage(ctx context.Context, p auth.Principal, courseID, lectureID id.ID) error {
	return s.tx.RunInTx(ctx, func(r Repos) error {
		c, err := loadEditable(ctx, r, p, courseID)
		if err != nil {
			return err
		}
		if _, ok := c.Lecture(lectureID); !ok {
			return ErrNotFound
		}
		return nil
	})
}

// loadEditable is loadManaged that also rejects courses that are not editable.
func loadEditable(ctx context.Context, r Repos, p auth.Principal, courseID id.ID) (domain.Course, error) {
	c, err := loadManaged(ctx, r, p, courseID)
	if err != nil {
		return domain.Course{}, err
	}
	if err := c.Editable(); err != nil {
		return domain.Course{}, err
	}
	return c, nil
}

func (s *CourseService) ListByOwner(ctx context.Context, p auth.Principal, ownerID id.ID) ([]domain.Course, error) {
	if p.UserID != ownerID && p.Role != auth.RoleRootAdmin {
		return nil, ErrForbidden
	}
	var cs []domain.Course
	err := s.tx.RunInTx(ctx, func(r Repos) error {
		var err error
		cs, err = r.Courses.ListByOwner(ctx, ownerID)
		return err
	})
	return cs, err
}

func (s *CourseService) UpdateDetails(ctx context.Context, p auth.Principal, courseID id.ID, in DetailsInput) error {
	title, err := newTitle(in.Title)
	if err != nil {
		return err
	}
	_, err = s.mutate(ctx, p, courseID, func(_ Repos, c *domain.Course) error {
		return c.UpdateDetails(title, in.Description, in.Level, in.ThumbnailURL, s.clock.Now())
	})
	return err
}

func (s *CourseService) SetPrice(ctx context.Context, p auth.Principal, courseID id.ID, amountMinor int64, currency string) (domain.Course, error) {
	price, err := domain.NewPrice(amountMinor, currency)
	if err != nil {
		return domain.Course{}, err
	}
	return s.mutate(ctx, p, courseID, func(_ Repos, c *domain.Course) error { return c.SetPrice(price, s.clock.Now()) })
}

// Publish snapshots the working copy as the next live version. A reviewed course pins the
// revisions captured at submit; a reviewer's direct publish pins the current heads.
func (s *CourseService) Publish(ctx context.Context, p auth.Principal, courseID id.ID) error {
	heads, err := s.assessments.Heads(ctx, courseID)
	if err != nil {
		return err
	}
	_, err = s.mutate(ctx, p, courseID, func(r Repos, c *domain.Course) error {
		reviewed := c.Status == domain.StatusInReview || c.Status == domain.StatusApproved
		now := s.clock.Now()
		if err := c.Publish(p.Role == auth.RoleRootAdmin, now); err != nil {
			return err
		}
		if err := r.Courses.InsertVersion(ctx, c, p.UserID); err != nil {
			return err
		}
		pins := heads
		if reviewed {
			if pins, err = r.Courses.ListSubmittedPins(ctx, c.ID); err != nil {
				return err
			}
		}
		if err := r.Courses.InsertVersionPins(ctx, c.ID, c.Live.Number, pins); err != nil {
			return err
		}
		return r.Events.Publish(ctx, domain.CoursePublished{CourseID: c.ID, OwnerID: c.OwnerID,
			PriceAmountMinor: c.Price.AmountMinor, PriceCurrency: c.Price.Currency, OccurredAt: now})
	})
	return err
}

// ListVersions returns the course's published versions, newest first, to its managers.
func (s *CourseService) ListVersions(ctx context.Context, p auth.Principal, courseID id.ID) ([]VersionSummary, error) {
	var out []VersionSummary
	err := s.tx.RunInTx(ctx, func(r Repos) error {
		if _, err := loadManaged(ctx, r, p, courseID); err != nil {
			return err
		}
		var err error
		out, err = r.Courses.ListVersions(ctx, courseID)
		return err
	})
	return out, err
}

// GetVersion returns one published version to the course's managers.
func (s *CourseService) GetVersion(ctx context.Context, p auth.Principal, courseID id.ID, number int) (domain.Course, error) {
	var c domain.Course
	err := s.tx.RunInTx(ctx, func(r Repos) error {
		if _, err := loadManaged(ctx, r, p, courseID); err != nil {
			return err
		}
		var err error
		c, err = r.Courses.FindVersion(ctx, courseID, number)
		return err
	})
	return c, err
}

// DiscardDraft throws away the working copy's changes and restores it, lecture content
// included, from the live version. Every restored lecture's content revision moves on, so
// an editor still holding the discarded content gets a revision conflict.
func (s *CourseService) DiscardDraft(ctx context.Context, p auth.Principal, courseID id.ID) (domain.Course, error) {
	var out domain.Course
	err := s.tx.RunInTx(ctx, func(r Repos) error {
		c, err := loadManaged(ctx, r, p, courseID)
		if err != nil {
			return err
		}
		if !c.IsLive() {
			return domain.ErrInvalidStatusTransition
		}
		live, err := r.Courses.FindVersion(ctx, courseID, c.Live.Number)
		if err != nil {
			return err
		}
		if err := c.DiscardDraft(live, s.clock.Now()); err != nil {
			return err
		}
		// Save the outline first so restored lectures exist before their blocks are written.
		if err := r.Courses.Update(ctx, &c); err != nil {
			return err
		}
		for _, l := range c.Lectures {
			blocks, err := r.Contents.ListVersionBlocks(ctx, courseID, c.Live.Number, l.ID)
			if err != nil {
				return err
			}
			if _, err := r.Contents.ReplaceBlocks(ctx, courseID, l.ID, blocks); err != nil {
				return err
			}
		}
		pins, err := r.Courses.ListVersionPins(ctx, courseID, c.Live.Number)
		if err != nil {
			return err
		}
		if err := r.Events.Publish(ctx, domain.DraftDiscarded{CourseID: courseID, ActorID: p.UserID,
			Pins: pins, OccurredAt: s.clock.Now()}); err != nil {
			return err
		}
		out = c
		return nil
	})
	return out, err
}

// Submit sends the course to review after re-checking every block reference, and captures the
// current quiz and exam revisions as what review approves. Any manager may submit.
func (s *CourseService) Submit(ctx context.Context, p auth.Principal, courseID id.ID) error {
	var blocks map[id.ID][]contentblocks.Block
	if err := s.tx.RunInTx(ctx, func(r Repos) error {
		var err error
		blocks, err = workingBlocks(ctx, r, p, courseID)
		return err
	}); err != nil {
		return err
	}
	if refErr, err := s.checkStoredRefs(ctx, courseID, blocks); refErr != nil || err != nil {
		return cmp.Or(err, refErr)
	}
	heads, err := s.assessments.Heads(ctx, courseID)
	if err != nil {
		return err
	}
	_, err = s.mutate(ctx, p, courseID, func(r Repos, c *domain.Course) error {
		rev, err := c.Submit(s.ids.New(), p.UserID, s.clock.Now())
		if err != nil {
			return err
		}
		if err := r.Courses.ReplaceSubmittedPins(ctx, courseID, heads); err != nil {
			return err
		}
		return r.Courses.InsertReview(ctx, rev)
	})
	return err
}

func (s *CourseService) Approve(ctx context.Context, p auth.Principal, courseID id.ID, note string) error {
	return s.review(ctx, p, courseID, func(r Repos, c *domain.Course) error {
		rev, err := c.Approve(s.ids.New(), p.UserID, note, s.clock.Now())
		if err != nil {
			return err
		}
		return r.Courses.InsertReview(ctx, rev)
	})
}

func (s *CourseService) RequestChanges(ctx context.Context, p auth.Principal, courseID id.ID, note string) error {
	return s.review(ctx, p, courseID, func(r Repos, c *domain.Course) error {
		rev, err := c.RequestChanges(s.ids.New(), p.UserID, note, s.clock.Now())
		if err != nil {
			return err
		}
		return r.Courses.InsertReview(ctx, rev)
	})
}

func (s *CourseService) Unpublish(ctx context.Context, p auth.Principal, courseID id.ID, note string) error {
	return s.review(ctx, p, courseID, func(r Repos, c *domain.Course) error {
		now := s.clock.Now()
		rev, err := c.Unpublish(s.ids.New(), p.UserID, note, now)
		if err != nil {
			return err
		}
		if err := r.Courses.InsertReview(ctx, rev); err != nil {
			return err
		}
		return r.Events.Publish(ctx, domain.CourseUnpublished{CourseID: c.ID, OwnerID: c.OwnerID, OccurredAt: now})
	})
}

// review is mutate restricted to reviewers. Only root admins review courses.
func (s *CourseService) review(ctx context.Context, p auth.Principal, courseID id.ID, fn func(r Repos, c *domain.Course) error) error {
	_, err := s.mutate(ctx, p, courseID, func(r Repos, c *domain.Course) error {
		if p.Role != auth.RoleRootAdmin {
			return ErrForbidden
		}
		return fn(r, c)
	})
	return err
}

// ListReviews returns the course's review trail, oldest first, to its managers.
func (s *CourseService) ListReviews(ctx context.Context, p auth.Principal, courseID id.ID) ([]domain.Review, error) {
	var out []domain.Review
	err := s.tx.RunInTx(ctx, func(r Repos) error {
		if _, err := loadManaged(ctx, r, p, courseID); err != nil {
			return err
		}
		var err error
		out, err = r.Courses.ListReviews(ctx, courseID)
		return err
	})
	return out, err
}

// ListInReview returns the review queue, longest waiting first. Root admins only.
func (s *CourseService) ListInReview(ctx context.Context, p auth.Principal) ([]domain.Course, error) {
	if p.Role != auth.RoleRootAdmin {
		return nil, ErrForbidden
	}
	var cs []domain.Course
	err := s.tx.RunInTx(ctx, func(r Repos) error {
		var err error
		cs, err = r.Courses.ListInReview(ctx)
		return err
	})
	return cs, err
}

func (s *CourseService) Archive(ctx context.Context, p auth.Principal, courseID id.ID) error {
	_, err := s.mutate(ctx, p, courseID, func(r Repos, c *domain.Course) error {
		now := s.clock.Now()
		if err := c.Archive(now); err != nil {
			return err
		}
		return r.Events.Publish(ctx, domain.CourseArchived{CourseID: c.ID, OwnerID: c.OwnerID, OccurredAt: now})
	})
	return err
}

func (s *CourseService) AddSection(ctx context.Context, p auth.Principal, courseID id.ID, rawTitle string) (domain.Course, error) {
	title, err := newTitle(rawTitle)
	if err != nil {
		return domain.Course{}, err
	}
	return s.mutate(ctx, p, courseID, func(_ Repos, c *domain.Course) error { return c.AddSection(s.ids.New(), title, s.clock.Now()) })
}

func (s *CourseService) RenameSection(ctx context.Context, p auth.Principal, courseID, sectionID id.ID, rawTitle string) error {
	title, err := newTitle(rawTitle)
	if err != nil {
		return err
	}
	_, err = s.mutate(ctx, p, courseID, func(_ Repos, c *domain.Course) error { return c.RenameSection(sectionID, title, s.clock.Now()) })
	return err
}

func (s *CourseService) RemoveSection(ctx context.Context, p auth.Principal, courseID, sectionID id.ID) error {
	_, err := s.mutate(ctx, p, courseID, func(_ Repos, c *domain.Course) error { return c.RemoveSection(sectionID, s.clock.Now()) })
	return err
}

// AddLecture adds the lecture, saves the course, then writes the initial blocks in the same transaction.
func (s *CourseService) AddLecture(ctx context.Context, p auth.Principal, courseID id.ID, in AddLectureInput) (domain.Course, error) {
	title, err := newTitle(in.Title)
	if err != nil {
		return domain.Course{}, err
	}
	content, err := buildContent(s.ids, in.Blocks, in.Legacy)
	if err != nil {
		return domain.Course{}, err
	}
	lectureID := s.ids.New()
	// Ask before opening the transaction (see ContentService.Get); report only after authorizing.
	refErr, err := checkBlockRefs(ctx, s.assets, s.quizzes, courseID, lectureID, in.Blocks)
	if err != nil {
		return domain.Course{}, err
	}
	var out domain.Course
	err = s.tx.RunInTx(ctx, func(r Repos) error {
		c, err := loadManaged(ctx, r, p, courseID)
		if err != nil {
			return err
		}
		if refErr != nil {
			return refErr
		}
		if err := c.AddLecture(lectureID, title, content.HasText(), content.HasVideo(), s.clock.Now()); err != nil {
			return err
		}
		if err := r.Courses.Update(ctx, &c); err != nil {
			return err
		}
		if blocks := content.Blocks(); len(blocks) > 0 {
			if _, err := r.Contents.ReplaceBlocks(ctx, c.ID, lectureID, blocks); err != nil {
				return err
			}
		}
		out = c
		return nil
	})
	return out, err
}

func (s *CourseService) RenameLecture(ctx context.Context, p auth.Principal, courseID, lectureID id.ID, rawTitle string) error {
	title, err := newTitle(rawTitle)
	if err != nil {
		return err
	}
	_, err = s.mutate(ctx, p, courseID, func(_ Repos, c *domain.Course) error { return c.RenameLecture(lectureID, title, s.clock.Now()) })
	return err
}

func (s *CourseService) RemoveLecture(ctx context.Context, p auth.Principal, courseID, lectureID id.ID) error {
	_, err := s.mutate(ctx, p, courseID, func(_ Repos, c *domain.Course) error { return c.RemoveLecture(lectureID, s.clock.Now()) })
	return err
}

func (s *CourseService) ReorderLectures(ctx context.Context, p auth.Principal, courseID id.ID, lectureIDs []id.ID) error {
	_, err := s.mutate(ctx, p, courseID, func(_ Repos, c *domain.Course) error { return c.ReorderLectures(lectureIDs, s.clock.Now()) })
	return err
}

func (s *CourseService) MoveLectureToSection(ctx context.Context, p auth.Principal, courseID, lectureID, sectionID id.ID) error {
	_, err := s.mutate(ctx, p, courseID, func(_ Repos, c *domain.Course) error {
		return c.MoveLectureToSection(lectureID, sectionID, s.clock.Now())
	})
	return err
}

func (s *CourseService) SetLectureFreePreview(ctx context.Context, p auth.Principal, courseID, lectureID id.ID, freePreview bool) error {
	_, err := s.mutate(ctx, p, courseID, func(_ Repos, c *domain.Course) error {
		return c.SetLectureFreePreview(lectureID, freePreview, s.clock.Now())
	})
	return err
}
