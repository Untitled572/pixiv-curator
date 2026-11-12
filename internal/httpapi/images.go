package httpapi

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/Untitled572/pixiv-curator/internal/app"
	"github.com/Untitled572/pixiv-curator/internal/domain"
	"github.com/Untitled572/pixiv-curator/internal/media"
)

func RegisterImages(mux *http.ServeMux, s *app.Images, p *media.Proxy, auth *Auth) {
	forward := func(w http.ResponseWriter, r *http.Request, page domain.SelectedPage, preview bool) {
		if p == nil {
			writeAPIError(w, http.StatusServiceUnavailable, "NOT_READY")
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
		var err error
		if preview && page.PreviewIsThumbnail {
			err = p.ForwardPreview(w, r, page.PreviewURL)
		} else {
			target := page.OriginalURL
			if preview {
				target = page.PreviewURL
			}
			err = p.Forward(w, r, target)
		}
		if err == nil {
			return
		}
		var upstream *media.Error
		if errors.As(err, &upstream) {
			if upstream.Started {
				panic(http.ErrAbortHandler)
			}
			if upstream.Code == "MEDIA_UNAVAILABLE" {
				_ = s.Repair(r.Context(), page.IllustID)
			}
			writeAPIError(w, http.StatusBadGateway, upstream.Code)
			return
		}
		writeAPIError(w, http.StatusBadGateway, "UPSTREAM_UNAVAILABLE")
	}
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
		if r.URL.Query().Get("delivery") == "original" {
			out.URL = out.OriginalURL
		}
		switch r.URL.Query().Get("format") {
		case "image":
			forward(w, r, out.SelectedPage, false)
		case "redirect":
			http.Redirect(w, r, out.URL, http.StatusFound)
		default:
			writeJSON(w, http.StatusOK, out)
		}
	})
	resolve := func(preview bool) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			query, err := url.ParseQuery(r.URL.RawQuery)
			if err != nil {
				apiError(w, domain.ErrInvalid)
				return
			}
			format := "json"
			if preview {
				format = "stream"
			}
			for key, values := range query {
				if len(values) != 1 || (key != "format" && (preview || key != "delivery")) {
					apiError(w, domain.ErrInvalid)
					return
				}
			}
			if values, present := query["format"]; present {
				format = values[0]
			}
			delivery := query.Get("delivery")
			if format != "json" && format != "redirect" && !(preview && format == "stream") && !(!preview && format == "image") {
				apiError(w, domain.ErrInvalid)
				return
			}
			if !validDelivery(query, format) {
				apiError(w, domain.ErrInvalid)
				return
			}
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
			if format == "stream" || format == "image" {
				forward(w, r, page, preview)
				return
			}
			if preview {
				if format == "redirect" {
					http.Redirect(w, r, page.PreviewURL, http.StatusFound)
					return
				}
				writeJSON(w, http.StatusOK, struct {
					domain.SelectedPage
					URL string `json:"url"`
				}{SelectedPage: page, URL: page.PreviewURL})
				return
			}
			out := s.Result(page)
			if delivery == "original" {
				out.URL = out.OriginalURL
			}
			if format == "redirect" {
				http.Redirect(w, r, out.URL, http.StatusFound)
				return
			}
			writeJSON(w, http.StatusOK, out)
		}
	}
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

// validDelivery keeps direct-origin URLs available only as an explicit opt-in.
func validDelivery(q url.Values, format string) bool {
	values, present := q["delivery"]
	if !present {
		return true
	}
	if len(values) != 1 || (values[0] != "proxy" && values[0] != "original") {
		return false
	}
	return values[0] != "original" || (format != "image" && format != "stream")
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
		case "delivery", "format", "collection", "orientation", "r18", "ai_type", "min_width", "min_height", "user_id", "strategy", "account_id":
			if len(v) != 1 {
				return out, domain.ErrInvalid
			}
		default:
			return out, domain.ErrInvalid
		}
	}
	if v, ok := q["format"]; ok && v[0] != "json" && v[0] != "redirect" && v[0] != "image" {
		return out, domain.ErrInvalid
	}
	if v, ok := q["collection"]; ok && v[0] != "preferred" {
		return out, domain.ErrInvalid
	}
	if !validDelivery(q, q.Get("format")) {
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
		case "unknown":
			out.AllowedRatings = []string{"unknown"}
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
		case "unknown":
			out.AllowedAI = []string{"unknown"}
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
