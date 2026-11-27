package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestCollectionScheduleDefaultsAndPersists(t *testing.T) {
	mux, store := newLibraryHandler(t)
	RegisterCollectionSchedule(mux, store, NewAuth([]byte(adminTestToken), AuthOptions{}))
	const path = "/admin/api/settings/sync"
	for _, tc := range []struct {
		method, body  string
		authenticated bool
		status        int
		hours         int
		version       int64
	}{
		{"GET", "", false, http.StatusUnauthorized, 0, 0},
		{"GET", "", true, http.StatusOK, 24, 0},
		{"PUT", `{"interval_hours":0,"version":0}`, true, http.StatusUnprocessableEntity, 0, 0},
		{"PUT", `{"interval_hours":48,"version":0}`, true, http.StatusOK, 48, 1},
		{"PUT", `{"interval_hours":72,"version":0}`, true, http.StatusConflict, 0, 0},
		{"GET", "", true, http.StatusOK, 48, 1},
	} {
		rec := libraryRequest(mux, tc.method, path, tc.body, tc.authenticated)
		if rec.Code != tc.status {
			t.Fatalf("%s %q: status=%d, body=%s", tc.method, tc.body, rec.Code, rec.Body)
		}
		if tc.status != http.StatusOK {
			continue
		}
		var result struct {
			Hours   int   `json:"interval_hours"`
			Version int64 `json:"version"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil || result.Hours != tc.hours || result.Version != tc.version {
			t.Fatalf("schedule=%+v err=%v", result, err)
		}
	}
}
