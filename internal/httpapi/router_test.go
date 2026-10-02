package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestEndpoints(t *testing.T) {
	handler := NewHandler("test-version")
	tests := []struct {
		name   string
		method string
		path   string
		status int
		fields map[string]string
	}{
		{name: "health", method: http.MethodGet, path: "/healthz", status: http.StatusOK,
			fields: map[string]string{"status": "ok"}},
		{name: "version", method: http.MethodGet, path: "/version", status: http.StatusOK,
			fields: map[string]string{"name": "pixiv-curator", "version": "test-version"}},
		{name: "unknown route", method: http.MethodGet, path: "/missing", status: http.StatusNotFound,
			fields: map[string]string{"error": "not_found"}},
		{name: "random not implemented", method: http.MethodGet, path: "/random", status: http.StatusNotFound,
			fields: map[string]string{"error": "not_found"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(tt.method, tt.path, nil))
			if recorder.Code != tt.status {
				t.Fatalf("status = %d; want %d", recorder.Code, tt.status)
			}
			if got := recorder.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
				t.Fatalf("unexpected content type: %q", got)
			}
			if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
				t.Fatalf("unexpected cache control: %q", got)
			}
			var body map[string]string
			if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			for key, want := range tt.fields {
				if body[key] != want {
					t.Errorf("body[%q] = %q; want %q", key, body[key], want)
				}
			}
		})
	}
}
