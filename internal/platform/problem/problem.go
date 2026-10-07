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
// Code is an extension member repeating Type for clients that read a `code` field.
type Problem struct {
	Type     string `json:"type"`
	Code     string `json:"code"`
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
		Type: typ, Code: typ, Title: title, Status: status, Detail: detail,
		Instance: logging.RequestID(r.Context()),
	})
}

// WriteWithExtensions sends a problem response with RFC 9457 extension members.
// Keys that collide with standard members or code are ignored.
func WriteWithExtensions(w http.ResponseWriter, r *http.Request, status int, typ, title, detail string, ext map[string]any) {
	body := make(map[string]any, len(ext)+6)
	for k, v := range ext {
		body[k] = v
	}
	body["type"], body["code"], body["title"], body["status"] = typ, typ, title, status
	delete(body, "detail")
	delete(body, "instance")
	if detail != "" {
		body["detail"] = detail
	}
	if id := logging.RequestID(r.Context()); id != "" {
		body["instance"] = id
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
