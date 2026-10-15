package httpapi

import (
	"errors"
	"net/http"

	"github.com/Untitled572/pixiv-curator/internal/accountlogin"
	"github.com/Untitled572/pixiv-curator/internal/app"
)

func RegisterAccountLogin(mux *http.ServeMux, s *app.AccountLogin, auth *Auth) {
	mux.Handle("GET /admin/api/account/login", auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, s.Status()) })))
	mux.Handle("POST /admin/api/account/login", auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct{}
		if !decodeBody(w, r, &input) {
			return
		}
		status, err := s.Start()
		if err != nil {
			var failure *accountlogin.Error
			if errors.As(err, &failure) {
				if failure.Code == "LOGIN_RATE_LIMITED" {
					w.Header().Set("Retry-After", "60")
					writeAPIError(w, 429, failure.Code)
				} else {
					writeAPIError(w, 503, "LOGIN_UNAVAILABLE")
				}
				return
			}
			apiError(w, err)
			return
		}
		writeJSON(w, 202, status)
	})))
	mux.Handle("POST /admin/api/account/login/code", auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Code string `json:"code"`
		}
		if !decodeBody(w, r, &input) {
			return
		}
		if err := s.SubmitCode(input.Code); err != nil {
			apiError(w, err)
			return
		}
		writeJSON(w, 202, s.Status())
	})))

	mux.Handle("POST /admin/api/account/login/cancel", auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.Cancel()
		writeJSON(w, 202, s.Status())
	})))
}
