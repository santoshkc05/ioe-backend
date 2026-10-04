// Package id provides the Snowflake identifier used for every entity.
package id

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/bwmarrin/snowflake"
)

// ErrInvalid reports a malformed identifier.
var ErrInvalid = errors.New("invalid id")

// ID is a positive Snowflake identifier. It is a JSON string on the wire because
// JavaScript numbers cannot represent every 63-bit value.
type ID int64

// Parse reads a canonical decimal ID: digits only, no sign, no leading zero, positive.
func Parse(s string) (ID, error) {
	if s == "" || s[0] == '0' {
		return 0, fmt.Errorf("%w: %q", ErrInvalid, s)
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, fmt.Errorf("%w: %q", ErrInvalid, s)
		}
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: %q", ErrInvalid, s)
	}
	return ID(v), nil
}

func (i ID) String() string { return strconv.FormatInt(int64(i), 10) }

// IsZero reports whether i is unset.
func (i ID) IsZero() bool { return i == 0 }

func (i ID) MarshalJSON() ([]byte, error) { return strconv.AppendQuote(nil, i.String()), nil }

func (i *ID) UnmarshalJSON(b []byte) error {
	s, err := strconv.Unquote(string(b))
	if err != nil {
		return fmt.Errorf("%w: must be a JSON string", ErrInvalid)
	}
	v, err := Parse(s)
	if err != nil {
		return err
	}
	*i = v
	return nil
}

// Generator mints IDs. Each running process needs a distinct node ID.
type Generator struct{ node *snowflake.Node }

// NewGenerator returns a generator for nodeID, which must be in 0-1023.
func NewGenerator(nodeID int64) (*Generator, error) {
	n, err := snowflake.NewNode(nodeID)
	if err != nil {
		return nil, fmt.Errorf("snowflake node %d: %w", nodeID, err)
	}
	return &Generator{node: n}, nil
}

// New returns a new, time-ordered ID. Safe for concurrent use.
func (g *Generator) New() ID { return ID(g.node.Generate().Int64()) }
