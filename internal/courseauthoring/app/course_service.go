package app

import (
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
	tx      TxRunner
	ids     *id.Generator
	clock   clock.Clock
	assets  AssetCatalog
	quizzes QuizCatalog
}

func NewCourseService(tx TxRunner, ids *id.Generator, c clock.Clock, assets AssetCatalog, quizzes QuizCatalog) *CourseService {
	return &CourseService{tx: tx, ids: ids, clock: c, assets: assets, quizzes: quizzes}
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

// visible reports whether p may read c at all.
func visible(p auth.Principal, c *domain.Course) bool {
	return c.IsManagedBy(p) || c.Status == domain.StatusPublished
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

func (s *CourseService) Get(ctx context.Context, p auth.Principal, courseID id.ID) (domain.Course, error) {
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
		return nil
	})
	return c, err
}

// CourseFacts is what other contexts may know about a course without a principal.
type CourseFacts struct {
	Published  bool
	Free       bool
	Price      domain.Price
	OwnerID    id.ID
	LectureIDs []id.ID // course order; empty, never nil, when the course has no lectures
}

// Facts returns a course's publication, price, ownership and lecture facts for internal
// callers. It applies no authorization and must not be exposed over HTTP.
func (s *CourseService) Facts(ctx context.Context, courseID id.ID) (CourseFacts, error) {
	var f CourseFacts
	err := s.tx.RunInTx(ctx, func(r Repos) error {
		c, err := r.Courses.FindByID(ctx, courseID)
		if err != nil {
			return err
		}
		lectureIDs := make([]id.ID, len(c.Lectures))
		for i, l := range c.Lectures {
			lectureIDs[i] = l.ID
		}
		f = CourseFacts{Published: c.Status == domain.StatusPublished, Free: c.Price.IsFree(), Price: c.Price, OwnerID: c.OwnerID, LectureIDs: lectureIDs}
		return nil
	})
	return f, err
}

// CheckManage returns nil when p manages the course and it is not archived: ErrNotFound when
// p cannot see it, ErrForbidden when p does not manage it, domain.ErrCourseNotEditable when
// archived. For internal callers.
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

// loadEditable is loadManaged that also rejects archived courses.
func loadEditable(ctx context.Context, r Repos, p auth.Principal, courseID id.ID) (domain.Course, error) {
	c, err := loadManaged(ctx, r, p, courseID)
	if err != nil {
		return domain.Course{}, err
	}
	if c.Status == domain.StatusArchived {
		return domain.Course{}, domain.ErrCourseNotEditable
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

func (s *CourseService) Publish(ctx context.Context, p auth.Principal, courseID id.ID) error {
	_, err := s.mutate(ctx, p, courseID, func(r Repos, c *domain.Course) error {
		now := s.clock.Now()
		if err := c.Publish(now); err != nil {
			return err
		}
		return r.Events.Publish(ctx, domain.CoursePublished{CourseID: c.ID, OwnerID: c.OwnerID,
			PriceAmountMinor: c.Price.AmountMinor, PriceCurrency: c.Price.Currency, OccurredAt: now})
	})
	return err
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
