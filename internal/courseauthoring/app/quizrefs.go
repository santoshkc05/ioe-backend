package app

import (
	"context"
	"fmt"

	"github.com/santoshkc2200/ioe-backend/internal/platform/contentblocks"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

// QuizCatalog reports the lecture of each given quiz that belongs to courseID. Quizzes that do
// not exist or belong to another course are absent from the result.
type QuizCatalog interface {
	Lectures(ctx context.Context, courseID id.ID, quizIDs []id.ID) (map[id.ID]id.ID, error)
}

type quizRef struct {
	id            id.ID
	clientBlockID string
}

// inputQuizRefs lists the quizzes the given inputs reference. IDs that do not parse are
// skipped; block building rejects them as invalid input.
func inputQuizRefs(in []BlockInput) []quizRef {
	var refs []quizRef
	for _, b := range in {
		if contentblocks.BlockType(b.Type) != contentblocks.BlockTypeQuiz {
			continue
		}
		if v, err := id.Parse(b.QuizID); err == nil {
			refs = append(refs, quizRef{id: v, clientBlockID: b.ClientBlockID})
		}
	}
	return refs
}

// checkQuizRefs has checkAssetRefs' contract. Every referenced quiz must belong to lectureID,
// the lecture the blocks are written to.
func checkQuizRefs(ctx context.Context, catalog QuizCatalog, courseID, lectureID id.ID, refs []quizRef) (refErr, err error) {
	if len(refs) == 0 {
		return nil, nil
	}
	ids := make([]id.ID, len(refs))
	for i, r := range refs {
		ids[i] = r.id
	}
	lectures, err := catalog.Lectures(ctx, courseID, ids)
	if err != nil {
		return nil, err
	}
	for _, r := range refs {
		if lectures[r.id] != lectureID {
			return fmt.Errorf("%w: block %q references quiz %s, which is not a quiz of this lecture",
				ErrInvalidQuizReference, r.clientBlockID, r.id), nil
		}
	}
	return nil, nil
}

// checkBlockRefs checks the media and quiz references of blocks written to lectureID.
func checkBlockRefs(ctx context.Context, assets AssetCatalog, quizzes QuizCatalog, courseID, lectureID id.ID, blocks []BlockInput) (refErr, err error) {
	refErr, err = checkAssetRefs(ctx, assets, courseID, inputAssetRefs(blocks))
	if refErr != nil || err != nil {
		return refErr, err
	}
	return checkQuizRefs(ctx, quizzes, courseID, lectureID, inputQuizRefs(blocks))
}

// blockQuizRefs lists the quizzes stored blocks reference.
func blockQuizRefs(blocks []contentblocks.Block) []quizRef {
	var refs []quizRef
	for _, b := range blocks {
		if b.Type() == contentblocks.BlockTypeQuiz {
			refs = append(refs, quizRef{id: b.QuizID(), clientBlockID: b.ClientBlockID()})
		}
	}
	return refs
}
