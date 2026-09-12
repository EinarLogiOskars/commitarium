package database

import (
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/worker"
)

func TestExecutionStoreBeginsReplanningTurnAtomically(t *testing.T) {
	db, store := newTestExecutionStore(t)
	run, original := createExecutionRecords(t, db, store)
	now := run.UpdatedAt.Add(time.Minute)
	if _, err := store.TransitionSession(t.Context(), execution.SessionTransition{
		SessionID: original.ID, Expected: execution.SessionStatusRunning,
		Status: execution.SessionStatusCompleted, OccurredAt: now,
	}); err != nil {
		t.Fatalf("complete fixture session: %v", err)
	}
	lead := execution.Session{
		ID: run.ID + ":lead", RunID: run.ID, AgentID: "codex-lead",
		Role: worker.RoleLead, Status: execution.SessionStatusWaitingForUser,
		ProviderSessionID: "provider-lead", StartedAt: now, UpdatedAt: now,
	}
	if err := store.CreateSession(t.Context(), lead); err != nil {
		t.Fatalf("create lead session: %v", err)
	}
	checkpoint, _, err := store.CreateWorkerAttempt(t.Context(), execution.WorkerAttemptCheckpoint{
		SessionID: lead.ID, AttemptID: lead.ID + ":planning:3",
		CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatalf("create prior attempt: %v", err)
	}
	planEvent, _, err := store.AppendEvent(t.Context(), execution.PendingEvent{
		ID: "sev_plan_v1", SessionID: lead.ID, Type: worker.EventPlanSubmitted,
		Text: "Original accepted plan", OccurredAt: now,
	})
	if err != nil {
		t.Fatalf("append original plan: %v", err)
	}
	if _, _, err := store.LinkPlanningMessage(t.Context(), execution.PendingPlanningMessage{
		RunID: run.ID, EventID: planEvent.ID, LinkedAt: now,
	}); err != nil {
		t.Fatalf("link original plan: %v", err)
	}
	if _, err := db.ExecContext(
		t.Context(),
		`UPDATE features
		 SET state = ?, accepted_goal = ?, goal_accepted_at = ?, updated_at = ?
		 WHERE id = ?`,
		feature.StatePlanning, "Original accepted goal", formatExecutionTime(now),
		formatExecutionTime(now), run.FeatureID,
	); err != nil {
		t.Fatalf("prepare accepted feature: %v", err)
	}
	baseline := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, err := db.ExecContext(
		t.Context(),
		`INSERT INTO feature_workspaces (
		    feature_id, id, project_id, repository_owner, repository_name,
		    base_branch, branch_name, base_commit_id, status, branch_created_at,
		    checkout_relative_path, checkout_created_at,
		    pull_request_number, pull_request_url, pull_request_recorded_at,
		    approved_commit_id, merge_ready_at, merge_commit_id, merged_at,
		    created_at, updated_at
		 ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '', NULL, ?, ?)`,
		run.FeatureID, "wsp_replanning", "prj_execution_test", "owner", "repository",
		"main", "commitarium/fea_execution_test", baseline, "branch_ready",
		formatExecutionTime(now), "wsp_replanning", formatExecutionTime(now),
		8, "http://forgejo/owner/repository/pulls/8", formatExecutionTime(now),
		baseline, formatExecutionTime(now), formatExecutionTime(now), formatExecutionTime(now),
	); err != nil {
		t.Fatalf("create ready workspace: %v", err)
	}
	if _, err := store.TransitionRun(t.Context(), execution.RunTransition{
		RunID: run.ID, Expected: execution.RunStatusRunning,
		Status: execution.RunStatusWaitingForUser, Reason: "Plan accepted.",
		WaitKind: execution.RunWaitKindPhaseCheckpoint, OccurredAt: now.Add(time.Second),
	}); err != nil {
		t.Fatalf("create paused checkpoint: %v", err)
	}
	queued, _, err := store.QueueIntervention(t.Context(), execution.InterventionRequest{
		ID: "int_scope_change", RunID: run.ID, Target: worker.RoleLead,
		Message: "Also add CSV export.", OccurredAt: now.Add(2 * time.Second),
	})
	if err != nil {
		t.Fatalf("queue scope change: %v", err)
	}
	answeredAt := now.Add(3 * time.Second)
	if _, err := db.ExecContext(
		t.Context(),
		`UPDATE run_interventions
		 SET status = ?, attempt_id = ?, effect = ?, answered_at = ?, updated_at = ?
		 WHERE id = ?`,
		execution.InterventionStatusAnswered, "att_intervention", worker.InterventionEffectReplanningRequired,
		formatExecutionTime(answeredAt), formatExecutionTime(answeredAt), queued.Intervention.ID,
	); err != nil {
		t.Fatalf("answer scope change: %v", err)
	}
	admission := execution.ReplanningTurnAdmission{
		ID: "resume_replanning", RunID: run.ID, InterventionID: queued.Intervention.ID,
		PlanVersion: 2, PreviousPlanEventID: planEvent.ID,
		EffectiveGoal:             "Original accepted goal\n\nApproved scope amendment for plan version 2:\nAlso add CSV export.",
		BaselineCommitID:          baseline,
		PreviousAttemptID:         checkpoint.AttemptID,
		PreviousLastEventSequence: checkpoint.LastEventSequence,
		NextAttempt: execution.WorkerAttemptCheckpoint{
			SessionID: lead.ID, AttemptID: lead.ID + ":planning:v2:1",
			CreatedAt: answeredAt.Add(time.Second), UpdatedAt: answeredAt.Add(time.Second),
		},
		RunReason:  "The lead is revising the plan after an approved scope change.",
		OccurredAt: answeredAt.Add(time.Second),
	}

	active, revision, admitted, err := store.BeginReplanningTurn(t.Context(), admission)
	if err != nil || !admitted {
		t.Fatalf("begin replanning: run=%+v revision=%+v admitted=%t err=%v", active, revision, admitted, err)
	}
	if active.Status != execution.RunStatusRunning || active.Paused || active.WaitKind != "" ||
		active.PlanVersion != 2 || revision.BaselineCommitID != baseline ||
		revision.PreviousPlanEventID != planEvent.ID {
		t.Fatalf("unexpected replanning result run=%+v revision=%+v", active, revision)
	}
	storedRevision, err := store.GetPlanRevision(t.Context(), run.ID, 2)
	if err != nil || storedRevision != revision {
		t.Fatalf("stored revision=%+v want=%+v err=%v", storedRevision, revision, err)
	}
	intervention, err := store.GetLatestIntervention(t.Context(), run.ID)
	if err != nil || intervention.ResolvedAt == nil ||
		intervention.ResolutionActionID != admission.ID {
		t.Fatalf("scope change was not resolved atomically: intervention=%+v err=%v", intervention, err)
	}
	activeLead, err := store.GetSession(t.Context(), lead.ID)
	if err != nil || activeLead.Status != execution.SessionStatusRunning {
		t.Fatalf("lead was not activated: session=%+v err=%v", activeLead, err)
	}
	next, err := store.GetWorkerAttempt(t.Context(), lead.ID)
	if err != nil || next.AttemptID != admission.NextAttempt.AttemptID || next.LastEventSequence != 0 {
		t.Fatalf("worker attempt was not replaced: checkpoint=%+v err=%v", next, err)
	}
	var approvedCommit string
	var mergeReadyAt *string
	if err := db.QueryRowContext(
		t.Context(),
		`SELECT approved_commit_id, merge_ready_at FROM feature_workspaces WHERE feature_id = ?`,
		run.FeatureID,
	).Scan(&approvedCommit, &mergeReadyAt); err != nil {
		t.Fatalf("read cleared merge approval: %v", err)
	}
	if approvedCommit != "" || mergeReadyAt != nil {
		t.Fatalf("stale merge approval survived replanning: commit=%q ready_at=%v", approvedCommit, mergeReadyAt)
	}
	retriedRun, retriedRevision, admitted, err := store.BeginReplanningTurn(t.Context(), admission)
	if err != nil || admitted || retriedRun != active || retriedRevision != revision {
		t.Fatalf("exact retry changed replanning: run=%+v revision=%+v admitted=%t err=%v", retriedRun, retriedRevision, admitted, err)
	}
}
