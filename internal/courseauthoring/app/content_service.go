package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/contentblocks"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

const (
	maxPatchOrderLen   = 500
	maxPatchUpsertsLen = 200
	maxPatchDeletesLen = 500
)

// ContentService reads and writes lecture block content.
type ContentService struct {
	tx          TxRunner
	ids         *id.Generator
	enrollments EnrollmentQuery
	assets      AssetCatalog
	quizzes     QuizCatalog
}

func NewContentService(tx TxRunner, ids *id.Generator, enrollments EnrollmentQuery, assets AssetCatalog, quizzes QuizCatalog) *ContentService {
	return &ContentService{tx: tx, ids: ids, enrollments: enrollments, assets: assets, quizzes: quizzes}
}

// LectureContentView is a lecture's full content.
type LectureContentView struct {
	LectureID       id.ID
	CourseID        id.ID
	Title           string
	FreePreview     bool
	ContentRevision int64
	Blocks          []contentblocks.Block
}

type PatchInput struct {
	BaseRevision *int64
	Order        []string
	Upserts      []BlockInput
	Deletes      []string
}

func readView(ctx context.Context, r Repos, h LectureHeader) (LectureContentView, error) {
	blocks, err := r.Contents.ListBlocks(ctx, h.LectureID)
	if err != nil {
		return LectureContentView{}, err
	}
	return LectureContentView{LectureID: h.LectureID, CourseID: h.CourseID, Title: h.Title,
		FreePreview: h.FreePreview, ContentRevision: h.ContentRevision, Blocks: blocks}, nil
}

// Get returns a lecture's working content to managers and its live content to everyone else.
func (s *ContentService) Get(ctx context.Context, p auth.Principal, courseID, lectureID id.ID) (LectureContentView, error) {
	return s.get(ctx, p, courseID, lectureID, false)
}

// GetLive returns a lecture's live content under Get's access rules.
func (s *ContentService) GetLive(ctx context.Context, p auth.Principal, courseID, lectureID id.ID) (LectureContentView, error) {
	return s.get(ctx, p, courseID, lectureID, true)
}

func (s *ContentService) get(ctx context.Context, p auth.Principal, courseID, lectureID id.ID, wantLive bool) (LectureContentView, error) {
	// Ask before opening the transaction: the lookup takes its own pool connection, and
	// asking from inside would hold two per read and can exhaust the pool under load.
	enrolled, err := s.enrollments.IsActivelyEnrolled(ctx, courseID, p.UserID)
	if err != nil {
		return LectureContentView{}, err
	}
	var v LectureContentView
	err = s.tx.RunInTx(ctx, func(r Repos) error {
		c, err := r.Courses.FindByID(ctx, courseID)
		if err != nil {
			return err
		}
		if !visible(p, &c) {
			return ErrNotFound
		}
		if !readsLive(p, &c, wantLive) {
			h, err := r.Contents.FindLecture(ctx, courseID, lectureID)
			if err != nil {
				return err
			}
			v, err = readView(ctx, r, h)
			return err
		}
		if !c.IsLive() {
			return ErrNotFound
		}
		h, err := r.Contents.FindVersionLecture(ctx, courseID, c.Live.Number, lectureID)
		if err != nil {
			return err
		}
		if !c.IsManagedBy(p) && !h.FreePreview && !enrolled {
			return ErrEnrollmentRequired
		}
		blocks, err := r.Contents.ListVersionBlocks(ctx, courseID, c.Live.Number, lectureID)
		v = LectureContentView{LectureID: h.LectureID, CourseID: h.CourseID, Title: h.Title,
			FreePreview: h.FreePreview, Blocks: blocks}
		return err
	})
	return v, err
}

// CheckAssetRead applies Get's access rules and returns ErrNotFound unless the lecture's blocks
// reference assetID. For internal callers.
// Managers may read assets of the working copy or the live version.
func (s *ContentService) CheckAssetRead(ctx context.Context, p auth.Principal, courseID, lectureID, assetID id.ID) error {
	v, err := s.Get(ctx, p, courseID, lectureID)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	if err == nil && blocksReference(v.Blocks, assetID) {
		return nil
	}
	// Get serves non-managers the live version already; a manager may be previewing it.
	live, liveErr := s.GetLive(ctx, p, courseID, lectureID)
	if liveErr != nil || !blocksReference(live.Blocks, assetID) {
		return ErrNotFound
	}
	return nil
}

// CheckLectureRead applies Get's access rules: ErrNotFound or ErrEnrollmentRequired. For
// internal callers.
func (s *ContentService) CheckLectureRead(ctx context.Context, p auth.Principal, courseID, lectureID id.ID) error {
	_, err := s.Get(ctx, p, courseID, lectureID)
	return err
}

// lockEditable checks the caller manages an editable course and locks the lecture row.
// The course stays locked so it cannot be submitted or archived until the write commits.
// A write to a published course reopens its working copy as a draft.
func lockEditable(ctx context.Context, r Repos, p auth.Principal, courseID, lectureID id.ID) (LectureHeader, error) {
	if err := r.Courses.LockForUpdate(ctx, courseID); err != nil {
		return LectureHeader{}, err
	}
	c, err := loadEditable(ctx, r, p, courseID)
	if err != nil {
		return LectureHeader{}, err
	}
	h, err := r.Contents.FindLectureForUpdate(ctx, courseID, lectureID)
	if err != nil {
		return LectureHeader{}, err
	}
	if c.Status == domain.StatusPublished {
		if err := c.BeginEdit(); err != nil {
			return LectureHeader{}, err
		}
		if err := r.Courses.Update(ctx, &c); err != nil {
			return LectureHeader{}, err
		}
	}
	return h, nil
}

func (s *ContentService) Replace(ctx context.Context, p auth.Principal, courseID, lectureID id.ID, blocks []BlockInput, legacy LegacyContent) error {
	content, err := buildContent(s.ids, blocks, legacy)
	if err != nil {
		return err
	}
	refErr, err := checkBlockRefs(ctx, s.assets, s.quizzes, courseID, lectureID, blocks)
	if err != nil {
		return err
	}
	return s.tx.RunInTx(ctx, func(r Repos) error {
		if _, err := lockEditable(ctx, r, p, courseID, lectureID); err != nil {
			return err
		}
		if refErr != nil {
			return refErr
		}
		_, err := r.Contents.ReplaceBlocks(ctx, courseID, lectureID, content.Blocks())
		return err
	})
}

func (s *ContentService) Patch(ctx context.Context, p auth.Principal, courseID, lectureID id.ID, in PatchInput) (int64, error) {
	if in.BaseRevision == nil {
		return 0, ErrRevisionRequired
	}
	if len(in.Order) > maxPatchOrderLen || len(in.Upserts) > maxPatchUpsertsLen || len(in.Deletes) > maxPatchDeletesLen {
		return 0, ErrPatchTooLarge
	}
	upserts, upsertIDs, err := indexPatch(in)
	if err != nil {
		return 0, err
	}
	refErr, err := checkBlockRefs(ctx, s.assets, s.quizzes, courseID, lectureID, in.Upserts)
	if err != nil {
		return 0, err
	}
	var rev int64
	err = s.tx.RunInTx(ctx, func(r Repos) error {
		h, err := lockEditable(ctx, r, p, courseID, lectureID)
		if err != nil {
			return err
		}
		if refErr != nil {
			return refErr
		}
		if h.ContentRevision != *in.BaseRevision {
			current, err := readView(ctx, r, h)
			if err != nil {
				return err
			}
			return &RevisionConflictError{Current: current}
		}
		existing, err := r.Contents.ListBlocks(ctx, lectureID)
		if err != nil {
			return err
		}
		byClient := make(map[string]contentblocks.Block, len(existing))
		existingIDs := make([]string, 0, len(existing))
		for _, b := range existing {
			byClient[b.ClientBlockID()] = b
			existingIDs = append(existingIDs, b.ClientBlockID())
		}
		if err := contentblocks.CheckBlockSet(existingIDs, in.Order, upsertIDs, in.Deletes); err != nil {
			return err
		}
		resulting := make([]contentblocks.Block, 0, len(in.Order))
		var changed []contentblocks.Block
		for i, cid := range in.Order {
			input, isUpsert := upserts[cid]
			if !isUpsert {
				b := byClient[cid]
				resulting = append(resulting, b.WithIdentity(b.ID(), cid, i))
				continue
			}
			blockID := s.ids.New()
			if prev, ok := byClient[cid]; ok {
				blockID = prev.ID() // edits keep the server ID for life
			}
			b, err := buildBlock(blockID, cid, i, input)
			if err != nil {
				return fmt.Errorf("%w: %w", ErrInvalidInput, err)
			}
			resulting = append(resulting, b)
			changed = append(changed, b)
		}
		if _, err := domain.NewLectureContent(resulting); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidInput, err)
		}
		rev, err = r.Contents.ApplyPatch(ctx, courseID, lectureID, *in.BaseRevision,
			BlockWritePlan{Upserts: changed, Deletes: in.Deletes, Order: in.Order})
		return err
	})
	return rev, err
}

// indexPatch validates upsert client IDs and the patch lists, and indexes upserts by client ID.
func indexPatch(in PatchInput) (map[string]BlockInput, []string, error) {
	ids := make([]string, len(in.Upserts))
	for i, u := range in.Upserts {
		if err := contentblocks.ValidateClientBlockID(u.ClientBlockID); err != nil {
			return nil, nil, fmt.Errorf("%w: %w", ErrInvalidInput, err)
		}
		ids[i] = u.ClientBlockID
	}
	if err := contentblocks.CheckPatchLists(in.Order, ids, in.Deletes); err != nil {
		return nil, nil, err
	}
	upserts := make(map[string]BlockInput, len(in.Upserts))
	for _, u := range in.Upserts {
		upserts[u.ClientBlockID] = u
	}
	return upserts, ids, nil
}
