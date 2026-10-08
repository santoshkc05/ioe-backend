package httpapi

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

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
	httpserver.WriteJSON(w, http.StatusOK, toCatalogPageWire(page, q.Q))
}

// catalogQuery parses the catalog parameters. A parameter that is present must not be empty;
// q must not be blank. The service validates the values. Without q the cursor is a course ID;
// with q it is a search cursor bound to that q.
func catalogQuery(v url.Values) (app.CatalogQuery, error) {
	q := app.CatalogQuery{Limit: defaultCatalogLimit}
	for _, name := range []string{"level", "price", "limit", "cursor", "q", "category", "tag"} {
		if v.Has(name) && strings.TrimSpace(v.Get(name)) == "" {
			return q, fmt.Errorf("%w: %s must not be empty", app.ErrInvalidInput, name)
		}
	}
	q.Level = v.Get("level")
	q.Price = app.PriceFilter(v.Get("price"))
	q.Q = strings.TrimSpace(v.Get("q"))
	q.Category = v.Get("category")
	q.Tag = v.Get("tag")
	if s := v.Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil {
			return q, fmt.Errorf("%w: limit must be a number", app.ErrInvalidInput)
		}
		q.Limit = n
	}
	if s := v.Get("cursor"); s != "" {
		var err error
		if q.Q == "" {
			q.After, err = id.Parse(s)
		} else {
			q.After, q.AfterRank, err = decodeSearchCursor(s, q.Q)
		}
		if err != nil {
			return q, fmt.Errorf("%w: invalid cursor", app.ErrInvalidInput)
		}
	}
	return q, nil
}
