package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/EinarLogiOskars/commitarium/internal/conversations"
	"github.com/EinarLogiOskars/commitarium/internal/featureartifact"
	"github.com/EinarLogiOskars/commitarium/internal/project"
)

// sayKeeper keeps Mayka's own words in a project's say (ADR-041).
type sayKeeper interface {
	SetSay(ctx context.Context, scope, words string) error
}

// applySetSay records how much Mayka does in a project, as the user told
// her: the level goes through the same path as the Project settings
// switcher, and her words are kept with the project's chats.
func (api *API) applySetSay(
	w http.ResponseWriter, r *http.Request, projectID string, action conversations.Action,
) (string, bool) {
	ctx := r.Context()
	result := "Noted how much Mayka does here."
	if action.Level != "" {
		mode := project.AssistantModeAssists
		result = "Mayka asks first here."
		if action.Level == featureartifact.SayBuildEverything {
			mode, result = project.AssistantModeBuilds, "Mayka builds everything here."
		}
		if _, err := api.setAssistantMode(ctx, projectID, mode); err != nil {
			switch {
			case errors.Is(err, errAssistantModeUnavailable):
				writeError(w, http.StatusServiceUnavailable, "assistant_mode_unavailable", err.Error())
			case errors.Is(err, project.ErrNotFound):
				writeError(w, http.StatusNotFound, "project_not_found", "project not found")
			default:
				writeConversationError(w, err)
			}
			return "", false
		}
	}
	if action.Words != "" {
		keeper, ok := api.conversations.(sayKeeper)
		if !ok {
			writeError(w, http.StatusServiceUnavailable, "assistant_unavailable", "the say cannot be kept")
			return "", false
		}
		if err := keeper.SetSay(ctx, projectID, action.Words); err != nil {
			writeConversationError(w, err)
			return "", false
		}
		if action.Level != "" {
			result += " Her words: " + action.Words
		} else {
			result = "Noted: " + action.Words
		}
	}
	return result, true
}
