package httpapi

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/conversations"
	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/orchestration"
	"github.com/EinarLogiOskars/commitarium/internal/worker"
	"github.com/EinarLogiOskars/commitarium/internal/workorder"
)

// applyRecoverWorkOrder recovers a blocked work order the way the assistant
// chose: re-check, continue with guidance, a message to the lead, a fresh
// conversation, or Back to Ready (ADR-032). The coordinator refuses a way
// already tried on the same failure, and caps the tries, with a reason the
// assistant can act on.
func (api *API) applyRecoverWorkOrder(
	w http.ResponseWriter, r *http.Request, projectID string, action conversations.Action,
) (string, bool) {
	ctx := r.Context()
	how := execution.RecoveryHow(action.How)
	if !how.Valid() {
		writeError(w, http.StatusBadRequest, "invalid_recovery", "how must be recheck, continue, message, fresh_conversation, or back_to_ready")
		return "", false
	}
	stored, err := api.features.GetByID(ctx, projectID, action.WorkOrderID)
	if err != nil {
		writeConversationError(w, err)
		return "", false
	}
	runs, err := api.execution.RunsForFeature(ctx, stored.ID)
	if err != nil || len(runs) == 0 {
		writeError(w, http.StatusConflict, "recovery_not_allowed", "the work order has no run to recover")
		return "", false
	}
	latest := execution.NewestRun(runs)
	// A run waiting for a sign-in continues by itself once the agent works
	// again; autopilot spends no recovery on it (the user may still).
	if waits, ok := api.execution.(interface {
		LoginWaitForRun(context.Context, string) (execution.LoginWait, error)
	}); ok && approvedByAutopilot(ctx) {
		if wait, err := waits.LoginWaitForRun(ctx, latest.ID); err == nil && wait.AgentID != "" {
			writeError(w, http.StatusConflict, "waits_for_sign_in", fmt.Sprintf(
				"“%s” waits for agent %s to sign in again and continues by itself once it works; "+
					"no recovery helps. Tell the user once, plainly, that it needs them to sign in.",
				stored.Title, wait.AgentID))
			return "", false
		}
	}
	roundCap := latest.Status == execution.RunStatusWaitingForUser && !latest.Paused &&
		latest.WaitKind == execution.RunWaitKindRoundCap
	if roundCap && how != execution.RecoveryBackToReady {
		return api.continueRoundCap(w, r, stored, latest, action, how)
	}
	fingerprint, refusal := api.recoveryRefusal(r, latest.ID, how)
	if roundCap {
		fingerprint, refusal = execution.RoundCapFingerprint, api.roundCapRefusal(ctx, stored)
	}
	if refusal != "" {
		writeError(w, http.StatusConflict, "recovery_refused", refusal)
		return "", false
	}
	actionID := runActionIDForKey("assistant-" + action.ID)
	record := func() {
		if store, ok := api.execution.(interface {
			RecordRunRecovery(context.Context, execution.Recovery) error
		}); ok {
			if err := store.RecordRunRecovery(ctx, execution.Recovery{
				ID: actionID, RunID: latest.ID, Fingerprint: fingerprint, How: how, Guidance: action.Guidance,
				FromAssistant: true, CreatedAt: time.Now().UTC(),
			}); err != nil {
				log.Printf("record recovery of run %q: %v", latest.ID, err)
			}
		}
	}
	switch how {
	case execution.RecoveryMessage:
		if _, _, err := api.realWorkflow.QueueIntervention(
			ctx, latest.ID, interventionIDForKey("assistant-"+action.ID), worker.RoleLead, action.Guidance,
		); err != nil {
			writeError(w, http.StatusConflict, "recovery_not_allowed", "the lead cannot take a message now: "+err.Error())
			return "", false
		}
		record()
		return fmt.Sprintf("Asked the lead of “%s”.", stored.Title), true
	case execution.RecoveryBackToReady:
		resetter, ok := api.featureDeletion.(workOrderResetter)
		if !ok {
			writeError(w, http.StatusServiceUnavailable, "return_to_ready_unavailable", "returning work orders to Ready is unavailable")
			return "", false
		}
		record()
		if _, err := resetter.ReturnToReady(ctx, projectID, stored.ID, "assistant-"+action.ID); err != nil {
			if errors.Is(err, workorder.ErrActive) {
				writeError(w, http.StatusConflict, "feature_active", "agent work is still stopping; try again shortly")
				return "", false
			}
			if errors.Is(err, feature.ErrNotFound) {
				writeConversationError(w, err)
				return "", false
			}
			log.Printf("return the assistant's work order %q to Ready: %v", stored.ID, err)
			writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
			return "", false
		}
		return fmt.Sprintf("Sent “%s” back to Ready.", stored.Title), true
	}
	directed, ok := api.realWorkflow.(directedRecoveryWorkflow)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "recovery_unavailable", "recovering a work order needs real agents")
		return "", false
	}
	directive := orchestration.RecoveryDirective{Message: action.Guidance, FromAssistant: true}
	switch how {
	case execution.RecoveryRecheck:
		directive.Action = orchestration.RecoveryActionRecheck
	case execution.RecoveryFreshConversation:
		directive.Action = orchestration.RecoveryActionReplace
	default:
		directive.Action = orchestration.RecoveryActionContinue
	}
	if _, _, err := directed.RecoverBlockerWithDirective(ctx, latest.ID, actionID, directive); err != nil {
		if errors.Is(err, orchestration.ErrRecoveryNotAllowed) {
			writeError(w, http.StatusConflict, "recovery_not_allowed", "the work order is not waiting on a blocker")
		} else {
			log.Printf("recover the assistant's work order %q: %v", stored.ID, err)
			writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		}
		return "", false
	}
	switch how {
	case execution.RecoveryRecheck:
		return fmt.Sprintf("Re-checked “%s”.", stored.Title), true
	case execution.RecoveryFreshConversation:
		return fmt.Sprintf("Restarted “%s” in a fresh conversation.", stored.Title), true
	}
	if action.Guidance != "" {
		return fmt.Sprintf("Continued “%s” with guidance.", stored.Title), true
	}
	return fmt.Sprintf("Continued “%s”.", stored.Title), true
}

// recoveryRefusal returns the blocker's fingerprint, and why the assistant
// may not recover this way now, or "" when it may.
func (api *API) recoveryRefusal(r *http.Request, runID string, how execution.RecoveryHow) (string, string) {
	blockers, ok := api.execution.(runBlockers)
	if !ok {
		return "", ""
	}
	blocker, found, err := blockers.RunBlocker(r.Context(), runID)
	if err != nil || !found {
		return "", ""
	}
	recoveries, err := blockers.RunRecoveries(r.Context(), runID)
	if err != nil {
		return blocker.Fingerprint, ""
	}
	onFailure := 0
	for _, recovery := range recoveries {
		if recovery.Fingerprint != blocker.Fingerprint {
			continue
		}
		onFailure++
		if recovery.How == how {
			return blocker.Fingerprint, fmt.Sprintf(
				"%s was already tried on this same failure and it came back; choose another way, or tell the user plainly that Commitarium could not finish it", how)
		}
	}
	if onFailure >= execution.MaxRecoveriesPerFailure || len(recoveries) >= execution.MaxRecoveriesPerRun {
		return blocker.Fingerprint, "this work order has had as many recoveries as it gets; tell the user plainly that Commitarium could not finish it, and let them report it or start over"
	}
	return blocker.Fingerprint, ""
}

// recoveryExhausted reports whether recovering a blocked run has run out:
// its failure had as many recoveries as it gets, the run as many in total,
// or the same failure came back after a fresh conversation (ADR-032).
// The user then hears it plainly, with a report to send or a start over.
func (api *API) recoveryExhausted(ctx context.Context, runID string) (execution.Blocker, bool) {
	blockers, ok := api.execution.(runBlockers)
	if !ok {
		return execution.Blocker{}, false
	}
	blocker, found, err := blockers.RunBlocker(ctx, runID)
	if err != nil || !found {
		return execution.Blocker{}, false
	}
	recoveries, err := blockers.RunRecoveries(ctx, runID)
	if err != nil {
		return blocker, false
	}
	return blocker, execution.RecoveryExhausted(blocker, recoveries)
}

// exhaustedDetail says in plain words why Commitarium could not finish a
// work order.
func exhaustedDetail(title string, kind execution.BlockerKind) string {
	switch kind {
	case execution.BlockerCheck:
		return title + ": a check on Commitarium's side keeps failing. Report it, or start the work order over."
	case execution.BlockerAgent:
		return title + ": its agents couldn't fix what went wrong. Report it, or start the work order over."
	case execution.BlockerCommitarium:
		return title + ": a check in Commitarium keeps refusing work its agent put right, which looks like a " +
			"problem in Commitarium. Report it, or start the work order over."
	}
	return title + " couldn't be finished. Report it, or start the work order over."
}

// roundCapWorkflow continues a planning run past its round limit.
type roundCapWorkflow interface {
	ContinueRoundCap(ctx context.Context, runID, actionID, guidance string, fromAssistant bool) (execution.Run, bool, error)
}

// continueRoundCap continues a work order whose run reached its planning
// round limit, with a few more rounds and the assistant's guidance: the
// assistant handles a work order's first round limit before the user does
// (ADR-032). A review round limit cannot be continued.
func (api *API) continueRoundCap(
	w http.ResponseWriter, r *http.Request, stored feature.Feature, latest execution.Run,
	action conversations.Action, how execution.RecoveryHow,
) (string, bool) {
	ctx := r.Context()
	if how != execution.RecoveryContinue && how != execution.RecoveryMessage {
		writeError(w, http.StatusConflict, "recovery_refused", fmt.Sprintf(
			"“%s” waits on its round limit, not a blocker: continue it with guidance that settles the open point, "+
				"or send it back to Ready (back_to_ready).", stored.Title))
		return "", false
	}
	if refusal := api.roundCapRefusal(ctx, stored); refusal != "" {
		writeError(w, http.StatusConflict, "recovery_refused", refusal)
		return "", false
	}
	continuing, ok := api.realWorkflow.(roundCapWorkflow)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "recovery_unavailable", "continuing a work order needs real agents")
		return "", false
	}
	actionID := runActionIDForKey("assistant-" + action.ID)
	if _, _, err := continuing.ContinueRoundCap(ctx, latest.ID, actionID, action.Guidance, true); err != nil {
		if errors.Is(err, orchestration.ErrRoundCapNotContinuable) {
			writeError(w, http.StatusConflict, "recovery_refused", fmt.Sprintf(
				"“%s” stopped at its review round limit, which cannot be continued: send it back to Ready "+
					"(back_to_ready) when the work went the wrong way, or tell the user plainly the one point its "+
					"agents disagree on and ask for their decision.", stored.Title))
			return "", false
		}
		log.Printf("continue the assistant's work order %q past its round limit: %v", stored.ID, err)
		writeError(w, http.StatusConflict, "recovery_not_allowed", "the work order cannot continue now: "+err.Error())
		return "", false
	}
	if store, ok := api.execution.(interface {
		RecordRunRecovery(context.Context, execution.Recovery) error
	}); ok {
		if err := store.RecordRunRecovery(ctx, execution.Recovery{
			ID: actionID, RunID: latest.ID, Fingerprint: execution.RoundCapFingerprint, How: execution.RecoveryContinue,
			Guidance: action.Guidance, FromAssistant: true, CreatedAt: time.Now().UTC(),
		}); err != nil {
			log.Printf("record the round-limit continuation of run %q: %v", latest.ID, err)
		}
	}
	return fmt.Sprintf("Continued “%s” with %d more planning rounds.", stored.Title, orchestration.RoundCapExtraRounds), true
}

// roundCapHandled reports whether the assistant already handled a round
// limit of the work order, on any of its runs.
func (api *API) roundCapHandled(ctx context.Context, featureID string) bool {
	blockers, ok := api.execution.(runBlockers)
	if !ok {
		return false
	}
	runs, err := api.execution.RunsForFeature(ctx, featureID)
	if err != nil {
		return false
	}
	for _, run := range runs {
		if recoveries, err := blockers.RunRecoveries(ctx, run.ID); err == nil && execution.RoundCapHandled(recoveries) {
			return true
		}
	}
	return false
}

// roundCapRefusal says why autopilot may not handle a work order's round
// limit again: it handled one already, so this one is the user's.
func (api *API) roundCapRefusal(ctx context.Context, stored feature.Feature) string {
	if !approvedByAutopilot(ctx) || !api.roundCapHandled(ctx, stored.ID) {
		return ""
	}
	return fmt.Sprintf("“%s” reached its round limit again after you handled it once; it is the user's now. "+
		"Tell them plainly, in a sentence or two, the one point its agents cannot settle, and ask for their decision.",
		stored.Title)
}
