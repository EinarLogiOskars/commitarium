package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/conversations"
	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/featureartifact"
	"github.com/EinarLogiOskars/commitarium/internal/previewcheck"
	"github.com/EinarLogiOskars/commitarium/internal/project"
)

const checkedCommit = "0123456789abcdef0123456789abcdef01234567"

// previewCheckWorkflow is a real workflow that hears about finished checks.
type previewCheckWorkflow struct {
	*planningStarterStub
	completed []string
}

func (workflow *previewCheckWorkflow) PreviewCheckCompleted(_ context.Context, checkID string) error {
	workflow.completed = append(workflow.completed, checkID)
	return nil
}

func TestPreviewCheckEndpointsRunTheDesktopLifecycle(t *testing.T) {
	checks := previewcheck.NewService(previewcheck.NewMemoryStore())
	requested, _, err := checks.Request(t.Context(), "prj_test", "fea_setup", "run_setup", checkedCommit)
	if err != nil {
		t.Fatal(err)
	}
	features := &recordingFeatureService{getResult: feature.Feature{ID: "fea_setup", ProjectID: "prj_test"}}
	executions := &recordingExecutionService{runs: []execution.Run{{ID: "run_setup", StartedAt: time.Now()}}}
	workflow := &previewCheckWorkflow{planningStarterStub: &planningStarterStub{}}
	handler := NewWithWorkspaceRealWorkflowDeletionAndModels(
		&recordingProjectService{}, features, nil, executions, nil, nil, nil, workflow, nil, nil, nil, nil, checks,
	)
	serve := func(method, path, body string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(method, path, strings.NewReader(body)))
		return recorder
	}

	pending := serve(http.MethodGet, "/api/v1/preview-checks/pending", "")
	if pending.Code != http.StatusOK || !strings.Contains(pending.Body.String(), requested.ID) {
		t.Fatalf("pending = %d %s", pending.Code, pending.Body.String())
	}
	claimed := serve(http.MethodPost, "/api/v1/preview-checks/"+requested.ID+"/claim", "")
	if claimed.Code != http.StatusOK || !strings.Contains(claimed.Body.String(), `"status":"running"`) {
		t.Fatalf("claim = %d %s", claimed.Code, claimed.Body.String())
	}
	completion := `{"passed":false,"evidence":{"stage":"start","reason":"service api exited with code 1","open":"web:5173",` +
		`"services":[{"service":"api","state":"exited","exit_code":1,"logs":"boom\n"}]}}`
	completed := serve(http.MethodPost, "/api/v1/preview-checks/"+requested.ID+"/complete", completion)
	if completed.Code != http.StatusOK || !strings.Contains(completed.Body.String(), `"status":"failed"`) ||
		len(workflow.completed) != 1 || workflow.completed[0] != requested.ID {
		t.Fatalf("complete = %d %s, workflow heard %v", completed.Code, completed.Body.String(), workflow.completed)
	}
	if again := serve(http.MethodPost, "/api/v1/preview-checks/"+requested.ID+"/complete", `{"passed":true,"evidence":{"stage":"ready","services":[]}}`); again.Code != http.StatusOK ||
		!strings.Contains(again.Body.String(), `"status":"failed"`) {
		t.Fatalf("a repeated completion changed the check: %d %s", again.Code, again.Body.String())
	}

	listed := serve(http.MethodGet, "/api/v1/projects/prj_test/features/fea_setup/preview-checks", "")
	var body struct {
		Checks []previewcheck.Check `json:"checks"`
	}
	if listed.Code != http.StatusOK || json.Unmarshal(listed.Body.Bytes(), &body) != nil || len(body.Checks) != 1 ||
		body.Checks[0].Evidence == nil || body.Checks[0].Evidence.Services[0].Logs != "boom\n" {
		t.Fatalf("work order checks = %d %s", listed.Code, listed.Body.String())
	}
	if missing := serve(http.MethodPost, "/api/v1/preview-checks/pvc_missing/claim", ""); missing.Code != http.StatusNotFound {
		t.Fatalf("claiming an unknown check = %d", missing.Code)
	}
}

func TestSetUpPreviewActionStartsTheCheckedFix(t *testing.T) {
	now := time.Date(2026, time.October, 14, 12, 0, 0, 0, time.UTC)
	// The automatic fixes are used up; the assistant's still starts.
	history := []feature.Feature{
		{Title: previewFixTitle, Background: true, State: feature.StateCompleted, CreatedAt: now},
		{Title: previewFixTitle, Background: true, State: feature.StateCompleted, CreatedAt: now.Add(time.Minute)},
	}
	api, features, starter := previewSetupAPI([]project.RepositoryTreeEntry{{Path: "compose.yaml", Type: "blob"}}, history)
	api.toolchains = &recordingToolchainService{}
	stub := &conversationStub{action: conversations.Action{
		ID: "act_preview", Status: conversations.ActionProposed,
		AssistantAction: featureartifact.AssistantAction{
			Type: featureartifact.AssistantSetUpPreview, Reason: "The app does not open",
			Guidance: "The api container exits: ModuleNotFoundError: fastapi.",
		},
	}}
	api.conversations = stub
	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{}`))
	request.SetPathValue("scope", "prj_test")
	request.SetPathValue("actionID", "act_preview")
	recorder := httptest.NewRecorder()
	api.approveConversationActionHandler(recorder, request)

	if recorder.Code != http.StatusOK || stub.resolvedStatus != conversations.ActionApplied ||
		!strings.Contains(stub.resolvedResult, "fea_setup") {
		t.Fatalf("set up preview = %d %s, resolved %s %q", recorder.Code, recorder.Body.String(), stub.resolvedStatus, stub.resolvedResult)
	}
	if features.receivedTitle != previewFixTitle || !features.receivedOverrides.PreviewCheck ||
		!features.receivedOverrides.Background ||
		!strings.Contains(features.receivedDescription, "ModuleNotFoundError: fastapi") || starter.featureID != "fea_setup" {
		t.Fatalf("the checked fix did not start: %q %+v %q", features.receivedTitle, features.receivedOverrides, features.receivedDescription)
	}
}

// reportingConversationStub records what the assistant was told.
type reportingConversationStub struct {
	conversationStub
	reported []string
}

func (stub *reportingConversationStub) Report(_ context.Context, scope, text string) error {
	stub.reported = append(stub.reported, scope+": "+text)
	return nil
}

func TestAPreviewFailureTellsTheAssistantWhatEachContainerDid(t *testing.T) {
	stub := &reportingConversationStub{}
	api := &API{projects: &recordingProjectService{getByIDResult: project.Project{ID: "prj_test"}}, conversations: stub}
	body := `{"error":"a preview service exited with an error","evidence":{"stage":"start","services":[` +
		`{"service":"web","state":"running"},{"service":"api","state":"exited","exit_code":1,"logs":"ModuleNotFoundError: fastapi\n"}]}}`
	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	request.SetPathValue("id", "prj_test")
	recorder := httptest.NewRecorder()
	api.reportPreviewFailureHandler(recorder, request)

	if recorder.Code != http.StatusNoContent || len(stub.reported) != 1 {
		t.Fatalf("report = %d, told %v", recorder.Code, stub.reported)
	}
	told := stub.reported[0]
	if !strings.HasPrefix(told, `prj_test: The app's preview failed to start: the "api" part of the app stops right after it starts (exit code 1).`) ||
		!strings.Contains(told, "set_up_preview") || !strings.Contains(told, "ModuleNotFoundError: fastapi") {
		t.Fatalf("the assistant was told %q", told)
	}
}
