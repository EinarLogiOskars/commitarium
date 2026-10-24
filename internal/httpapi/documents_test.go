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
	recorder = call(http.MethodGet, "/api/v1/projects/prj_shop/documents/stock.csv", "")
	var preview struct {
		Preview   string `json:"preview"`
		Truncated bool   `json:"truncated"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &preview); err != nil || preview.Preview != "sku,count\n1,4\n" ||
		preview.Truncated {
		t.Fatalf("preview %d %s: %v", recorder.Code, recorder.Body.String(), err)
	}
	binary := base64.StdEncoding.EncodeToString([]byte{0x89, 'P', 'N', 'G', 0, 0xff})
	if recorder := call(http.MethodPost, "/api/v1/projects/prj_shop/documents",
		`{"name":"logo.png","content_base64":"`+binary+`"}`); recorder.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", recorder.Code, recorder.Body.String())
	}
	recorder = call(http.MethodGet, "/api/v1/projects/prj_shop/documents/logo.png", "")
	if err := json.Unmarshal(recorder.Body.Bytes(), &preview); err != nil || preview.Preview != "" {
		t.Fatalf("a file that is not text: %s", recorder.Body.String())
	}
	if recorder := call(http.MethodGet, "/api/v1/projects/prj_shop/documents/missing.csv", ""); recorder.Code != http.StatusNotFound {
		t.Fatalf("a missing document: status %d", recorder.Code)
	}
	if recorder := call(http.MethodDelete, "/api/v1/projects/prj_shop/documents/stock.csv", ""); recorder.Code != http.StatusNoContent {
		t.Fatalf("delete: status %d", recorder.Code)
	}
	if recorder := call(http.MethodDelete, "/api/v1/projects/prj_shop/documents/stock.csv", ""); recorder.Code != http.StatusNotFound {
		t.Fatalf("delete again: status %d", recorder.Code)
	}
}
