package app

import (
	"fmt"

	"github.com/santoshkc2200/ioe-backend/internal/assessment/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

// buildQuestions validates question inputs. owned is nil on create, where any supplied ID is
// rejected; on update it holds the current question and option IDs, the only IDs the input may
// reuse. Omitted IDs are generated and omitted points mean 1.
func buildQuestions(ids *id.Generator, in []QuestionInput, owned map[id.ID]struct{}) ([]domain.Question, error) {
	if len(in) > domain.MaxQuestions {
		return nil, fmt.Errorf("%w: at most %d questions", ErrInvalidInput, domain.MaxQuestions)
	}
	resolve := func(field, raw string) (id.ID, error) {
		if raw == "" {
			return ids.New(), nil
		}
		v, err := id.Parse(raw)
		if err != nil {
			return 0, fmt.Errorf("%w: %s: %w", ErrInvalidInput, field, err)
		}
		if _, ok := owned[v]; !ok {
			return 0, fmt.Errorf("%w: %s %s does not belong here", ErrInvalidInput, field, raw)
		}
		return v, nil
	}
	questions := make([]domain.Question, len(in))
	for i, qi := range in {
		if len(qi.Options) > domain.MaxOptions {
			return nil, fmt.Errorf("%w: question %d has more than %d options", ErrInvalidInput, i+1, domain.MaxOptions)
		}
		qid, err := resolve("question id", qi.ID)
		if err != nil {
			return nil, err
		}
		var ref id.ID
		if qi.ReferenceLectureID != "" {
			if ref, err = id.Parse(qi.ReferenceLectureID); err != nil {
				return nil, fmt.Errorf("%w: reference_lecture_id: %w", ErrInvalidInput, err)
			}
		}
		opts := make([]domain.Option, len(qi.Options))
		for j, oi := range qi.Options {
			oid, err := resolve("option id", oi.ID)
			if err != nil {
				return nil, err
			}
			opts[j] = domain.Option{ID: oid, Label: oi.Label, IsCorrect: oi.IsCorrect}
		}
		points := qi.Points
		if points == 0 {
			points = 1
		}
		q, err := domain.NewQuestion(qid, qi.Prompt, domain.QuestionType(qi.Type), qi.Explanation, points, ref, opts)
		if err != nil {
			return nil, fmt.Errorf("%w: question %d: %w", ErrInvalidInput, i+1, err)
		}
		questions[i] = q
	}
	return questions, nil
}
