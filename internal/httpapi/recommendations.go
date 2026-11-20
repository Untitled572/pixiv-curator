package httpapi

import (
	"github.com/Untitled572/pixiv-curator/internal/app"
	"github.com/Untitled572/pixiv-curator/internal/domain"
	"net/http"
	"net/url"
)

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
		runID, jobID, err := s.Start(r.Context())
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
