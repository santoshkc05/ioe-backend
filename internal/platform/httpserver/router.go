package httpserver

import (
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/gorilla/mux"
	"go.opentelemetry.io/contrib/instrumentation/github.com/gorilla/mux/otelmux"

	"github.com/santoshkc2200/ioe-backend/internal/platform/problem"
)

const maxBodyBytes = 1 << 20

// Options configures NewRouter.
type Options struct {
	Logger         *slog.Logger
	AllowedOrigins []string
	ServiceName    string
}

var standardMethods = []string{
	http.MethodGet,
	http.MethodHead,
	http.MethodPost,
	http.MethodPut,
	http.MethodPatch,
	http.MethodDelete,
	http.MethodConnect,
	http.MethodOptions,
	http.MethodTrace,
}

// AllowedMethods probes r with standard HTTP methods to find which methods match req's path.
func AllowedMethods(r *mux.Router, req *http.Request) []string {
	var allowed []string
	for _, m := range standardMethods {
		probe := req.Clone(req.Context())
		probe.Method = m
		var match mux.RouteMatch
		if r.Match(probe, &match) && match.Route != nil {
			if methods, err := match.Route.GetMethods(); err == nil && slices.Contains(methods, m) {
				allowed = append(allowed, m)
			}
		}
	}
	return allowed
}

func methodNotAllowedHandler(r *mux.Router) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if allowed := AllowedMethods(r, req); len(allowed) > 0 {
			w.Header().Set("Allow", strings.Join(allowed, ", "))
		}
		problem.Write(w, req, http.StatusMethodNotAllowed, problem.TypeMethodNotAllowed, "Method Not Allowed", "")
	}
}

// NewRouter returns the router for route registration and the handler to serve.
// The outer chain runs for every request, including 404 and 405. OpenTelemetry
// instrumentation runs inside the router so spans are named by route template.
func NewRouter(o Options) (*mux.Router, http.Handler) {
	r := mux.NewRouter()
	r.Use(otelmux.Middleware(o.ServiceName))
	r.MethodNotAllowedHandler = methodNotAllowedHandler(r)
	r.NotFoundHandler = http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if allowed := AllowedMethods(r, req); len(allowed) > 0 {
			r.MethodNotAllowedHandler.ServeHTTP(w, req)
			return
		}
		problem.Write(w, req, http.StatusNotFound, problem.TypeNotFound, "Not Found", "")
	})
	h := Chain(r,
		RequestID,
		Recover(o.Logger),
		AccessLog(o.Logger),
		SecurityHeaders,
		CORS(o.AllowedOrigins),
		BodyLimit(maxBodyBytes),
	)
	return r, h
}
