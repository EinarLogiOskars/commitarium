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
	starter.noteBlockerFingerprinted(ctx, runID, kind, check, detail, detail, reason)
}

// noteBlockerFingerprinted is noteBlocker for a detail that varies with
// state while the failure stays the same: fingerprintDetail, not detail,
// recognises repeats.
func (starter *RemoteLeadStarter) noteBlockerFingerprinted(
	ctx context.Context, runID string, kind execution.BlockerKind, check, detail, fingerprintDetail, reason string,
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
		Fingerprint: execution.BlockerFingerprint(kind, check, fingerprintDetail), RecordedAt: time.Now().UTC(),
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

// turnRejection is one check that refuses an agent's turn for something the
// agent can put right. A refusal wraps it with what the check expected, what
// it found, and how to fix it; the blocker's fingerprint is the check's own
// words, so a reworded detail or one naming other state is still the same
// failure. A refusal the agent can fix is handed straight back to it
// (self_correction.go).
type turnRejection struct {
	kind  execution.BlockerKind
	check string
}

func (rejection *turnRejection) Error() string { return rejection.check }

// agentRejection is a check refusing what the agent handed in.
func agentRejection(check string) *turnRejection {
	return &turnRejection{kind: execution.BlockerAgent, check: check}
}

// stateRejection is a check of git or Forgejo state refusing the turn, for
// something the agent's next turn can change.
func stateRejection(check string) *turnRejection {
	return &turnRejection{kind: execution.BlockerCheck, check: check}
}

// blockerFingerprintDetail is the part of cause that recognises a repeat of
// the same failure: a checklist mismatch is one failure however many steps
// its detail names.
func blockerFingerprintDetail(cause error) string {
	var rejection *turnRejection
	if errors.As(cause, &rejection) {
		return rejection.check
	}
	return cause.Error()
}

// classifyCause names what a coordinator-side failure was: a check of git
// or Forgejo state, an interrupted environment, the agent's result not
// holding up, or something not yet named. check is the failing step, the
// error's outermost context.
func classifyCause(cause error) (execution.BlockerKind, string) {
	text := cause.Error()
	check, _, _ := strings.Cut(text, ": ")
	var rejection *turnRejection
	switch {
	case interruption(cause) != "":
		return execution.BlockerEnvironment, check
	case errors.As(cause, &rejection):
		return rejection.kind, check
	case errors.Is(cause, workspace.ErrAuditCommentMissing):
		// The agent's own step is missing: it fixes it once told exactly
		// what to post where.
		return execution.BlockerAgent, check
	case errors.Is(cause, workspace.ErrPullRequestConflict), errors.Is(cause, workspace.ErrCheckoutConflict),
		strings.HasPrefix(text, "verify "):
		return execution.BlockerCheck, check
	case strings.Contains(text, "without structured"),
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
