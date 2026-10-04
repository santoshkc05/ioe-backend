package httpserver

import (
	"log/slog"
	"net/http"

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

// NewRouter returns the router for route registration and the handler to serve.
// The outer chain runs for every request, including 404 and 405. OpenTelemetry
// instrumentation runs inside the router so spans are named by route template.
func NewRouter(o Options) (*mux.Router, http.Handler) {
	r := mux.NewRouter()
	r.Use(otelmux.Middleware(o.ServiceName))
	r.NotFoundHandler = http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		problem.Write(w, req, http.StatusNotFound, problem.TypeNotFound, "Not Found", "")
	})
	r.MethodNotAllowedHandler = http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		problem.Write(w, req, http.StatusMethodNotAllowed, problem.TypeMethodNotAllowed, "Method Not Allowed", "")
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
