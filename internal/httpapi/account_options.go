package httpapi

import (
	"bytes"
	"encoding/json"
	"github.com/Untitled572/pixiv-curator/internal/accounts"
	"github.com/Untitled572/pixiv-curator/internal/domain"
	"io"
	"net/http"
	"strings"
)

func RegisterAccountOptions(mux *http.ServeMux, manager *accounts.Manager, auth *Auth) {
	mux.Handle("PATCH /admin/api/accounts/{id}", auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			IsDefault       *bool `json:"is_default"`
			FollowOnApprove *bool `json:"follow_on_approve"`
		}
		if !decodeAccountOptions(w, r, &input) {
			return
		}
		if input.IsDefault != nil && !*input.IsDefault {
			apiError(w, domain.ErrInvalid)
			return
		}
		if err := manager.Configure(r.Context(), r.PathValue("id"), input.IsDefault != nil && *input.IsDefault, input.FollowOnApprove); err != nil {
			apiError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"updated": true})
	})))
	mux.Handle("POST /admin/api/accounts/{id}/copy-following", auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Source string `json:"source_account_id"`
		}
		if !decodeAccountOptions(w, r, &input) {
			return
		}
		if input.Source == "" {
			apiError(w, domain.ErrInvalid)
			return
		}
		count, err := manager.CopyFollowing(r.Context(), input.Source, r.PathValue("id"))
		if err != nil {
			apiError(w, err)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]int{"queued": count})
	})))
}
func decodeAccountOptions(w http.ResponseWriter, r *http.Request, destination any) bool {
	if r.URL.RawQuery != "" {
		apiError(w, domain.ErrInvalid)
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	body, err := io.ReadAll(r.Body)
	if err != nil || !uniqueOptionKeys(body) {
		apiError(w, domain.ErrInvalid)
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(destination) != nil || decoder.Decode(new(any)) != io.EOF {
		apiError(w, domain.ErrInvalid)
		return false
	}
	return true
}

func uniqueOptionKeys(body []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(body))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return false
	}
	seen := map[string]bool{}
	for decoder.More() {
		token, err = decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok {
			return false
		}
		key = strings.ToLower(key)
		if seen[key] {
			return false
		}
		seen[key] = true
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return false
		}
	}
	token, err = decoder.Token()
	if err != nil || token != json.Delim('}') {
		return false
	}
	return decoder.Decode(new(any)) == io.EOF
}
