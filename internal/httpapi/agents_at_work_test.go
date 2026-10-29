package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/conversations"
	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/vision"
	"github.com/EinarLogiOskars/commitarium/internal/worker"
)

// listedHelpers lists the helpers the assistant started.
type listedHelpers struct {
	*conversationStub
	helpers []conversations.Helper
}

func (stub *listedHelpers) HelpersOf(context.Context, string) ([]conversations.Helper, error) {
	return stub.helpers, nil
}

// The Agents card reads a running run's lead (in a turn) and reviewer
// (waiting on it), and a helper the assistant started, in one read; finished
// helpers are left out.
func TestAgentsAtWorkListsRunAgentsAndHelpers(t *testing.T) {
	now := time.Date(2026, time.October, 29, 12, 0, 0, 0, time.UTC)
	projects := &recordingProjectService{getByIDResult: project.Project{ID: "prj_test"}}
	features := &recordingFeatureService{listResult: []feature.Feature{
		{ID: "fea_one", ProjectID: "prj_test", Title: "Ship it", State: feature.StateImplementing},
		{ID: "fea_done", ProjectID: "prj_test", Title: "Old", State: feature.StateCompleted},
	}}
	executions := &recordingExecutionService{
		runs: []execution.Run{{
			ID: "run_one", FeatureID: "fea_one", Status: execution.RunStatusRunning, StartedAt: now, UpdatedAt: now,
			AgentProviders: project.AgentProviders{Lead: "claude", Reviewer: "codex", LeadAgent: "claude", ReviewerAgent: "codex"},
			AgentModels:    project.AgentModels{Lead: "claude-opus-5-5", Reviewer: "gpt-6"},
		}},
		sessions: []execution.Session{
			{ID: "ses_lead", RunID: "run_one", AgentID: "claude", Role: worker.RoleLead, Status: execution.SessionStatusRunning},
			{ID: "ses_review", RunID: "run_one", AgentID: "codex", Role: worker.RoleReviewer, Status: execution.SessionStatusWaitingForUser},
		},
	}
	visions := waitingVision()
	visions.session.Model = "gpt-6"
	stub := &listedHelpers{conversationStub: &conversationStub{}, helpers: []conversations.Helper{
		{ID: "vis_1", Kind: conversations.HelperVision},
		{ID: "vis_old", Kind: conversations.HelperVision, Done: true},
	}}
	handler := NewWithWorkspaceRealWorkflowDeletionAndModels(projects, features, nil, executions, nil, nil, nil, nil, nil,
		nil, nil, nil, stub, visions)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/projects/prj_test/agents-at-work", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d: %s", recorder.Code, recorder.Body.String())
	}
	var response agentsAtWorkResponse
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(response.Agents) != 3 {
		t.Fatalf("agents = %+v", response.Agents)
	}
	lead, reviewer, writer := response.Agents[0], response.Agents[1], response.Agents[2]
	if lead.Role != "lead" || !lead.Working || lead.State != agentWorking || lead.Agent != "claude" ||
		lead.Model != "claude-opus-5-5" || lead.WorkOrderID != "fea_one" || lead.WorkOrderTitle != "Ship it" {
		t.Fatalf("lead = %+v", lead)
	}
	if reviewer.Role != "reviewer" || reviewer.Working || reviewer.State != agentIdle || reviewer.Model != "gpt-6" ||
		reviewer.Detail != "Waiting for the lead" {
		t.Fatalf("reviewer = %+v", reviewer)
	}
	if writer.Role != agentRoleVision || writer.Working || writer.State != agentWaiting || writer.Agent != "codex" ||
		writer.Model != "gpt-6" || writer.ID != "vis_1" {
		t.Fatalf("vision writer = %+v", writer)
	}
}

// A run waiting for the user leaves its agents waiting, in plain words, and
// a run that skips review has no reviewer.
func TestAgentsAtWorkWaitingRun(t *testing.T) {
	order := feature.Feature{ID: "fea_one", Title: "Ship it", State: feature.StateReadyToMerge}
	run := execution.Run{
		ID: "run_one", Status: execution.RunStatusWaitingForUser, WaitKind: execution.RunWaitKindMergeGate,
		AgentProviders: project.AgentProviders{Lead: "codex"}, AgentModels: project.AgentModels{Lead: "gpt-6"},
		Steps: project.WorkSteps{SkipReviewer: true},
	}
	agents := runAgentsOf(order, run, nil)
	if len(agents) != 1 || agents[0].Agent != "codex" || agents[0].State != agentWaiting ||
		agents[0].Detail != "Waiting to merge" {
		t.Fatalf("agents = %+v", agents)
	}
}

func TestAgentsAtWorkUnknownProject(t *testing.T) {
	projects := &recordingProjectService{getByIDErr: project.ErrNotFound}
	handler := NewWithWorkspaceRealWorkflowDeletionAndModels(projects, &recordingFeatureService{}, nil,
		&recordingExecutionService{}, nil, nil, nil, nil, nil, nil, nil, nil, &listedHelpers{conversationStub: &conversationStub{}},
		&helperVisions{session: vision.Session{}})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/projects/prj_none/agents-at-work", nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status %d: %s", recorder.Code, recorder.Body.String())
	}
}
