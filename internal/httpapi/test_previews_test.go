package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/testpreviews"
)

// The host lists the test previews, claims a pending one, reports its
// address, and says once it removed one that ended.
func TestTestPreviewRoutes(t *testing.T) {
	ctx := context.Background()
	store, err := testpreviews.NewStore(filepath.Join(t.TempDir(), "test-previews.json"))
	if err != nil {
		t.Fatal(err)
	}
	const id = "tsk_0123456789abcdef01234567"
	if _, err := store.Request(ctx, id, "prj_test", "claude"); err != nil {
		t.Fatal(err)
	}
	handler := NewWithWorkspaceRealWorkflowDeletionAndModels(&recordingProjectService{}, &recordingFeatureService{},
		nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, store)
	serve := func(method, path, body string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(method, path, strings.NewReader(body)))
		return recorder
	}

	list := serve(http.MethodGet, "/api/v1/test-previews", "")
	var listed struct {
		Previews []testpreviews.Preview `json:"previews"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &listed); err != nil || list.Code != http.StatusOK ||
		len(listed.Previews) != 1 || listed.Previews[0].Status != testpreviews.Pending ||
		listed.Previews[0].Agent != "claude" {
		t.Fatalf("list = %d %s", list.Code, list.Body)
	}
	if claimed := serve(http.MethodPost, "/api/v1/test-previews/"+id+"/claim", ""); claimed.Code != http.StatusOK {
		t.Fatalf("claim = %d %s", claimed.Code, claimed.Body)
	}
	if again := serve(http.MethodPost, "/api/v1/test-previews/"+id+"/claim", ""); again.Code != http.StatusConflict {
		t.Fatalf("a second claim = %d, want 409", again.Code)
	}
	if bad := serve(http.MethodPost, "/api/v1/test-previews/"+id+"/ready", `{"address":"http://example.com:80"}`); bad.Code != http.StatusBadRequest {
		t.Fatalf("a public address = %d, want 400", bad.Code)
	}
	if ready := serve(http.MethodPost, "/api/v1/test-previews/"+id+"/ready", `{"address":"http://172.20.0.4:3000"}`); ready.Code != http.StatusOK {
		t.Fatalf("ready = %d %s", ready.Code, ready.Body)
	}
	if early := serve(http.MethodPost, "/api/v1/test-previews/"+id+"/removed", ""); early.Code != http.StatusConflict {
		t.Fatalf("removed while wanted = %d, want 409", early.Code)
	}
	if err := store.End(ctx, id); err != nil {
		t.Fatal(err)
	}
	if removed := serve(http.MethodPost, "/api/v1/test-previews/"+id+"/removed", ""); removed.Code != http.StatusNoContent {
		t.Fatalf("removed = %d %s", removed.Code, removed.Body)
	}
	if missing := serve(http.MethodPost, "/api/v1/test-previews/"+id+"/failed", `{"reason":"x"}`); missing.Code != http.StatusNotFound {
		t.Fatalf("failing a removed one = %d, want 404", missing.Code)
	}
}
