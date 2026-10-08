package httpapi

import (
	"encoding/base64"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/blog/app"
	"github.com/santoshkc2200/ioe-backend/internal/platform/httpserver"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
	"github.com/santoshkc2200/ioe-backend/internal/platform/problem"
)

var errBadCursor = errors.New("cursor is malformed")

// EncodeCursor renders c as base64url of "<first_published_at UnixMicro>:<post_id>".
// Microseconds match PostgreSQL's timestamp precision, so no row is skipped between pages.
func EncodeCursor(c app.Cursor) string {
	raw := strconv.FormatInt(c.FirstPublishedAt.UnixMicro(), 10) + ":" + c.PostID.String()
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// DecodeCursor parses EncodeCursor's output; an empty string means the first page.
func DecodeCursor(s string) (*app.Cursor, error) {
	if s == "" {
		return nil, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, errBadCursor
	}
	micros, postID, ok := strings.Cut(string(raw), ":")
	if !ok {
		return nil, errBadCursor
	}
	us, err := strconv.ParseInt(micros, 10, 64)
	if err != nil {
		return nil, errBadCursor
	}
	pid, err := id.Parse(postID)
	if err != nil {
		return nil, errBadCursor
	}
	return &app.Cursor{FirstPublishedAt: time.UnixMicro(us).UTC(), PostID: pid}, nil
}

// pageQuery parses cursor and limit; a missing limit is 0, the service default.
func pageQuery(w http.ResponseWriter, r *http.Request) (*app.Cursor, int, bool) {
	q := r.URL.Query()
	after, err := DecodeCursor(q.Get("cursor"))
	if err != nil {
		problem.Write(w, r, http.StatusBadRequest, "invalid_input", "Invalid Input", err.Error())
		return nil, 0, false
	}
	limit := 0
	if s := q.Get("limit"); s != "" {
		if limit, err = strconv.Atoi(s); err != nil {
			problem.Write(w, r, http.StatusBadRequest, "invalid_input", "Invalid Input", "limit must be an integer")
			return nil, 0, false
		}
	}
	return after, limit, true
}

func nextCursor(c *app.Cursor) *string {
	if c == nil {
		return nil
	}
	s := EncodeCursor(*c)
	return &s
}

func (h *Handler) publicList(w http.ResponseWriter, r *http.Request) {
	after, limit, ok := pageQuery(w, r)
	if !ok {
		return
	}
	page, err := h.public.List(r.Context(), r.URL.Query().Get("tag"), after, limit)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	out := cardPageWire{Items: make([]cardWire, 0, len(page.Items)), NextCursor: nextCursor(page.Next)}
	for _, c := range page.Items {
		out.Items = append(out.Items, toCardWire(c))
	}
	httpserver.WriteJSON(w, http.StatusOK, out)
}

func (h *Handler) publicGet(w http.ResponseWriter, r *http.Request) {
	p, err := h.public.GetBySlug(r.Context(), r.PathValue("slug"))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toPublicPostWire(p))
}

func (h *Handler) publicTags(w http.ResponseWriter, r *http.Request) {
	tags, err := h.public.Tags(r.Context())
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	out := make([]tagCountWire, 0, len(tags))
	for _, t := range tags {
		out = append(out, tagCountWire{Tag: t.Tag, Count: t.Count})
	}
	httpserver.WriteJSON(w, http.StatusOK, out)
}

func (h *Handler) publicIndex(w http.ResponseWriter, r *http.Request) {
	after, limit, ok := pageQuery(w, r)
	if !ok {
		return
	}
	page, err := h.public.Index(r.Context(), after, limit)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	out := indexPageWire{Items: make([]indexEntryWire, 0, len(page.Items)), NextCursor: nextCursor(page.Next)}
	for _, e := range page.Items {
		out.Items = append(out.Items, indexEntryWire{Slug: e.Slug, FirstPublishedAt: e.FirstPublishedAt, PublishedAt: e.PublishedAt})
	}
	httpserver.WriteJSON(w, http.StatusOK, out)
}
