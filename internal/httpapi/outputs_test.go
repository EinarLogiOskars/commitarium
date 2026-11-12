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

func (stub *delegatingConversations) StartTask(_ context.Context, scope, _, title, brief string) (conversations.Helper, error) {
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
