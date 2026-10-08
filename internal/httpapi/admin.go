package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/Untitled572/pixiv-curator/internal/app"
	"github.com/Untitled572/pixiv-curator/internal/domain"
	"io"
	"net/http"
	"strconv"
)

// NewAdminHandler must be mounted behind the management authentication middleware.
func NewAdminHandler(service *app.Catalog) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /admin/api/artists", func(w http.ResponseWriter, r *http.Request) {
		cursor, limit, err := listParams(r)
		if err != nil {
			apiError(w, err)
			return
		}
		var after int64
		if cursor != "" {
			after, err = strconv.ParseInt(cursor, 10, 64)
			if err != nil || after <= 0 {
				apiError(w, domain.ErrInvalid)
				return
			}
		}
		items, err := service.Artists(r.Context(), after, limit)
		if err != nil {
			apiError(w, err)
			return
		}
		next := ""
		if len(items) == limit {
			next = encodeCursor(strconv.FormatInt(items[len(items)-1].ID, 10))
		}
		writeJSON(w, 200, map[string]any{"items": items, "next_cursor": next})
	})
	mux.HandleFunc("POST /admin/api/artists", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			ID int64 `json:"id,string"`
		}
		if !decodeBody(w, r, &in) {
			return
		}
		if err := service.ImportArtist(r.Context(), in.ID); err != nil {
			apiError(w, err)
			return
		}
		writeJSON(w, 201, map[string]string{"id": strconv.FormatInt(in.ID, 10), "status": "imported"})
	})
	mux.HandleFunc("POST /admin/api/artists/{id}/{action}", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			apiError(w, domain.ErrInvalid)
			return
		}
		job, err := service.DecideArtist(r.Context(), id, r.PathValue("action"))
		if err != nil {
			apiError(w, err)
			return
		}
		writeJSON(w, 200, map[string]string{"job_id": job})
	})
	mux.HandleFunc("PATCH /admin/api/artists/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			apiError(w, domain.ErrInvalid)
			return
		}
		var in struct {
			Enabled *bool `json:"collection_enabled"`
		}
		if !decodeBody(w, r, &in) {
			return
		}
		if in.Enabled == nil {
			apiError(w, domain.ErrInvalid)
			return
		}
		if err = service.SetCollection(r.Context(), id, *in.Enabled); err != nil {
			apiError(w, err)
			return
		}
		writeJSON(w, 200, map[string]bool{"collection_enabled": *in.Enabled})
	})
	mux.HandleFunc("GET /admin/api/jobs", func(w http.ResponseWriter, r *http.Request) {
		after, limit, err := listParams(r)
		if err != nil {
			apiError(w, err)
			return
		}
		items, err := service.Jobs(r.Context(), after, limit)
		if err != nil {
			apiError(w, err)
			return
		}
		next := ""
		if len(items) == limit {
			next = encodeCursor(items[len(items)-1].ID)
		}
		writeJSON(w, 200, map[string]any{"items": items, "next_cursor": next})
	})
	mux.HandleFunc("POST /admin/api/jobs/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		if err := service.Cancel(r.Context(), r.PathValue("id")); err != nil {
			apiError(w, err)
			return
		}
		writeJSON(w, 200, map[string]bool{"cancel_requested": true})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { apiError(w, domain.ErrNotFound) })
	return mux
}

func decodeBody(w http.ResponseWriter, r *http.Request, value any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	err := d.Decode(value)
	if err == nil {
		if d.Decode(new(any)) != io.EOF {
			err = domain.ErrInvalid
		}
	}
	if err != nil {
		var large *http.MaxBytesError
		if errors.As(err, &large) {
			writeAPIError(w, 413, "BODY_TOO_LARGE")
		} else {
			apiError(w, domain.ErrInvalid)
		}
		return false
	}
	return true
}
func encodeCursor(raw string) string { return base64.RawURLEncoding.EncodeToString([]byte(raw)) }
func listParams(r *http.Request) (string, int, error) {
	q, err := r.URL.Query(), error(nil)
	limit := 50
	for k, v := range q {
		if (k != "cursor" && k != "limit") || len(v) != 1 {
			return "", 0, domain.ErrInvalid
		}
	}
	if q.Get("limit") != "" {
		limit, err = strconv.Atoi(q.Get("limit"))
		if err != nil || limit < 1 || limit > 200 {
			return "", 0, domain.ErrInvalid
		}
	}
	var cursor []byte
	if len(q.Get("cursor")) > 256 {
		return "", 0, domain.ErrInvalid
	}
	if q.Get("cursor") != "" {
		cursor, err = base64.RawURLEncoding.DecodeString(q.Get("cursor"))
		if err != nil {
			return "", 0, domain.ErrInvalid
		}
	}
	return string(cursor), limit, nil
}
func apiError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrInvalid):
		writeAPIError(w, 422, "INVALID_INPUT")
	case errors.Is(err, domain.ErrNotFound):
		writeAPIError(w, 404, "NOT_FOUND")
	case errors.Is(err, domain.ErrConflict):
		writeAPIError(w, 409, "CONFLICT")
	default:
		writeAPIError(w, 503, "NOT_READY")
	}
}
func writeAPIError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": http.StatusText(status)}})
}
