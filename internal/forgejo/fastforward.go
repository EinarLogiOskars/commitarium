package forgejo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/EinarLogiOskars/commitarium/internal/project"
)

// ErrNotFastForward is a fast-forward Forgejo refused: the base branch has
// commits the head lacks.
var ErrNotFastForward = errors.New("not a fast-forward")

// FastForwardBranch moves base up to commit, the tip of head, through a
// pull request the merge gate approves, since a protected branch takes no
// push. It brings commits made on a linked repository into the project
// (SERVER_COMPLETE_PLAN.md, S2). head is deleted once merged.
func (client *Client) FastForwardBranch(
	ctx context.Context,
	owner, repository, base, head, commit, title string,
) error {
	var err error
	owner, repository, err = project.NormalizeRepositoryCoordinate(owner, repository)
	if err != nil {
		return err
	}
	pulls := pullRequestsPath(owner, repository)
	status, body, err := client.doJSON(ctx, http.MethodPost, pulls, map[string]string{
		"base": base, "head": head, "title": title,
		"body": "Commits brought in from the project's linked repository.",
	})
	if err != nil {
		return err
	}
	if status == http.StatusConflict {
		// One is open already, from a bring-in cut short.
		status, body, err = client.doJSON(ctx, http.MethodGet,
			pulls+"/"+url.PathEscape(base)+"/"+url.PathEscape(head), nil)
		if err != nil {
			return err
		}
	}
	if status != http.StatusOK && status != http.StatusCreated {
		return fmt.Errorf("%w: opening the pull request returned HTTP %d: %s",
			project.ErrForgejoUnavailable, status, forgejoMessage(body))
	}
	var opened struct {
		Number int64 `json:"number"`
	}
	if err := json.Unmarshal(body, &opened); err != nil || opened.Number == 0 {
		return fmt.Errorf("%w: the pull request could not be read", project.ErrForgejoUnavailable)
	}
	if err := client.approvePullRequest(ctx, owner, repository, opened.Number, commit,
		"Brought in from the linked repository."); err != nil {
		return err
	}
	status, body, err = client.doJSON(ctx, http.MethodPost, fmt.Sprintf("%s/%d/merge", pulls, opened.Number),
		map[string]any{"Do": "fast-forward-only", "head_commit_id": commit, "delete_branch_after_merge": true})
	if err != nil {
		return err
	}
	switch status {
	case http.StatusOK:
		return nil
	case http.StatusConflict, http.StatusMethodNotAllowed:
		// Main moved on meanwhile. The pull request goes, so the next
		// bring-in starts clean.
		_, _, _ = client.doJSON(ctx, http.MethodPatch, fmt.Sprintf("%s/%d", pulls, opened.Number),
			map[string]string{"state": "closed"})
		return ErrNotFastForward
	default:
		return fmt.Errorf("%w: the fast-forward returned HTTP %d: %s",
			project.ErrForgejoUnavailable, status, forgejoMessage(body))
	}
}

// forgejoMessage is the message of a Forgejo error answer, when it has one.
func forgejoMessage(body []byte) string {
	var answer struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &answer) == nil && answer.Message != "" {
		return answer.Message
	}
	return strings.TrimSpace(string(body))
}
