package app

import (
	"context"

	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/contentblocks"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

// AssessmentCatalog reports the head revision of every quiz and exam of a course that is not
// deleted.
type AssessmentCatalog interface {
	Heads(ctx context.Context, courseID id.ID) ([]domain.AssessmentPin, error)
}

// workingBlocks returns each working-copy lecture's blocks, after authorizing p as a manager.
func workingBlocks(ctx context.Context, r Repos, p auth.Principal, courseID id.ID) (map[id.ID][]contentblocks.Block, error) {
	c, err := loadManaged(ctx, r, p, courseID)
	if err != nil {
		return nil, err
	}
	out := make(map[id.ID][]contentblocks.Block, len(c.Lectures))
	for _, l := range c.Lectures {
		if out[l.ID], err = r.Contents.ListBlocks(ctx, l.ID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// checkStoredRefs re-checks the media and quiz references of stored blocks, lecture by lecture.
func (s *CourseService) checkStoredRefs(ctx context.Context, courseID id.ID, blocks map[id.ID][]contentblocks.Block) (refErr, err error) {
	for lectureID, bs := range blocks {
		if refErr, err = checkAssetRefs(ctx, s.assets, courseID, blockAssetRefs(bs)); refErr != nil || err != nil {
			return refErr, err
		}
		if refErr, err = checkQuizRefs(ctx, s.quizzes, courseID, lectureID, blockQuizRefs(bs)); refErr != nil || err != nil {
			return refErr, err
		}
	}
	return nil, nil
}

// LivePins returns the live version's pins, and false when the course is not live. It applies
// no authorization: assessment serves students from it after its own read checks.
func (s *CourseService) LivePins(ctx context.Context, courseID id.ID) ([]domain.AssessmentPin, bool, error) {
	var pins []domain.AssessmentPin
	var live bool
	err := s.tx.RunInTx(ctx, func(r Repos) error {
		c, err := r.Courses.FindByID(ctx, courseID)
		if err != nil || !c.IsLive() {
			return err
		}
		live = true
		pins, err = r.Courses.ListVersionPins(ctx, courseID, c.Live.Number)
		return err
	})
	return pins, live, err
}

// VersionPins returns a published version's pins to the course's managers.
func (s *CourseService) VersionPins(ctx context.Context, p auth.Principal, courseID id.ID, number int) ([]domain.AssessmentPin, error) {
	var pins []domain.AssessmentPin
	err := s.tx.RunInTx(ctx, func(r Repos) error {
		c, err := loadManaged(ctx, r, p, courseID)
		if err != nil {
			return err
		}
		if number < 1 || number > c.LastVersion {
			return ErrNotFound
		}
		pins, err = r.Courses.ListVersionPins(ctx, courseID, number)
		return err
	})
	return pins, err
}

// BeginAssessmentEdit authorizes p to change the course's quizzes and exams and applies the
// course edit rule: frozen courses refuse, a published course reopens as a draft. lectureID zero
// skips the lecture check. For internal callers.
func (s *CourseService) BeginAssessmentEdit(ctx context.Context, p auth.Principal, courseID, lectureID id.ID) error {
	_, err := s.mutate(ctx, p, courseID, func(_ Repos, c *domain.Course) error {
		if !lectureID.IsZero() {
			if _, ok := c.Lecture(lectureID); !ok {
				return ErrNotFound
			}
		}
		return c.BeginEdit()
	})
	return err
}
