package httpapi

import (
	"github.com/Untitled572/pixiv-curator/internal/app"
	"github.com/Untitled572/pixiv-curator/internal/domain"
	"net/http"
	"net/url"
	"strings"
)

func RegisterRecommendations(mux *http.ServeMux, s *app.Recommender, auth *Auth) {
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
		run, _, err := s.Candidates(r.Context(), runID)
		if err != nil {
			apiError(w, err)
			return
		}
		writeJSON(w, 202, map[string]any{"run_id": runID, "job_id": jobID, "reused": run.State == "succeeded"})
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
	if values, ok := q["content_rating"]; ok {
		if len(values) != 1 || strings.TrimSpace(values[0]) != values[0] || (values[0] != "all" && values[0] != "adult" && values[0] != "safe" && values[0] != "unknown") {
			return nil, domain.ErrInvalid
		}
	}
	if values, ok := q["ai_type"]; ok {
		if len(values) != 1 || strings.TrimSpace(values[0]) != values[0] || (values[0] != "all" && values[0] != "ai" && values[0] != "non_ai" && values[0] != "unknown") {
			return nil, domain.ErrInvalid
		}
	}
	return q, nil
}
