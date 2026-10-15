package httpapi

import (
	"errors"
	"github.com/Untitled572/pixiv-curator/internal/app"
	"github.com/Untitled572/pixiv-curator/internal/domain"
	"github.com/Untitled572/pixiv-curator/internal/media"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

func RegisterImages(mux *http.ServeMux, s *app.Images, p *media.Proxy, auth *Auth) {
	mux.HandleFunc("GET /random", func(w http.ResponseWriter, r *http.Request) {
		req, err := selectionQuery(r)
		if err != nil {
			apiError(w, err)
			return
		}
		out, err := s.Select(r.Context(), req)
		if err != nil {
			imageError(w, err)
			return
		}
		if wantsRedirect(r) {
			http.Redirect(w, r, out.URL, http.StatusFound)
			return
		}
		writeJSON(w, 200, out)
	})
	resolve := func(preview bool) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
			if err != nil {
				apiError(w, domain.ErrInvalid)
				return
			}
			index, err := strconv.Atoi(r.PathValue("page"))
			if err != nil {
				apiError(w, domain.ErrInvalid)
				return
			}
			page, err := s.Page(r.Context(), id, index, preview)
			if err != nil {
				imageError(w, err)
				return
			}
			if _, present := r.URL.Query()["format"]; present {
				values := r.URL.Query()["format"]
				if len(values) != 1 || (values[0] != "json" && values[0] != "redirect") {
					apiError(w, domain.ErrInvalid)
					return
				}
			}
			target := page.OriginalURL
			if preview {
				target = page.PreviewURL
			}
			if wantsRedirect(r) {
				http.Redirect(w, r, target, http.StatusFound)
				return
			}
			writeJSON(w, http.StatusOK, struct {
				domain.SelectedPage
				URL string `json:"url"`
			}{SelectedPage: page, URL: target})
		}
	}
	_ = p // retained in the registration signature for existing application wiring
	mux.HandleFunc("GET /i/{id}/{page}", resolve(false))
	mux.Handle("GET /admin/api/previews/{id}/{page}", auth.Middleware(resolve(true)))
	mux.Handle("GET /admin/api/settings/preferences", auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rules, v, err := s.Preferences(r.Context())
		if err != nil {
			apiError(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"rules": rules, "version": v})
	})))
	mux.Handle("PUT /admin/api/settings/preferences", auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Rules   domain.SelectionRules `json:"rules"`
			Version int64                 `json:"version"`
		}
		if !decodeBody(w, r, &in) {
			return
		}
		if err := s.PutPreferences(r.Context(), in.Rules, in.Version); err != nil {
			apiError(w, err)
			return
		}
		writeJSON(w, 200, map[string]int64{"version": in.Version + 1})
	})))
	mux.Handle("PUT /admin/api/preferences/artworks/{id}", auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			apiError(w, domain.ErrInvalid)
			return
		}
		var in struct {
			Value string `json:"value"`
		}
		if !decodeBody(w, r, &in) {
			return
		}
		if err = s.SetPreference(r.Context(), id, in.Value); err != nil {
			apiError(w, err)
			return
		}
		writeJSON(w, 200, map[string]string{"value": in.Value})
	})))
}

func wantsRedirect(r *http.Request) bool {
	return r.URL.Query().Get("format") == "redirect"
}

func selectionQuery(r *http.Request) (domain.SelectionRequest, error) {
	q, err := url.ParseQuery(r.URL.RawQuery)
	out := domain.SelectionRequest{}
	if err != nil {
		return out, domain.ErrInvalid
	}
	for k, v := range q {
		switch k {
		case "included_tags", "excluded_tags":
		case "format", "collection", "orientation", "r18", "ai_type", "min_width", "min_height", "user_id", "strategy", "account_id":
			if len(v) != 1 {
				return out, domain.ErrInvalid
			}
		default:
			return out, domain.ErrInvalid
		}
	}
	if v, ok := q["format"]; ok && v[0] != "json" && v[0] != "redirect" {
		return out, domain.ErrInvalid
	}
	if v, ok := q["collection"]; ok && v[0] != "preferred" {
		return out, domain.ErrInvalid
	}
	out.Orientation = q.Get("orientation")
	out.Strategy = q.Get("strategy")
	for _, key := range []string{"orientation", "strategy"} {
		if values, present := q[key]; present && values[0] == "" {
			return out, domain.ErrInvalid
		}
	}
	if v, ok := q["r18"]; ok {
		switch v[0] {
		case "0":
			out.AllowedRatings = []string{"safe"}
		case "1":
			out.AllowedRatings = []string{"r18", "r18g"}
		case "2":
			out.AllowedRatings = []string{"safe", "r18", "r18g"}
		default:
			return out, domain.ErrInvalid
		}
	}
	if v, ok := q["ai_type"]; ok {
		switch v[0] {
		case "0":
			out.AllowedAI = []string{"non_ai"}
		case "1":
			out.AllowedAI = []string{"ai"}
		case "any":
			out.AllowedAI = []string{"non_ai", "ai"}
		default:
			return out, domain.ErrInvalid
		}
	}
	for k, target := range map[string]*int{"min_width": &out.MinWidth, "min_height": &out.MinHeight} {
		if v, ok := q[k]; ok {
			*target, err = strconv.Atoi(v[0])
			if err != nil || *target <= 0 {
				return out, domain.ErrInvalid
			}
		}
	}
	if v, ok := q["user_id"]; ok {
		out.ArtistID, err = strconv.ParseInt(v[0], 10, 64)
		if err != nil || out.ArtistID <= 0 {
			return out, domain.ErrInvalid
		}
	}
	if v, ok := q["account_id"]; ok {
		if len(v[0]) == 0 || len(v[0]) > 64 || !safeAccountID(v[0]) {
			return out, domain.ErrInvalid
		}
		out.AccountID = v[0]
	}
	for _, group := range q["included_tags"] {
		tags := strings.Split(group, "|")
		for i := range tags {
			tags[i] = strings.ToLower(strings.TrimSpace(tags[i]))
		}
		out.IncludeTags = append(out.IncludeTags, tags)
	}
	for _, tag := range q["excluded_tags"] {
		out.ExcludeTags = append(out.ExcludeTags, strings.ToLower(strings.TrimSpace(tag)))
	}
	return out, domain.ValidateRules(out.SelectionRules)
}

func safeAccountID(value string) bool {
	for i := 0; i < len(value); i++ {
		c := value[i]
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

func imageError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrCatalogEmpty):
		writeAPIError(w, 503, "CATALOG_EMPTY")
	case errors.Is(err, domain.ErrNoMatch):
		writeAPIError(w, 404, "NO_MATCH")
	default:
		apiError(w, err)
	}
}
