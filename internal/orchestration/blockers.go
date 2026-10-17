package orchestration

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/workspace"
)

// blockerStore keeps what blocked a run and the recoveries tried on it
// (ADR-032).
type blockerStore interface {
	RecordRunBlocker(context.Context, execution.Blocker) error
	RunBlocker(context.Context, string) (execution.Blocker, bool, error)
	RecordRunRecovery(context.Context, execution.Recovery) error
	RunRecoveries(context.Context, string) ([]execution.Recovery, error)
}

// maxBlockerDetail bounds a blocker's stored detail.
const maxBlockerDetail = 4 * 1024

// noteBlocker records what is about to block a run: its kind, the check that
// failed, and the full detail, for the assistant to recover from. It is
// accounting for recovery only and never blocks the workflow.
func (starter *RemoteLeadStarter) noteBlocker(
	ctx context.Context, runID string, kind execution.BlockerKind, check, detail, reason string,
) {
	store, ok := starter.executions.(blockerStore)
	if !ok {
		return
	}
	detail = strings.TrimSpace(detail)
	if len(detail) > maxBlockerDetail {
		detail = detail[:maxBlockerDetail] + "…"
	}
	if err := store.RecordRunBlocker(ctx, execution.Blocker{
		RunID: runID, Kind: kind, Check: check, Detail: detail, Reason: reason,
		Fingerprint: execution.BlockerFingerprint(kind, check, detail), RecordedAt: time.Now().UTC(),
	}); err != nil {
		starter.reportError(err)
	}
}

// noteUnnamedBlocker records a blocker no site named, so every blocker has
// a record; one already noted for the same reason is kept.
func (starter *RemoteLeadStarter) noteUnnamedBlocker(ctx context.Context, runID, reason string) {
	store, ok := starter.executions.(blockerStore)
	if !ok {
		return
	}
	if existing, found, err := store.RunBlocker(ctx, runID); err == nil && found && existing.Reason == reason {
		return
	}
	starter.noteBlocker(ctx, runID, execution.BlockerOther, "", reason, reason)
}

// classifyCause names what a coordinator-side failure was: a check of git
// or Forgejo state, an interrupted environment, the agent's result not
// holding up, or something not yet named. check is the failing step, the
// error's outermost context.
func classifyCause(cause error) (execution.BlockerKind, string) {
	text := cause.Error()
	check, _, _ := strings.Cut(text, ": ")
	switch {
	case interruption(cause) != "":
		return execution.BlockerEnvironment, check
	case errors.Is(cause, workspace.ErrAuditCommentMissing):
		// The agent's own step is missing: it fixes it once told exactly
		// what to post where.
		return execution.BlockerAgent, check
	case errors.Is(cause, workspace.ErrPullRequestConflict), errors.Is(cause, workspace.ErrCheckoutConflict),
		strings.HasPrefix(text, "verify "):
		return execution.BlockerCheck, check
	case strings.Contains(text, "without structured"), strings.Contains(text, "checklist is incomplete"),
		strings.Contains(text, "no durable agreed plan"):
		return execution.BlockerAgent, check
	}
	return execution.BlockerOther, check
}

// noteRecovery records a recovery the user or the assistant chose for the
// run's current blocker, so a move that already failed on the same failure
// is not tried again (ADR-032). Automatic retries are not recoveries.
func (starter *RemoteLeadStarter) noteRecovery(
	ctx context.Context, runID, actionID string, directive RecoveryDirective,
) {
	store, ok := starter.executions.(blockerStore)
	if !ok || directive.interruption != "" || directive.previewCheck != "" {
		return
	}
	how := execution.RecoveryContinue
	switch directive.Action {
	case RecoveryActionRecheck:
		how = execution.RecoveryRecheck
	case RecoveryActionReplace:
		how = execution.RecoveryFreshConversation
	}
	fingerprint := ""
	if blocker, found, err := store.RunBlocker(ctx, runID); err == nil && found {
		fingerprint = blocker.Fingerprint
	}
	if err := store.RecordRunRecovery(ctx, execution.Recovery{
		ID: actionID, RunID: runID, Fingerprint: fingerprint, How: how, Guidance: directive.Message,
		FromAssistant: directive.FromAssistant, CreatedAt: time.Now().UTC(),
	}); err != nil {
		starter.reportError(err)
	}
}
