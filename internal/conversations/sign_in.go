package conversations

import (
	"context"
	"fmt"
	"log"
	"slices"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
)

// A turn whose agent's login fails keeps what it was asked and waits
// (LOGIN_RECOVERY_PLAN.md): it tries again by itself a few times, since a
// refresh that failed once can work on the next turn; then the agent is
// marked as needing a sign-in, and the turn runs again once the agent works:
// after a sign-in, after any turn on the agent that succeeds, or when the
// coordinator tries the work waiting on it. Nothing the user said is dropped.

// signInBackoff is how long each automatic try of a turn waits.
var signInBackoff = [...]time.Duration{30 * time.Second, 2 * time.Minute, 5 * time.Minute}

// SignInWait is a conversation whose last turn waits for its agent's login.
type SignInWait struct {
	Agent string    `json:"agent"`
	Since time.Time `json:"since"`
	// NeedsUser is set once the automatic tries are spent: the user has to
	// sign the agent in again. Until then the turn tries again at RetryAt.
	NeedsUser bool       `json:"needs_user,omitempty"`
	Retries   int        `json:"retries,omitempty"`
	RetryAt   *time.Time `json:"retry_at,omitempty"`
}

// turnInput is the prompt a turn was started on, kept to start it again.
type turnInput struct {
	Role   string `json:"role"`
	Prompt string `json:"prompt"`
	// Attachments are the files the turn carried.
	Attachments []string `json:"attachments,omitempty"`
}

// SignIns records agents that need the user to sign in again, shared with
// the work orders waiting on them.
type SignIns interface {
	MarkAgentNeedsSignIn(ctx context.Context, agentID, lastError string) error
	AgentNeedsSignIn(ctx context.Context, agentID string) bool
	// AgentAnswered notes a turn on the agent that worked, so the work
	// waiting on its sign-in resumes.
	AgentAnswered(ctx context.Context, agentID string)
}

// loginFailed reports whether a turn failed on its agent's login.
func loginFailed(result *workerhttp.TerminalResult) bool {
	return result != nil && result.Error != nil && result.Error.Code == workerhttp.ErrorProviderLoginRequired
}

// waitForSignIn keeps a turn that failed on its login: it tries again soon,
// or, once the tries are spent (or the agent is known to need a sign-in),
// waits for the user to sign the agent in again. It tells the user once per
// stage, plainly.
func (service *Service) waitForSignIn(ctx context.Context, stored *record, result *workerhttp.TerminalResult, now time.Time) {
	agentID := stored.Agent
	if stored.SignIn == nil || stored.SignIn.Agent != agentID {
		stored.SignIn = &SignInWait{Agent: agentID, Since: now}
	}
	wait := stored.SignIn
	known := service.config.SignIns != nil && service.config.SignIns.AgentNeedsSignIn(ctx, agentID)
	name := service.agentName(ctx, agentID)
	note := func(text string) {
		stored.Messages = append(stored.Messages, Message{
			ID: messageID(*stored, len(stored.Messages)), Role: "event", Text: text, OccurredAt: now,
		})
	}
	if !known && !wait.NeedsUser && wait.Retries < len(signInBackoff) {
		retryAt := now.Add(signInBackoff[wait.Retries])
		if wait.Retries == 0 {
			note(fmt.Sprintf("%s could not sign in just now. Your message is kept; the assistant tries again by itself.", name))
		}
		wait.Retries++
		wait.RetryAt = &retryAt
		return
	}
	wait.RetryAt = nil
	if !wait.NeedsUser {
		wait.NeedsUser = true
		note(fmt.Sprintf("%s needs you to sign in again. Your message is kept and is sent as soon as it works.", name))
	}
	if service.config.SignIns != nil {
		cause := "the provider did not accept the agent's login"
		if result != nil && result.Error != nil && result.Error.Message != "" {
			cause = result.Error.Message
		}
		if err := service.config.SignIns.MarkAgentNeedsSignIn(ctx, agentID, cause); err != nil {
			log.Printf("record that agent %q needs a sign-in: %v", agentID, err)
		}
	}
}

// agentName is how the user knows an agent.
func (service *Service) agentName(ctx context.Context, agentID string) string {
	if service.config.Agents != nil {
		if agents, err := service.config.Agents.List(ctx); err == nil {
			for _, known := range agents {
				if known.ID == agentID && known.Name != "" {
					return known.Name
				}
			}
		}
	}
	return "The assistant's agent"
}

// retrySignIn starts again the turn a chat keeps for its agent's login,
// when its try is due or force says the agent works again. What waited
// meanwhile (events, the user's messages) rides along.
func (service *Service) retrySignIn(ctx context.Context, where chatRef, force bool, now time.Time) error {
	settings, err := service.Readiness(ctx)
	if err != nil || settings.Agent == "" || settings.Model == "" {
		return err
	}
	stored, done, err := service.open(where.scope, where.chat)
	if err != nil {
		return err
	}
	defer done()
	if stored.SignIn == nil || stored.Status == StatusRunning || stored.LastTurn == nil {
		return nil
	}
	wait := stored.SignIn
	// The user chose another agent for the assistant: no need to wait.
	switched := settings.Agent != wait.Agent
	due := wait.RetryAt != nil && !now.Before(*wait.RetryAt)
	if !force && !switched && !due {
		return nil
	}
	prompt := stored.LastTurn.Prompt
	if len(stored.PendingEvents) > 0 {
		prompt = eventsPrompt(stored.PendingEvents) + "\n\n" + prompt
		stored.PendingEvents = nil
	}
	_, err = service.startTurn(ctx, stored, settings, fmt.Sprintf("sign-in:%d", stored.Turn+1), stored.LastTurn.Role, prompt)
	return err
}

// AgentSignedIn starts again every chat's turn that waits on the agent: its
// login works again. The helpers it stopped are started again first, so a
// turn kept for the sign-in hears of them too.
func (service *Service) AgentSignedIn(ctx context.Context, agentID string) {
	for _, scope := range service.helperSignInScopes(agentID) {
		service.restartHelpers(scope, agentID)
	}
	for _, where := range service.signInChats(agentID, false) {
		if err := service.retrySignIn(ctx, where, true, service.now()); err != nil {
			log.Printf("resume the assistant in %s after %q signed in: %v", where, agentID, err)
		}
	}
}

// TrySignIn starts again the longest-waiting chat turn that waits for the
// agent's sign-in: if it works, the agent's login is back.
func (service *Service) TrySignIn(ctx context.Context, agentID string) {
	waiting := service.signInChats(agentID, true)
	if len(waiting) == 0 {
		// No turn waits: a helper the login stopped is the try.
		if helpers := service.helperSignInScopes(agentID); len(helpers) > 0 {
			service.restartHelpers(helpers[0], agentID)
		}
		return
	}
	if err := service.retrySignIn(ctx, waiting[0], true, service.now()); err != nil {
		log.Printf("try the assistant in %s for %q: %v", waiting[0], agentID, err)
	}
}

// RetryDueSignIns starts again the chat turns whose next try is due, at the
// top level too.
func (service *Service) RetryDueSignIns(ctx context.Context) {
	for _, where := range service.signInChats("", false) {
		if err := service.retrySignIn(ctx, where, false, service.now()); err != nil {
			log.Printf("retry the assistant in %s: %v", where, err)
		}
	}
}

// chatRef names a chat in a scope.
type chatRef struct{ scope, chat string }

func (where chatRef) String() string { return where.scope + "/" + where.chat }

// signInChats lists the chats waiting on the agent's login (any agent's for
// ""), the longest waiting first; only those waiting for the user when
// needsUser.
func (service *Service) signInChats(agentID string, needsUser bool) []chatRef {
	type waiting struct {
		where chatRef
		since time.Time
	}
	found := make([]waiting, 0)
	for _, scope := range append(service.projectScopes(), TopLevel) {
		chats, err := service.chatsOf(scope)
		if err != nil {
			continue
		}
		for _, chat := range chats {
			if chat.SignIn == nil || (agentID != "" && chat.SignIn.Agent != agentID) ||
				(needsUser && !chat.SignIn.NeedsUser) {
				continue
			}
			found = append(found, waiting{chatRef{scope, chat.ID}, chat.SignIn.Since})
		}
	}
	slices.SortStableFunc(found, func(a, b waiting) int { return a.since.Compare(b.since) })
	chats := make([]chatRef, 0, len(found))
	for _, item := range found {
		chats = append(chats, item.where)
	}
	return chats
}

// Helpers the assistant started run in the background on its agent
// (ADR-035), so a login failure would stop them unseen. A helper that stops
// on its agent's login waits like a turn: the assistant hears why and tells
// the user, and once the agent works again it is woken to start the helper
// again from what it gathered.

// waitsForSignIn reports whether a helper stopped on its agent's login, and
// if so keeps it waiting for the sign-in and tells the assistant.
func (service *Service) waitsForSignIn(
	ctx context.Context, stored *record, helper *Helper, state HelperState, now time.Time,
) bool {
	if state.Agent == "" || service.config.SignIns == nil || !service.config.SignIns.AgentNeedsSignIn(ctx, state.Agent) {
		return false
	}
	helper.SignIn = state.Agent
	agent := service.agentName(ctx, state.Agent)
	stored.addEvent(fmt.Sprintf("%s stopped: %s needs you to sign in again. It starts again once the agent works.",
		capitalized(helper.Name()), agent), now)
	stored.PendingEvents = append(stored.PendingEvents, fmt.Sprintf(
		"%s (helper %s) stopped because its agent, %s, needs the user to sign in again. Do not start it again now: "+
			"Commitarium tells you once the agent works, and you start it again then. Tell the user plainly to "+
			"sign %s in again on the Agents page.", capitalized(helper.Name()), helper.ID, agent, agent))
	return true
}

// restartHelpers wakes the assistant to start again the helpers in the scope
// that the agent's login stopped, in the chat that started each.
func (service *Service) restartHelpers(scope, agentID string) {
	shared, err := service.sharedOf(scope)
	if err != nil {
		return
	}
	for _, chat := range service.helperChats(scope, shared.Helpers, func(helper Helper) bool { return helper.SignIn == agentID }) {
		service.restartHelpersIn(scope, chat, agentID)
	}
}

func (service *Service) restartHelpersIn(scope, chat, agentID string) {
	stored, done, err := service.open(scope, chat)
	if err != nil {
		return
	}
	defer done()
	now, restarted := service.now(), false
	for index := range stored.Helpers {
		helper := &stored.Helpers[index]
		if helper.SignIn != agentID || service.helperChat(stored.Scope, *helper) != stored.Chat {
			continue
		}
		helper.SignIn, restarted = "", true
		stored.addEvent(capitalized(helper.Name())+" can go on: its agent works again.", now)
		stored.PendingEvents = append(stored.PendingEvents, fmt.Sprintf(
			"The agent works again, so %s can go on: start it again now with %s, from what you gathered before.",
			helper.Name(), helper.restart()))
	}
	if !restarted {
		return
	}
	stored.UpdatedAt = now
	if service.write(stored) == nil {
		service.nudgeWatcher()
	}
}

// helperSignInScopes lists the projects with a helper the agent's login
// stopped.
func (service *Service) helperSignInScopes(agentID string) []string {
	scopes := make([]string, 0)
	for _, scope := range append(service.projectScopes(), TopLevel) {
		stored, err := service.sharedOf(scope)
		if err == nil && slices.ContainsFunc(stored.Helpers, func(helper Helper) bool { return helper.SignIn == agentID }) {
			scopes = append(scopes, scope)
		}
	}
	return scopes
}
