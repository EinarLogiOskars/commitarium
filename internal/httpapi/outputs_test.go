package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/conversations"
	"github.com/EinarLogiOskars/commitarium/internal/featureartifact"
	"github.com/EinarLogiOskars/commitarium/internal/outputs"
	"github.com/EinarLogiOskars/commitarium/internal/project"
)

// delegatingConversations starts a general helper as the assistant's
// service does.
type delegatingConversations struct {
	*helperConversations
	briefs []string
}

func (stub *delegatingConversations) StartTask(_ context.Context, scope, _, title, brief string, _ bool) (conversations.Helper, error) {
	stub.briefs = append(stub.briefs, scope+": "+brief)
	return conversations.Helper{ID: "tsk_1", Kind: conversations.HelperTask, Title: title, Agent: "codex"}, nil
}

// delegate starts a general helper at the top level as in a project.
func TestDelegateStartsAHelperAnywhere(t *testing.T) {
	for _, scope := range []string{conversations.TopLevel, "prj_test"} {
		stub := &delegatingConversations{helperConversations: &helperConversations{conversationStub: &conversationStub{
			action: conversations.Action{ID: "act_1", Status: conversations.ActionProposed, AssistantAction: featureartifact.AssistantAction{
				Type: featureartifact.AssistantDelegate, Reason: "Asked for", Title: "Pitch deck", Description: "A deck.",
			}},
		}}}
		projects := &recordingProjectService{getByIDResult: project.Project{ID: "prj_test"}}
		handler := NewWithWorkspaceRealWorkflowDeletionAndModels(projects, &recordingFeatureService{}, nil, nil, nil, nil, nil,
			nil, nil, nil, nil, nil, stub)
		if err := AssistantActionApplier(handler)(t.Context(), scope, "act_1"); err != nil {
			t.Fatalf("%s: %v", scope, err)
		}
		if len(stub.briefs) != 1 || stub.briefs[0] != scope+": A deck." || stub.started == nil ||
			stub.started.ID != "tsk_1" || stub.resolvedResult != "A helper is working on “Pitch deck”." {
			t.Fatalf("%s: briefs = %v started = %+v result = %q", scope, stub.briefs, stub.started, stub.resolvedResult)
		}
	}
}

// The app lists a scope's outputs and reads each; a helper's HTML is
// served sandboxed, and a file can be deleted.
func TestOutputRoutes(t *testing.T) {
	store, err := outputs.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	from := t.TempDir()
	if err := os.MkdirAll(filepath.Join(from, outputs.Dir), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, contents := range map[string]string{"deck.html": "<h1>Deck</h1>", "plan.docx": "PK docx",
		"logo.svg": "<svg/>", "data.weird": "bytes"} {
		if err := os.WriteFile(filepath.Join(from, outputs.Dir, name), []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.Collect(t.Context(), conversations.TopLevel, from, "tsk_1", "Deck"); err != nil {
		t.Fatal(err)
	}
	projects := &recordingProjectService{getByIDResult: project.Project{ID: "prj_test"}}
	handler := NewWithWorkspaceRealWorkflowDeletionAndModels(projects, &recordingFeatureService{}, nil, nil, nil, nil, nil,
		nil, nil, nil, nil, nil, store)

	list := httptest.NewRecorder()
	handler.ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/api/v1/outputs/commitarium", nil))
	var listed outputsResponse
	if err := json.Unmarshal(list.Body.Bytes(), &listed); err != nil || list.Code != http.StatusOK ||
		len(listed.Outputs) != 4 {
		t.Fatalf("list = %d %s", list.Code, list.Body)
	}
	read := httptest.NewRecorder()
	handler.ServeHTTP(read, httptest.NewRequest(http.MethodGet, "/api/v1/outputs/commitarium/deck.html", nil))
	if read.Code != http.StatusOK || read.Body.String() != "<h1>Deck</h1>" ||
		!strings.HasPrefix(read.Header().Get("Content-Security-Policy"), "sandbox") {
		t.Fatalf("read = %d %v %s", read.Code, read.Header(), read.Body)
	}
	// Office files and the unknown are attachments; SVG is an image, under
	// the same sandbox.
	for name, want := range map[string][2]string{
		"plan.docx":  {"application/vnd.openxmlformats-officedocument.wordprocessingml.document", `attachment; filename=plan.docx`},
		"data.weird": {"application/octet-stream", `attachment; filename=data.weird`},
		"logo.svg":   {"image/svg+xml", ""},
	} {
		got := httptest.NewRecorder()
		handler.ServeHTTP(got, httptest.NewRequest(http.MethodGet, "/api/v1/outputs/commitarium/"+name, nil))
		if got.Code != http.StatusOK || got.Header().Get("Content-Type") != want[0] ||
			got.Header().Get("Content-Disposition") != want[1] ||
			!strings.HasPrefix(got.Header().Get("Content-Security-Policy"), "sandbox") {
			t.Errorf("%s = %d %v", name, got.Code, got.Header())
		}
	}
	deleted := httptest.NewRecorder()
	handler.ServeHTTP(deleted, httptest.NewRequest(http.MethodDelete, "/api/v1/outputs/commitarium/deck.html", nil))
	missing := httptest.NewRecorder()
	handler.ServeHTTP(missing, httptest.NewRequest(http.MethodGet, "/api/v1/outputs/commitarium/deck.html", nil))
	if deleted.Code != http.StatusNoContent || missing.Code != http.StatusNotFound {
		t.Fatalf("delete = %d, then read = %d", deleted.Code, missing.Code)
	}
}

// revisingConversations sends a change back to a helper as the assistant's
// service does, and keeps a helper's files for the panel.
type revisingConversations struct {
	*helperConversations
	revised  []string
	dir      string
	deleted  []string
	listed   []conversations.Helper
	reviseOK string
}

func (stub *revisingConversations) ReviseTask(_ context.Context, scope, chat, target, change string) (string, error) {
	stub.revised = append(stub.revised, scope+"|"+chat+"|"+target+"|"+change)
	return stub.reviseOK, nil
}

func (stub *revisingConversations) OutputDeleted(_ context.Context, _, helper string) error {
	stub.deleted = append(stub.deleted, helper)
	return nil
}

func (stub *revisingConversations) HelperFiles(_ context.Context, _, helper string) (string, error) {
	if helper != "tsk_1" || stub.dir == "" {
		return "", conversations.ErrNotFound
	}
	return stub.dir, nil
}

func (stub *revisingConversations) HelpersOf(context.Context, string) ([]conversations.Helper, error) {
	return stub.listed, nil
}

// revise is applied at once, from the chat that sent it.
func TestReviseIsAppliedAtOnce(t *testing.T) {
	stub := &revisingConversations{reviseOK: "The helper on “Deck” is making the change.",
		helperConversations: &helperConversations{conversationStub: &conversationStub{
			action: conversations.Action{ID: "act_1", Status: conversations.ActionProposed, Chat: "chat_1",
				AssistantAction: featureartifact.AssistantAction{
					Type: featureartifact.AssistantRevise, Reason: "Feedback", Helper: "deck.html", Guidance: "Blue titles.",
				}},
		}}}
	projects := &recordingProjectService{getByIDResult: project.Project{ID: "prj_test"}}
	handler := NewWithWorkspaceRealWorkflowDeletionAndModels(projects, &recordingFeatureService{}, nil, nil, nil, nil, nil,
		nil, nil, nil, nil, nil, stub)
	if err := AssistantActionApplier(handler)(t.Context(), conversations.TopLevel, "act_1"); err != nil {
		t.Fatal(err)
	}
	if strings.Join(stub.revised, ",") != "commitarium|chat_1|deck.html|Blue titles." ||
		stub.resolvedResult != stub.reviseOK || stub.started != nil {
		t.Fatalf("revised = %v result = %q started = %+v", stub.revised, stub.resolvedResult, stub.started)
	}
}

// An output's versions are listed, read and restored; Done and deleting
// end its helper's work; a helper's files are served as they are built,
// sandboxed, and never through a link.
func TestOutputVersionAndHelperFileRoutes(t *testing.T) {
	store, _ := outputs.NewStore(t.TempDir())
	from := t.TempDir()
	path := filepath.Join(from, outputs.Dir, "cv.html")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, contents := range []string{"<p>v1</p>", "<p>v2</p>"} {
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Collect(t.Context(), "prj_test", from, "tsk_1", "CV"); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("/etc/passwd", filepath.Join(from, outputs.Dir, "secret.txt")); err != nil {
		t.Fatal(err)
	}
	stub := &revisingConversations{dir: from, helperConversations: &helperConversations{conversationStub: &conversationStub{}},
		listed: []conversations.Helper{{ID: "tsk_1", Kind: conversations.HelperTask, Kept: true, Done: true}}}
	projects := &recordingProjectService{getByIDResult: project.Project{ID: "prj_test"}}
	handler := NewWithWorkspaceRealWorkflowDeletionAndModels(projects, &recordingFeatureService{}, nil, nil, nil, nil, nil,
		nil, nil, nil, nil, nil, stub, store)
	serve := func(method, target string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(method, target, nil))
		return recorder
	}

	var listed outputsResponse
	if list := serve(http.MethodGet, "/api/v1/outputs/prj_test"); json.Unmarshal(list.Body.Bytes(), &listed) != nil ||
		len(listed.Outputs) != 1 || listed.Outputs[0].Version != 2 || !listed.Outputs[0].Kept || listed.Outputs[0].Working {
		t.Fatalf("list = %s", list.Body)
	}
	var versions outputVersionsResponse
	if got := serve(http.MethodGet, "/api/v1/outputs/prj_test/cv.html/versions"); json.Unmarshal(got.Body.Bytes(), &versions) != nil ||
		len(versions.Versions) != 2 || versions.Versions[0].Number != 2 {
		t.Fatalf("versions = %s", got.Body)
	}
	if got := serve(http.MethodGet, "/api/v1/outputs/prj_test/cv.html/versions/1"); got.Body.String() != "<p>v1</p>" ||
		!strings.HasPrefix(got.Header().Get("Content-Security-Policy"), "sandbox") {
		t.Fatalf("version 1 = %d %s", got.Code, got.Body)
	}
	if got := serve(http.MethodGet, "/api/v1/outputs/prj_test/cv.html/versions/x"); got.Code != http.StatusNotFound {
		t.Fatalf("version x = %d", got.Code)
	}
	if got := serve(http.MethodPost, "/api/v1/outputs/prj_test/cv.html/versions/1/restore"); got.Code != http.StatusOK {
		t.Fatalf("restore = %d %s", got.Code, got.Body)
	}
	if got := serve(http.MethodGet, "/api/v1/outputs/prj_test/cv.html"); got.Body.String() != "<p>v1</p>" {
		t.Fatalf("latest after restore = %s", got.Body)
	}

	// While the helper builds it: its files now, read-only and sandboxed.
	if err := os.WriteFile(path, []byte("<p>v3 in progress</p>"), 0o600); err != nil {
		t.Fatal(err)
	}
	var live helperFilesResponse
	if got := serve(http.MethodGet, "/api/v1/helper-files/prj_test/tsk_1"); json.Unmarshal(got.Body.Bytes(), &live) != nil ||
		len(live.Files) != 1 || live.Files[0].Name != "cv.html" || strings.Contains(got.Body.String(), from) {
		t.Fatalf("live = %s", got.Body)
	}
	if got := serve(http.MethodGet, "/api/v1/helper-files/prj_test/tsk_1/cv.html"); got.Body.String() != "<p>v3 in progress</p>" ||
		!strings.HasPrefix(got.Header().Get("Content-Security-Policy"), "sandbox") {
		t.Fatalf("live file = %d %s", got.Code, got.Body)
	}
	for _, target := range []string{"/api/v1/helper-files/prj_test/tsk_1/secret.txt", "/api/v1/helper-files/prj_test/tsk_1/..%2Fcv.html",
		"/api/v1/helper-files/prj_test/tsk_2", "/api/v1/helper-files/prj_test/tsk_2/cv.html"} {
		if got := serve(http.MethodGet, target); got.Code != http.StatusNotFound && got.Code != http.StatusBadRequest {
			t.Errorf("%s = %d %s", target, got.Code, got.Body)
		}
	}
	// A folder swapped for a link is not followed.
	if err := os.RemoveAll(filepath.Join(from, outputs.Dir)); err != nil {
		t.Fatal(err)
	}
	elsewhere := t.TempDir()
	if err := os.WriteFile(filepath.Join(elsewhere, "cv.html"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(from, outputs.Dir)); err != nil {
		t.Fatal(err)
	}
	if got := serve(http.MethodGet, "/api/v1/helper-files/prj_test/tsk_1/cv.html"); got.Code != http.StatusNotFound {
		t.Fatalf("a linked folder was served: %d %s", got.Code, got.Body)
	}

	if got := serve(http.MethodDelete, "/api/v1/outputs/prj_test/cv.html"); got.Code != http.StatusNoContent ||
		strings.Join(stub.deleted, ",") != "tsk_1" {
		t.Fatalf("delete = %d %v", got.Code, stub.deleted)
	}
}
