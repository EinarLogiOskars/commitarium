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

// Recovery limits on the assistant's choices (ADR-032). The user's own
// buttons are never limited.
const (
	// maxRecoveriesPerFailure is how many recoveries one failure (blocker
	// fingerprint) gets before the user hears of it.
	maxRecoveriesPerFailure = 4
	// maxRecoveriesPerRun bounds a run whose failures keep changing.
	maxRecoveriesPerRun = 8
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
	latest := newestRun(runs)
	fingerprint, refusal := api.recoveryRefusal(r, latest.ID, how)
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
	if onFailure >= maxRecoveriesPerFailure || len(recoveries) >= maxRecoveriesPerRun {
		return blocker.Fingerprint, "this work order has had as many recoveries as it gets; tell the user plainly that Commitarium could not finish it, and let them report it or start over"
	}
	return blocker.Fingerprint, ""
}

// recoveryExhausted reports whether recovering a blocked run has run out:
// its failure had as many recoveries as it gets, the run as many in total,
// or a coordinator check kept failing after a fresh conversation (ADR-032).
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
	onFailure, freshTried := 0, false
	for _, recovery := range recoveries {
		if recovery.Fingerprint == blocker.Fingerprint {
			onFailure++
			freshTried = freshTried || recovery.How == execution.RecoveryFreshConversation
		}
	}
	exhausted := onFailure >= maxRecoveriesPerFailure || len(recoveries) >= maxRecoveriesPerRun ||
		(blocker.Kind == execution.BlockerCheck && freshTried)
	return blocker, exhausted
}

// exhaustedDetail says in plain words why Commitarium could not finish a
// work order.
func exhaustedDetail(title string, kind execution.BlockerKind) string {
	switch kind {
	case execution.BlockerCheck:
		return title + ": a check on Commitarium's side keeps failing. Report it, or start the work order over."
	case execution.BlockerAgent:
		return title + ": its agents couldn't fix what went wrong. Report it, or start the work order over."
	}
	return title + " couldn't be finished. Report it, or start the work order over."
}
