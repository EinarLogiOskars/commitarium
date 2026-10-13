package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type stubAgentUsage []AgentUsage

func (stub stubAgentUsage) AgentUsage(context.Context) ([]AgentUsage, error) {
	return stub, nil
}

func TestAgentUsageListsNoWindowsAsEmpty(t *testing.T) {
	for name, reported := range map[string]stubAgentUsage{
		"no agents":      nil,
		"agent not used": {{Agent: "codex"}},
	} {
		t.Run(name, func(t *testing.T) {
			api := &API{agentUsage: reported}
			recorder := httptest.NewRecorder()
			api.agentUsageHandler(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/agents/usage", nil))
			body := recorder.Body.String()
			if recorder.Code != http.StatusOK || strings.Contains(body, "null") {
				t.Fatalf("usage = %d %s, want lists without null", recorder.Code, body)
			}
		})
	}
}
