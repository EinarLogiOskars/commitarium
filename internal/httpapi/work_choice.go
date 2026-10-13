package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/agent"
	"github.com/EinarLogiOskars/commitarium/internal/conversations"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/modelcatalog"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
)

// Who chooses a work order's agents and models (ADR-027): the user in the
// Start form, or the assistant in its actions. The coordinator never picks
// for them; it only refuses a choice that cannot run, with a reason the
// assistant (or the user) can act on: no agents named, an agent that is gone,
// signed out, not connected, or at its usage limit, or a model its worker
// does not offer. Premium models still need the user's approval on the
// assistant's actions (premiumModels).

// choiceError is a refused choice of agents and models.
type choiceError struct {
	status  int
	code    string
	message string
}

func (err *choiceError) Error() string { return err.message }

var errAgentsNotChosen = &choiceError{
	status: http.StatusBadRequest, code: "agents_not_chosen",
	message: "no agents are chosen for this work order: name the lead agent and model, and the reviewer's when it has a reviewer",
}

// writeChoiceError writes a refused choice and reports whether err was one.
func writeChoiceError(w http.ResponseWriter, err error) bool {
	var refused *choiceError
	if !errors.As(err, &refused) {
		return false
	}
	writeError(w, refused.status, refused.code, refused.message)
	return true
}

// checkWorkChoice refuses agents and models that cannot run new work now. A
// work order without a reviewer step checks only its lead.
func (api *API) checkWorkChoice(
	ctx context.Context, providers project.AgentProviders, models project.AgentModels, skipReviewer bool,
) error {
	if providers.IsZero() || models.Lead == "" || models.Reviewer == "" {
		return errAgentsNotChosen
	}
	type pick struct {
		agentID  string
		provider project.AgentProvider
		role     modelcatalog.Role
		model    string
	}
	picks := []pick{{providers.LeadAgent, providers.Lead, modelcatalog.RoleLead, models.Lead}}
	if !skipReviewer {
		picks = append(picks, pick{providers.ReviewerAgent, providers.Reviewer, modelcatalog.RoleReviewer, models.Reviewer})
	}
	var usage []AgentUsage
	if api.agentUsage != nil {
		// Asked once for both roles; nil means the workers cannot be asked.
		usage, _ = api.agentUsage.AgentUsage(ctx)
	}
	for _, chosen := range picks {
		if err := api.agentReady(ctx, chosen.agentID, usage); err != nil {
			return err
		}
		if err := api.modelOffered(ctx, chosen.agentID, chosen.provider, chosen.role, chosen.model); err != nil {
			return err
		}
	}
	return nil
}

// agentReady refuses an agent that is gone, signed out, not connected, or at
// its usage limit. usage is the agents' latest reports; nil skips what only
// the workers know.
func (api *API) agentReady(ctx context.Context, agentID string, usage []AgentUsage) error {
	if api.agents != nil {
		if _, err := api.agents.Get(ctx, agentID); errors.Is(err, agent.ErrNotFound) || errors.Is(err, agent.ErrInvalid) {
			return &choiceError{status: http.StatusConflict, code: "agent_not_found",
				message: fmt.Sprintf("agent %q does not exist; choose one of the connected agents", agentID)}
		} else if err != nil {
			return err
		}
	}
	if waits, ok := api.execution.(interface {
		RunsAwaitingLogin(context.Context, string) ([]string, error)
	}); ok {
		if waiting, err := waits.RunsAwaitingLogin(ctx, agentID); err == nil && len(waiting) > 0 {
			return &choiceError{status: http.StatusConflict, code: "agent_signed_out",
				message: fmt.Sprintf("agent %q is signed out; sign it in again on the Agents page, or choose another agent", agentID)}
		}
	}
	now := api.clock()
	if waits, ok := api.execution.(runLimitWaits); ok {
		if pending, err := waits.LimitWaits(ctx); err == nil {
			for _, wait := range pending {
				if wait.AgentID == agentID && (wait.ResetsAt == nil || wait.ResetsAt.After(now)) {
					return atLimit(agentID, wait.ResetsAt)
				}
			}
		}
	}
	if usage == nil {
		return nil
	}
	index := slices.IndexFunc(usage, func(reported AgentUsage) bool { return reported.Agent == agentID })
	if index < 0 {
		return &choiceError{status: http.StatusConflict, code: "agent_not_connected",
			message: fmt.Sprintf("agent %q is not connected right now; connect it on the Agents page, or choose another agent", agentID)}
	}
	if limited, until := usage[index].LimitedUntil(now); limited {
		return atLimit(agentID, until)
	}
	return nil
}

func atLimit(agentID string, until *time.Time) error {
	message := fmt.Sprintf("agent %q has reached its usage limit", agentID)
	if until != nil {
		message += " until " + until.UTC().Format(time.RFC3339)
	}
	return &choiceError{status: http.StatusConflict, code: "agent_at_limit",
		message: message + "; choose an agent with usage left, or wait for the reset"}
}

// modelOffered refuses a model the agent's worker does not offer for the
// role. Without a model catalog (the simulated runner) any explicit model is
// accepted.
func (api *API) modelOffered(
	ctx context.Context, agentID string, provider project.AgentProvider, role modelcatalog.Role, model string,
) error {
	if err := project.ValidateExactModelID(model); err != nil {
		return &choiceError{status: http.StatusBadRequest, code: "invalid_agent_models",
			message: fmt.Sprintf("%q is not an exact model ID; choose one of the models listed for agent %q", model, agentID)}
	}
	if api.modelCatalog == nil {
		return nil
	}
	for _, catalog := range api.modelCatalog.List(ctx) {
		if catalog.Provider != provider || catalog.Role != role {
			continue
		}
		if catalog.FetchedAt == nil || len(catalog.Models) == 0 {
			return &choiceError{status: http.StatusServiceUnavailable, code: "model_catalog_unavailable",
				message: fmt.Sprintf("the models of agent %q are not known yet; try again once its worker is up", agentID)}
		}
		if !slices.ContainsFunc(catalog.Models, func(offered workerhttp.Model) bool { return offered.ID == model }) {
			return &choiceError{status: http.StatusBadRequest, code: "model_unavailable",
				message: fmt.Sprintf("agent %q does not offer the model %q; choose one of the models listed for it", agentID, model)}
		}
		return nil
	}
	return &choiceError{status: http.StatusServiceUnavailable, code: "model_catalog_unavailable",
		message: fmt.Sprintf("the models of agent %q are not known yet; try again once its worker is up", agentID)}
}

// fillSkippedReviewer names the lead as the reviewer of a work order whose
// steps skip the reviewer, when the request names none. Runs record both
// roles; this reviewer never runs, so nothing is chosen for the user.
func fillSkippedReviewer(providers *agentProvidersRequest, models *agentModelsRequest, skipReviewer bool) {
	if !skipReviewer {
		return
	}
	if providers != nil && providers.ReviewerAgent == nil && providers.Reviewer == nil {
		providers.ReviewerAgent, providers.Reviewer = providers.LeadAgent, providers.Lead
	}
	if models != nil && models.Reviewer == nil {
		models.Reviewer = models.Lead
	}
}

// startChoice is what a Start would run on: the request's agents, models, and
// steps over the work order's own. It writes the error response itself and
// reports whether to continue.
func (api *API) startChoice(
	w http.ResponseWriter, r *http.Request, stored feature.Feature, request *startRunRequest,
) (project.AgentProviders, project.AgentModels, bool, bool) {
	skipReviewer := stored.Steps.SkipReviewer
	if request != nil && request.Steps != nil && request.Steps.Reviewer != nil {
		skipReviewer = !*request.Steps.Reviewer
	}
	// A work order from before agents names only providers; normalizing
	// names their migrated agents.
	providers, err := stored.AgentProviders.NormalizeChosen()
	if err != nil {
		providers = project.AgentProviders{}
	}
	models := stored.AgentModels
	if request == nil {
		return providers, models, skipReviewer, true
	}
	fillSkippedReviewer(request.AgentProviders, request.AgentModels, skipReviewer)
	if request.AgentProviders != nil {
		decoded, err := api.decodeAgentProviders(r.Context(), request.AgentProviders, false)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_agent_providers", "agent_providers must name existing lead and reviewer agents")
			return project.AgentProviders{}, project.AgentModels{}, false, false
		}
		providers = decoded
	}
	if request.AgentModels != nil {
		decoded, err := decodeAgentModels(request.AgentModels, false)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_agent_models", "agent_models must include explicit lead and reviewer model IDs")
			return project.AgentProviders{}, project.AgentModels{}, false, false
		}
		models = decoded
	}
	return providers, models, skipReviewer, true
}

var (
	errNoAgents = &choiceError{status: http.StatusConflict, code: "no_agents",
		message: conversations.ErrNoAgents.Error()}
	errAssistantNotSetUp = &choiceError{status: http.StatusConflict, code: "assistant_not_set_up",
		message: conversations.ErrNotSetUp.Error()}
)

// assistantAgent is the agent and model the user chose for the assistant.
// Work Commitarium starts by itself (writing a vision, clarifying a work
// order, setting a project or its preview up) runs on them: a choice the user
// made, never one the coordinator makes.
func (api *API) assistantAgent(ctx context.Context) (project.AgentProviders, string, error) {
	if api.conversations == nil {
		return project.AgentProviders{}, "", errAssistantNotSetUp
	}
	settings, err := api.conversations.Settings(ctx)
	if err != nil {
		return project.AgentProviders{}, "", err
	}
	if settings.Agent == "" || settings.Model == "" {
		if api.agents != nil {
			if agents, err := api.agents.List(ctx); err == nil && len(agents) == 0 {
				return project.AgentProviders{}, "", errNoAgents
			}
		}
		return project.AgentProviders{}, "", errAssistantNotSetUp
	}
	providers, err := api.decodeAgentProviders(ctx, &agentProvidersRequest{
		LeadAgent: &settings.Agent, ReviewerAgent: &settings.Agent,
	}, false)
	if err != nil {
		return project.AgentProviders{}, "", errAssistantNotSetUp
	}
	return providers, settings.Model, nil
}
