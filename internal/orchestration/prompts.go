package orchestration

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/workspace"
)

func interventionInstructions(state feature.State, message string) string {
	return fmt.Sprintf(`The user paused the Commitarium workflow during the %s phase and sent this intervention:

%s

Answer the user directly using the restored conversation and current project context. This is an intervention-only turn: do not edit files, run implementation work, commit, push, publish or update a pull request, submit a formal review, or continue the workflow. Return "guidance_applied" when the message can guide later work without changing the accepted goal or agreed plan; return "clarification_required" when you need more information before classifying or applying it; return "replanning_required" when following it would change the accepted goal, scope, or agreed plan.`, state, strings.TrimSpace(message))
}

func replanningInstructions(
	planVersion int,
	effectiveGoal string,
	previousPlan string,
	userAmendment string,
	prepared workspace.Workspace,
	baselineCommitID string,
) string {
	return "Continue as the same lead agent and begin plan version " + strconv.Itoa(planVersion) +
		" after the user's scope-changing intervention. Inspect the managed repository, current Git " +
		"HEAD, branch, status, and diff before proposing anything. Existing branch commits and " +
		"uncommitted user or agent edits are intentional external state: preserve them, do not reset, " +
		"clean, overwrite, commit, push, or modify the pull request during this planning turn. Compare " +
		"the prior plan and work already present against the amended goal, identify what remains useful, " +
		"and propose a complete revised implementation plan for the same reviewer to challenge. Durable " +
		"Git, Forgejo, and coordinator state are authoritative over conversational memory.\n\n" +
		"Effective amended goal:\n" + effectiveGoal +
		"\n\nUser's exact scope amendment:\n" + strings.TrimSpace(userAmendment) +
		"\n\nPrevious agreed plan:\n" + previousPlan +
		"\n\nRepository: " + prepared.RepositoryOwner + "/" + prepared.RepositoryName +
		"\nBase branch: " + prepared.BaseBranch +
		"\nExisting feature branch: " + prepared.Branch +
		"\nReplanning baseline commit: " + baselineCommitID +
		fmt.Sprintf("\nExisting draft pull request: #%d (%s)", prepared.PullRequestNumber, prepared.PullRequestURL)
}

func remoteLeadInstructions(goal string) string {
	return "You are the lead agent helping the user define a software-development goal. " +
		"This is goal clarification only: do not modify files, run destructive commands, " +
		"create commits, or begin implementation. Inspect the available project read-only " +
		"when useful. Restate your understanding, identify important ambiguity or risk, and " +
		"ask the user the smallest useful set of questions needed before planning. Return action 'ask' " +
		"while information is missing, put the user-facing question in message, put your current best " +
		"complete goal draft in goal when one is useful (or an empty string when it would be misleading), " +
		"and list the unresolved questions in open_questions. Once the goal is ready to accept, return " +
		"action 'propose', explain that in message, put the complete proposed goal in goal, and return an " +
		"empty open_questions array. The conversational message is not the proposed goal. " +
		"The user's current goal is:\n\n" + goal
}

func remoteLeadReplyInstructions(message string) string {
	return "Continue the same goal-clarification conversation. This is still clarification only: " +
		"do not modify files, run destructive commands, create commits, or begin implementation. " +
		"Use the existing conversation context, incorporate the user's reply, and ask only the " +
		"next questions genuinely needed before planning. Return action 'ask' with a user-facing message, " +
		"the current best complete goal draft in goal when useful (otherwise an empty string), and unresolved " +
		"questions in open_questions. When ready, return action 'propose' with the complete goal, an empty " +
		"open_questions array, and a separate user-facing message. The conversational message is never the " +
		"proposed goal. The user replied:\n\n" + message
}

func reviewerPlanningInstructions(
	storedFeature feature.Feature,
	prepared workspace.Workspace,
	proposal string,
	lead string,
) string {
	return "You are the independent reviewer, discussing an implementation plan with " + lead + ". " +
		"Planning is read-only: do not modify files, install dependencies, commit, or push. Check the " +
		"proposal against the accepted goal and the actual repository: missing steps, unsafe assumptions, " +
		"scope problems, weak verification, and steps that should be merged or split. Reply to " + lead +
		" directly. Number each concern (R1, R2, ...) so it can be answered by number, and leave out matters " +
		"of taste. Where the plan is sound, say so briefly rather than restating it. End by stating whether " +
		"you accept the plan as written. Durable repository and workflow state are authoritative over the " +
		"proposal.\n\nAccepted goal:\n" + storedFeature.AcceptedGoal +
		"\n\n" + planningWorkspaceFacts(prepared) +
		"\n\n" + lead + "'s proposal:\n" + proposal
}

func acceptanceTestsInstructions(prepared workspace.Workspace) string {
	return "Continue the same provider conversation as the independent reviewer. Work only from the accepted goal, " +
		"the agreed plan (read it with 'commitarium-artifact plan show'), and the clean planning baseline already " +
		"present in your private checkout. Do not fetch, inspect, " +
		"or query the lead's implementation or pull-request head. Write the smallest useful executable acceptance tests " +
		"you would have written before implementation: one per essential behavior the goal requires, not one per " +
		"detail, so a small change needs only a few and a documentation-only change usually one. Do not alter production code. Run the tests against the " +
		"baseline when practical; they are expected to expose missing behavior. Commit only the acceptance-test changes " +
		"locally and do not push any branch or commit. Return action 'authored', a concise summary, the exact local test " +
		"commit ID, and ordered stable test IDs and user-facing titles. If independent executable acceptance tests are " +
		"not meaningful or cannot safely be authored, return action 'blocked' with no commit or tests.\n\n" +
		"Current workflow phase: independent acceptance test authoring" +
		"\nRepository: " + prepared.RepositoryOwner + "/" + prepared.RepositoryName +
		"\nBase branch: " + prepared.BaseBranch + "\nPrivate checkout baseline commit: " + prepared.BaseCommitID
}

func implementationInstructions(
	prepared workspace.Workspace,
	attemptID string,
) string {
	marker := implementationPublicationMarker(attemptID)
	return "Continue the same provider conversation as the lead and begin implementing the agreed plan. " +
		"First check Git HEAD and status against the facts below. Preserve any unexpected user work: do " +
		"not reset, clean, overwrite, or silently discard it. If the state is missing, contradictory, or " +
		"ambiguous, stop and explain the problem without making changes. " +
		implementationToolchainInstructions +
		implementationChecklistInstructions +
		"When you decide the implementation is ready for independent review, commit all intended work " +
		"on the assigned feature branch, push that exact HEAD to the 'commitarium' remote, and post one " +
		"Forgejo pull-request comment using the worker-provided Forgejo URL and token-file environment " +
		"variables. Never print, log, commit, or include the token in a URL. The comment must be exactly " +
		"the marker below, a blank line, the heading " +
		"'## Implementation summary', another blank line, and your concise summary. Check existing PR " +
		"comments for the marker before posting so a retry never duplicates the audit entry. Do not " +
		"change the PR body or merge the PR. Then return action 'published' with the same summary, exact " +
		"lowercase Git HEAD in commit_id, and the PR number. If any step is unsafe or cannot be confirmed, " +
		"return action 'blocked', explain why in summary, leave commit_id empty, and still return the known " +
		"PR number. Durable " +
		"repository and coordinator state are authoritative over conversational memory.\n\n" +
		"Current workflow phase: implementing" +
		"\nRepository: " + prepared.RepositoryOwner + "/" + prepared.RepositoryName +
		"\nBase branch: " + prepared.BaseBranch + "\nFeature branch: " + prepared.Branch +
		"\nPlanning baseline commit: " + prepared.BaseCommitID +
		fmt.Sprintf("\nDraft pull request: #%d (%s)", prepared.PullRequestNumber, prepared.PullRequestURL) +
		"\nImplementation audit marker:\n" + marker
}

func implementationContinuationInstructions(
	prepared workspace.Workspace,
	userMessage string,
	attemptID string,
) string {
	marker := implementationPublicationMarker(attemptID)
	return "Continue the same provider conversation and implementation work after the user's " +
		"guidance below. First check Git HEAD and status against the facts below and the work already " +
		"completed in this conversation. Preserve all existing changes, including manual user edits; " +
		"do not reset, clean, overwrite, or repeat completed work. If partial work is ambiguous, facts " +
		"conflict, an external side effect may or may not have happened, or the guidance would change " +
		"the accepted goal or agreed plan, stop and explain the problem without making further changes. " +
		"Otherwise apply the user's guidance within the accepted plan, continue the implementation, and " +
		"run the relevant available tests. " + implementationToolchainInstructions +
		implementationChecklistInstructions +
		"When you decide the result is ready for independent review, " +
		"commit the intended work, push the exact HEAD to the 'commitarium' remote, and post one Forgejo " +
		"PR comment using the worker-provided URL and token-file environment variables. Never print, log, " +
		"commit, or include the token in a URL. The comment must contain exactly the audit " +
		"marker below, a blank line, '## Implementation summary', another blank " +
		"line, and your concise summary. Check for the marker before posting so retries do not duplicate " +
		"it. Do not change the PR body or merge. Return action 'published' with that same summary, exact " +
		"lowercase HEAD in commit_id, and the PR number. If publication is unsafe or cannot be confirmed, " +
		"return action 'blocked' with commit_id empty and explain the blocker. Durable repository, pull-request, and " +
		"coordinator state are authoritative over conversational memory.\n\n" +
		"User's continuation guidance:\n" + userMessage +
		"\n\nCurrent workflow phase: implementing" +
		"\nRepository: " + prepared.RepositoryOwner + "/" + prepared.RepositoryName +
		"\nBase branch: " + prepared.BaseBranch + "\nFeature branch: " + prepared.Branch +
		"\nPlanning baseline commit: " + prepared.BaseCommitID +
		fmt.Sprintf("\nDraft pull request: #%d (%s)", prepared.PullRequestNumber, prepared.PullRequestURL) +
		"\nImplementation audit marker:\n" + marker
}

const implementationToolchainInstructions = "If an additional supported language runtime is required, " +
	"run 'commitarium-toolchain require <tool>@<exact-version>' with a generous timeout and wait for it " +
	"to finish; its shim becomes available on PATH immediately. If an operating-system package is missing, " +
	"do not run apt. Return action 'environment_required', leave commit_id empty and pull_request_number zero, " +
	"and provide only the Debian package names in system_packages plus a concise environment_reason. Commitarium " +
	"will ask the user and provision the same package set for lead, reviewer, and isolated validation. Do not use " +
	"mise use, floating versions such as latest, or repository mise configuration to provision tools. "

const implementationChecklistInstructions = "The coordinator owns a structured commit-sized checklist. " +
	"Run 'commitarium-artifact plan show' before changing files. Work through its steps in order without " +
	"waiting for an intermediate review. Immediately before beginning a pending step run " +
	"'commitarium-artifact plan start <step-id>'. Implement only that cohesive slice, run its listed " +
	"verification, and commit it with the planned commit subject. Then run " +
	"'commitarium-artifact plan complete <step-id> <lowercase-commit-id>' before continuing. Resume from " +
	"the statuses already recorded after an interruption. Do not amend, squash, or combine planned commits, " +
	"and do not create or commit a local checklist file. The final published HEAD must be the commit recorded " +
	"for the final checklist step. "

func leadPlanningResponseInstructions(reviewerResponse string, reviewer string) string {
	return "Continue planning as the lead. Reply to " + reviewer + " directly. Answer each numbered " +
		"point by its number: accepted, changed (say exactly what changes in the plan), or disputed " +
		"(with repository evidence). Send only what changes, not the whole plan, and keep agreement " +
		"brief. Planning is read-only and nothing has changed since your last turn, so inspect the " +
		"repository only where a point depends on code you have not read yet. While anything is open, " +
		"use action 'respond' with your reply in content and leave plan_title, plan_subtitle, and steps " +
		"empty. When you and " + reviewer + " genuinely agree and the plan fully satisfies the accepted " +
		"goal, use action 'submit_plan': put the complete final plan in content and supply plan_title, " +
		"plan_subtitle, and the ordered steps, as few as the goal needs. Each step is one cohesive commit " +
		"with a stable lowercase ID, title, subtitle, detailed Markdown instructions, concrete verification " +
		"checks, and an imperative commit_subject. Do not submit merely to end the discussion.\n\n" +
		reviewer + " replied:\n" + reviewerResponse
}

func reviewerResponseInstructions(leadResponse string, lead string) string {
	return "Continue planning as the independent reviewer. Reply to " + lead + " directly. For each " +
		"open point, say in a line whether it is resolved or still open and why; number any new concern " +
		"after your last one. Do not restate the plan. Planning is read-only and nothing has changed since " +
		"your last turn, so inspect the repository only to check a specific claim. When nothing is open, " +
		"say clearly that you agree with the plan; " + lead + " then submits it. Do not use a special " +
		"approval marker.\n\n" + lead + " replied:\n" + leadResponse
}

func planningInstructions(storedFeature feature.Feature, prepared workspace.Workspace, reviewer string) string {
	return "The goal is accepted and planning begins. You are the lead. Inspect the repository as " +
		"needed to plan; planning is read-only, so do not modify files, install dependencies, commit, or " +
		"push. Propose a concrete implementation plan to " + reviewer + ", who will review it with you. " +
		"Split the work into ordered commit-sized steps, as few as the goal needs: a small change can be " +
		"a single step. For each step give a short title, what to change, how to verify it, and the " +
		"commit subject. Note assumptions and risks only where they matter. Durable repository and " +
		"workflow state are authoritative over conversational memory.\n\nAccepted goal:\n" +
		storedFeature.AcceptedGoal + "\n\n" + planningWorkspaceFacts(prepared)
}

// leadParticipant and reviewerParticipant name the agents for each other, so
// the dialogue reads as a conversation even when both use the same provider.
func leadParticipant(providers project.AgentProviders) string {
	return providerDisplayName(providers.Lead) + " (lead)"
}

func reviewerParticipant(providers project.AgentProviders) string {
	return providerDisplayName(providers.Reviewer) + " (reviewer)"
}

func providerDisplayName(provider project.AgentProvider) string {
	switch provider {
	case project.AgentProviderClaude:
		return "Claude"
	case project.AgentProviderCodex:
		return "Codex"
	default:
		return string(provider)
	}
}

func planningWorkspaceFacts(prepared workspace.Workspace) string {
	return "Repository: " + prepared.RepositoryOwner + "/" + prepared.RepositoryName +
		"\nBase branch: " + prepared.BaseBranch +
		"\nBase commit: " + prepared.BaseCommitID +
		"\nReserved feature branch name: " + prepared.Branch +
		"\nThe coordinator creates the feature branch and draft pull request only after " +
		"the final agreed plan is submitted. Do not require either resource during planning."
}

func recoveryPublicationInstructions(sessionID, attemptID string) string {
	marker := ""
	heading := ""
	if _, ok := implementationTurnNumber(sessionID, attemptID); ok {
		marker = implementationPublicationMarker(attemptID)
		heading = "Implementation summary"
	} else if _, ok := implementationCorrectionTurnNumber(sessionID, attemptID); ok {
		marker = implementationReviewResponseMarker(attemptID)
		heading = "Review response"
	} else if _, ok := implementationReadinessTurnNumber(sessionID, attemptID); ok {
		marker = workspace.ImplementationPublicationMergeReadiness.Marker(attemptID)
		heading = "Merge readiness"
	} else if _, ok := implementationReviewTurnNumber(sessionID, attemptID); ok {
		marker = implementationReviewMarker(attemptID)
		heading = "Review"
	}
	if marker == "" {
		return ""
	}
	return "\n\nRecovery publication identity:\n" +
		"This recovery successor has a new durable attempt identity. If you publish or verify " +
		"external work, use the exact marker below rather than any marker from an earlier attempt. " +
		"Check for this exact marker before writing so a retry does not duplicate it.\n" +
		marker + "\n\nRequired heading after the marker: ## " + heading
}

func implementationReviewInstructions(
	prepared workspace.Workspace,
	implementationSummary string,
	commitID string,
	attemptID string,
	previousReviewedCommitID string,
	acceptanceTestCommitIDs ...string,
) string {
	marker := implementationReviewMarker(attemptID)
	acceptanceTestCommitID := ""
	if len(acceptanceTestCommitIDs) > 0 {
		acceptanceTestCommitID = acceptanceTestCommitIDs[0]
	}
	rereview := previousReviewedCommitID != ""
	acceptanceInstructions := ""
	workspaceRules := "Reset this disposable reviewer checkout to the planning baseline, fetch the exact implementation commit below, and check out that exact commit detached. Then do not modify tracked files, commit, push, change the pull-request body, or merge. "
	if acceptanceTestCommitID != "" {
		workspaceRules = "Reset this disposable private checkout to the exact private acceptance-test commit below, discarding only any prior local review merge. Fetch the exact implementation commit below without inspecting other lead history, then merge that exact commit locally into your private test commit. Resolve only mechanical merge conflicts; if a conflict changes test meaning, mark the affected test not applicable with a reason instead of silently rewriting it. Never push your private test commit, the local merge, or any test changes. Do not change the pull-request body or merge the pull request. "
		acceptanceInstructions = "Run every pending private acceptance test against that local combination, using the test IDs listed by `commitarium-artifact acceptance show`. Run them together in one command where possible. Then record every result in a single shell call by chaining, for each test, `commitarium-artifact acceptance start <test-id>` followed by `acceptance pass <test-id>`, `acceptance fail <test-id> <note>`, or `acceptance not-applicable <test-id> <note>`; each tool call re-reads this whole conversation, so keep them few. A failed acceptance test is review evidence, not automatically proof that production code is wrong: inspect whether the implementation, the test, or the shared understanding is incorrect. Private acceptance test commit: " + acceptanceTestCommitID + "\n"
	}
	scope := "The lead has now published an implementation for review. Inspect before judging: confirm the current branch, " +
		"Git HEAD, status, diff from the planning baseline, and the exact pull-request head. Review only " +
		"the exact commit below against the accepted goal and the agreed plan (read it with " +
		"'commitarium-artifact plan show'), and run relevant tests when practical. Number each finding " +
		"(F1, F2, ...) so the lead can answer it by number. "
	if rereview {
		scope = "You requested changes to commit " + previousReviewedCommitID + " and the lead has published " +
			"a correction. You already reviewed everything up to that commit, so review what changed: run " +
			"'git diff " + previousReviewedCommitID + " " + commitID + "'. Confirm each of your numbered findings " +
			"as fixed or not fixed, and look beyond the diff only where the change touches code outside your " +
			"findings. Re-run the relevant tests. Number any new finding after your last one. "
	}
	return "Continue the same provider conversation as the independent reviewer. " + scope +
		workspaceRules + acceptanceInstructions +
		"If you find material problems, submit one formal Forgejo review with event REQUEST_CHANGES. If the " +
		"implementation is correct and sufficiently tested, tell the lead that you think the exact revision is " +
		"ready to merge and submit one review with event APPROVED. Use the worker-provided " +
		"Forgejo URL and token-file environment variables; never print, log, commit, or put the token in a URL. " +
		"Set commit_id on the review to the exact commit below. The review body must begin with exactly the marker below, " +
		"a blank line, '## Review', and another blank line, followed by your structured findings or approval. " +
		"Check existing reviews for the marker before posting so recovery never duplicates it. Return action " +
		"'approved' or 'changes_requested' with a concise session summary, exact commit ID, PR number, and returned review ID. " +
		"If state is contradictory, the exact revision is unavailable, or you cannot safely establish whether a " +
		"review was posted, return action 'blocked', leave commit_id empty and review_id zero, and explain why. " +
		"Durable Git, Forgejo, and coordinator state are authoritative over conversational memory.\n\n" +
		"Current workflow phase: reviewing" +
		"\n\nLead's latest implementation or readiness summary:\n" + implementationSummary +
		"\n\nRepository: " + prepared.RepositoryOwner + "/" + prepared.RepositoryName +
		"\nBase branch: " + prepared.BaseBranch + "\nFeature branch: " + prepared.Branch +
		"\nPlanning baseline commit: " + prepared.BaseCommitID + "\nExact implementation commit: " + commitID +
		fmt.Sprintf("\nDraft pull request: #%d (%s)", prepared.PullRequestNumber, prepared.PullRequestURL) +
		"\nReview audit marker:\n" + marker
}

func implementationCorrectionInstructions(
	prepared workspace.Workspace,
	reviewSummary string,
	reviewedCommitID string,
	reviewID int64,
	attemptID string,
) string {
	marker := implementationReviewResponseMarker(attemptID)
	return "Continue the same provider conversation as the lead. The independent reviewer requested " +
		"changes to the exact commit below. First check Git HEAD and status against these facts. Do not " +
		"repeat completed work or discard unexpected user changes. Address every material review finding " +
		"while preserving the accepted goal and agreed plan, then run the relevant tests. Answer each " +
		"numbered finding by its number: fixed (say how) or disputed (with evidence). " +
		implementationToolchainInstructions + "When the " +
		"correction is ready, create a new commit descended from the reviewed commit, push that exact HEAD to " +
		"the 'commitarium' remote, and post one pull-request comment using the worker-provided Forgejo URL and " +
		"token-file environment variables. Never print, log, commit, or include the token in a URL. The comment " +
		"must be exactly the marker below, a blank line, '## Review response', another blank line, and your " +
		"answer to each finding by number, with how it was tested. Check existing comments for the marker " +
		"before posting so recovery never duplicates it. Do not change the PR body or merge. Return action " +
		"'published' with that same summary, the new lowercase Git HEAD in commit_id, and the PR number. If state " +
		"is contradictory, the prior commit or review is unavailable, work is ambiguous, or publication cannot " +
		"be confirmed, return action 'blocked', leave commit_id empty, and explain why. Durable Git, Forgejo, and " +
		"coordinator state are authoritative over conversational memory.\n\n" +
		"Current workflow phase: reviewing (corrective implementation)" +
		"\n\nVerified reviewer findings:\n" + reviewSummary +
		"\n\nRepository: " + prepared.RepositoryOwner + "/" + prepared.RepositoryName +
		"\nBase branch: " + prepared.BaseBranch + "\nFeature branch: " + prepared.Branch +
		"\nPlanning baseline commit: " + prepared.BaseCommitID +
		"\nExact reviewed commit: " + reviewedCommitID +
		fmt.Sprintf("\nFormal review: #%d\nDraft pull request: #%d (%s)", reviewID, prepared.PullRequestNumber, prepared.PullRequestURL) +
		"\nReview-response audit marker:\n" + marker
}

func implementationReadinessInstructions(
	prepared workspace.Workspace,
	reviewSummary string,
	commitID string,
	reviewID int64,
	attemptID string,
) string {
	marker := workspace.ImplementationPublicationMergeReadiness.Marker(attemptID)
	return "Continue the same provider conversation as the lead. The independent reviewer has said " +
		"that the exact commit below is ready to merge. You know the work, so do not re-review it: confirm " +
		"that your Git HEAD is the approved commit and that nothing is left unpushed. Do not modify files, " +
		"commit, push, change the pull-request body, or merge. If you agree that no " +
		"material blocker remains, give the merge green light. If you do not agree, state the concrete " +
		"remaining concern. For either decision, post one pull-request comment using the worker-provided " +
		"Forgejo URL and token-file environment variables. Never print, log, commit, or include the token " +
		"in a URL. The comment must be exactly the marker below, a blank line, '## Merge readiness', " +
		"another blank line, and your concise decision summary. Check existing comments for the marker " +
		"before posting so recovery never duplicates it. Return action 'ready_to_merge' with that same " +
		"summary when you agree, or action 'concern' with the same summary when you do not. If state is " +
		"contradictory, the exact revision or approval is unavailable, or you cannot safely establish " +
		"whether the comment was posted, return action 'blocked' and explain why. Durable Git, Forgejo, " +
		"and coordinator state are authoritative over conversational memory.\n\n" +
		"Current workflow phase: reviewing (merge-readiness acknowledgement)" +
		"\n\nReviewer's exact approval summary:\n" + reviewSummary +
		"\n\nRepository: " + prepared.RepositoryOwner + "/" + prepared.RepositoryName +
		"\nBase branch: " + prepared.BaseBranch + "\nFeature branch: " + prepared.Branch +
		"\nPlanning baseline commit: " + prepared.BaseCommitID +
		"\nExact approved commit: " + commitID +
		fmt.Sprintf("\nFormal review: #%d\nDraft pull request: #%d (%s)", reviewID, prepared.PullRequestNumber, prepared.PullRequestURL) +
		"\nMerge-readiness audit marker:\n" + marker
}
