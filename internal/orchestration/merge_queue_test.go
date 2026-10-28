package orchestration

import (
	"strings"
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/database"
	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/project"
)

// addWaitingRun adds another work order of the fixture's project whose run
// waits at the merge gate, as one in line to merge would.
func (fixture *baseUpdateFixture) addWaitingRun(t *testing.T, runID string) {
	t.Helper()
	other, err := feature.NewService(fixture.features, project.NewService(database.NewProjectStore(fixture.db))).Create(
		t.Context(), fixture.project.ID, "Other work "+runID, "Runs side by side", feature.SettingsOverrides{},
	)
	if err != nil {
		t.Fatalf("create other work order: %v", err)
	}
	if _, _, err := fixture.executions.CreateRun(
		t.Context(), runID, other.ID, 3, 3, fixture.project.AgentProviders, project.MergePolicyAutoAfterGates,
	); err != nil {
		t.Fatalf("create other run: %v", err)
	}
	if _, err := fixture.executions.TransitionRun(
		t.Context(), runID, execution.RunStatusRunning, execution.RunStatusWaitingForUser,
		execution.MergeQueueReason(1), execution.RunWaitKindMergeGate,
	); err != nil {
		t.Fatalf("wait other run at the merge gate: %v", err)
	}
}

func (fixture *baseUpdateFixture) line(t *testing.T) []string {
	t.Helper()
	line, err := fixture.executions.MergeQueue(t.Context(), fixture.project.ID)
	if err != nil {
		t.Fatalf("read the merge queue: %v", err)
	}
	return line
}

// A work order whose gates pass while another is first in line waits its
// turn, without merging or updating. When the first merges, its turn comes:
// main moved, so the lead brings it in, and the update merges.
func TestQueuedWorkOrderWaitsItsTurnThenUpdatesOntoMainAndMerges(t *testing.T) {
	fixture := startBaseUpdateRun(t, "run_queued_second", false, project.MergePolicyAutoAfterGates, 1,
		func(fixture *baseUpdateFixture) {
			fixture.addWaitingRun(t, "run_queued_first")
			if err := fixture.executions.JoinMergeQueue(t.Context(), fixture.project.ID, "run_queued_first"); err != nil {
				t.Fatal(err)
			}
		})
	waiting := fixture.waitAtMergeGate(t)
	if waiting.Reason != execution.MergeQueueReason(2) || !strings.HasPrefix(waiting.Reason, "Waiting to merge: 2nd in line.") ||
		fixture.workspace.mergeCalls != 0 || len(fixture.updateRequests()) != 0 {
		t.Fatalf("second in line did not wait its turn: run=%+v merges=%d", waiting, fixture.workspace.mergeCalls)
	}
	if line := fixture.line(t); len(line) != 2 || line[0] != "run_queued_first" || line[1] != fixture.runID {
		t.Fatalf("line = %v", line)
	}

	// The first merges; main moved past the second meanwhile.
	fixture.workspace.mainHead = baseUpdateMainHead
	fixture.workspace.mergeErrs = []error{baseUpdateNoMerge}
	if _, err := fixture.executions.TransitionRun(
		t.Context(), "run_queued_first", execution.RunStatusWaitingForUser, execution.RunStatusSucceeded, "Merged.",
	); err != nil {
		t.Fatal(err)
	}
	fixture.starter.leaveMergeQueue(t.Context(), "run_queued_first")

	fixture.waitMerged(t)
	if merged := fixture.workspace.mergedCommits; len(merged) != 1 || merged[0] != baseUpdateCommits[0] ||
		len(fixture.updateRequests()) != 1 {
		t.Fatalf("merged %v after %d updates, want the update's commit after one", merged, len(fixture.updateRequests()))
	}
	run, err := fixture.executions.GetRun(t.Context(), fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	if failed, err := fixture.starter.baseUpdatesFailed(t.Context(), run); err != nil || failed != 0 {
		t.Fatalf("failed updates = %d err=%v, want none", failed, err)
	}
	if line := fixture.line(t); len(line) != 0 {
		t.Fatalf("line after the merges = %v", line)
	}
}

// The line keeps its order across a restart: a new coordinator over the same
// database finds the same runs in the same places, and drops one that left
// it while it was down.
func TestMergeQueueKeepsItsOrderAcrossARestart(t *testing.T) {
	fixture := startBaseUpdateRun(t, "run_restart_last", false, project.MergePolicyAutoAfterGates, 0,
		func(fixture *baseUpdateFixture) {
			for _, runID := range []string{"run_restart_a", "run_restart_b", "run_restart_c"} {
				fixture.addWaitingRun(t, runID)
				if err := fixture.executions.JoinMergeQueue(t.Context(), fixture.project.ID, runID); err != nil {
					t.Fatal(err)
				}
			}
		})
	if waiting := fixture.waitAtMergeGate(t); waiting.Reason != execution.MergeQueueReason(4) {
		t.Fatalf("reason = %q, want 4th in line", waiting.Reason)
	}
	// b blocks while the coordinator is down.
	if _, err := fixture.executions.TransitionRun(
		t.Context(), "run_restart_b", execution.RunStatusWaitingForUser, execution.RunStatusWaitingForUser,
		"Blocked.", execution.RunWaitKindBlocker,
	); err != nil {
		t.Fatal(err)
	}

	restarted, err := NewRemoteLeadStarter(RemoteLeadConfig{
		Executions: execution.NewService(database.NewExecutionStore(fixture.db)), Features: fixture.features,
		Goals: fixture.workflow, Planning: fixture.workflow, Briefs: fixture.workflow, Workspaces: fixture.workspace,
		Worker: fixture.worker, Pump: &conversationalRemoteLeadPump{executions: fixture.executions, worker: fixture.worker},
		Lifetime: t.Context(),
	})
	if err != nil {
		t.Fatal(err)
	}
	place, err := restarted.mergePlace(t.Context(), fixture.project.ID, fixture.runID)
	if err != nil || place != 3 {
		t.Fatalf("place after the restart = %d err=%v, want 3rd", place, err)
	}
	if line := fixture.line(t); strings.Join(line, ",") != "run_restart_a,run_restart_c,"+fixture.runID {
		t.Fatalf("line after the restart = %v", line)
	}
	restarted.advanceMergeQueue(t.Context(), fixture.project.ID)
	if run, err := fixture.executions.GetRun(t.Context(), "run_restart_c"); err != nil ||
		run.Reason != execution.MergeQueueReason(2) {
		t.Fatalf("c's reason = %q err=%v, want 2nd in line", run.Reason, err)
	}
}

// While the first in line brings main in, the others wait behind it: only
// one updates at a time.
func TestWorkOrdersWaitBehindOneUpdatingOntoMain(t *testing.T) {
	fixture := startBaseUpdateRun(t, "run_side_last", false, project.MergePolicyAutoAfterGates, 0,
		func(fixture *baseUpdateFixture) {
			for _, runID := range []string{"run_side_1", "run_side_2"} {
				fixture.addWaitingRun(t, runID)
			}
			if _, err := fixture.executions.TransitionRun(
				t.Context(), "run_side_1", execution.RunStatusWaitingForUser, execution.RunStatusRunning,
				implementationBaseUpdateRunningReason,
			); err != nil {
				t.Fatal(err)
			}
			for _, runID := range []string{"run_side_1", "run_side_2"} {
				if err := fixture.executions.JoinMergeQueue(t.Context(), fixture.project.ID, runID); err != nil {
					t.Fatal(err)
				}
			}
		})
	if waiting := fixture.waitAtMergeGate(t); waiting.Reason != execution.MergeQueueReason(3) {
		t.Fatalf("reason = %q, want 3rd in line behind one updating and one waiting", waiting.Reason)
	}
}

// Being overtaken is normal: a work order whose main moves on after each
// update brings it in again and again without reaching the limit, which
// counts only failed updates. An update Forgejo still refuses with main
// where the lead brought it in from failed (see
// TestAutomaticUpdatesStopAtTheLimitAndContinueTakesGuidance).
func TestBeingOvertakenDoesNotCountTowardTheUpdateLimit(t *testing.T) {
	updates := maxAutomaticBaseUpdates + 1
	fixture := startBaseUpdateRun(t, "run_overtaken", false, project.MergePolicyAutoAfterGates, updates,
		func(fixture *baseUpdateFixture) {
			fixture.workspace.mainHead = baseUpdateMainHead
			fixture.workspace.mainMoves = true
			for range updates {
				fixture.workspace.mergeErrs = append(fixture.workspace.mergeErrs, baseUpdateNoMerge)
			}
		})
	run := fixture.waitMerged(t)
	if len(fixture.updateRequests()) != updates {
		t.Fatalf("%d updates, want %d", len(fixture.updateRequests()), updates)
	}
	if merged := fixture.workspace.mergedCommits; len(merged) != 1 || merged[0] != baseUpdateCommits[updates-1] {
		t.Fatalf("merged %v, want the last update's commit", merged)
	}
	if failed, err := fixture.starter.baseUpdatesFailed(t.Context(), run); err != nil || failed != 0 {
		t.Fatalf("failed updates = %d err=%v, want none", failed, err)
	}
}
