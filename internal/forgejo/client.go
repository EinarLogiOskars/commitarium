package forgejo

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	Owner          string
	TokenFile      string
	RequestTimeout time.Duration
	HTTPClient     *http.Client
}

type Client struct {
	baseURL        string
	owner          string
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
	owner := strings.TrimSpace(config.Owner)
	if owner != "" && strings.ContainsAny(owner, "/\\") {
		return nil, fmt.Errorf("%w: owner must be a single path segment", ErrInvalidClientConfig)
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
		baseURL: baseURL, owner: owner, tokenFile: tokenFile,
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

func (client *Client) EnsureImportRepository(
	ctx context.Context,
	spec project.RepositoryImportSpec,
) (project.ForgejoRepository, error) {
	owner, err := client.importRepositoryOwner(ctx)
	if err != nil {
		return project.ForgejoRepository{}, err
	}
	stored, found, err := client.getImportRepository(ctx, owner, spec.Repository)
	if err != nil {
		return project.ForgejoRepository{}, err
	}
	if found {
		return validateImportRepository(stored, owner, spec, true)
	}
	payload := struct {
		Name          string `json:"name"`
		Description   string `json:"description"`
		Private       bool   `json:"private"`
		AutoInit      bool   `json:"auto_init"`
		DefaultBranch string `json:"default_branch"`
	}{
		Name: spec.Repository, Description: importRepositoryDescription(spec),
		Private: true, AutoInit: false, DefaultBranch: spec.DefaultBranch,
	}
	status, body, err := client.doJSON(ctx, http.MethodPost, "/api/v1/user/repos", payload)
	if err != nil {
		return project.ForgejoRepository{}, err
	}
	switch status {
	case http.StatusCreated:
		stored, err = decodeImportRepository(body)
		if err != nil {
			return project.ForgejoRepository{}, err
		}
		return validateImportRepository(stored, owner, spec, true)
	case http.StatusConflict, http.StatusUnprocessableEntity:
		stored, found, err = client.getImportRepository(ctx, owner, spec.Repository)
		if err != nil {
			return project.ForgejoRepository{}, err
		}
		if !found {
			return project.ForgejoRepository{}, project.ErrImportConflict
		}
		return validateImportRepository(stored, owner, spec, true)
	default:
		return project.ForgejoRepository{}, fmt.Errorf(
			"%w: repository creation returned HTTP %d", project.ErrForgejoUnavailable, status,
		)
	}
}

func (client *Client) FinalizeImportRepository(
	ctx context.Context,
	spec project.RepositoryImportSpec,
) (project.ForgejoRepository, error) {
	owner, err := client.importRepositoryOwner(ctx)
	if err != nil {
		return project.ForgejoRepository{}, err
	}
	status, body, err := client.doJSON(
		ctx, http.MethodPatch,
		"/api/v1/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(spec.Repository),
		struct {
			DefaultBranch string `json:"default_branch"`
		}{DefaultBranch: spec.DefaultBranch},
	)
	if err != nil {
		return project.ForgejoRepository{}, err
	}
	if status != http.StatusOK {
		return project.ForgejoRepository{}, fmt.Errorf(
			"%w: default branch update returned HTTP %d", project.ErrForgejoUnavailable, status,
		)
	}
	// Forgejo returns the updated repository. Validate that mutation response
	// directly instead of immediately issuing a read that can briefly lag the
	// just-completed Git push.
	stored, err := decodeImportRepository(body)
	if err != nil {
		return project.ForgejoRepository{}, err
	}
	return validateImportRepository(stored, owner, spec, false)
}

func (client *Client) VerifyImportRepository(
	ctx context.Context,
	spec project.RepositoryImportSpec,
) (project.ForgejoRepository, error) {
	owner, err := client.importRepositoryOwner(ctx)
	if err != nil {
		return project.ForgejoRepository{}, err
	}
	stored, found, err := client.getImportRepository(ctx, owner, spec.Repository)
	if err != nil {
		return project.ForgejoRepository{}, err
	}
	if !found {
		return project.ForgejoRepository{}, project.ErrForgejoRepositoryNotFound
	}
	return validateImportRepository(stored, owner, spec, false)
}

type importRepository struct {
	Owner         string
	Name          string
	Description   string
	DefaultBranch string
	Private       bool
	Empty         bool
	Archived      bool
}

func (client *Client) authenticatedUser(ctx context.Context) (string, error) {
	status, body, err := client.doJSON(ctx, http.MethodGet, "/api/v1/user", nil)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("%w: authenticated user request returned HTTP %d", project.ErrForgejoUnavailable, status)
	}
	var decoded struct {
		Login string `json:"login"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil || strings.TrimSpace(decoded.Login) == "" {
		return "", fmt.Errorf("%w: authenticated user response is invalid", project.ErrForgejoUnavailable)
	}
	return strings.TrimSpace(decoded.Login), nil
}

func (client *Client) importRepositoryOwner(ctx context.Context) (string, error) {
	if client.owner != "" {
		return client.owner, nil
	}
	return client.authenticatedUser(ctx)
}

func (client *Client) getImportRepository(ctx context.Context, owner, name string) (importRepository, bool, error) {
	status, body, err := client.doJSON(
		ctx, http.MethodGet,
		"/api/v1/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(name), nil,
	)
	if err != nil {
		return importRepository{}, false, err
	}
	switch status {
	case http.StatusOK:
		stored, err := decodeImportRepository(body)
		return stored, true, err
	case http.StatusNotFound:
		return importRepository{}, false, nil
	default:
		return importRepository{}, false, fmt.Errorf(
			"%w: repository request returned HTTP %d", project.ErrForgejoUnavailable, status,
		)
	}
}

func decodeImportRepository(body []byte) (importRepository, error) {
	var decoded struct {
		Name          string `json:"name"`
		Description   string `json:"description"`
		DefaultBranch string `json:"default_branch"`
		Private       bool   `json:"private"`
		Empty         bool   `json:"empty"`
		Archived      bool   `json:"archived"`
		Owner         struct {
			Login string `json:"login"`
		} `json:"owner"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return importRepository{}, fmt.Errorf("%w: repository response is invalid JSON", project.ErrForgejoUnavailable)
	}
	return importRepository{
		Owner: strings.TrimSpace(decoded.Owner.Login), Name: strings.TrimSpace(decoded.Name),
		Description: decoded.Description, DefaultBranch: strings.TrimSpace(decoded.DefaultBranch),
		Private: decoded.Private, Empty: decoded.Empty, Archived: decoded.Archived,
	}, nil
}

func validateImportRepository(
	stored importRepository,
	expectedOwner string,
	spec project.RepositoryImportSpec,
	allowIncomplete bool,
) (project.ForgejoRepository, error) {
	if !strings.EqualFold(stored.Owner, expectedOwner) ||
		!strings.EqualFold(stored.Name, spec.Repository) ||
		stored.Description != importRepositoryDescription(spec) || !stored.Private || stored.Archived {
		return project.ForgejoRepository{}, project.ErrImportConflict
	}
	// Forgejo can briefly report an imported repository as empty after a
	// successful synchronous Git push. The caller has already validated and
	// pushed the required branch, so finalization relies on the exact default
	// branch rather than this eventually consistent metadata bit.
	if !allowIncomplete && stored.DefaultBranch != spec.DefaultBranch {
		return project.ForgejoRepository{}, project.ErrImportConflict
	}
	if allowIncomplete && stored.DefaultBranch != "" && stored.DefaultBranch != spec.DefaultBranch {
		return project.ForgejoRepository{}, project.ErrImportConflict
	}
	return project.ForgejoRepository{
		Owner: stored.Owner, Name: stored.Name, DefaultBranch: spec.DefaultBranch,
	}, nil
}

func importRepositoryDescription(spec project.RepositoryImportSpec) string {
	digest := sha256.Sum256([]byte(spec.ImportID))
	return "Commitarium managed import " + hex.EncodeToString(digest[:12]) + " " + spec.BundleDigest
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

func (client *Client) EnsurePullRequestPlan(
	ctx context.Context,
	owner string,
	repository string,
	spec workspace.PlanPublicationSpec,
) (workspace.PullRequest, bool, error) {
	return client.reconcilePullRequestPlan(ctx, owner, repository, spec, true)
}

func (client *Client) VerifyPullRequestPlan(
	ctx context.Context,
	owner string,
	repository string,
	spec workspace.PlanPublicationSpec,
) (workspace.PullRequest, error) {
	pullRequest, _, err := client.reconcilePullRequestPlan(
		ctx, owner, repository, spec, false,
	)
	return pullRequest, err
}

func (client *Client) VerifyPullRequestImplementation(
	ctx context.Context,
	owner string,
	repository string,
	spec workspace.ImplementationPublicationSpec,
) (workspace.PullRequest, error) {
	var err error
	owner, repository, err = project.NormalizeRepositoryCoordinate(owner, repository)
	if err != nil {
		return workspace.PullRequest{}, err
	}
	if err := spec.Validate(); err != nil {
		return workspace.PullRequest{}, err
	}
	pullRequest, err := client.getPullRequest(ctx, owner, repository, spec.Number)
	if err != nil {
		return workspace.PullRequest{}, err
	}
	planSection := spec.PlanPublicationMarker + "\n\n## Agreed implementation plan\n\n" + spec.Plan
	if pullRequest.State != "open" || !pullRequest.Draft ||
		pullRequest.BaseBranch != spec.BaseBranch ||
		pullRequest.HeadBranch != spec.HeadBranch ||
		pullRequest.HeadCommitID != spec.HeadCommitID ||
		strings.Count(pullRequest.Body, spec.FeatureMarker) != 1 ||
		strings.Count(pullRequest.Body, spec.PlanPublicationMarker) != 1 ||
		!strings.HasSuffix(pullRequest.Body, planSection) {
		return workspace.PullRequest{}, workspace.ErrPullRequestConflict
	}

	status, body, err := client.doJSON(
		ctx,
		http.MethodGet,
		fmt.Sprintf(
			"/api/v1/repos/%s/%s/issues/%d/comments?limit=50",
			url.PathEscape(owner), url.PathEscape(repository), spec.Number,
		),
		nil,
	)
	if err != nil {
		return workspace.PullRequest{}, err
	}
	if status != http.StatusOK {
		return workspace.PullRequest{}, fmt.Errorf(
			"%w: pull request comments returned HTTP %d",
			project.ErrForgejoUnavailable, status,
		)
	}
	var comments []struct {
		Body string `json:"body"`
		User struct {
			Login string `json:"login"`
		} `json:"user"`
	}
	if err := json.Unmarshal(body, &comments); err != nil {
		return workspace.PullRequest{}, fmt.Errorf(
			"%w: pull request comments response is invalid JSON",
			project.ErrForgejoUnavailable,
		)
	}
	// Refuse to guess whether a marker exists beyond the bounded first page.
	// Implementation runs precede review discussion, so reaching this limit is
	// itself an unexpected state that should stop automatic routing.
	if len(comments) >= 50 {
		return workspace.PullRequest{}, workspace.ErrPullRequestConflict
	}
	publicationMarker := spec.PublicationKind.Marker(spec.AttemptID)
	wantBody := publicationMarker + "\n\n## " + spec.PublicationKind.CommentHeading() + "\n\n" + spec.Summary
	matches := 0
	for _, comment := range comments {
		if strings.Contains(comment.Body, publicationMarker) {
			if comment.Body != wantBody ||
				!strings.EqualFold(strings.TrimSpace(comment.User.Login), spec.ExpectedAuthor) {
				return workspace.PullRequest{}, workspace.ErrPullRequestConflict
			}
			matches++
		}
	}
	if matches != 1 {
		return workspace.PullRequest{}, workspace.ErrPullRequestConflict
	}
	return pullRequest, nil
}

func (client *Client) VerifyPullRequestReview(
	ctx context.Context,
	owner string,
	repository string,
	spec workspace.ReviewPublicationSpec,
) (workspace.PullRequest, error) {
	var err error
	owner, repository, err = project.NormalizeRepositoryCoordinate(owner, repository)
	if err != nil {
		return workspace.PullRequest{}, err
	}
	if err := spec.Validate(); err != nil {
		return workspace.PullRequest{}, err
	}
	pullRequest, err := client.getPullRequest(ctx, owner, repository, spec.Number)
	if err != nil {
		return workspace.PullRequest{}, err
	}
	planSection := spec.PlanPublicationMarker + "\n\n## Agreed implementation plan\n\n" + spec.Plan
	if pullRequest.State != "open" || !pullRequest.Draft ||
		pullRequest.BaseBranch != spec.BaseBranch || pullRequest.HeadBranch != spec.HeadBranch ||
		pullRequest.HeadCommitID != spec.HeadCommitID ||
		strings.Count(pullRequest.Body, spec.FeatureMarker) != 1 ||
		strings.Count(pullRequest.Body, spec.PlanPublicationMarker) != 1 ||
		!strings.HasSuffix(pullRequest.Body, planSection) {
		return workspace.PullRequest{}, workspace.ErrPullRequestConflict
	}
	status, body, err := client.doJSON(
		ctx, http.MethodGet,
		fmt.Sprintf(
			"/api/v1/repos/%s/%s/pulls/%d/reviews/%d",
			url.PathEscape(owner), url.PathEscape(repository), spec.Number, spec.ReviewID,
		), nil,
	)
	if err != nil {
		return workspace.PullRequest{}, err
	}
	if status != http.StatusOK {
		return workspace.PullRequest{}, fmt.Errorf(
			"%w: pull request review returned HTTP %d", project.ErrForgejoUnavailable, status,
		)
	}
	var review struct {
		ID       int64  `json:"id"`
		Body     string `json:"body"`
		CommitID string `json:"commit_id"`
		State    string `json:"state"`
		Stale    bool   `json:"stale"`
		User     struct {
			Login string `json:"login"`
		} `json:"user"`
	}
	if err := json.Unmarshal(body, &review); err != nil {
		return workspace.PullRequest{}, fmt.Errorf(
			"%w: pull request review response is invalid JSON", project.ErrForgejoUnavailable,
		)
	}
	wantBody := spec.PublicationMarker + "\n\n## Review\n\n" + spec.Summary
	if review.ID != spec.ReviewID || review.CommitID != spec.HeadCommitID ||
		review.State != spec.ExpectedState || review.Stale || review.Body != wantBody ||
		!strings.EqualFold(strings.TrimSpace(review.User.Login), spec.ExpectedAuthor) {
		return workspace.PullRequest{}, workspace.ErrPullRequestConflict
	}
	return pullRequest, nil
}

func (client *Client) reconcilePullRequestPlan(
	ctx context.Context,
	owner string,
	repository string,
	spec workspace.PlanPublicationSpec,
	publishMissing bool,
) (workspace.PullRequest, bool, error) {
	var err error
	owner, repository, err = project.NormalizeRepositoryCoordinate(owner, repository)
	if err != nil {
		return workspace.PullRequest{}, false, err
	}
	if err := spec.Validate(); err != nil {
		return workspace.PullRequest{}, false, err
	}
	stored, err := client.getPullRequest(ctx, owner, repository, spec.Number)
	if err != nil {
		return workspace.PullRequest{}, false, err
	}
	if err := validatePlanPullRequest(stored, spec); err != nil {
		return workspace.PullRequest{}, false, err
	}
	section := spec.PublicationMarker + "\n\n## Agreed implementation plan\n\n" + spec.Plan
	if markerCount := strings.Count(stored.Body, spec.PublicationMarker); markerCount > 0 {
		if markerCount != 1 || !strings.HasSuffix(stored.Body, section) {
			return workspace.PullRequest{}, false, workspace.ErrPullRequestConflict
		}
		return stored, false, nil
	}
	if !publishMissing {
		return workspace.PullRequest{}, false, workspace.ErrPullRequestConflict
	}
	payload := struct {
		Body string `json:"body"`
	}{Body: strings.TrimRight(stored.Body, "\n") + "\n\n" + section}
	status, body, err := client.doJSON(
		ctx, http.MethodPatch,
		fmt.Sprintf("%s/%d", pullRequestsPath(owner, repository), spec.Number),
		payload,
	)
	if err != nil {
		return workspace.PullRequest{}, false, err
	}
	switch status {
	case http.StatusOK, http.StatusCreated:
	case http.StatusNotFound, http.StatusForbidden, http.StatusLocked:
		return workspace.PullRequest{}, false, project.ErrForgejoRepositoryNotReady
	default:
		return workspace.PullRequest{}, false, fmt.Errorf(
			"%w: pull request update returned HTTP %d",
			project.ErrForgejoUnavailable, status,
		)
	}
	updated, err := decodePullRequest(body)
	if err != nil {
		return workspace.PullRequest{}, false, err
	}
	if err := validatePlanPullRequest(updated, spec); err != nil || updated.Body != payload.Body {
		return workspace.PullRequest{}, false, workspace.ErrPullRequestConflict
	}
	return updated, true, nil
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

// MergePullRequest always reconciles the remote pull request before mutating
// it. The exact approved head is also sent to Forgejo's merge endpoint, so a
// push between our read and write is rejected by Forgejo rather than merged.
func (client *Client) MergePullRequest(
	ctx context.Context,
	owner string,
	repository string,
	spec workspace.PullRequestMergeSpec,
) (workspace.PullRequest, error) {
	var err error
	owner, repository, err = project.NormalizeRepositoryCoordinate(owner, repository)
	if err != nil {
		return workspace.PullRequest{}, err
	}
	if err := spec.Validate(); err != nil {
		return workspace.PullRequest{}, err
	}
	stored, err := client.getPullRequest(ctx, owner, repository, spec.Number)
	if err != nil {
		return workspace.PullRequest{}, err
	}
	if err := validateMergePullRequest(stored, spec); err != nil {
		return workspace.PullRequest{}, err
	}
	if stored.Merged {
		return stored, nil
	}
	if stored.Draft {
		readyTitle, found := strings.CutPrefix(stored.Title, "WIP:")
		readyTitle = strings.TrimSpace(readyTitle)
		if !found || readyTitle == "" {
			return workspace.PullRequest{}, workspace.ErrPullRequestConflict
		}
		updateStatus, _, updateErr := client.doJSON(
			ctx,
			http.MethodPatch,
			fmt.Sprintf("%s/%d", pullRequestsPath(owner, repository), spec.Number),
			struct {
				Title string `json:"title"`
			}{Title: readyTitle},
		)
		stored, err = client.getPullRequest(ctx, owner, repository, spec.Number)
		if err != nil {
			if updateErr != nil {
				return workspace.PullRequest{}, updateErr
			}
			return workspace.PullRequest{}, err
		}
		if err := validateMergePullRequest(stored, spec); err != nil {
			return workspace.PullRequest{}, err
		}
		if stored.Merged {
			return stored, nil
		}
		if updateErr != nil {
			return workspace.PullRequest{}, updateErr
		}
		if stored.Draft {
			if updateStatus == http.StatusNotFound {
				return workspace.PullRequest{}, workspace.ErrPullRequestConflict
			}
			return workspace.PullRequest{}, fmt.Errorf(
				"%w: pull request draft update returned HTTP %d",
				project.ErrForgejoUnavailable, updateStatus,
			)
		}
	}

	status, _, mergeErr := client.doJSON(
		ctx,
		http.MethodPost,
		fmt.Sprintf("%s/%d/merge", pullRequestsPath(owner, repository), spec.Number),
		struct {
			Do                     string `json:"Do"`
			HeadCommitID           string `json:"head_commit_id"`
			MergeWhenChecksSucceed bool   `json:"merge_when_checks_succeed"`
			DeleteBranchAfterMerge bool   `json:"delete_branch_after_merge"`
		}{
			Do: "merge", HeadCommitID: spec.HeadCommitID,
			MergeWhenChecksSucceed: false, DeleteBranchAfterMerge: false,
		},
	)
	// A request can succeed remotely while its response is lost. Read the PR
	// after every outcome and trust that durable Forgejo state over the response.
	stored, reconcileErr := client.getPullRequest(ctx, owner, repository, spec.Number)
	if reconcileErr == nil {
		if err := validateMergePullRequest(stored, spec); err != nil {
			return workspace.PullRequest{}, err
		}
		if stored.Merged {
			return stored, nil
		}
	}
	if mergeErr != nil {
		return workspace.PullRequest{}, mergeErr
	}
	if reconcileErr != nil {
		return workspace.PullRequest{}, reconcileErr
	}
	switch status {
	case http.StatusConflict, http.StatusMethodNotAllowed, http.StatusNotFound:
		return workspace.PullRequest{}, workspace.ErrPullRequestConflict
	default:
		return workspace.PullRequest{}, fmt.Errorf(
			"%w: pull request merge returned HTTP %d without a merged result",
			project.ErrForgejoUnavailable,
			status,
		)
	}
}

func validateMergePullRequest(
	pullRequest workspace.PullRequest,
	spec workspace.PullRequestMergeSpec,
) error {
	if pullRequest.Number != spec.Number || pullRequest.BaseBranch != spec.BaseBranch ||
		pullRequest.HeadBranch != spec.HeadBranch || pullRequest.HeadCommitID != spec.HeadCommitID ||
		strings.Count(pullRequest.Body, spec.FeatureMarker) != 1 {
		return workspace.ErrPullRequestConflict
	}
	if pullRequest.Merged {
		if pullRequest.State != "closed" || pullRequest.MergeCommitID == "" || pullRequest.MergedAt == nil {
			return workspace.ErrPullRequestConflict
		}
		return nil
	}
	if pullRequest.State != "open" {
		return workspace.ErrPullRequestConflict
	}
	return nil
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
		Number        int64   `json:"number"`
		HTMLURL       string  `json:"html_url"`
		Title         string  `json:"title"`
		Body          string  `json:"body"`
		State         string  `json:"state"`
		Draft         bool    `json:"draft"`
		CreatedAt     string  `json:"created_at"`
		Merged        bool    `json:"merged"`
		MergedAt      *string `json:"merged_at"`
		MergeCommitID string  `json:"merge_commit_sha"`
		Base          struct {
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
		Merged: decoded.Merged, MergeCommitID: strings.TrimSpace(decoded.MergeCommitID),
	}
	if decoded.MergedAt != nil {
		mergedAt, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(*decoded.MergedAt))
		if err != nil {
			return workspace.PullRequest{}, fmt.Errorf(
				"%w: pull request response has an invalid merged time",
				project.ErrForgejoUnavailable,
			)
		}
		pullRequest.MergedAt = &mergedAt
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

func validatePlanPullRequest(
	pullRequest workspace.PullRequest,
	spec workspace.PlanPublicationSpec,
) error {
	if pullRequest.Number != spec.Number || pullRequest.State != "open" ||
		!pullRequest.Draft || pullRequest.BaseBranch != spec.BaseBranch ||
		pullRequest.HeadBranch != spec.HeadBranch ||
		pullRequest.HeadCommitID != spec.HeadCommitID ||
		!strings.Contains(pullRequest.Body, spec.FeatureMarker) {
		return workspace.ErrPullRequestConflict
	}
	return nil
}
