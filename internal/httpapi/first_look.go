package httpapi

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"

	"github.com/EinarLogiOskars/commitarium/internal/conversations"
	"github.com/EinarLogiOskars/commitarium/internal/project"
)

var _ firstLooker = (*conversations.Service)(nil)

// firstLooker wakes a project's assistant for its first look at a project
// just imported (SERVER_COMPLETE_PLAN.md, S7).
type firstLooker interface {
	FirstLook(ctx context.Context, scope string, settings []string, builds bool) error
}

// firstLookHandler serves POST /api/v1/projects/{id}/first-look.
func (api *API) firstLookHandler(w http.ResponseWriter, r *http.Request) {
	if err := api.firstLook(r.Context(), r.PathValue("id")); err != nil {
		if errors.Is(err, project.ErrNotFound) {
			writeError(w, http.StatusNotFound, "project_not_found", "project not found")
			return
		}
		writeError(w, http.StatusServiceUnavailable, "first_look_unavailable", err.Error())
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// firstLook has the assistant look an imported project over, with its
// settings in plain words.
func (api *API) firstLook(ctx context.Context, projectID string) error {
	looker, ok := api.conversations.(firstLooker)
	if !ok {
		return errors.New("the assistant is unavailable")
	}
	found, err := api.projects.GetByID(ctx, projectID)
	if err != nil {
		return err
	}
	return looker.FirstLook(ctx, projectID, api.settingsInWords(ctx, found), found.AssistantBuilds())
}

// lookFirstAfterImport starts the first look of a project an import just
// created; the import stands whether or not it starts.
func (api *API) lookFirstAfterImport(ctx context.Context, projectID string) {
	if err := api.firstLook(context.WithoutCancel(ctx), projectID); err != nil {
		log.Printf("first look at imported project %s: %v", projectID, err)
	}
}

// settingsInWords says how a project is set up, as the user would put it.
func (api *API) settingsInWords(ctx context.Context, current project.Project) []string {
	var settings []string
	switch current.AutonomyPolicy {
	case project.AutonomyPolicyRunToCompletion:
		settings = append(settings, "Work orders run through planning and implementation without stopping, up to "+
			"the merge.")
	default:
		settings = append(settings, "Each work order stops for the user's review after its plan and after its "+
			"implementation.")
	}
	switch current.MergePolicy {
	case project.MergePolicyAutoAfterGates:
		settings = append(settings, "A work order merges into main by itself once it passes its gates: the "+
			"reviewer's approval and the validation checks.")
	default:
		settings = append(settings, "Nothing merges into main until the user approves it.")
	}
	if api.validations != nil {
		if config, err := api.validations.GetConfig(ctx, current.ID); err == nil && len(config.Commands) > 0 {
			settings = append(settings, "Before a merge, these validation checks must pass: "+
				strings.Join(config.Commands, "; ")+".")
		} else {
			settings = append(settings, "There are no validation checks, so a merge rests on the lead's and "+
				"the reviewer's approval alone.")
		}
	}
	limits := current.DialogueLimits
	settings = append(settings, fmt.Sprintf("The agents may take %s on a plan and %s of code review before "+
		"they stop for the user.", rounds(limits.PlanningRounds), rounds(limits.ImplementationReviewRounds)))
	switch current.RecoveryPolicy {
	case project.RecoveryPolicyAutomatic:
		settings = append(settings, "A run that fails is retried by itself.")
	default:
		settings = append(settings, "A run that fails waits for the user to retry or change it.")
	}
	return settings
}

func rounds(limit int) string {
	switch limit {
	case 0:
		return "any number of rounds"
	case 1:
		return "1 round"
	default:
		return fmt.Sprintf("%d rounds", limit)
	}
}

// FirstLooker starts the first look of an imported project through
// handler's own route, for imports served outside it.
func FirstLooker(handler http.Handler) func(ctx context.Context, projectID string) {
	return func(ctx context.Context, projectID string) {
		request, err := http.NewRequestWithContext(context.WithoutCancel(ctx), http.MethodPost,
			"/api/v1/projects/"+url.PathEscape(projectID)+"/first-look", nil)
		if err != nil {
			return
		}
		response := &recordedResponse{header: http.Header{}, status: http.StatusOK}
		handler.ServeHTTP(response, request)
		if response.status != http.StatusAccepted {
			log.Printf("first look at imported project %s: HTTP %d", projectID, response.status)
		}
	}
}
