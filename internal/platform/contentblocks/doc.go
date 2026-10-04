// Package contentblocks holds the block-level value objects shared by every
// bounded context that owns ordered, block-structured content: title and
// text-body value objects, video/image references, flashcard decks, the
// block type enum, block construction/validation, and the JSONB payload
// codec used to persist a block. It imports nothing from any bounded
// context - courseauthoring and blog both depend on it, never the reverse.
package contentblocks
