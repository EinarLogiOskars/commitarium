package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/documents"
	"github.com/EinarLogiOskars/commitarium/internal/project"
)

// Documents are added to a project, listed, and removed through the API.
func TestAProjectsDocumentsAreAddedListedAndRemoved(t *testing.T) {
	store, err := documents.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	projects := &recordingProjectService{getByIDResult: project.Project{ID: "prj_shop"}}
	handler := NewWithWorkspaceRealWorkflowDeletionAndModels(projects, &recordingFeatureService{},
		nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, store)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(method, path, strings.NewReader(body)))
		return recorder
	}
	content := base64.StdEncoding.EncodeToString([]byte("sku,count\n1,4\n"))
	if recorder := call(http.MethodPost, "/api/v1/projects/prj_shop/documents",
		`{"name":"stock.csv","content_base64":"`+content+`"}`); recorder.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", recorder.Code, recorder.Body.String())
	}
	if recorder := call(http.MethodPost, "/api/v1/projects/prj_shop/documents",
		`{"name":"../stock.csv","content_base64":"`+content+`"}`); recorder.Code != http.StatusBadRequest {
		t.Fatalf("an unsafe name: status %d", recorder.Code)
	}
	recorder := call(http.MethodGet, "/api/v1/projects/prj_shop/documents", "")
	var listed struct {
		Documents []documents.Document `json:"documents"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &listed); err != nil || len(listed.Documents) != 1 ||
		listed.Documents[0].Name != "stock.csv" || listed.Documents[0].Size != 14 {
		t.Fatalf("listed %s: %v", recorder.Body.String(), err)
	}
	if recorder := call(http.MethodDelete, "/api/v1/projects/prj_shop/documents/stock.csv", ""); recorder.Code != http.StatusNoContent {
		t.Fatalf("delete: status %d", recorder.Code)
	}
	if recorder := call(http.MethodDelete, "/api/v1/projects/prj_shop/documents/stock.csv", ""); recorder.Code != http.StatusNotFound {
		t.Fatalf("delete again: status %d", recorder.Code)
	}
}
