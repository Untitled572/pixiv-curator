package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"github.com/Untitled572/pixiv-curator/internal/domain"
)

type imageFeedbackStore interface {
	SetImageFeedback(context.Context, int64, int, string) (string, error)
}

// RegisterImageFeedback adds the authenticated current-page feedback route.
func RegisterImageFeedback(mux *http.ServeMux, store imageFeedbackStore, auth *Auth) {
	mux.Handle("PUT /admin/api/image-feedback", auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil || len(q) != 0 {
			apiError(w, domain.ErrInvalid)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		var input struct {
			IllustID  string `json:"illust_id"`
			PageIndex *int   `json:"page_index"`
			Value     string `json:"value"`
		}
		if err = dec.Decode(&input); err != nil {
			apiError(w, domain.ErrInvalid)
			return
		}
		if dec.Decode(new(any)) != io.EOF {
			apiError(w, domain.ErrInvalid)
			return
		}
		illustID, parseErr := strconv.ParseInt(input.IllustID, 10, 64)
		if parseErr != nil || illustID <= 0 || input.PageIndex == nil || *input.PageIndex < 0 || (input.Value != "like" && input.Value != "dislike" && input.Value != "neutral") {
			apiError(w, domain.ErrInvalid)
			return
		}
		value, err := store.SetImageFeedback(r.Context(), illustID, *input.PageIndex, input.Value)
		if err != nil {
			apiError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"value": value})
	})))
}
