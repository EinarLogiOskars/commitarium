package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/documents"
	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
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

// A message's files upload under names of their own, for a project or for
// the top-level conversation, and read back as themselves.
func TestAMessagesFilesUploadAndReadBack(t *testing.T) {
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
	png := base64.StdEncoding.EncodeToString([]byte{0x89, 'P', 'N', 'G'})
	for _, owner := range []string{"prj_shop", "commitarium"} {
		for _, want := range []string{"screenshot-2026-10-28-1612.png", "screenshot-2026-10-28-1612-2.png"} {
			recorder := call(http.MethodPost, "/api/v1/attachments/"+owner,
				`{"name":"screenshot-2026-10-28-1612.png","content_base64":"`+png+`"}`)
			var added documents.Document
			if recorder.Code != http.StatusCreated || json.Unmarshal(recorder.Body.Bytes(), &added) != nil || added.Name != want {
				t.Fatalf("%s: status %d: %s", owner, recorder.Code, recorder.Body.String())
			}
		}
		recorder := call(http.MethodGet, "/api/v1/attachments/"+owner+"/screenshot-2026-10-28-1612.png", "")
		if recorder.Code != http.StatusOK || recorder.Header().Get("Content-Type") != "image/png" ||
			recorder.Body.String() != "\x89PNG" {
			t.Fatalf("%s read back %d %q %q", owner, recorder.Code, recorder.Header().Get("Content-Type"), recorder.Body.String())
		}
	}
	big := base64.StdEncoding.EncodeToString(make([]byte, documents.MaxBytes+1))
	if recorder := call(http.MethodPost, "/api/v1/attachments/prj_shop",
		`{"name":"big.png","content_base64":"`+big+`"}`); recorder.Code != http.StatusBadRequest ||
		!strings.Contains(recorder.Body.String(), "larger than 5 MB") {
		t.Fatalf("an oversized file: %d %s", recorder.Code, recorder.Body.String())
	}
	if recorder := call(http.MethodGet, "/api/v1/attachments/commitarium/missing.png", ""); recorder.Code != http.StatusNotFound {
		t.Fatalf("a missing file: status %d", recorder.Code)
	}
}

// A session message's files are named in the message the agent reads; a
// file that is not there is refused in plain words.
func TestASessionMessageNamesItsFiles(t *testing.T) {
	store, err := documents.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Attach(t.Context(), "prj_test", "shot.png", []byte("png")); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	run := execution.Run{ID: "run_session", FeatureID: "fea_session", Status: execution.RunStatusRunning, StartedAt: now, UpdatedAt: now}
	features := &recordingFeatureService{getResult: feature.Feature{
		ID: "fea_session", ProjectID: "prj_test", Kind: feature.KindSession, State: feature.StateImplementing,
	}}
	messenger := &sessionMessengerStub{planningStarterStub: &planningStarterStub{run: run}}
	handler := NewWithWorkspaceRealWorkflowDeletionAndModels(
		&recordingProjectService{getByIDResult: project.Project{ID: "prj_test"}}, features, nil,
		&recordingExecutionService{run: run, runs: []execution.Run{run}}, nil, nil, nil, messenger,
		nil, nil, nil, nil, store)
	send := func(body, key string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/api/v1/projects/prj_test/features/fea_session/messages", strings.NewReader(body))
		request.Header.Set("Idempotency-Key", key)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder
	}
	if recorder := send(`{"message":"Like this","attachments":["shot.png"]}`, "msg-1"); recorder.Code != http.StatusAccepted {
		t.Fatalf("status %d: %s", recorder.Code, recorder.Body.String())
	}
	if want := "Like this\n\n" + documents.Attached([]string{"shot.png"}); messenger.command.Message != want {
		t.Fatalf("the agent reads %q", messenger.command.Message)
	}
	if recorder := send(`{"message":"And this","attachments":["gone.png"]}`, "msg-2"); recorder.Code != http.StatusBadRequest ||
		!strings.Contains(recorder.Body.String(), "attach it again") {
		t.Fatalf("a missing file: %d %s", recorder.Code, recorder.Body.String())
	}
}
