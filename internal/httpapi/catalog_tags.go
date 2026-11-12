package httpapi

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/Untitled572/pixiv-curator/internal/domain"
)

type libraryTagStore interface {
	ArtistTagPage(context.Context, string, string, int64, int) ([]domain.Artist, error)
	ArtistTagPageStats(context.Context, string, string, int, int) ([]domain.Artist, int64, int64, int, string, error)
	ArtistTags(context.Context) ([]string, error)
	SetArtistTagOverride(context.Context, int64, []string) error
	ClearArtistTagOverride(context.Context, int64) error
}

type catalogStatsStore interface {
	CatalogStats(context.Context) (domain.CatalogStats, error)
}

// RegisterLibrary adds the authenticated artist tag management routes.
func RegisterLibrary(mux *http.ServeMux, store libraryTagStore, auth *Auth) {
	protect := func(pattern string, handler http.HandlerFunc) { mux.Handle(pattern, auth.Middleware(handler)) }
	protect("GET /admin/api/library/artists", func(w http.ResponseWriter, r *http.Request) {
		q, parseErr := url.ParseQuery(r.URL.RawQuery)
		if parseErr != nil {
			apiError(w, domain.ErrInvalid)
			return
		}
		for key, values := range q {
			if key != "tag" && key != "account_id" && key != "after" && key != "limit" && key != "page" || len(values) != 1 {
				apiError(w, domain.ErrInvalid)
				return
			}
		}
		limit := 50
		if q.Get("limit") != "" {
			n, err := strconv.Atoi(q.Get("limit"))
			if err != nil || n < 1 || n > 200 {
				apiError(w, domain.ErrInvalid)
				return
			}
			limit = n
		}
		after := int64(0)
		if q.Get("after") != "" {
			n, err := strconv.ParseInt(q.Get("after"), 10, 64)
			if err != nil || n <= 0 {
				apiError(w, domain.ErrInvalid)
				return
			}
			after = n
		}
		_, hasPage := q["page"]
		_, hasAfter := q["after"]
		if hasPage && hasAfter {
			apiError(w, domain.ErrInvalid)
			return
		}
		tag, accountID := q.Get("tag"), q.Get("account_id")
		if len(tag) > 160 || len(accountID) > 256 || strings.TrimSpace(tag) != tag || strings.TrimSpace(accountID) != accountID {
			apiError(w, domain.ErrInvalid)
			return
		}
		if hasPage {
			page, err := strconv.Atoi(q.Get("page"))
			if err != nil || page < 1 {
				apiError(w, domain.ErrInvalid)
				return
			}
			items, total, libraryTotal, totalPages, next, err := store.ArtistTagPageStats(r.Context(), tag, accountID, page, limit)
			if err != nil {
				apiError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": total, "library_total": libraryTotal, "page": page, "page_size": limit, "total_pages": totalPages, "next_cursor": next})
			return
		}
		items, err := store.ArtistTagPage(r.Context(), tag, accountID, after, limit)
		if err != nil {
			apiError(w, err)
			return
		}
		next := ""
		if len(items) > limit {
			next = strconv.FormatInt(items[limit-1].ID, 10)
			items = items[:limit]
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": next})
	})
	protect("GET /admin/api/stats", func(w http.ResponseWriter, r *http.Request) {
		q, parseErr := url.ParseQuery(r.URL.RawQuery)
		if parseErr != nil || len(q) != 0 {
			apiError(w, domain.ErrInvalid)
			return
		}
		statsStore, ok := store.(catalogStatsStore)
		if !ok {
			apiError(w, domain.ErrNotFound)
			return
		}
		stats, err := statsStore.CatalogStats(r.Context())
		if err != nil {
			apiError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, stats)
	})
	protect("GET /admin/api/library/tags", func(w http.ResponseWriter, r *http.Request) {
		q, parseErr := url.ParseQuery(r.URL.RawQuery)
		if parseErr != nil || len(q) != 0 {
			apiError(w, domain.ErrInvalid)
			return
		}
		items, err := store.ArtistTags(r.Context())
		if err != nil {
			apiError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": items})
	})
	protect("PUT /admin/api/library/artists/{id}/tags", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id <= 0 {
			apiError(w, domain.ErrInvalid)
			return
		}
		var in struct {
			Tags []string `json:"tags"`
		}
		if !decodeBody(w, r, &in) {
			return
		}
		if in.Tags == nil {
			apiError(w, domain.ErrInvalid)
			return
		}
		if err = store.SetArtistTagOverride(r.Context(), id, in.Tags); err != nil {
			apiError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"tags": in.Tags, "override": true})
	})
	protect("DELETE /admin/api/library/artists/{id}/tags", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id <= 0 {
			apiError(w, domain.ErrInvalid)
			return
		}
		if err = store.ClearArtistTagOverride(r.Context(), id); err != nil {
			apiError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"override": false})
	})
}
