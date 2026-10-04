package httpserver

import (
	"context"
	"net/http"
	"time"

	"github.com/gorilla/mux"
)

// MountHealth registers /healthz (process alive) and /readyz (dependencies reachable).
func MountHealth(r *mux.Router, ready func(context.Context) error) {
	r.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}).Methods(http.MethodGet)
	r.HandleFunc("/readyz", func(w http.ResponseWriter, req *http.Request) {
		ctx, cancel := context.WithTimeout(req.Context(), 2*time.Second)
		defer cancel()
		if err := ready(ctx); err != nil {
			WriteJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
			return
		}
		WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}).Methods(http.MethodGet)
}
