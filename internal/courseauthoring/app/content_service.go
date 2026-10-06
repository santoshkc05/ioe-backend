package app

import (
	"context"
	"fmt"
	"sort"

	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/contentblocks"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

const (
	maxPatchOrderLen         = 500
	maxPatchUpsertsLen       = 200
	maxPatchDeletesLen       = 500
	maxSetMismatchIDsInError = 10
)

// ContentService reads and writes lecture block content.
type ContentService struct {
	tx          TxRunner
	ids         *id.Generator
	enrollments EnrollmentQuery
	assets      AssetCatalog
}

func NewContentService(tx TxRunner, ids *id.Generator, enrollments EnrollmentQuery, assets AssetCatalog) *ContentService {
	return &ContentService{tx: tx, ids: ids, enrollments: enrollments, assets: assets}
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

func (s *ContentService) Get(ctx context.Context, p auth.Principal, courseID, lectureID id.ID) (LectureContentView, error) {
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
		h, err := r.Contents.FindLecture(ctx, courseID, lectureID)
		if err != nil {
			return err
		}
		if !c.IsManagedBy(p) && !h.FreePreview && !enrolled {
			return ErrEnrollmentRequired
		}
		v, err = readView(ctx, r, h)
		return err
	})
	return v, err
}

// CheckAssetRead applies Get's access rules and returns ErrNotFound unless the lecture's blocks
// reference assetID. For internal callers.
func (s *ContentService) CheckAssetRead(ctx context.Context, p auth.Principal, courseID, lectureID, assetID id.ID) error {
	v, err := s.Get(ctx, p, courseID, lectureID)
	if err != nil {
		return err
	}
	if !blocksReference(v.Blocks, assetID) {
		return ErrNotFound
	}
	return nil
}

// lockEditable checks the caller manages an editable course and locks the lecture row.
func lockEditable(ctx context.Context, r Repos, p auth.Principal, courseID, lectureID id.ID) (LectureHeader, error) {
	c, err := loadManaged(ctx, r, p, courseID)
	if err != nil {
		return LectureHeader{}, err
	}
	if c.Status == domain.StatusArchived {
		return LectureHeader{}, domain.ErrCourseNotEditable
	}
	return r.Contents.FindLectureForUpdate(ctx, courseID, lectureID)
}

func (s *ContentService) Replace(ctx context.Context, p auth.Principal, courseID, lectureID id.ID, blocks []BlockInput, legacy LegacyContent) error {
	content, err := buildContent(s.ids, blocks, legacy)
	if err != nil {
		return err
	}
	refErr, err := checkAssetRefs(ctx, s.assets, courseID, inputAssetRefs(blocks))
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
	upserts, orderSet, deleteSet, err := indexPatch(in)
	if err != nil {
		return 0, err
	}
	refErr, err := checkAssetRefs(ctx, s.assets, courseID, inputAssetRefs(in.Upserts))
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
		existingIDs := make(map[string]struct{}, len(existing))
		for _, b := range existing {
			byClient[b.ClientBlockID()] = b
			existingIDs[b.ClientBlockID()] = struct{}{}
		}
		if err := validateBlockSetIdentity(existingIDs, upserts, deleteSet, orderSet); err != nil {
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

// indexPatch rejects duplicates within each list and overlap between order and deletes.
func indexPatch(in PatchInput) (map[string]BlockInput, map[string]struct{}, map[string]struct{}, error) {
	upserts := make(map[string]BlockInput, len(in.Upserts))
	for _, u := range in.Upserts {
		if err := contentblocks.ValidateClientBlockID(u.ClientBlockID); err != nil {
			return nil, nil, nil, fmt.Errorf("%w: %w", ErrInvalidInput, err)
		}
		if _, dup := upserts[u.ClientBlockID]; dup {
			return nil, nil, nil, ErrDuplicateClientBlockID
		}
		upserts[u.ClientBlockID] = u
	}
	orderSet := make(map[string]struct{}, len(in.Order))
	for _, o := range in.Order {
		if _, dup := orderSet[o]; dup {
			return nil, nil, nil, ErrDuplicateClientBlockID
		}
		orderSet[o] = struct{}{}
	}
	deleteSet := make(map[string]struct{}, len(in.Deletes))
	for _, d := range in.Deletes {
		if _, dup := deleteSet[d]; dup {
			return nil, nil, nil, ErrDuplicateClientBlockID
		}
		if _, both := orderSet[d]; both {
			return nil, nil, nil, ErrOrderDeleteOverlap
		}
		deleteSet[d] = struct{}{}
	}
	return upserts, orderSet, deleteSet, nil
}

// validateBlockSetIdentity asserts set(order) == (existing ∪ upserts) − deletes.
// Deleting an ID the server does not have is a no-op, so a retried patch stays idempotent.
func validateBlockSetIdentity(existing map[string]struct{}, upserts map[string]BlockInput, deletes, order map[string]struct{}) error {
	expected := make(map[string]struct{}, len(existing)+len(upserts))
	for k := range existing {
		if _, deleted := deletes[k]; !deleted {
			expected[k] = struct{}{}
		}
	}
	for k := range upserts {
		if _, deleted := deletes[k]; !deleted {
			expected[k] = struct{}{}
		}
	}
	if len(order) == len(expected) && subset(order, expected) {
		return nil
	}
	diff := symmetricDifference(order, expected)
	if len(diff) > maxSetMismatchIDsInError {
		diff = diff[:maxSetMismatchIDsInError]
	}
	return fmt.Errorf("%w: order has %d ids, expected %d, differing ids (up to %d): %v",
		ErrBlockSetMismatch, len(order), len(expected), maxSetMismatchIDsInError, diff)
}

func subset(a, b map[string]struct{}) bool {
	for k := range a {
		if _, ok := b[k]; !ok {
			return false
		}
	}
	return true
}

func symmetricDifference(a, b map[string]struct{}) []string {
	var out []string
	for k := range a {
		if _, ok := b[k]; !ok {
			out = append(out, k)
		}
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}
