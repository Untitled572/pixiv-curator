package httpapi

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"github.com/Untitled572/pixiv-curator/internal/domain"
)

type libraryCleanupStore interface {
	LibraryCleanupReview(context.Context) ([]domain.LibraryCleanupItem, error)
	RemoveLibraryArtist(context.Context, int64) error
}

// RegisterLibraryCleanup mounts the standalone authenticated cleanup endpoints.
func RegisterLibraryCleanup(mux *http.ServeMux, store libraryCleanupStore, auth *Auth) {
	mux.Handle("GET /admin/api/library/review", auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil || len(q) != 0 {
			apiError(w, domain.ErrInvalid)
			return
		}
		items, err := store.LibraryCleanupReview(r.Context())
		if err != nil {
			apiError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": items})
	})))
	mux.Handle("POST /admin/api/library/artists/{id}/remove", auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil || len(q) != 0 {
			apiError(w, domain.ErrInvalid)
			return
		}
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id <= 0 {
			apiError(w, domain.ErrInvalid)
			return
		}
		if r.ContentLength > 0 {
			apiError(w, domain.ErrInvalid)
			return
		}
		if err = store.RemoveLibraryArtist(r.Context(), id); err != nil {
			apiError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": strconv.FormatInt(id, 10), "review_status": "rejected", "collection_enabled": false})
	})))
}
