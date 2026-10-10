package agentworker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/agent"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
)

func newTestRegistry(t *testing.T, template string) (*Registry, string) {
	t.Helper()
	dir := t.TempDir()
	registry, err := New(Config{
		URLTemplate: template, TokenDir: dir,
		RequestTimeout: time.Second, AttemptStartTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("create registry: %v", err)
	}
	return registry, dir
}

func writeToken(t *testing.T, dir, agentID, token string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, TokenFileName(agentID)), []byte(token+"\n"), 0o600); err != nil {
		t.Fatalf("write token: %v", err)
	}
}

func TestRegistryReadsAgentTokensOnDemand(t *testing.T) {
	registry, dir := newTestRegistry(t, "")
	if _, err := registry.Client("claude-max"); !errors.Is(err, ErrUnknownAgent) {
		t.Fatalf("missing token error = %v", err)
	}
	if _, err := registry.Client("../escape"); !errors.Is(err, ErrUnknownAgent) {
		t.Fatalf("invalid agent ID error = %v", err)
	}

	writeToken(t, dir, "claude-max", "first")
	first, err := registry.Client("claude-max")
	if err != nil {
		t.Fatalf("client after token appeared: %v", err)
	}
	if again, _ := registry.Client("claude-max"); again != first {
		t.Fatal("expected the cached client while the token is unchanged")
	}
	writeToken(t, dir, "claude-max", "rotated")
	if rotated, _ := registry.Client("claude-max"); rotated == first {
		t.Fatal("expected a new client after the token rotated")
	}
}

func TestRegistryRejectsTemplatesWithoutOneAgentPlaceholder(t *testing.T) {
	for _, template := range []string{"http://worker:8081", "http://{agent}-{agent}:8081"} {
		if _, err := New(Config{URLTemplate: template, TokenDir: t.TempDir()}); err == nil {
			t.Fatalf("expected template %q to be rejected", template)
		}
	}
	if _, err := New(Config{TokenDir: "relative"}); err == nil {
		t.Fatal("expected a relative token directory to be rejected")
	}
}

type agentList []agent.Agent

func (list agentList) List(context.Context) ([]agent.Agent, error) { return list, nil }

type modelStub struct {
	response workerhttp.ModelsResponse
	err      error
}

func (stub modelStub) Models(context.Context) (workerhttp.ModelsResponse, error) {
	return stub.response, stub.err
}

func TestModelSourceAsksTheProvidersAgentWorkers(t *testing.T) {
	var asked []string
	workers := map[string]ModelLister{
		"claude-down": modelStub{err: errors.New("unavailable")},
		"claude":      modelStub{response: workerhttp.ModelsResponse{Provider: workerhttp.ProviderClaudeCode}},
	}
	source := ModelSource{
		Provider: project.AgentProviderClaude,
		Agents: agentList{
			{ID: "codex", Provider: project.AgentProviderCodex},
			{ID: "claude-down", Provider: project.AgentProviderClaude},
			{ID: "claude", Provider: project.AgentProviderClaude},
		},
		Workers: func(agentID string) (ModelLister, error) {
			asked = append(asked, agentID)
			return workers[agentID], nil
		},
	}
	response, err := source.Models(t.Context())
	if err != nil || response.Provider != workerhttp.ProviderClaudeCode {
		t.Fatalf("models = %+v, %v", response, err)
	}
	if strings.Join(asked, ",") != "claude-down,claude" {
		t.Fatalf("asked workers %v", asked)
	}

	source.Agents = agentList{{ID: "codex", Provider: project.AgentProviderCodex}}
	if _, err := source.Models(t.Context()); !errors.Is(err, ErrUnknownAgent) {
		t.Fatalf("no claude agent error = %v", err)
	}
}
