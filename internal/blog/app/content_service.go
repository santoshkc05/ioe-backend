package app

import (
	"github.com/santoshkc2200/ioe-backend/internal/platform/contentblocks"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

// ContentView is a post's draft content.
type ContentView struct {
	PostID          id.ID
	ContentRevision int64
	Blocks          []contentblocks.Block
}
