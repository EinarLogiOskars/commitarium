package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/toolchain"
)

type toolchainAssistantStub struct {
	session  toolchain.AssistantSession
	manifest toolchain.Manifest
	err      error
	message  string
	purpose  toolchain.AssistantPurpose
}

func (stub *toolchainAssistantStub) Start(_ context.Context, _ string, _ project.AgentProvider, _, _ string, purpose toolchain.AssistantPurpose, _ string) (toolchain.AssistantSession, bool, error) {
	stub.purpose = purpose
	return stub.session, true, stub.err
}
func (stub *toolchainAssistantStub) Get(context.Context, string, string) (toolchain.AssistantSession, error) {
	return stub.session, stub.err
}
func (stub *toolchainAssistantStub) Reply(_ context.Context, _, _, message, _ string) (toolchain.AssistantSession, bool, error) {
	stub.message = message
	return stub.session, true, stub.err
}
func (stub *toolchainAssistantStub) Apply(context.Context, string, string) (toolchain.Manifest, error) {
	return stub.manifest, stub.err
}

func TestToolchainAssistantHTTPWorkflow(t *testing.T) {
	stub := &toolchainAssistantStub{session: toolchain.AssistantSession{ID: "tcs_test", ProjectID: "prj_test",
		Provider: project.AgentProviderCodex, Model: "gpt-5.6-sol", Status: toolchain.AssistantStatusRunning},
		manifest: toolchain.Manifest{ProjectID: "prj_test", Status: toolchain.StatusConfigured,
			Source: toolchain.SourceAssistant, Tools: map[string]string{"python": "3.14.7"}, Services: []string{}}}
	handler := newAPI(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, stub)

	start := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/projects/prj_test/toolchain/assistant-sessions",
		strings.NewReader(`{"provider":"codex","model":"gpt-5.6-sol","message":"Verify this","purpose":"verify_repository"}`))
	request.Header.Set("Idempotency-Key", "setup-1")
	handler.ServeHTTP(start, request)
	if start.Code != http.StatusAccepted || start.Header().Get("Location") == "" ||
		stub.purpose != toolchain.AssistantPurposeVerifyRepository {
		t.Fatalf("start status=%d body=%s", start.Code, start.Body.String())
	}

	get := httptest.NewRecorder()
	handler.ServeHTTP(get, httptest.NewRequest(http.MethodGet,
		"/api/v1/projects/prj_test/toolchain/assistant-sessions/tcs_test", nil))
	if get.Code != http.StatusOK {
		t.Fatalf("get status=%d body=%s", get.Code, get.Body.String())
	}

	reply := httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost,
		"/api/v1/projects/prj_test/toolchain/assistant-sessions/tcs_test/messages", strings.NewReader(`{"message":"API only"}`))
	request.Header.Set("Idempotency-Key", "reply-1")
	handler.ServeHTTP(reply, request)
	if reply.Code != http.StatusAccepted || stub.message != "API only" {
		t.Fatalf("reply status=%d body=%s", reply.Code, reply.Body.String())
	}

	apply := httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost,
		"/api/v1/projects/prj_test/toolchain/assistant-sessions/tcs_test/apply", nil)
	request.Header.Set("Idempotency-Key", "apply-1")
	handler.ServeHTTP(apply, request)
	if apply.Code != http.StatusOK || !strings.Contains(apply.Body.String(), `"source":"assistant"`) {
		t.Fatalf("apply status=%d body=%s", apply.Code, apply.Body.String())
	}
}

func TestToolchainAssistantRejectsUnknownPurposeAndMapsStaleVerification(t *testing.T) {
	stub := &toolchainAssistantStub{}
	handler := newAPI(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, stub)

	request := httptest.NewRequest(http.MethodPost, "/api/v1/projects/prj_test/toolchain/assistant-sessions",
		strings.NewReader(`{"provider":"codex","model":"gpt-5.6-sol","message":"Verify","purpose":"unsafe"}`))
	request.Header.Set("Idempotency-Key", "verify-1")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "invalid_toolchain_assistant_request") {
		t.Fatalf("invalid purpose status=%d body=%s", response.Code, response.Body.String())
	}

	stub.err = toolchain.ErrAssistantStale
	request = httptest.NewRequest(http.MethodPost,
		"/api/v1/projects/prj_test/toolchain/assistant-sessions/tcs_test/apply", nil)
	request.Header.Set("Idempotency-Key", "apply-1")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "toolchain_assistant_stale") {
		t.Fatalf("stale apply status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestToolchainAssistantMutationsRequireIdempotencyKeys(t *testing.T) {
	handler := newAPI(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, &toolchainAssistantStub{})
	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodPost, "/api/v1/projects/prj/toolchain/assistant-sessions", strings.NewReader(`{}`)),
		httptest.NewRequest(http.MethodPost, "/api/v1/projects/prj/toolchain/assistant-sessions/tcs/messages", strings.NewReader(`{}`)),
		httptest.NewRequest(http.MethodPost, "/api/v1/projects/prj/toolchain/assistant-sessions/tcs/apply", nil),
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "idempotency_key_required") {
			t.Fatalf("%s returned %d: %s", request.URL.Path, response.Code, response.Body.String())
		}
	}
}
