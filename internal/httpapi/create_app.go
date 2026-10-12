package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"

	"github.com/EinarLogiOskars/commitarium/internal/conversations"
	"github.com/EinarLogiOskars/commitarium/internal/project"
)

// applyCreateApp creates the app the user and the assistant settled on
// (APPS_PLAN.md): a project used as an app that runs to its merge gate and
// merges on its own, on the agents and models of the newest configured
// project; its launch; and its vision, written from the agreed description.
// Each step goes through the API's own route, so it keeps that route's
// checks; a retry repeats them with the same keys.
func (api *API) applyCreateApp(w http.ResponseWriter, r *http.Request, action conversations.Action) (string, string, bool) {
	ctx := r.Context()
	key := "assistant-" + action.ID
	body := map[string]any{
		"name":            action.Title,
		"merge_policy":    project.MergePolicyAutoAfterGates,
		"autonomy_policy": project.AutonomyPolicyRunToCompletion,
	}
	if template, ok := api.newestConfiguredProject(ctx); ok {
		lead, reviewer := template.AgentProviders.Lead, template.AgentProviders.Reviewer
		leadAgent, reviewerAgent := template.AgentProviders.LeadAgent, template.AgentProviders.ReviewerAgent
		body["agent_providers"] = agentProvidersRequest{Lead: &lead, Reviewer: &reviewer, LeadAgent: &leadAgent, ReviewerAgent: &reviewerAgent}
		leadModel, reviewerModel := template.AgentModels.Lead, template.AgentModels.Reviewer
		body["agent_models"] = agentModelsRequest{Lead: &leadModel, Reviewer: &reviewerModel}
	}
	var created projectResponse
	if err := api.callOwn(ctx, http.MethodPost, "/api/v1/projects", key+":project", body, &created); err != nil {
		writeError(w, http.StatusBadGateway, "app_not_created", "the app could not be created: "+err.Error())
		return "", "", false
	}
	base := "/api/v1/projects/" + url.PathEscape(created.ID)
	steps := []struct {
		method, path, key string
		body              any
	}{
		{http.MethodPut, base + "/app", "", map[string]bool{"app": true}},
		{http.MethodPost, "/api/v1/assistant/conversations/" + url.PathEscape(created.ID) + "/launch", "", nil},
		{http.MethodPost, base + "/vision/assistant", key + ":vision", map[string]string{"mode": "describe", "message": action.Description}},
	}
	for _, step := range steps {
		if err := api.callOwn(ctx, step.method, step.path, step.key, step.body, nil); err != nil {
			writeError(w, http.StatusBadGateway, "app_not_started",
				fmt.Sprintf("the app %q was created, but starting it failed: %v", action.Title, err))
			return "", "", false
		}
	}
	return fmt.Sprintf("Created the app “%s”. It is being planned, set up, and built.", action.Title), created.ID, true
}

// newestConfiguredProject is the most recently created project with chosen
// models: the user's current choice of agents.
func (api *API) newestConfiguredProject(ctx context.Context) (project.Project, bool) {
	projects, err := api.projects.List(ctx)
	if err != nil {
		return project.Project{}, false
	}
	sort.Slice(projects, func(i, j int) bool { return projects[i].CreatedAt.After(projects[j].CreatedAt) })
	for _, candidate := range projects {
		if candidate.AgentModels.Lead != "" {
			return candidate, true
		}
	}
	return project.Project{}, false
}

// callOwn sends a request to the API's own routes and decodes the answer.
func (api *API) callOwn(ctx context.Context, method, path, key string, body, into any) error {
	if api.mux == nil {
		return errors.New("the API is not ready")
	}
	var reader *bytes.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(encoded)
	} else {
		reader = bytes.NewReader(nil)
	}
	request, err := http.NewRequestWithContext(ctx, method, path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		request.Header.Set("Idempotency-Key", key)
	}
	response := &recordedResponse{header: http.Header{}, status: http.StatusOK}
	api.mux.ServeHTTP(response, request)
	if response.status >= 300 {
		var failure errorResponse
		if json.Unmarshal(response.body.Bytes(), &failure) == nil && failure.Error.Message != "" {
			return errors.New(failure.Error.Message)
		}
		return fmt.Errorf("HTTP %d", response.status)
	}
	if into != nil {
		return json.Unmarshal(response.body.Bytes(), into)
	}
	return nil
}
