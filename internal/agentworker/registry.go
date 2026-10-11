// Package agentworker addresses agent workers by convention (ADR-016): the
// worker for agent "<id>" is the Compose service "agent-<id>-worker", and its
// bearer token is the file "agent-<id>-worker-token" in a shared directory.
// Tokens are read on demand, so agents added after the coordinator started
// need no restart.
package agentworker

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/agent"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/secretfile"
	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
)

// AgentPlaceholder marks where the agent ID goes in a worker URL template.
const AgentPlaceholder = "{agent}"

// DefaultURLTemplate is the worker address inside the Compose network.
const DefaultURLTemplate = "http://agent-" + AgentPlaceholder + "-worker:8081"

var ErrUnknownAgent = errors.New("agent worker is not available")

// ServiceName is the Compose service that runs an agent's worker.
func ServiceName(agentID string) string { return "agent-" + agentID + "-worker" }

// TokenFileName is the bearer token file for an agent's worker.
func TokenFileName(agentID string) string { return ServiceName(agentID) + "-token" }

type Config struct {
	URLTemplate         string
	TokenDir            string
	RequestTimeout      time.Duration
	AttemptStartTimeout time.Duration
}

type Registry struct {
	config  Config
	mu      sync.Mutex
	clients map[string]cachedClient
}

type cachedClient struct {
	token  string
	client *workerhttp.Client
}

func New(config Config) (*Registry, error) {
	if strings.TrimSpace(config.URLTemplate) == "" {
		config.URLTemplate = DefaultURLTemplate
	}
	if strings.Count(config.URLTemplate, AgentPlaceholder) != 1 {
		return nil, fmt.Errorf("agent worker URL template must contain %s exactly once", AgentPlaceholder)
	}
	if !filepath.IsAbs(config.TokenDir) {
		return nil, errors.New("agent worker token directory must be an absolute path")
	}
	return &Registry{config: config, clients: make(map[string]cachedClient)}, nil
}

// Client returns the worker client for an agent. The token file is read on
// every call so a token written or rotated by the desktop takes effect without
// a coordinator restart; the client is rebuilt only when the token changes.
func (registry *Registry) Client(agentID string) (*workerhttp.Client, error) {
	if !agent.ValidID(agentID) {
		return nil, fmt.Errorf("%w: invalid agent ID %q", ErrUnknownAgent, agentID)
	}
	token, err := secretfile.Read(
		filepath.Join(registry.config.TokenDir, TokenFileName(agentID)),
		fmt.Sprintf("agent %q worker API token", agentID),
	)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnknownAgent, err)
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if cached, ok := registry.clients[agentID]; ok && cached.token == token {
		return cached.client, nil
	}
	client, err := workerhttp.NewClient(workerhttp.ClientConfig{
		BaseURL:             strings.Replace(registry.config.URLTemplate, AgentPlaceholder, agentID, 1),
		BearerToken:         token,
		RequestTimeout:      registry.config.RequestTimeout,
		AttemptStartTimeout: registry.config.AttemptStartTimeout,
	})
	if err != nil {
		return nil, fmt.Errorf("create agent %q worker client: %w", agentID, err)
	}
	registry.clients[agentID] = cachedClient{token: token, client: client}
	return client, nil
}

// AgentLister lists the configured agents.
type AgentLister interface {
	List(context.Context) ([]agent.Agent, error)
}

// ModelLister is the part of a worker client that lists models.
type ModelLister interface {
	Models(context.Context) (workerhttp.ModelsResponse, error)
}

// ModelSource answers a provider's model catalog from the first of its agents
// whose worker responds. Models depend on the provider, not the account.
type ModelSource struct {
	Workers  func(agentID string) (ModelLister, error)
	Agents   AgentLister
	Provider project.AgentProvider
}

// Models adapts the registry for ModelSource.Workers.
func (registry *Registry) Models(agentID string) (ModelLister, error) {
	client, err := registry.Client(agentID)
	if err != nil {
		return nil, err
	}
	return client, nil
}

func (source ModelSource) Models(ctx context.Context) (workerhttp.ModelsResponse, error) {
	agents, err := source.Agents.List(ctx)
	if err != nil {
		return workerhttp.ModelsResponse{}, err
	}
	var failures []error
	for _, candidate := range agents {
		if candidate.Provider != source.Provider {
			continue
		}
		client, err := source.Workers(candidate.ID)
		if err == nil {
			var response workerhttp.ModelsResponse
			if response, err = client.Models(ctx); err == nil {
				return response, nil
			}
		}
		failures = append(failures, fmt.Errorf("agent %q: %w", candidate.ID, err))
	}
	if len(failures) == 0 {
		return workerhttp.ModelsResponse{}, fmt.Errorf("%w: no %s agent is configured", ErrUnknownAgent, source.Provider)
	}
	return workerhttp.ModelsResponse{}, errors.Join(failures...)
}
