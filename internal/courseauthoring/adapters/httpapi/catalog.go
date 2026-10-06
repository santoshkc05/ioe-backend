package httpapi

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/app"
	"github.com/santoshkc2200/ioe-backend/internal/platform/httpserver"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

const defaultCatalogLimit = 20

func (h *Handler) listCatalog(w http.ResponseWriter, r *http.Request) {
	q, err := catalogQuery(r.URL.Query())
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	page, err := h.courses.ListPublished(r.Context(), q)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toCatalogPageWire(page))
}

// catalogQuery parses the catalog parameters. A parameter that is present must not be empty;
// the service validates the values.
func catalogQuery(v url.Values) (app.CatalogQuery, error) {
	q := app.CatalogQuery{Limit: defaultCatalogLimit}
	for _, name := range []string{"level", "price", "limit", "cursor"} {
		if v.Has(name) && v.Get(name) == "" {
			return q, fmt.Errorf("%w: %s must not be empty", app.ErrInvalidInput, name)
		}
	}
	q.Level = v.Get("level")
	q.Price = app.PriceFilter(v.Get("price"))
	if s := v.Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil {
			return q, fmt.Errorf("%w: limit must be a number", app.ErrInvalidInput)
		}
		q.Limit = n
	}
	if s := v.Get("cursor"); s != "" {
		after, err := id.Parse(s)
		if err != nil {
			return q, fmt.Errorf("%w: invalid cursor", app.ErrInvalidInput)
		}
		q.After = after
	}
	return q, nil
}
