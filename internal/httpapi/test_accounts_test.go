package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/testaccounts"
)

// Settings adds, lists, changes, copies and removes a project's test
// logins; the list never carries a password, and the host reads the
// preview's file with them.
func TestTestAccountRoutes(t *testing.T) {
	store, err := testaccounts.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	projects := &recordingProjectService{getByIDResult: project.Project{ID: "prj_test"}}
	handler := NewWithWorkspaceRealWorkflowDeletionAndModels(projects, &recordingFeatureService{}, nil, nil, nil, nil, nil,
		nil, nil, nil, nil, nil, store)
	serve := func(method, path, body string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(method, path, strings.NewReader(body)))
		return recorder
	}

	added := serve(http.MethodPost, "/api/v1/projects/prj_test/test-accounts",
		`{"label":"Admin","role":"admin","note":"Sees everything"}`)
	var account testaccounts.Summary
	if err := json.Unmarshal(added.Body.Bytes(), &account); err != nil || added.Code != http.StatusCreated ||
		account.Username != "admin@commitarium.test" {
		t.Fatalf("add = %d %s", added.Code, added.Body)
	}
	if strings.Contains(added.Body.String(), "password") {
		t.Fatalf("add answered a password: %s", added.Body)
	}

	password := serve(http.MethodGet, "/api/v1/projects/prj_test/test-accounts/admin@commitarium.test/password", "")
	var revealed map[string]string
	if err := json.Unmarshal(password.Body.Bytes(), &revealed); err != nil || password.Code != http.StatusOK ||
		len(revealed["password"]) < 20 || password.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("password = %d %v", password.Code, password.Header())
	}

	list := serve(http.MethodGet, "/api/v1/projects/prj_test/test-accounts", "")
	var listed testAccountsResponse
	if err := json.Unmarshal(list.Body.Bytes(), &listed); err != nil || list.Code != http.StatusOK ||
		len(listed.Accounts) != 1 || listed.Accounts[0].Note != "Sees everything" {
		t.Fatalf("list = %d %s", list.Code, list.Body)
	}
	if strings.Contains(list.Body.String(), revealed["password"]) || strings.Contains(list.Body.String(), "password") {
		t.Fatal("the list carries the password")
	}

	updated := serve(http.MethodPatch, "/api/v1/projects/prj_test/test-accounts/admin@commitarium.test", `{"role":"owner"}`)
	if err := json.Unmarshal(updated.Body.Bytes(), &account); err != nil || updated.Code != http.StatusOK ||
		account.Role != "owner" || account.Note != "Sees everything" {
		t.Fatalf("update = %d %s", updated.Code, updated.Body)
	}

	preview := serve(http.MethodGet, "/api/v1/projects/prj_test/test-accounts/preview", "")
	var file testaccounts.PreviewFile
	if err := json.Unmarshal(preview.Body.Bytes(), &file); err != nil || preview.Code != http.StatusOK ||
		file.Domain != "commitarium.test" || len(file.Accounts) != 1 ||
		file.Accounts[0].Password != revealed["password"] || file.Accounts[0].Role != "owner" {
		t.Fatalf("preview = %d", preview.Code)
	}

	if invalid := serve(http.MethodPost, "/api/v1/projects/prj_test/test-accounts", `{"label":""}`); invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid = %d %s", invalid.Code, invalid.Body)
	}
	if unknown := serve(http.MethodPost, "/api/v1/projects/prj_test/test-accounts", `{"label":"A","password":"mine"}`); unknown.Code != http.StatusBadRequest {
		t.Fatalf("a chosen password = %d %s", unknown.Code, unknown.Body)
	}

	removed := serve(http.MethodDelete, "/api/v1/projects/prj_test/test-accounts/admin@commitarium.test", "")
	if removed.Code != http.StatusNoContent {
		t.Fatalf("remove = %d %s", removed.Code, removed.Body)
	}
	if missing := serve(http.MethodGet, "/api/v1/projects/prj_test/test-accounts/admin@commitarium.test/password", ""); missing.Code != http.StatusNotFound {
		t.Fatalf("removed password = %d", missing.Code)
	}
	emptied := serve(http.MethodGet, "/api/v1/projects/prj_test/test-accounts/preview", "")
	if !strings.Contains(emptied.Body.String(), `"accounts":[]`) {
		t.Fatalf("emptied preview = %s", emptied.Body)
	}
}

func TestTestAccountRoutesNeedTheProject(t *testing.T) {
	store, err := testaccounts.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	projects := &recordingProjectService{getByIDErr: project.ErrNotFound}
	handler := NewWithWorkspaceRealWorkflowDeletionAndModels(projects, &recordingFeatureService{}, nil, nil, nil, nil, nil,
		nil, nil, nil, nil, nil, store)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/projects/prj_gone/test-accounts", nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("list = %d %s", recorder.Code, recorder.Body)
	}
}
