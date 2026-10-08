package httpapi

import (
	"encoding/json"
	"net/http"
)

func NewHandler(version string) http.Handler { return NewHandlerWithRoutes(version, nil) }

// NewHandlerWithRoutes keeps health routes available while allowing application wiring.
func NewHandlerWithRoutes(version string, register func(*http.ServeMux)) http.Handler {
	mux := http.NewServeMux()
	if register != nil {
		register(mux)
	}
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /version", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{
			"name": "pixiv-curator", "version": version,
		})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
	})
	return mux
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
