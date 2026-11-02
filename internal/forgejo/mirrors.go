package forgejo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/project"
)

// PushMirror is a remote Forgejo keeps a repository's copy up to date on
// (SERVER_COMPLETE_PLAN.md, S2). Its credentials stay in Forgejo.
type PushMirror struct {
	Name       string    `json:"name"`
	RemoteURL  string    `json:"remote_url"`
	LastUpdate time.Time `json:"last_update,omitzero"`
	LastError  string    `json:"last_error,omitempty"`
}

// ErrInvalidMirror is a push mirror asked for with a bad address or no token.
var ErrInvalidMirror = errors.New("invalid push mirror")

// pushMirrorInterval is how often Forgejo pushes even without a new commit:
// a push it missed is made up within the day.
const pushMirrorInterval = "8h0m0s"

func mirrorsPath(owner, name string) (string, error) {
	owner, name, err := project.NormalizeRepositoryCoordinate(owner, name)
	if err != nil {
		return "", err
	}
	return "/api/v1/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name) + "/push_mirrors", nil
}

// ListPushMirrors returns a repository's push mirrors.
func (client *Client) ListPushMirrors(ctx context.Context, owner, name string) ([]PushMirror, error) {
	path, err := mirrorsPath(owner, name)
	if err != nil {
		return nil, err
	}
	status, body, err := client.doJSON(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("%w: listing push mirrors returned HTTP %d", project.ErrForgejoUnavailable, status)
	}
	var found []struct {
		RemoteName    string    `json:"remote_name"`
		RemoteAddress string    `json:"remote_address"`
		LastUpdate    time.Time `json:"last_update"`
		LastError     string    `json:"last_error"`
	}
	if err := json.Unmarshal(body, &found); err != nil {
		return nil, fmt.Errorf("%w: push mirrors could not be read", project.ErrForgejoUnavailable)
	}
	mirrors := make([]PushMirror, 0, len(found))
	for _, mirror := range found {
		mirrors = append(mirrors, PushMirror{
			Name: mirror.RemoteName, RemoteURL: mirror.RemoteAddress,
			LastUpdate: mirror.LastUpdate, LastError: mirror.LastError,
		})
	}
	return mirrors, nil
}

// AddPushMirror has Forgejo push the repository to remoteURL on every
// commit, signing in with username and token, and asks it to push now.
func (client *Client) AddPushMirror(ctx context.Context, owner, name, remoteURL, username, token string) error {
	path, err := mirrorsPath(owner, name)
	if err != nil {
		return err
	}
	if err := validRemoteURL(remoteURL); err != nil {
		return err
	}
	if strings.TrimSpace(token) == "" {
		return fmt.Errorf("%w: a push mirror needs an access token", ErrInvalidMirror)
	}
	if username == "" {
		// GitHub and GitLab take any name beside a token.
		username = "commitarium"
	}
	status, body, err := client.doJSON(ctx, http.MethodPost, path, map[string]any{
		"remote_address":  remoteURL,
		"remote_username": username,
		"remote_password": token,
		"interval":        pushMirrorInterval,
		"sync_on_commit":  true,
	})
	if err != nil {
		return err
	}
	if status != http.StatusOK && status != http.StatusCreated {
		return fmt.Errorf("%w: adding the push mirror returned HTTP %d: %s",
			project.ErrForgejoUnavailable, status, forgejoMessage(body))
	}
	// A remote that refuses the push fails this at once, but the mirror is
	// set up: Forgejo records the error on it, where the user sees it.
	_ = client.SyncPushMirrors(ctx, owner, name)
	return nil
}

// SyncPushMirrors pushes to every mirror now.
func (client *Client) SyncPushMirrors(ctx context.Context, owner, name string) error {
	path, err := mirrorsPath(owner, name)
	if err != nil {
		return err
	}
	status, _, err := client.doJSON(ctx, http.MethodPost, path+"-sync", nil)
	if err != nil {
		return err
	}
	if status != http.StatusOK && status != http.StatusNoContent {
		return fmt.Errorf("%w: pushing to the mirrors returned HTTP %d", project.ErrForgejoUnavailable, status)
	}
	return nil
}

// RemovePushMirror stops pushing to one mirror. The remote keeps its copy.
func (client *Client) RemovePushMirror(ctx context.Context, owner, name, mirror string) error {
	path, err := mirrorsPath(owner, name)
	if err != nil {
		return err
	}
	status, _, err := client.doJSON(ctx, http.MethodDelete, path+"/"+url.PathEscape(mirror), nil)
	if err != nil {
		return err
	}
	switch status {
	case http.StatusOK, http.StatusNoContent, http.StatusNotFound:
		return nil
	default:
		return fmt.Errorf("%w: removing the push mirror returned HTTP %d", project.ErrForgejoUnavailable, status)
	}
}

// validRemoteURL accepts an https URL of a repository, without credentials:
// those go in the token, which Forgejo keeps apart.
func validRemoteURL(raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil ||
		len(strings.Trim(parsed.Path, "/")) == 0 {
		return fmt.Errorf("%w: give the repository's https address, such as https://github.com/you/app.git",
			ErrInvalidMirror)
	}
	return nil
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
