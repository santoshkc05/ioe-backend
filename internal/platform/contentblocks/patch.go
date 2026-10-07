package contentblocks

import (
	"errors"
	"fmt"
	"sort"
)

var (
	ErrOrderDeleteOverlap = errors.New("contentblocks: a block cannot appear in both order and deletes")
	ErrBlockSetMismatch   = errors.New("contentblocks: order, upserts and deletes do not match the stored blocks")
)

const maxSetMismatchIDsInError = 10

// CheckPatchLists rejects a duplicate client block ID within upserts, order or deletes,
// and an ID listed in both order and deletes.
func CheckPatchLists(order, upserts, deletes []string) error {
	if hasDuplicate(upserts) || hasDuplicate(order) || hasDuplicate(deletes) {
		return ErrDuplicateClientBlockID
	}
	inOrder := toSet(order)
	for _, d := range deletes {
		if _, both := inOrder[d]; both {
			return ErrOrderDeleteOverlap
		}
	}
	return nil
}

// CheckBlockSet asserts set(order) == (existing ∪ upserts) − deletes. Deleting an ID the
// server does not have is a no-op, so a retried patch stays idempotent.
func CheckBlockSet(existing, order, upserts, deletes []string) error {
	deleted := toSet(deletes)
	expected := make(map[string]struct{}, len(existing)+len(upserts))
	for _, list := range [][]string{existing, upserts} {
		for _, k := range list {
			if _, gone := deleted[k]; !gone {
				expected[k] = struct{}{}
			}
		}
	}
	got := toSet(order)
	var diff []string
	for k := range got {
		if _, ok := expected[k]; !ok {
			diff = append(diff, k)
		}
	}
	for k := range expected {
		if _, ok := got[k]; !ok {
			diff = append(diff, k)
		}
	}
	if len(diff) == 0 {
		return nil
	}
	sort.Strings(diff)
	if len(diff) > maxSetMismatchIDsInError {
		diff = diff[:maxSetMismatchIDsInError]
	}
	return fmt.Errorf("%w: order has %d ids, expected %d, differing ids (up to %d): %v",
		ErrBlockSetMismatch, len(got), len(expected), maxSetMismatchIDsInError, diff)
}

func toSet(ids []string) map[string]struct{} {
	out := make(map[string]struct{}, len(ids))
	for _, k := range ids {
		out[k] = struct{}{}
	}
	return out
}

func hasDuplicate(ids []string) bool {
	return len(toSet(ids)) != len(ids)
}
