package httpserver

import (
	"log/slog"
	"net/http"
	"strings"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/trace"

	"github.com/santoshkc2200/ioe-backend/internal/platform/problem"
)

const maxBodyBytes = 1 << 20

// Options configures NewRouter.
type Options struct {
	Logger         *slog.Logger
	AllowedOrigins []string
	ServiceName    string
}

// Router is an http.ServeMux whose unmatched requests become problem+json responses.
// Every pattern must include a method, for example "GET /v1/me".
type Router struct {
	*http.ServeMux
	unmatched []unmatchedRule
}

type unmatchedRule struct {
	prefix string
	mw     Middleware
}

// WrapUnmatched applies mw to 404 and 405 responses for request paths starting with prefix.
// Call it during setup only; it is not safe to call while serving.
func (r *Router) WrapUnmatched(prefix string, mw Middleware) {
	r.unmatched = append(r.unmatched, unmatchedRule{prefix: prefix, mw: mw})
}

// NewRouter returns the router for route registration and the handler to serve.
// The outer chain runs for every request, including 404 and 405.
func NewRouter(o Options) (*Router, http.Handler) {
	r := &Router{ServeMux: http.NewServeMux()}
	// otelhttp names the span with the formatter at start (no pattern matched
	// yet) and re-applies it after the handler runs, when ServeMux has set
	// req.Pattern. The formatter is therefore pattern-aware: a matched request
	// ends up named by its "METHOD /path" pattern, anything else "<METHOD> unmatched".
	traced := otelhttp.NewHandler(http.HandlerFunc(r.dispatch), o.ServiceName,
		otelhttp.WithSpanNameFormatter(func(_ string, req *http.Request) string {
			if req.Pattern != "" {
				return req.Pattern
			}
			return req.Method + " unmatched"
		}))
	h := Chain(traced,
		RequestID,
		Recover(o.Logger),
		AccessLog(o.Logger),
		SecurityHeaders,
		CORS(o.AllowedOrigins),
		BodyLimit(maxBodyBytes),
	)
	return r, h
}

// dispatch names the span after the matched pattern; unmatched requests keep the
// "<METHOD> unmatched" name and are answered with problem+json.
func (r *Router) dispatch(w http.ResponseWriter, req *http.Request) {
	if _, pattern := r.Handler(req); pattern != "" {
		trace.SpanFromContext(req.Context()).SetName(pattern)
		r.ServeHTTP(w, req)
		return
	}
	var h http.Handler = http.HandlerFunc(r.problemFallback)
	for _, rule := range r.unmatched {
		if strings.HasPrefix(req.URL.Path, rule.prefix) {
			h = rule.mw(h)
		}
	}
	h.ServeHTTP(w, req)
}

// problemFallback runs the mux's built-in 404/405 handler only to learn the status and
// Allow header, then writes problem+json instead of its plain-text body.
func (r *Router) problemFallback(w http.ResponseWriter, req *http.Request) {
	h, _ := r.Handler(req)
	capture := &statusCapture{header: http.Header{}}
	h.ServeHTTP(capture, req)
	if capture.status == http.StatusMethodNotAllowed {
		if allow := capture.header.Get("Allow"); allow != "" {
			w.Header().Set("Allow", allow)
		}
		problem.Write(w, req, http.StatusMethodNotAllowed, problem.TypeMethodNotAllowed, "Method Not Allowed", "")
		return
	}
	problem.Write(w, req, http.StatusNotFound, problem.TypeNotFound, "Not Found", "")
}

type statusCapture struct {
	header http.Header
	status int
}

func (c *statusCapture) Header() http.Header         { return c.header }
func (c *statusCapture) Write(b []byte) (int, error) { return len(b), nil }
func (c *statusCapture) WriteHeader(status int)      { c.status = status }
