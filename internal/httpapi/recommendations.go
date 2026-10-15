package httpapi

import (
	"github.com/Untitled572/pixiv-curator/internal/app"
	"github.com/Untitled572/pixiv-curator/internal/domain"
	"net/http"
)

func RegisterRecommendations(mux *http.ServeMux, s *app.Recommender, auth *Auth) {
	mux.Handle("GET /admin/api/recommendation-runs/latest", auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		run, items, err := s.Latest(r.Context())
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
		q := r.URL.Query()
		if len(q) != 1 || len(q["run_id"]) != 1 || q.Get("run_id") == "" {
			apiError(w, domain.ErrInvalid)
			return
		}
		run, items, err := s.Candidates(r.Context(), q.Get("run_id"))
		if err != nil {
			apiError(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"run": run, "items": items})
	})))
}
