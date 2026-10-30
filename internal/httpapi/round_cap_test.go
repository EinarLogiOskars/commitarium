package httpapi

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/conversations"
	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/featureartifact"
	"github.com/EinarLogiOskars/commitarium/internal/project"
)

// roundCapExecutions is a run waiting on its round limit, keeping the
// recoveries recorded on it.
type roundCapExecutions struct {
	*blockedExecutions
}

func (stub *roundCapExecutions) RunBlocker(context.Context, string) (execution.Blocker, bool, error) {
	return execution.Blocker{}, false, nil
}

func (stub *roundCapExecutions) RecordRunRecovery(_ context.Context, recovery execution.Recovery) error {
	stub.recoveries = append(stub.recoveries, recovery)
	return nil
}

// roundCapWorkflowStub continues runs past their round limit.
type roundCapWorkflowStub struct {
	*planningStarterStub
	runID, guidance string
	fromAssistant   bool
}

func (stub *roundCapWorkflowStub) ContinueRoundCap(
	_ context.Context, runID, _, guidance string, fromAssistant bool,
) (execution.Run, bool, error) {
	stub.runID, stub.guidance, stub.fromAssistant = runID, guidance, fromAssistant
	return execution.Run{ID: runID, Status: execution.RunStatusRunning}, true, nil
}

// Mayka handles a work order's first round limit: autopilot continues it
// with her guidance and the inbox stays quiet. The next round limit is the
// user's: autopilot refuses to continue it again, and the inbox asks the
// user plainly (ADR-032).
func TestMaykaHandlesTheFirstRoundLimit(t *testing.T) {
	now := time.Now().UTC()
	stub := &conversationStub{
		action: conversations.Action{
			ID: "act_1", Status: conversations.ActionProposed,
			AssistantAction: featureartifact.AssistantAction{
				Type: featureartifact.AssistantContinueWorkOrder, Reason: "The plan settles it",
				WorkOrderID: "fea_log", Guidance: "Keep the export as CSV, as the plan decides.",
			},
		},
		handling: map[string]bool{"fea_log": true},
	}
	features := &recordingFeatureService{
		getResult: feature.Feature{
			ID: "fea_log", ProjectID: "prj_test", Title: "Show the log", CreatedBy: feature.CreatedByAssistant,
		},
		listResult: []feature.Feature{{ID: "fea_log", ProjectID: "prj_test", Title: "Show the log", State: feature.StatePlanning}},
	}
	projects := &recordingProjectService{
		getByIDResult: project.Project{ID: "prj_test"}, listResult: []project.Project{{ID: "prj_test", Name: "Test"}},
	}
	executions := &roundCapExecutions{&blockedExecutions{recordingExecutionService: &recordingExecutionService{
		runs: []execution.Run{{
			ID: "run_1", FeatureID: "fea_log", StartedAt: now, UpdatedAt: now, Status: execution.RunStatusWaitingForUser,
			WaitKind: execution.RunWaitKindRoundCap, Reason: "The planning discussion completed 10 dialogue rounds without a submitted plan.",
		}},
	}}}
	workflow := &roundCapWorkflowStub{planningStarterStub: &planningStarterStub{run: execution.Run{ID: "run_1"}}}
	handler := NewWithWorkspaceRealWorkflowDeletionAndModels(projects, features, nil, executions, nil, nil, nil, workflow, nil, nil, nil, nil, stub)
	api := &API{projects: projects, features: features, execution: executions, conversations: stub, now: time.Now}
	attention := func() attentionItem {
		t.Helper()
		items, _, err := api.collectAttention(t.Context())
		if err != nil || len(items) != 1 {
			t.Fatalf("attention = %+v, %v", items, err)
		}
		return items[0]
	}
	apply := func() error {
		stub.action.Status = conversations.ActionProposed
		return AssistantActionApplier(handler)(t.Context(), "prj_test", "act_1")
	}

	if item := attention(); item.Kind != "autopilot_handling" || item.Actionable {
		t.Fatalf("the first round limit = %+v", item)
	}
	if err := apply(); err != nil {
		t.Fatal(err)
	}
	if workflow.runID != "run_1" || !workflow.fromAssistant || !strings.Contains(workflow.guidance, "as CSV") ||
		len(executions.recoveries) != 1 || executions.recoveries[0].Fingerprint != execution.RoundCapFingerprint {
		t.Fatalf("continued %+v, recoveries %+v", workflow, executions.recoveries)
	}

	// The run reached its (extended) round limit again.
	workflow.runID = ""
	if err := apply(); err == nil || !strings.Contains(err.Error(), "reached its round limit again") || workflow.runID != "" {
		t.Fatalf("continuing again = %v (continued %q)", err, workflow.runID)
	}
	if item := attention(); !item.Actionable || item.Title != "This needs your decision" ||
		!strings.Contains(item.Detail, "Show the log: its agents still can't agree") {
		t.Fatalf("the second round limit = %+v", item)
	}
}
