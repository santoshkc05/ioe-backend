// Package problem writes RFC 9457 problem+json responses.
package problem

import (
	"encoding/json"
	"net/http"

	"github.com/santoshkc2200/ioe-backend/internal/platform/logging"
)

// Shared problem type codes. Bounded contexts define their own codes alongside these.
const (
	TypeInvalidRequest       = "invalid_request"
	TypePayloadTooLarge      = "payload_too_large"
	TypeUnsupportedMediaType = "unsupported_media_type"
	TypeRateLimited          = "rate_limited"
	TypeOriginNotAllowed     = "origin_not_allowed"
	TypeNotFound             = "not_found"
	TypeMethodNotAllowed     = "method_not_allowed"
	TypeInternal             = "internal"
)

// Problem is an RFC 9457 problem details document. Instance carries the request ID.
type Problem struct {
	Type     string `json:"type"`
	Title    string `json:"title"`
	Status   int    `json:"status"`
	Detail   string `json:"detail,omitempty"`
	Instance string `json:"instance,omitempty"`
}

// Write sends a problem response.
func Write(w http.ResponseWriter, r *http.Request, status int, typ, title, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(Problem{
		Type: typ, Title: title, Status: status, Detail: detail,
		Instance: logging.RequestID(r.Context()),
	})
}
