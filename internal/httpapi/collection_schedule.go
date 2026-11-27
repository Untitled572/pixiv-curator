package httpapi

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/Untitled572/pixiv-curator/internal/app"
	"github.com/Untitled572/pixiv-curator/internal/domain"
)

type collectionScheduleStore interface {
	Setting(context.Context, string) (json.RawMessage, int64, error)
	PutSetting(context.Context, string, json.RawMessage, int64) error
}

func RegisterCollectionSchedule(mux *http.ServeMux, store collectionScheduleStore, auth *Auth) {
	type scheduleResponse struct {
		domain.CollectionSchedule
		Version int64 `json:"version"`
	}
	mux.Handle("GET /admin/api/settings/sync", auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" {
			apiError(w, domain.ErrInvalid)
			return
		}
		schedule, version, err := app.ReadCollectionSchedule(r.Context(), store)
		if err != nil {
			apiError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, scheduleResponse{schedule, version})
	})))
	mux.Handle("PUT /admin/api/settings/sync", auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" {
			apiError(w, domain.ErrInvalid)
			return
		}
		var in scheduleResponse
		if !decodeBody(w, r, &in) {
			return
		}
		if !in.Valid() {
			apiError(w, domain.ErrInvalid)
			return
		}
		raw, err := json.Marshal(in.CollectionSchedule)
		if err == nil {
			err = store.PutSetting(r.Context(), "sync", raw, in.Version)
		}
		if err != nil {
			apiError(w, err)
			return
		}
		in.Version++
		writeJSON(w, http.StatusOK, in)
	})))
}
