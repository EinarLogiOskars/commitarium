package forgejo

import (
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
)

const maxRepositoryResponseBytes = 512 * 1024

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
	tokenBytes, err := os.ReadFile(client.tokenFile)
	if err != nil {
		return project.ForgejoRepository{}, fmt.Errorf("%w: access token file cannot be read", project.ErrForgejoUnavailable)
	}
	token := strings.TrimSpace(string(tokenBytes))
	if token == "" {
		return project.ForgejoRepository{}, fmt.Errorf("%w: access token file is empty", project.ErrForgejoUnavailable)
	}

	requestContext, cancel := context.WithTimeout(ctx, client.requestTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(
		requestContext,
		http.MethodGet,
		client.baseURL+"/api/v1/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(name),
		nil,
	)
	if err != nil {
		return project.ForgejoRepository{}, fmt.Errorf("create Forgejo repository request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "token "+token)
	response, err := client.httpClient.Do(request)
	if err != nil {
		return project.ForgejoRepository{}, fmt.Errorf("%w: repository request failed: %v", project.ErrForgejoUnavailable, err)
	}
	defer response.Body.Close()

	switch response.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return project.ForgejoRepository{}, project.ErrForgejoRepositoryNotFound
	default:
		return project.ForgejoRepository{}, fmt.Errorf(
			"%w: repository request returned HTTP %d", project.ErrForgejoUnavailable, response.StatusCode,
		)
	}
	limited := io.LimitReader(response.Body, maxRepositoryResponseBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return project.ForgejoRepository{}, fmt.Errorf("%w: read repository response", project.ErrForgejoUnavailable)
	}
	if len(body) > maxRepositoryResponseBytes {
		return project.ForgejoRepository{}, fmt.Errorf("%w: repository response is too large", project.ErrForgejoUnavailable)
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
