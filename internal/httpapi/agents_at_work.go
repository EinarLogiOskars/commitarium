package httpapi

import (
	"context"
	"errors"
	"log"
	"net/http"
	"sort"

	"github.com/EinarLogiOskars/commitarium/internal/assistant"
	"github.com/EinarLogiOskars/commitarium/internal/conversations"
	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/toolchain"
	"github.com/EinarLogiOskars/commitarium/internal/vision"
	"github.com/EinarLogiOskars/commitarium/internal/worker"
)

// The agents at work in a project, in one read, for the Agents card beside
// its chats (ADR-038): the lead and reviewer of each open run, and the
// helpers the assistant started that have not finished.

// Roles of agents at work besides a run's lead and reviewer.
const (
	agentRoleSession     = "session"
	agentRoleBriefWriter = "brief_writer"
	agentRoleVision      = "vision_writer"
	agentRoleStack       = "stack_designer"
	agentRoleResearch    = "research"
)

// Agent states: working has a turn in flight; idle waits on the other agent
// of its run; waiting waits on the user, a checkpoint, or a limit.
const (
	agentWorking = "working"
	agentIdle    = "idle"
	agentWaiting = "waiting"
)

type agentAtWork struct {
	// ID is unique in the response: a run and role, or a helper's session.
	ID string `json:"id"`
	// Agent is the agent's ID, AgentName the name the user gave it, and
	// Provider the agent's kind (claude, codex).
	Agent     string `json:"agent"`
	AgentName string `json:"agent_name,omitempty"`
	Provider  string `json:"provider,omitempty"`
	Model     string `json:"model,omitempty"`
	// Role is lead or reviewer for runs, session for a work session's agent,
	// or the helper's job: brief_writer, vision_writer, stack_designer,
	// research.
	Role           string `json:"role"`
	WorkOrderID    string `json:"work_order_id,omitempty"`
	WorkOrderTitle string `json:"work_order_title,omitempty"`
	RunID          string `json:"run_id,omitempty"`
	// Title names what a helper without a work order works on.
	Title   string `json:"title,omitempty"`
	State   string `json:"state"`
	Working bool   `json:"working"`
	// Detail says, in plain words, what an agent that is not working waits on.
	Detail string `json:"detail,omitempty"`
}

type agentsAtWorkResponse struct {
	Agents []agentAtWork `json:"agents"`
}

// helperLister lists the helpers the assistant started in a scope.
type helperLister interface {
	HelpersOf(ctx context.Context, scope string) ([]conversations.Helper, error)
}

func (api *API) agentsAtWorkHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projectID := r.PathValue("id")
	if _, err := api.projects.GetByID(ctx, projectID); err != nil {
		if errors.Is(err, project.ErrNotFound) {
			writeError(w, http.StatusNotFound, "project_not_found", "project not found")
			return
		}
		log.Printf("agents at work in %q: %v", projectID, err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	agents, err := api.runAgents(ctx, projectID)
	if err != nil {
		log.Printf("agents at work in %q: %v", projectID, err)
		writeError(w, http.StatusInternalServerError, "agents_unavailable", "the agents at work could not be read")
		return
	}
	agents = append(agents, api.helperAgents(ctx, projectID)...)
	api.nameAgents(ctx, agents)
	sort.SliceStable(agents, func(i, j int) bool { return agents[i].Working && !agents[j].Working })
	writeJSON(w, http.StatusOK, agentsAtWorkResponse{Agents: agents}, "agents at work")
}

// runAgents lists the lead and reviewer of each open run in the project,
// the newest run of each work order that has not ended.
func (api *API) runAgents(ctx context.Context, projectID string) ([]agentAtWork, error) {
	features, err := api.features.List(ctx, projectID)
	if err != nil {
		return nil, err
	}
	agents := make([]agentAtWork, 0)
	for _, order := range features {
		if order.State.IsTerminal() {
			continue
		}
		runs, err := api.execution.RunsForFeature(ctx, order.ID)
		if err != nil {
			return nil, err
		}
		if len(runs) == 0 {
			continue
		}
		run := execution.NewestRun(runs)
		if run.Status != execution.RunStatusRunning && run.Status != execution.RunStatusWaitingForUser {
			continue
		}
		sessions, err := api.execution.SessionsForRun(ctx, run.ID)
		if err != nil {
			return nil, err
		}
		agents = append(agents, runAgentsOf(order, run, sessions)...)
	}
	return agents, nil
}

// runAgentsOf is a run's lead, and its reviewer unless the run skips review.
// An agent works while the run runs and its own session has a turn in
// flight.
func runAgentsOf(order feature.Feature, run execution.Run, sessions []execution.Session) []agentAtWork {
	roles := []worker.Role{worker.RoleLead, worker.RoleReviewer}
	hasReviewer := false
	for _, session := range sessions {
		hasReviewer = hasReviewer || session.Role == worker.RoleReviewer
	}
	if order.Kind == feature.KindSession || (run.Steps.SkipReviewer && !hasReviewer) {
		roles = roles[:1]
	}
	agents := make([]agentAtWork, 0, len(roles))
	for _, role := range roles {
		agent := agentAtWork{
			ID: run.ID + ":" + string(role), Role: string(role), RunID: run.ID,
			WorkOrderID: order.ID, WorkOrderTitle: order.Title,
		}
		if role == worker.RoleLead {
			agent.Provider, agent.Agent, agent.Model = string(run.AgentProviders.Lead), run.AgentProviders.LeadAgent, run.AgentModels.Lead
		} else {
			agent.Provider, agent.Agent, agent.Model = string(run.AgentProviders.Reviewer), run.AgentProviders.ReviewerAgent, run.AgentModels.Reviewer
		}
		if order.Kind == feature.KindSession {
			agent.Role = agentRoleSession
		}
		inTurn := false
		for _, session := range sessions {
			if session.Role != role {
				continue
			}
			if session.AgentID != "" {
				agent.Agent = session.AgentID
			}
			inTurn = inTurn || session.Status == execution.SessionStatusRunning || session.Status == execution.SessionStatusStarting
		}
		if agent.Agent == "" {
			agent.Agent = agent.Provider
		}
		switch {
		case run.Status == execution.RunStatusRunning && inTurn:
			agent.State, agent.Working = agentWorking, true
		case run.Status == execution.RunStatusRunning:
			agent.State = agentIdle
			if role == worker.RoleLead {
				agent.Detail = "Waiting for the reviewer"
			} else {
				agent.Detail = "Waiting for the lead"
			}
		default:
			agent.State, agent.Detail = agentWaiting, runWaitWords(run)
		}
		agents = append(agents, agent)
	}
	return agents
}

// runWaitWords says, in plain words, what a waiting run waits on.
func runWaitWords(run execution.Run) string {
	switch run.WaitKind {
	case execution.RunWaitKindClarification:
		return "Has a question"
	case execution.RunWaitKindPhaseCheckpoint:
		if execution.HeldBeforeImplementation(run.Reason) {
			return "Planned, waiting to build"
		}
		return "Waiting for approval"
	case execution.RunWaitKindRoundCap:
		return "Reached its round limit"
	case execution.RunWaitKindMergeGate:
		return "Waiting to merge"
	case execution.RunWaitKindPaused:
		return "Paused"
	default:
		return "Stopped by a problem"
	}
}

// helperAgents lists the helpers the assistant started in the project that
// have not finished, with the agent and model their sessions run on. A
// helper that cannot be read is left out rather than failing the card.
func (api *API) helperAgents(ctx context.Context, projectID string) []agentAtWork {
	lister, ok := api.conversations.(helperLister)
	if !ok {
		return nil
	}
	helpers, err := lister.HelpersOf(ctx, projectID)
	if err != nil {
		if !errors.Is(err, conversations.ErrNotFound) {
			log.Printf("helpers of %q: %v", projectID, err)
		}
		return nil
	}
	agents := make([]agentAtWork, 0)
	for _, helper := range helpers {
		if helper.Done {
			continue
		}
		agent, ok, err := api.helperAgent(ctx, projectID, helper)
		if err != nil {
			log.Printf("helper %s of %q: %v", helper.ID, projectID, err)
			continue
		}
		if ok {
			agents = append(agents, agent)
		}
	}
	return agents
}

// helperAgent reads one helper's session: ok is false once it has ended.
func (api *API) helperAgent(ctx context.Context, projectID string, helper conversations.Helper) (agentAtWork, bool, error) {
	agent := agentAtWork{ID: helper.ID, Agent: helper.Agent, Title: helper.Title}
	status := ""
	switch helper.Kind {
	case conversations.HelperBrief:
		if api.assistant == nil {
			return agent, false, nil
		}
		session, err := api.assistant.Get(ctx, projectID, helper.WorkOrderID)
		if errors.Is(err, assistant.ErrNotFound) {
			return agent, false, nil
		} else if err != nil {
			return agent, false, err
		}
		agent.Role, agent.WorkOrderID, agent.WorkOrderTitle = agentRoleBriefWriter, helper.WorkOrderID, helper.Title
		agent.Title = ""
		agent.Agent, agent.Model, status = session.Agent, session.Model, string(session.Status)
		if session.Status == assistant.StatusFailed {
			return agent, false, nil
		}
		if session.Status == assistant.StatusProposalReady {
			status, agent.Detail = string(assistant.StatusWaitingForUser), "The brief is ready for approval"
		}
	case conversations.HelperVision:
		if api.visions == nil {
			return agent, false, nil
		}
		session, err := api.visions.GetSession(ctx, projectID, helper.ID)
		if errors.Is(err, vision.ErrNotFound) {
			return agent, false, nil
		} else if err != nil {
			return agent, false, err
		}
		if session.Status == vision.StatusWritten || session.Status == vision.StatusFailed {
			return agent, false, nil
		}
		agent.Role, agent.Agent, agent.Model, status = agentRoleVision, session.Agent, session.Model, string(session.Status)
	case conversations.HelperStack:
		if api.toolchainAssistant == nil {
			return agent, false, nil
		}
		session, err := api.toolchainAssistant.Get(ctx, projectID, helper.ID)
		if errors.Is(err, toolchain.ErrAssistantNotFound) {
			return agent, false, nil
		} else if err != nil {
			return agent, false, err
		}
		switch session.Status {
		case toolchain.AssistantStatusApplied, toolchain.AssistantStatusFailed:
			return agent, false, nil
		case toolchain.AssistantStatusProposalReady:
			status, agent.Detail = string(toolchain.AssistantStatusWaitingForUser), "The stack is ready for approval"
		default:
			status = string(session.Status)
		}
		agent.Role, agent.Agent, agent.Model = agentRoleStack, session.Agent, session.Model
	case conversations.HelperResearch:
		// Research asks nothing: it works until it reports back, and the
		// assistant's watcher marks it done then. It runs on the
		// assistant's own agent and model.
		agent.Role, status = agentRoleResearch, "running"
		if settings, err := api.conversations.Settings(ctx); err == nil && settings.Agent == helper.Agent {
			agent.Model = settings.Model
		}
	default:
		return agent, false, nil
	}
	if status == "running" {
		agent.State, agent.Working = agentWorking, true
	} else {
		agent.State = agentWaiting
		if agent.Detail == "" {
			agent.Detail = "Has a question for the assistant"
		}
	}
	return agent, true, nil
}

// nameAgents adds each agent's name, and its provider where the session did
// not say, from the agents the user set up.
func (api *API) nameAgents(ctx context.Context, agents []agentAtWork) {
	if api.agents == nil || len(agents) == 0 {
		return
	}
	known, err := api.agents.List(ctx)
	if err != nil {
		log.Printf("agents at work: list agents: %v", err)
		return
	}
	for index := range agents {
		for _, candidate := range known {
			if candidate.ID != agents[index].Agent {
				continue
			}
			agents[index].AgentName = candidate.Name
			if agents[index].Provider == "" {
				agents[index].Provider = string(candidate.Provider)
			}
		}
	}
}
