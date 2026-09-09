package forgejo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/workspace"
)

const maxResponseBytes = 512 * 1024

var ErrInvalidClientConfig = errors.New("invalid Forgejo client configuration")

type ClientConfig struct {
	BaseURL        string
	TokenFile      string
	RequestTimeout time.Duration
	HTTPClient     *http.Client
}

type Client struct {
	baseURL        string
	tokenFile      string
	requestTimeout time.Duration
	httpClient     *http.Client
}

func NewClient(config ClientConfig) (*Client, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(config.BaseURL), "/")
	parsed, err := url.Parse(baseURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("%w: base URL must be an absolute HTTP URL without credentials, query, or fragment", ErrInvalidClientConfig)
	}
	tokenFile := strings.TrimSpace(config.TokenFile)
	if tokenFile == "" {
		return nil, fmt.Errorf("%w: token file path is required", ErrInvalidClientConfig)
	}
	if config.RequestTimeout <= 0 {
		return nil, fmt.Errorf("%w: request timeout must be positive", ErrInvalidClientConfig)
	}
	httpClient := config.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	clientCopy := *httpClient
	clientCopy.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &Client{
		baseURL: baseURL, tokenFile: tokenFile,
		requestTimeout: config.RequestTimeout, httpClient: &clientCopy,
	}, nil
}

func (client *Client) VerifyRepository(
	ctx context.Context,
	owner string,
	name string,
) (project.ForgejoRepository, error) {
	owner, name, err := project.NormalizeRepositoryCoordinate(owner, name)
	if err != nil {
		return project.ForgejoRepository{}, err
	}
	status, body, err := client.doJSON(
		ctx,
		http.MethodGet,
		"/api/v1/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(name),
		nil,
	)
	if err != nil {
		return project.ForgejoRepository{}, err
	}
	switch status {
	case http.StatusOK:
	case http.StatusNotFound:
		return project.ForgejoRepository{}, project.ErrForgejoRepositoryNotFound
	default:
		return project.ForgejoRepository{}, fmt.Errorf(
			"%w: repository request returned HTTP %d", project.ErrForgejoUnavailable, status,
		)
	}
	var decoded struct {
		Name          string `json:"name"`
		DefaultBranch string `json:"default_branch"`
		Empty         bool   `json:"empty"`
		Archived      bool   `json:"archived"`
		Owner         struct {
			Login string `json:"login"`
		} `json:"owner"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return project.ForgejoRepository{}, fmt.Errorf("%w: repository response is invalid JSON", project.ErrForgejoUnavailable)
	}
	canonicalOwner := strings.TrimSpace(decoded.Owner.Login)
	canonicalName := strings.TrimSpace(decoded.Name)
	defaultBranch := strings.TrimSpace(decoded.DefaultBranch)
	if decoded.Empty || decoded.Archived || defaultBranch == "" {
		return project.ForgejoRepository{}, project.ErrForgejoRepositoryNotReady
	}
	if canonicalOwner == "" || canonicalName == "" {
		return project.ForgejoRepository{}, fmt.Errorf("%w: repository response omitted its identity", project.ErrForgejoUnavailable)
	}
	if !strings.EqualFold(canonicalOwner, owner) || !strings.EqualFold(canonicalName, name) {
		return project.ForgejoRepository{}, fmt.Errorf("%w: repository response returned a different identity", project.ErrForgejoUnavailable)
	}
	repository := project.ForgejoRepository{
		Owner: canonicalOwner, Name: canonicalName, DefaultBranch: defaultBranch,
	}
	// BoundAt belongs to the coordinator command, not the remote lookup, so it
	// is assigned by project.Service immediately before persistence.
	return repository, nil
}

func (client *Client) GetBranch(
	ctx context.Context,
	owner string,
	repository string,
	branch string,
) (workspace.Branch, error) {
	var err error
	owner, repository, err = project.NormalizeRepositoryCoordinate(owner, repository)
	if err != nil {
		return workspace.Branch{}, err
	}
	if strings.TrimSpace(branch) == "" || branch != strings.TrimSpace(branch) {
		return workspace.Branch{}, errors.New("branch name is required and must be trimmed")
	}
	status, body, err := client.doJSON(
		ctx,
		http.MethodGet,
		repositoryBranchPath(owner, repository, branch),
		nil,
	)
	if err != nil {
		return workspace.Branch{}, err
	}
	switch status {
	case http.StatusOK:
		return decodeBranch(body)
	case http.StatusNotFound:
		return workspace.Branch{}, workspace.ErrBranchNotFound
	default:
		return workspace.Branch{}, fmt.Errorf(
			"%w: branch request returned HTTP %d", project.ErrForgejoUnavailable, status,
		)
	}
}

func (client *Client) EnsureBranch(
	ctx context.Context,
	owner string,
	repository string,
	branch string,
	baseCommitID string,
) (workspace.Branch, error) {
	var err error
	owner, repository, err = project.NormalizeRepositoryCoordinate(owner, repository)
	if err != nil {
		return workspace.Branch{}, err
	}
	if err := (workspace.Branch{Name: branch, CommitID: baseCommitID}).Validate(); err != nil {
		return workspace.Branch{}, err
	}
	payload := struct {
		NewBranchName string `json:"new_branch_name"`
		OldRefName    string `json:"old_ref_name"`
	}{NewBranchName: branch, OldRefName: baseCommitID}
	status, body, err := client.doJSON(
		ctx,
		http.MethodPost,
		"/api/v1/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(repository)+"/branches",
		payload,
	)
	if err != nil {
		return workspace.Branch{}, err
	}
	var stored workspace.Branch
	switch status {
	case http.StatusCreated:
		stored, err = decodeBranch(body)
	case http.StatusConflict:
		stored, err = client.GetBranch(ctx, owner, repository, branch)
	case http.StatusNotFound, http.StatusForbidden, http.StatusLocked:
		return workspace.Branch{}, project.ErrForgejoRepositoryNotReady
	default:
		return workspace.Branch{}, fmt.Errorf(
			"%w: branch creation returned HTTP %d", project.ErrForgejoUnavailable, status,
		)
	}
	if err != nil {
		return workspace.Branch{}, err
	}
	if stored.Name != branch || stored.CommitID != baseCommitID {
		return workspace.Branch{}, workspace.ErrBranchConflict
	}
	return stored, nil
}

func (client *Client) EnsureDraftPullRequest(
	ctx context.Context,
	owner string,
	repository string,
	spec workspace.PullRequestSpec,
) (workspace.PullRequest, error) {
	var err error
	owner, repository, err = project.NormalizeRepositoryCoordinate(owner, repository)
	if err != nil {
		return workspace.PullRequest{}, err
	}
	if err := spec.Validate(); err != nil {
		return workspace.PullRequest{}, err
	}
	if spec.ExistingNumber > 0 {
		stored, err := client.getPullRequest(ctx, owner, repository, spec.ExistingNumber)
		if err != nil {
			return workspace.PullRequest{}, err
		}
		if err := validateManagedPullRequest(stored, spec, false); err != nil {
			return workspace.PullRequest{}, err
		}
		return stored, nil
	}

	stored, found, err := client.findPullRequest(ctx, owner, repository, spec)
	if err != nil || found {
		return stored, err
	}
	payload := struct {
		Base  string `json:"base"`
		Head  string `json:"head"`
		Title string `json:"title"`
		Body  string `json:"body"`
	}{Base: spec.BaseBranch, Head: spec.HeadBranch, Title: spec.Title, Body: spec.Body}
	status, body, err := client.doJSON(
		ctx,
		http.MethodPost,
		pullRequestsPath(owner, repository),
		payload,
	)
	if err != nil {
		return workspace.PullRequest{}, err
	}
	switch status {
	case http.StatusCreated:
		stored, err = decodePullRequest(body)
		if err != nil {
			return workspace.PullRequest{}, err
		}
		if err := validateManagedPullRequest(stored, spec, true); err != nil {
			return workspace.PullRequest{}, err
		}
		return stored, nil
	case http.StatusConflict, http.StatusUnprocessableEntity:
		stored, found, err = client.findPullRequest(ctx, owner, repository, spec)
		if err != nil {
			return workspace.PullRequest{}, err
		}
		if found {
			return stored, nil
		}
		return workspace.PullRequest{}, workspace.ErrPullRequestConflict
	case http.StatusNotFound, http.StatusForbidden, http.StatusLocked:
		return workspace.PullRequest{}, project.ErrForgejoRepositoryNotReady
	default:
		return workspace.PullRequest{}, fmt.Errorf(
			"%w: pull request creation returned HTTP %d",
			project.ErrForgejoUnavailable,
			status,
		)
	}
}

func (client *Client) getPullRequest(
	ctx context.Context,
	owner string,
	repository string,
	number int64,
) (workspace.PullRequest, error) {
	status, body, err := client.doJSON(
		ctx,
		http.MethodGet,
		fmt.Sprintf("%s/%d", pullRequestsPath(owner, repository), number),
		nil,
	)
	if err != nil {
		return workspace.PullRequest{}, err
	}
	switch status {
	case http.StatusOK:
		return decodePullRequest(body)
	case http.StatusNotFound:
		return workspace.PullRequest{}, workspace.ErrPullRequestConflict
	default:
		return workspace.PullRequest{}, fmt.Errorf(
			"%w: pull request request returned HTTP %d",
			project.ErrForgejoUnavailable,
			status,
		)
	}
}

func (client *Client) findPullRequest(
	ctx context.Context,
	owner string,
	repository string,
	spec workspace.PullRequestSpec,
) (workspace.PullRequest, bool, error) {
	query := url.Values{}
	query.Set("state", "all")
	query.Set("base", spec.BaseBranch)
	query.Set("head", spec.HeadBranch)
	query.Set("limit", "20")
	status, body, err := client.doJSON(
		ctx,
		http.MethodGet,
		pullRequestsPath(owner, repository)+"?"+query.Encode(),
		nil,
	)
	if err != nil {
		return workspace.PullRequest{}, false, err
	}
	if status != http.StatusOK {
		return workspace.PullRequest{}, false, fmt.Errorf(
			"%w: pull request list returned HTTP %d",
			project.ErrForgejoUnavailable,
			status,
		)
	}
	var decoded []json.RawMessage
	if err := json.Unmarshal(body, &decoded); err != nil {
		return workspace.PullRequest{}, false, fmt.Errorf(
			"%w: pull request list is invalid JSON",
			project.ErrForgejoUnavailable,
		)
	}
	matches := make([]workspace.PullRequest, 0, 1)
	for _, raw := range decoded {
		candidate, err := decodePullRequest(raw)
		if err != nil {
			return workspace.PullRequest{}, false, err
		}
		if candidate.BaseBranch == spec.BaseBranch && candidate.HeadBranch == spec.HeadBranch {
			matches = append(matches, candidate)
		}
	}
	if len(matches) == 0 {
		return workspace.PullRequest{}, false, nil
	}
	if len(matches) != 1 {
		return workspace.PullRequest{}, false, workspace.ErrPullRequestConflict
	}
	if err := validateManagedPullRequest(matches[0], spec, true); err != nil {
		return workspace.PullRequest{}, false, err
	}
	return matches[0], true, nil
}

func (client *Client) doJSON(
	ctx context.Context,
	method string,
	path string,
	body any,
) (int, []byte, error) {
	tokenBytes, err := os.ReadFile(client.tokenFile)
	if err != nil {
		return 0, nil, fmt.Errorf("%w: access token file cannot be read", project.ErrForgejoUnavailable)
	}
	token := strings.TrimSpace(string(tokenBytes))
	if token == "" {
		return 0, nil, fmt.Errorf("%w: access token file is empty", project.ErrForgejoUnavailable)
	}
	var requestBody io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return 0, nil, fmt.Errorf("encode Forgejo request: %w", err)
		}
		requestBody = bytes.NewReader(encoded)
	}
	requestContext, cancel := context.WithTimeout(ctx, client.requestTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(
		requestContext, method, client.baseURL+path, requestBody,
	)
	if err != nil {
		return 0, nil, fmt.Errorf("create Forgejo request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	request.Header.Set("Authorization", "token "+token)
	response, err := client.httpClient.Do(request)
	if err != nil {
		return 0, nil, fmt.Errorf("%w: request failed: %v", project.ErrForgejoUnavailable, err)
	}
	defer response.Body.Close()
	limited := io.LimitReader(response.Body, maxResponseBytes+1)
	responseBody, err := io.ReadAll(limited)
	if err != nil {
		return 0, nil, fmt.Errorf("%w: response cannot be read", project.ErrForgejoUnavailable)
	}
	if len(responseBody) > maxResponseBytes {
		return 0, nil, fmt.Errorf("%w: response is too large", project.ErrForgejoUnavailable)
	}
	return response.StatusCode, responseBody, nil
}

func repositoryBranchPath(owner, repository, branch string) string {
	return "/api/v1/repos/" + url.PathEscape(owner) + "/" +
		url.PathEscape(repository) + "/branches/" + url.PathEscape(branch)
}

func pullRequestsPath(owner, repository string) string {
	return "/api/v1/repos/" + url.PathEscape(owner) + "/" +
		url.PathEscape(repository) + "/pulls"
}

func decodeBranch(body []byte) (workspace.Branch, error) {
	var decoded struct {
		Name   string `json:"name"`
		Commit struct {
			ID string `json:"id"`
		} `json:"commit"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return workspace.Branch{}, fmt.Errorf("%w: branch response is invalid JSON", project.ErrForgejoUnavailable)
	}
	branch := workspace.Branch{
		Name: strings.TrimSpace(decoded.Name), CommitID: strings.TrimSpace(decoded.Commit.ID),
	}
	if err := branch.Validate(); err != nil {
		return workspace.Branch{}, fmt.Errorf("%w: branch response is invalid", project.ErrForgejoUnavailable)
	}
	return branch, nil
}

func decodePullRequest(body []byte) (workspace.PullRequest, error) {
	var decoded struct {
		Number    int64  `json:"number"`
		HTMLURL   string `json:"html_url"`
		Title     string `json:"title"`
		Body      string `json:"body"`
		State     string `json:"state"`
		Draft     bool   `json:"draft"`
		CreatedAt string `json:"created_at"`
		Base      struct {
			Ref string `json:"ref"`
		} `json:"base"`
		Head struct {
			Ref string `json:"ref"`
			SHA string `json:"sha"`
		} `json:"head"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return workspace.PullRequest{}, fmt.Errorf(
			"%w: pull request response is invalid JSON",
			project.ErrForgejoUnavailable,
		)
	}
	createdAt, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(decoded.CreatedAt))
	if err != nil {
		return workspace.PullRequest{}, fmt.Errorf(
			"%w: pull request response has an invalid creation time",
			project.ErrForgejoUnavailable,
		)
	}
	pullRequest := workspace.PullRequest{
		Number: decoded.Number, URL: strings.TrimSpace(decoded.HTMLURL),
		Title: strings.TrimSpace(decoded.Title), Body: decoded.Body,
		State: strings.TrimSpace(decoded.State), Draft: decoded.Draft,
		BaseBranch:   strings.TrimSpace(decoded.Base.Ref),
		HeadBranch:   strings.TrimSpace(decoded.Head.Ref),
		HeadCommitID: strings.TrimSpace(decoded.Head.SHA), CreatedAt: createdAt.UTC(),
	}
	if err := pullRequest.Validate(); err != nil {
		return workspace.PullRequest{}, fmt.Errorf(
			"%w: pull request response is invalid",
			project.ErrForgejoUnavailable,
		)
	}
	return pullRequest, nil
}

func validateManagedPullRequest(
	pullRequest workspace.PullRequest,
	spec workspace.PullRequestSpec,
	requireInitial bool,
) error {
	if pullRequest.State != "open" || !pullRequest.Draft ||
		pullRequest.BaseBranch != spec.BaseBranch ||
		pullRequest.HeadBranch != spec.HeadBranch ||
		!strings.Contains(pullRequest.Body, spec.FeatureMarker) {
		return workspace.ErrPullRequestConflict
	}
	if requireInitial && (pullRequest.Title != spec.Title ||
		pullRequest.HeadCommitID != spec.InitialHeadCommitID) {
		return workspace.ErrPullRequestConflict
	}
	return nil
}
