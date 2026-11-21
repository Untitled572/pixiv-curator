package httpapi

import (
	"encoding/json"
	"github.com/Untitled572/pixiv-curator/internal/app"
	"github.com/Untitled572/pixiv-curator/internal/domain"
	"net/http"
	"net/url"
	"strconv"
)

type exposureArtistID int64

func (id *exposureArtistID) UnmarshalJSON(raw []byte) error {
	text := string(raw)
	if len(raw) > 0 && raw[0] == '"' {
		if err := json.Unmarshal(raw, &text); err != nil {
			return err
		}
	}
	value, err := strconv.ParseInt(text, 10, 64)
	if err != nil || value <= 0 {
		return domain.ErrInvalid
	}
	*id = exposureArtistID(value)
	return nil
}

func RegisterRecommendations(mux *http.ServeMux, s *app.Recommender, auth *Auth) {
	mux.Handle("POST /admin/api/recommendations/directed", auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" {
			apiError(w, domain.ErrInvalid)
			return
		}
		var options app.DirectedDiscovery
		if !decodeBody(w, r, &options) {
			return
		}
		runID, jobID, err := s.StartDirected(r.Context(), options)
		if err != nil {
			apiError(w, err)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]string{"run_id": runID, "job_id": jobID})
	})))
	mux.Handle("POST /admin/api/recommendations/activity", auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" {
			apiError(w, domain.ErrInvalid)
			return
		}
		if err := s.TouchActivity(r.Context()); err != nil {
			apiError(w, err)
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	})))
	mux.Handle("GET /admin/api/recommendation-runs/latest", auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q, err := recommendationQuery(r.URL.RawQuery, false)
		if err != nil {
			apiError(w, domain.ErrInvalid)
			return
		}
		run, items, err := s.LatestFiltered(r.Context(), q.Get("content_rating"), q.Get("ai_type"))
		if err != nil {
			apiError(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"run": run, "items": items})
	})))
	mux.Handle("POST /admin/api/recommendations", auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" {
			apiError(w, domain.ErrInvalid)
			return
		}
		var input struct {
			ParentRun  string             `json:"parent_run"`
			ExposedIDs []exposureArtistID `json:"exposed_ids"`
		}
		var runID, jobID string
		var err error
		if r.Body == nil || r.Body == http.NoBody || r.ContentLength == 0 {
			runID, jobID, err = s.Start(r.Context())
		} else {
			r.Body = http.MaxBytesReader(w, r.Body, 4096)
			if !decodeBody(w, r, &input) {
				return
			}
			ids := make([]int64, len(input.ExposedIDs))
			for i, value := range input.ExposedIDs {
				ids[i] = int64(value)
			}
			runID, jobID, err = s.StartWithExposure(r.Context(), input.ParentRun, ids)
		}
		if err != nil {
			apiError(w, err)
			return
		}
		writeJSON(w, 202, map[string]string{"run_id": runID, "job_id": jobID})
	})))
	mux.Handle("GET /admin/api/recommendations", auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q, err := recommendationQuery(r.URL.RawQuery, true)
		if err != nil || q.Get("run_id") == "" {
			apiError(w, domain.ErrInvalid)
			return
		}
		run, items, err := s.CandidatesFiltered(r.Context(), q.Get("run_id"), q.Get("content_rating"), q.Get("ai_type"))
		if err != nil {
			apiError(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"run": run, "items": items})
	})))
}

func recommendationQuery(raw string, needRun bool) (url.Values, error) {
	q, err := url.ParseQuery(raw)
	if err != nil {
		return nil, err
	}
	allowed := map[string]bool{"content_rating": true, "ai_type": true}
	if needRun {
		allowed["run_id"] = true
	}
	for key, values := range q {
		if !allowed[key] || len(values) != 1 {
			return nil, domain.ErrInvalid
		}
	}
	if needRun && len(q["run_id"]) != 1 {
		return nil, domain.ErrInvalid
	}
	for _, key := range []string{"content_rating", "ai_type"} {
		if q.Has(key) && q.Get(key) == "" {
			return nil, domain.ErrInvalid
		}
	}
	return q, nil
}
