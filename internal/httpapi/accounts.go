package httpapi

import (
	"errors"
	"github.com/Untitled572/pixiv-curator/internal/accounts"
	"github.com/Untitled572/pixiv-curator/internal/pixiv"
	"net/http"
)

func RegisterAccounts(mux *http.ServeMux, s *accounts.Manager, auth *Auth) {
	mux.Handle("GET /admin/api/accounts", auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a, e := s.List(r.Context())
		if e != nil {
			apiError(w, e)
			return
		}
		writeJSON(w, 200, map[string]any{"items": a})
	})))
	save := auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Name  string `json:"name"`
			Token string `json:"refresh_token"`
		}
		if !decodeBody(w, r, &in) {
			return
		}
		a, e := s.Save(r.Context(), r.PathValue("id"), in.Name, in.Token)
		if e != nil {
			var p *pixiv.Error
			if errors.As(e, &p) {
				writeAPIError(w, 502, p.Code)
			} else {
				apiError(w, e)
			}
			return
		}
		writeJSON(w, 200, a)
	}))
	mux.Handle("POST /admin/api/accounts", save)
	mux.Handle("PUT /admin/api/accounts/{id}", save)
	mux.Handle("DELETE /admin/api/accounts/{id}", auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if e := s.Delete(r.Context(), r.PathValue("id")); e != nil {
			apiError(w, e)
			return
		}
		writeJSON(w, 200, map[string]bool{"deleted": true})
	})))
	mux.Handle("POST /admin/api/accounts/{id}/sync", auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, e := s.Sync(r.Context(), r.PathValue("id"))
		if e != nil {
			apiError(w, e)
			return
		}
		writeJSON(w, 202, map[string]string{"job_id": id})
	})))
}
