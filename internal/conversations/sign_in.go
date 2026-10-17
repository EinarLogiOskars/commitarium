package conversations

import (
	"context"
	"fmt"
	"log"
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

// retrySignIn starts again the turn a conversation keeps for its agent's
// login, when its try is due or force says the agent works again. What
// waited meanwhile (events, the user's messages) rides along.
func (service *Service) retrySignIn(ctx context.Context, scope string, force bool, now time.Time) error {
	settings, err := service.Readiness(ctx)
	if err != nil || settings.Agent == "" || settings.Model == "" {
		return err
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	stored, err := service.read(scope)
	if err != nil || stored.SignIn == nil || stored.Status == StatusRunning || stored.LastTurn == nil {
		return err
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

// AgentSignedIn starts again every conversation's turn that waits on the
// agent: its login works again.
func (service *Service) AgentSignedIn(ctx context.Context, agentID string) {
	for _, scope := range service.signInScopes(agentID, false) {
		if err := service.retrySignIn(ctx, scope, true, service.now()); err != nil {
			log.Printf("resume the assistant in %s after %q signed in: %v", scope, agentID, err)
		}
	}
}

// TrySignIn starts again the longest-waiting conversation turn that waits
// for the agent's sign-in: if it works, the agent's login is back.
func (service *Service) TrySignIn(ctx context.Context, agentID string) {
	scopes := service.signInScopes(agentID, true)
	if len(scopes) == 0 {
		return
	}
	if err := service.retrySignIn(ctx, scopes[0], true, service.now()); err != nil {
		log.Printf("try the assistant in %s for %q: %v", scopes[0], agentID, err)
	}
}

// RetryDueSignIns starts again the conversation turns whose next try is
// due, the top-level one too.
func (service *Service) RetryDueSignIns(ctx context.Context) {
	for _, scope := range append(service.projectScopes(), TopLevel) {
		if err := service.retrySignIn(ctx, scope, false, service.now()); err != nil {
			log.Printf("retry the assistant in %s: %v", scope, err)
		}
	}
}

// signInScopes lists the conversations waiting on the agent's login, the
// longest waiting first; only those waiting for the user when needsUser.
func (service *Service) signInScopes(agentID string, needsUser bool) []string {
	service.mu.Lock()
	defer service.mu.Unlock()
	type waiting struct {
		scope string
		since time.Time
	}
	found := make([]waiting, 0)
	for _, scope := range append(service.projectScopes(), TopLevel) {
		stored, err := service.read(scope)
		if err != nil || stored.SignIn == nil || stored.SignIn.Agent != agentID ||
			(needsUser && !stored.SignIn.NeedsUser) {
			continue
		}
		found = append(found, waiting{scope, stored.SignIn.Since})
	}
	for i := 1; i < len(found); i++ {
		for j := i; j > 0 && found[j].since.Before(found[j-1].since); j-- {
			found[j], found[j-1] = found[j-1], found[j]
		}
	}
	scopes := make([]string, 0, len(found))
	for _, item := range found {
		scopes = append(scopes, item.scope)
	}
	return scopes
}
