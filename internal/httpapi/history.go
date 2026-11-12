package httpapi

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"github.com/Untitled572/pixiv-curator/internal/domain"
)

type HistoryReader interface {
	History(context.Context, int, int) (domain.DistributionHistoryPage, error)
}

// RegisterHistory installs the authenticated read-only distribution history route.
func RegisterHistory(mux *http.ServeMux, reader HistoryReader, auth *Auth) {
	mux.Handle("GET /admin/api/history", auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil {
			apiError(w, domain.ErrInvalid)
			return
		}
		for key, values := range query {
			if (key != "page" && key != "limit") || len(values) != 1 {
				apiError(w, domain.ErrInvalid)
				return
			}
		}
		page, limit := 1, 24
		if values, ok := query["page"]; ok {
			page, err = positiveQueryInt(values[0])
			if err != nil {
				apiError(w, domain.ErrInvalid)
				return
			}
		}
		if values, ok := query["limit"]; ok {
			limit, err = positiveQueryInt(values[0])
			if err != nil || limit > 96 {
				apiError(w, domain.ErrInvalid)
				return
			}
		}
		result, err := reader.History(r.Context(), page, limit)
		if err != nil {
			apiError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, result)
	})))
}

func positiveQueryInt(value string) (int, error) {
	if value == "" {
		return 0, strconv.ErrSyntax
	}
	for i := 0; i < len(value); i++ {
		if value[i] < '0' || value[i] > '9' {
			return 0, strconv.ErrSyntax
		}
	}
	n, err := strconv.Atoi(value)
	if err != nil || n < 1 {
		return 0, strconv.ErrSyntax
	}
	return n, nil
}
