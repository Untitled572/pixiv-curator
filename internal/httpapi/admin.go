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
func NewAdminHandler(service *app.Catalog, ingestion ...*app.Ingest) http.Handler {
	mux := http.NewServeMux()
	if len(ingestion) > 0 && ingestion[0] != nil {
		registerAccount(mux, ingestion[0])
	}
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
	mux.HandleFunc("POST /admin/api/artists/approve-batch", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			IDs []string `json:"ids"`
		}
		if !decodeBody(w, r, &in) {
			return
		}
		if len(in.IDs) == 0 || len(in.IDs) > 200 {
			apiError(w, domain.ErrInvalid)
			return
		}
		results := []map[string]string{}
		for _, raw := range in.IDs {
			item := map[string]string{"id": raw}
			id, err := strconv.ParseInt(raw, 10, 64)
			job := ""
			if err == nil {
				job, err = service.DecideArtist(r.Context(), id, "approve")
			}
			if err != nil {
				item["error"] = "INVALID_INPUT"
				if errors.Is(err, domain.ErrConflict) {
					item["error"] = "CONFLICT"
				}
				if errors.Is(err, domain.ErrNotFound) {
					item["error"] = "NOT_FOUND"
				}
			} else {
				item["job_id"] = job
				item["status"] = "approved"
			}
			results = append(results, item)
		}
		writeJSON(w, 200, map[string]any{"results": results})
	})
	mux.HandleFunc("GET /admin/api/artworks", func(w http.ResponseWriter, r *http.Request) {
		artistID := int64(0)
		query := r.URL.Query()
		if v, ok := query["artist_id"]; ok {
			var err error
			if len(v) != 1 {
				apiError(w, domain.ErrInvalid)
				return
			}
			artistID, err = strconv.ParseInt(v[0], 10, 64)
			if err != nil || artistID <= 0 {
				apiError(w, domain.ErrInvalid)
				return
			}
			query.Del("artist_id")
		}
		copyRequest := r.Clone(r.Context())
		u := *r.URL
		u.RawQuery = query.Encode()
		copyRequest.URL = &u
		cursor, limit, err := listParams(copyRequest)
		if err != nil {
			apiError(w, err)
			return
		}
		after := int64(0)
		if cursor != "" {
			after, err = strconv.ParseInt(cursor, 10, 64)
			if err != nil || after <= 0 {
				apiError(w, domain.ErrInvalid)
				return
			}
		}
		items, err := service.Artworks(r.Context(), after, limit, artistID)
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
	mux.HandleFunc("GET /admin/api/jobs/{id}", func(w http.ResponseWriter, r *http.Request) {
		job, err := service.Job(r.Context(), r.PathValue("id"))
		if err != nil {
			apiError(w, err)
			return
		}
		writeJSON(w, 200, job)
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
	mux.HandleFunc("POST /admin/api/jobs/{id}/retry", func(w http.ResponseWriter, r *http.Request) {
		id, err := service.Retry(r.Context(), r.PathValue("id"))
		if err != nil {
			apiError(w, err)
			return
		}
		writeJSON(w, 202, map[string]string{"job_id": id})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { apiError(w, domain.ErrNotFound) })
	return mux
}

func registerAccount(mux *http.ServeMux, s *app.Ingest) {
	mux.HandleFunc("POST /admin/api/following/sync", func(w http.ResponseWriter, r *http.Request) {
		id, err := s.SyncFollowing(r.Context())
		if err != nil {
			apiError(w, err)
			return
		}
		writeJSON(w, 202, map[string]string{"job_id": id})
	})
	mux.HandleFunc("GET /admin/api/account/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]string{"status": s.AccountStatus()})
	})
	mux.HandleFunc("POST /admin/api/account/credentials", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Token string `json:"refresh_token"`
		}
		if !decodeBody(w, r, &in) {
			return
		}
		if in.Token == "" {
			apiError(w, domain.ErrInvalid)
			return
		}
		if err := s.Credentials(r.Context(), in.Token); err != nil {
			writeAPIError(w, 503, "CREDENTIAL_SAVE_FAILED")
			return
		}
		writeJSON(w, 200, map[string]string{"status": s.AccountStatus()})
	})
	mux.HandleFunc("POST /admin/api/jobs/{id}/resume", func(w http.ResponseWriter, r *http.Request) {
		if err := s.Resume(r.Context(), r.PathValue("id")); err != nil {
			apiError(w, err)
			return
		}
		writeJSON(w, 202, map[string]string{"state": "pending"})
	})
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
	id, _ := randomToken()
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": http.StatusText(status), "request_id": id}})
}
