package httpapi

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/Untitled572/pixiv-curator/internal/domain"
)

type libraryCleanupStore interface {
	LibraryCleanupReview(context.Context) (int64, []domain.LibraryCleanupItem, []string, error)
	NextLibraryCleanup(context.Context, int64) (int64, []domain.LibraryCleanupItem, []string, error)
	KeepLibraryCleanupArtist(context.Context, int64, int64) error
	RemoveLibraryArtist(context.Context, int64) error
}

// RegisterLibraryCleanup mounts the standalone authenticated cleanup endpoints.
func RegisterLibraryCleanup(mux *http.ServeMux, store libraryCleanupStore, auth *Auth) {
	if summaries, ok := store.(interface {
		LibraryArtistSummary(context.Context, int64) (domain.LibraryCleanupSummary, error)
	}); ok {
		mux.Handle("GET /admin/api/library/artists/{id}/summary", auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
			if err != nil || id <= 0 || r.URL.RawQuery != "" {
				apiError(w, domain.ErrInvalid)
				return
			}
			summary, err := summaries.LibraryArtistSummary(r.Context(), id)
			if err != nil {
				apiError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, summary)
		})))
	}
	mux.Handle("GET /admin/api/library/review", auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil || len(q) != 0 {
			apiError(w, domain.ErrInvalid)
			return
		}
		round, items, kept, err := store.LibraryCleanupReview(r.Context())
		if err != nil {
			apiError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"round": round, "items": items, "kept_ids": kept})
	})))
	mux.Handle("POST /admin/api/library/review/next", auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil || len(q) != 0 {
			apiError(w, domain.ErrInvalid)
			return
		}
		var in struct {
			Round int64 `json:"round"`
		}
		if !decodeBody(w, r, &in) {
			return
		}
		round, items, kept, err := store.NextLibraryCleanup(r.Context(), in.Round)
		if err != nil {
			apiError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"round": round, "items": items, "kept_ids": kept})
	})))
	mux.Handle("POST /admin/api/library/artists/{id}/keep", auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		var in struct {
			Round int64 `json:"round"`
		}
		if !decodeBody(w, r, &in) {
			return
		}
		if err = store.KeepLibraryCleanupArtist(r.Context(), id, in.Round); err != nil {
			apiError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": strconv.FormatInt(id, 10), "kept": true})
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
		var in struct {
			NotInterested bool   `json:"not_interested"`
			Round         *int64 `json:"round"`
		}
		if r.ContentLength != 0 {
			if !decodeBody(w, r, &in) {
				return
			}
		}
		if in.Round != nil && *in.Round <= 0 {
			apiError(w, domain.ErrInvalid)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
		defer cancel()
		if in.NotInterested || in.Round != nil {
			extended, ok := store.(interface {
				RemoveLibraryArtistWithDecision(context.Context, int64, bool, ...int64) error
			})
			if !ok {
				apiError(w, domain.ErrInvalid)
				return
			}
			if in.Round != nil {
				err = extended.RemoveLibraryArtistWithDecision(ctx, id, in.NotInterested, *in.Round)
			} else {
				err = extended.RemoveLibraryArtistWithDecision(ctx, id, in.NotInterested)
			}
		} else {
			err = store.RemoveLibraryArtist(ctx, id)
		}
		if err != nil {
			if ctx.Err() == context.DeadlineExceeded {
				writeAPIError(w, http.StatusGatewayTimeout, "REMOVE_TIMEOUT")
				return
			}
			apiError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": strconv.FormatInt(id, 10), "review_status": "approved", "collection_enabled": false})
	})))
}
