package httpserver

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"

	"github.com/santoshkc2200/ioe-backend/internal/platform/problem"
)

// DecodeJSON strictly decodes exactly one JSON value into dst. Requiring application/json
// forces browsers to send a CORS preflight, which blocks cross-site form posts.
// On failure it writes a problem response and returns false.
func DecodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mt != "application/json" {
		problem.Write(w, r, http.StatusUnsupportedMediaType, problem.TypeUnsupportedMediaType,
			"Unsupported Media Type", "Content-Type must be application/json")
		return false
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return decodeFailed(w, r, err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("trailing data")
		}
		return decodeFailed(w, r, err)
	}
	return true
}

func decodeFailed(w http.ResponseWriter, r *http.Request, err error) bool {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		problem.Write(w, r, http.StatusRequestEntityTooLarge, problem.TypePayloadTooLarge, "Payload Too Large", "")
		return false
	}
	problem.Write(w, r, http.StatusBadRequest, problem.TypeInvalidRequest, "Invalid Request",
		"request body must be a single JSON object with known fields")
	return false
}

// WriteJSON sends v as a JSON response.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
