package httpapi

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"

	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

var errForeignCursor = errors.New("cursor belongs to another search")

// queryHash ties a search cursor to the search text it was issued for.
func queryHash(q string) string {
	sum := sha256.Sum256([]byte(q))
	return hex.EncodeToString(sum[:8])
}

// encodeSearchCursor encodes the last row of a search page as base64url of
// "<id>:<rank>:<hash>". The rank is formatted to round-trip a float32 exactly, so the next
// page's (rank, id) comparison sees the same value the database computed.
func encodeSearchCursor(after id.ID, rank float32, q string) string {
	raw := after.String() + ":" + strconv.FormatFloat(float64(rank), 'g', -1, 32) + ":" + queryHash(q)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeSearchCursor(s, q string) (id.ID, float32, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return 0, 0, err
	}
	parts := strings.Split(string(raw), ":")
	if len(parts) != 3 {
		return 0, 0, errForeignCursor
	}
	if parts[2] != queryHash(q) {
		return 0, 0, errForeignCursor
	}
	after, err := id.Parse(parts[0])
	if err != nil {
		return 0, 0, err
	}
	rank, err := strconv.ParseFloat(parts[1], 32)
	if err != nil {
		return 0, 0, err
	}
	return after, float32(rank), nil
}
