package httpapi

import (
	"github.com/Untitled572/pixiv-curator/internal/network"
	"net/http"
)

func RegisterNetwork(mux *http.ServeMux, m *network.Manager, auth *Auth) {
	mux.Handle("GET /admin/api/settings/network", auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, m.Status()) })))
	mux.Handle("PUT /admin/api/settings/network", auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input network.Settings
		if !decodeBody(w, r, &input) {
			return
		}
		if err := m.Save(input); err != nil {
			apiError(w, err)
			return
		}
		writeJSON(w, 200, m.Status())
	})))
}
