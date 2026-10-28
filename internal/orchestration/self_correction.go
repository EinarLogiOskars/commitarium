package orchestration

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
)

// A turn a check refused for something the agent can put right (a
// turnRejection) is handed straight back to the agent, in the same
// conversation, with the check's reason: a self-correction. It is the
// Continue with guidance the assistant would give, recorded as a recovery of
// its own kind, so the recovery limits count it (ADR-032). Each failure gets
// MaxSelfCorrections; then the work order blocks as before, saying what was
// tried. When the agent handed its result in again and the check found
// exactly the same thing, the check is likely wrong rather than the work:
// the blocker says so, and nothing more is handed back.

// MaxSelfCorrections is how many times one failure (blocker fingerprint) is
// handed back to the agent in a run.
const MaxSelfCorrections = 2

// maxCorrectionReason bounds the reason a correction quotes.
const maxCorrectionReason = 2000

// selfCorrection is what to do with a refused turn.
type selfCorrection struct {
	// prompt is the correction handed back to the agent, or "" when the
	// turn blocks.
	prompt string
	// tried is how many corrections this failure already had.
	tried int
	// suspect says the same refusal came back after a correction the agent
	// answered: likely a Commitarium problem.
	suspect bool
}

// planSelfCorrection decides whether a refused turn goes back to its agent.
// fingerprint is the failure's.
func (starter *RemoteLeadStarter) planSelfCorrection(
	ctx context.Context, runID string, cause error, fingerprint string,
) selfCorrection {
	var rejection *turnRejection
	store, ok := starter.executions.(blockerStore)
	if !ok || interruption(cause) != "" || !errors.As(cause, &rejection) {
		return selfCorrection{}
	}
	recoveries, err := store.RunRecoveries(ctx, runID)
	if err != nil {
		starter.reportError(err)
		return selfCorrection{}
	}
	plan := selfCorrection{prompt: selfCorrectionPrompt(cause)}
	var last execution.Recovery
	for _, recovery := range recoveries {
		if recovery.Fingerprint != fingerprint {
			continue
		}
		last = recovery
		if recovery.How == execution.RecoverySelfCorrection {
			plan.tried++
		}
	}
	switch {
	case last.How == execution.RecoverySelfCorrection && last.Guidance == plan.prompt:
		// The agent answered the correction and handed its result in again,
		// and the check found exactly what it found before.
		plan.prompt, plan.suspect = "", true
	case plan.tried >= MaxSelfCorrections,
		execution.RecoveryExhausted(execution.Blocker{Fingerprint: fingerprint}, recoveries):
		plan.prompt = ""
	}
	return plan
}

// selfCorrectionPrompt is what the agent is told when its turn is handed
// back: the check's reason, without secrets, and bounded.
func selfCorrectionPrompt(cause error) string {
	reason := strings.TrimSuffix(strings.TrimSpace(sanitizeReason(cause.Error())), ".")
	var incomplete acceptanceResultsIncomplete
	if errors.As(cause, &incomplete) {
		// As a guided Continue tells it (continueOrVerifyReviewTurn).
		reason += ". " + strings.TrimSuffix(acceptanceResultsGuidance, ".")
	}
	if len(reason) > maxCorrectionReason {
		cut := maxCorrectionReason
		for cut > 0 && !utf8.RuneStart(reason[cut]) {
			cut--
		}
		reason = reason[:cut] + "…"
	}
	return "Commitarium could not accept your last turn: " + reason + ". Fix exactly that, without redoing " +
		"work that was accepted, and hand in your result again. If you already did exactly this and the " +
		"check is wrong, do not work around it with a change the work does not need: say so in your result."
}

// secretPatterns find what looks like a credential in a check's reason. The
// worker redacts the secrets it knows from agents' activity; the coordinator
// does not know them, so it redacts by shape. Commit IDs stay: the agent
// needs them.
var secretPatterns = []*regexp.Regexp{
	// A URL's user and password.
	regexp.MustCompile(`(://)[^/\s:@]+:[^/\s@]+(@)`),
	// An authorization header, or a named secret's value.
	regexp.MustCompile(`(?i)(authorization\s*:\s*(?:bearer|token|basic)\s+|\b(?:token|password|passwd|secret|api[_-]?key|access[_-]?token)\s*[=:]\s*)[^\s'"]+`),
	// Provider tokens by their prefixes.
	regexp.MustCompile(`()\b(?:ghp|gho|ghs|ghu|github_pat|glpat|sk|xox[abp])[-_][A-Za-z0-9_-]{16,}`),
}

// sanitizeReason replaces what looks like a credential with [REDACTED], as
// the worker does.
func sanitizeReason(reason string) string {
	for _, pattern := range secretPatterns {
		reason = pattern.ReplaceAllString(reason, "${1}[REDACTED]${2}")
	}
	return reason
}

// selfCorrectionReason is the run's reason while a refused turn is handed
// back, or once handing it back has run out.
func selfCorrectionReason(agentName string, plan selfCorrection) string {
	switch {
	case plan.prompt != "":
		return fmt.Sprintf("Commitarium could not accept the %s's turn and handed the reason back to it to fix "+
			"(%d of %d).", agentName, plan.tried+1, MaxSelfCorrections)
	case plan.suspect:
		return " Commitarium handed this back to the " + agentName + ", which handed its result in again, but " +
			"the check found exactly the same thing: this is likely a problem in Commitarium, not in the work."
	case plan.tried > 0:
		return fmt.Sprintf(" Commitarium handed it back to the %s %d times; it came back each time.",
			agentName, plan.tried)
	}
	return ""
}

// pendingCorrection is a refused turn waiting to be handed back while
// another goroutine holds its run.
type pendingCorrection struct {
	// correct is set once the refusal is noted.
	correct func()
	// released says the holder let the run go before then; the run stays
	// held for the correction.
	released bool
}

// holdForCorrection keeps a refused turn's run held from before its blocker
// is noted until the correction starts, so no one sees the run free and
// blocked in between. It reports whether it claimed the run itself; when
// another goroutine holds it, its release hands the run over instead.
func (starter *RemoteLeadStarter) holdForCorrection(runID string) bool {
	starter.activeMu.Lock()
	defer starter.activeMu.Unlock()
	if _, held := starter.active[runID]; !held {
		starter.active[runID] = struct{}{}
		return true
	}
	if starter.corrections == nil {
		starter.corrections = map[string]*pendingCorrection{}
	}
	starter.corrections[runID] = &pendingCorrection{}
	return false
}

// correctWhenFree hands a refused turn back once the run is the
// correction's: at once when holdForCorrection claimed it or its holder has
// let it go, otherwise when the holder releases it.
func (starter *RemoteLeadStarter) correctWhenFree(runID string, owned bool, actionID, prompt string) {
	// correct runs holding the run: it moves the run off its blocker, then
	// releases it, and recovery claims it again to continue.
	correct := func() {
		ctx := starter.lifetime
		recovery, err := starter.beginRecovery(ctx, runID, actionID, RecoveryDirective{
			Action: RecoveryActionContinue, Message: prompt, selfCorrection: true,
		})
		starter.activeMu.Lock()
		delete(starter.active, runID)
		starter.activeMu.Unlock()
		admitted := false
		if err == nil && recovery.rechecking.ID != "" {
			_, admitted, err = starter.finishRecovery(ctx, recovery)
		}
		switch {
		case errors.Is(err, ErrRecoveryNotAllowed):
			// Someone else moved the run on first.
		case err != nil:
			starter.reportError(fmt.Errorf("hand a refused turn of run %q back to its agent: %w", runID, err))
		case !admitted:
			starter.reportError(fmt.Errorf("hand a refused turn of run %q back to its agent: not admitted", runID))
		}
	}
	if !owned {
		starter.activeMu.Lock()
		pending := starter.corrections[runID]
		if pending != nil && !pending.released {
			pending.correct = correct
			starter.activeMu.Unlock()
			return
		}
		delete(starter.corrections, runID)
		starter.activeMu.Unlock()
	}
	correct()
}
