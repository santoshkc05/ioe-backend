// Package enrollment adapts enrollment facts for course authoring. Until the
// enrollment context exists, nobody is enrolled.
package enrollment

import (
	"context"

	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

// Deny reports every user as not enrolled.
type Deny struct{}

func (Deny) IsActivelyEnrolled(context.Context, id.ID, id.ID) (bool, error) { return false, nil }
