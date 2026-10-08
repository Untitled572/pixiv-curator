package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Untitled572/pixiv-curator/internal/app"
	"github.com/Untitled572/pixiv-curator/internal/store/sqlite"
)

const adminTestToken = "admin-test-token"

func newAdminTestServer(t *testing.T) (http.Handler, *sqlite.Store) {
	t.Helper()
	store, err := sqlite.Open(context.Background(), t.TempDir()+"/catalog.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	service := app.NewCatalog(store)
	auth := NewAuth([]byte(adminTestToken), AuthOptions{})
	admin := auth.Middleware(NewAdminHandler(service))
	return admin, store
}

func adminRequest(handler http.Handler, method, target, body string, authenticated bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if authenticated {
		req.Header.Set("Authorization", "Bearer "+adminTestToken)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestAdminRequiresAuthenticationForReadsAndWrites(t *testing.T) {
	handler, _ := newAdminTestServer(t)
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodGet, "/admin/api/artists", ""},
		{http.MethodGet, "/admin/api/jobs", ""},
		{http.MethodPost, "/admin/api/artists", `{"id":"1"}`},
		{http.MethodPatch, "/admin/api/artists/1", `{"collection_enabled":true}`},
		{http.MethodPost, "/admin/api/artists/1/approve", ""},
	} {
		rec := adminRequest(handler, tc.method, tc.path, tc.body, false)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s unauthenticated status=%d; want 401", tc.method, tc.path, rec.Code)
		}
	}
}

func TestAdminManualImportApprovalAndBlockRestore(t *testing.T) {
	handler, store := newAdminTestServer(t)
	const id int64 = 9007199254740993

	rec := adminRequest(handler, http.MethodPost, "/admin/api/artists", fmt.Sprintf(`{"id":"%d"}`, id), true)
	if rec.Code != http.StatusCreated {
		t.Fatalf("import status=%d body=%s", rec.Code, rec.Body.String())
	}
	var imported map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &imported); err != nil {
		t.Fatal(err)
	}
	if imported["id"] != fmt.Sprint(id) || imported["status"] != "imported" {
		t.Fatalf("import response=%v; ID should be a decimal string", imported)
	}

	rec = adminRequest(handler, http.MethodGet, "/admin/api/artists?limit=1", "", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", rec.Code, rec.Body.String())
	}
	var listed struct {
		Items []struct {
			ID           int64  `json:"id,string"`
			ReviewStatus string `json:"review_status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Items) != 1 || listed.Items[0].ID != id || listed.Items[0].ReviewStatus != "pending" {
		t.Fatalf("manual import should remain pending: %#v", listed.Items)
	}

	// A blocked artist needs an explicit restore step before approval.
	rec = adminRequest(handler, http.MethodPost, fmt.Sprintf("/admin/api/artists/%d/block", id), "", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("block status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = adminRequest(handler, http.MethodPost, fmt.Sprintf("/admin/api/artists/%d/approve", id), "", true)
	if rec.Code != http.StatusConflict {
		t.Fatalf("approve blocked status=%d; want 409", rec.Code)
	}
	rec = adminRequest(handler, http.MethodPost, fmt.Sprintf("/admin/api/artists/%d/restore", id), "", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("restore status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = adminRequest(handler, http.MethodPost, fmt.Sprintf("/admin/api/artists/%d/approve", id), "", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("approve restored status=%d body=%s", rec.Code, rec.Body.String())
	}
	var approval map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &approval); err != nil {
		t.Fatal(err)
	}
	if approval["job_id"] == "" {
		t.Fatalf("approval did not return its created job: %v", approval)
	}

	jobs, err := store.Jobs(context.Background(), "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].ID != approval["job_id"] || jobs[0].Kind != "sync_artist" || jobs[0].State != "pending" {
		t.Fatalf("approval job=%#v", jobs)
	}
}

func TestAdminPatchUsesStrictWhitelist(t *testing.T) {
	handler, _ := newAdminTestServer(t)
	if rec := adminRequest(handler, http.MethodPost, "/admin/api/artists", `{"id":"42"}`, true); rec.Code != http.StatusCreated {
		t.Fatalf("import status=%d body=%s", rec.Code, rec.Body.String())
	}
	for _, body := range []string{
		`{"collection_enabled":true,"review_status":"approved"}`,
		`{"collection_enabled":true,"unknown":1}`,
		`{"review_status":"approved"}`,
	} {
		rec := adminRequest(handler, http.MethodPatch, "/admin/api/artists/42", body, true)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("PATCH body %s status=%d; want 422", body, rec.Code)
		}
	}
	rec := adminRequest(handler, http.MethodPatch, "/admin/api/artists/42", `{"collection_enabled":true}`, true)
	if rec.Code != http.StatusConflict {
		t.Fatalf("pending artist collection update status=%d; want conflict", rec.Code)
	}
}

func TestAdminRejectsMalformedBodiesAndListParameters(t *testing.T) {
	handler, _ := newAdminTestServer(t)
	for _, body := range []string{
		`{"id":12}`, // ID must be a JSON string.
		`{"id":"12","extra":true}`,
		`{"id":"12"} {}`,
		`{"id":"not-a-number"}`,
		strings.Repeat("x", (64<<10)+1),
	} {
		rec := adminRequest(handler, http.MethodPost, "/admin/api/artists", body, true)
		if rec.Code != http.StatusUnprocessableEntity && rec.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("body length=%d status=%d body=%s", len(body), rec.Code, rec.Body.String())
		}
	}
	for _, path := range []string{
		"/admin/api/artists?limit=0",
		"/admin/api/artists?limit=201",
		"/admin/api/artists?limit=abc",
		"/admin/api/artists?limit=1&limit=2",
		"/admin/api/artists?other=x",
		"/admin/api/artists?cursor=!!",
		"/admin/api/artists?cursor=" + encodeCursor("0"),
		"/admin/api/artists?cursor=" + encodeCursor("-1"),
		"/admin/api/jobs?limit=0",
	} {
		rec := adminRequest(handler, http.MethodGet, path, "", true)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("GET %s status=%d; want 422", path, rec.Code)
		}
	}
}

func TestAdminJobsResponseHidesPayloadAndLease(t *testing.T) {
	handler, store := newAdminTestServer(t)
	if _, err := store.Enqueue(context.Background(), "sync_artist", "artist:99", json.RawMessage(`{"artist_id":99,"private_marker":"must-not-leak"}`)); err != nil {
		t.Fatal(err)
	}
	rec := adminRequest(handler, http.MethodGet, "/admin/api/jobs", "", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("jobs status=%d body=%s", rec.Code, rec.Body.String())
	}
	var response map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	encoded := rec.Body.String()
	for _, forbidden := range []string{"payload", "lease", "private_marker", "artist_id"} {
		if strings.Contains(strings.ToLower(encoded), forbidden) {
			t.Errorf("jobs response leaked %q: %s", forbidden, encoded)
		}
	}
	items, ok := response["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("unexpected jobs response: %#v", response)
	}
	item, ok := items[0].(map[string]any)
	if !ok {
		t.Fatalf("unexpected job item: %#v", items[0])
	}
	for _, forbidden := range []string{"payload", "lease_token", "lease_until", "key"} {
		if _, exists := item[forbidden]; exists {
			t.Errorf("job response contains %q: %#v", forbidden, item)
		}
	}
}
