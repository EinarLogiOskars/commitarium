package conversations

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/featureartifact"
)

// Mayka's say (ADR-041): each project keeps how much she does there, a level
// and her own words. The level is the project's assistant mode (builds is
// Build everything, assists is Ask first, the default); the words are kept
// in the project's shared state. In every project she decides, action by
// action, whether to take it (applied right after her turn) or ask for it (a
// card), and the coordinator's hard limits (HardLimit) stop what is never
// hers to take.

// Say is Mayka's own words on how much she does in a project.
type Say struct {
	Words     string    `json:"words"`
	UpdatedAt time.Time `json:"updated_at"`
}

// SayOf is a project's say as she keeps it; empty when she has none.
func (service *Service) SayOf(scope string) Say {
	shared, err := service.sharedOf(scope)
	if err != nil || shared.Say == nil {
		return Say{}
	}
	return *shared.Say
}

// SetSay replaces Mayka's words in a project's say.
func (service *Service) SetSay(ctx context.Context, scope, words string) error {
	if scope == TopLevel {
		return fmt.Errorf("%w: a say belongs to a project", ErrInvalid)
	}
	if err := service.checkScope(ctx, scope); err != nil {
		return err
	}
	words = strings.TrimSpace(words)
	if len(words) > featureartifact.MaxSayWordsBytes {
		return fmt.Errorf("%w: the say's words are too long", ErrInvalid)
	}
	return service.updateShared(scope, func(shared *sharedState) bool {
		shared.Say = &Say{Words: words, UpdatedAt: service.now()}
		return true
	})
}

// sayLevel names a project's say level in the user's words.
func sayLevel(builds bool) string {
	if builds {
		return "Build everything"
	}
	return "Ask first"
}

// Limits are what a hard limit is judged on, beyond the action itself.
type Limits struct {
	// Builds is the say's level: Build everything.
	Builds bool
	// Theirs is the title of the work order the action acts on when the
	// user created it by hand and did not hand it over with a goal; empty
	// when it is Mayka's, or the action names none.
	Theirs string
	// Premium names the models it runs that need the user's explicit
	// approval.
	Premium []string
}

// HardLimit says, in plain words, why an action Mayka took is not hers to
// apply on her own, whatever her say is (ADR-041): empty when nothing stops
// it. The watcher checks it before it applies a taken action, and the
// approve handler again when the watcher's approval reaches it. The loop
// bounds (SafetyCap, maxAutoContinues, maxRefusals) are the watcher's.
func HardLimit(action Action, limits Limits) string {
	switch action.Type {
	case featureartifact.AssistantCreateApp, featureartifact.AssistantPlanProject:
		return "a new app or project waits for your approval."
	case featureartifact.AssistantBuildPlan:
		return "building the plan waits for your approval."
	case featureartifact.AssistantStartPlanning:
		return "starting plan mode waits for your approval."
	}
	if featureartifact.AppliedByTheApp(action.Type) {
		// The user's app applies the server's cards through the host, the
		// ones the user asked for at once; never the watcher.
		return "changes on the server are applied by the app, not on their own."
	}
	if len(limits.Premium) > 0 {
		return fmt.Sprintf("it uses %s, which needs your approval.", strings.Join(limits.Premium, " and "))
	}
	if limits.Theirs != "" {
		if action.Type == featureartifact.AssistantCloseWorkOrder {
			return fmt.Sprintf("“%s” is your work order, and closing it is yours to decide.", limits.Theirs)
		}
		if !action.Asked {
			return fmt.Sprintf("“%s” is your work order, so it waits for you.", limits.Theirs)
		}
	}
	if action.Type == featureartifact.AssistantRecoverWorkOrder && action.How == "back_to_ready" && !limits.Builds {
		// Back to Ready throws work away: the user's call, unless she builds
		// everything here, where she may start stuck work over as she may
		// close and replace it.
		return "sending work back to Ready needs your approval."
	}
	return ""
}

// taken reports whether Mayka takes an action: she marked it take, or it is
// sent at once whatever she marked.
func taken(action Action) bool {
	return action.Take || sentAtOnce(action.Type)
}
