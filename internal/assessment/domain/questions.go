package domain

import (
	"fmt"

	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

// checkUniqueIDs rejects a question or option ID used twice across questions.
func checkUniqueIDs(questions []Question) error {
	seen := make(map[id.ID]struct{})
	claim := func(v id.ID) error {
		if _, dup := seen[v]; dup {
			return fmt.Errorf("id %s is used twice", v)
		}
		seen[v] = struct{}{}
		return nil
	}
	for _, q := range questions {
		if err := claim(q.ID); err != nil {
			return err
		}
		for _, o := range q.Options {
			if err := claim(o.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

// questionIDs returns every question and option ID in questions.
func questionIDs(questions []Question) map[id.ID]struct{} {
	out := make(map[id.ID]struct{})
	for _, q := range questions {
		out[q.ID] = struct{}{}
		for _, o := range q.Options {
			out[o.ID] = struct{}{}
		}
	}
	return out
}

// CorrectOptionIDs returns the IDs of the question's correct options in option order.
func (q Question) CorrectOptionIDs() []id.ID {
	out := []id.ID{}
	for _, o := range q.Options {
		if o.IsCorrect {
			out = append(out, o.ID)
		}
	}
	return out
}
