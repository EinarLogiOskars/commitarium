package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/conversations"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
)

// chatWorker takes every turn and leaves it running.
type chatWorker struct{}

func (chatWorker) PutAttempt(_ context.Context, identity workerhttp.MutationIdentity, request workerhttp.PutAttemptRequest) (workerhttp.Attempt, bool, error) {
	return workerhttp.Attempt{
		AttemptReference: identity.AttemptReference, Mode: request.Mode, Assignment: request.Assignment,
		State: workerhttp.AttemptStateRunning,
	}, true, nil
}

func (chatWorker) GetAttempt(_ context.Context, reference workerhttp.AttemptReference) (workerhttp.Attempt, error) {
	return workerhttp.Attempt{AttemptReference: reference, State: workerhttp.AttemptStateRunning}, nil
}

func (chatWorker) SendCommand(context.Context, workerhttp.MutationIdentity, workerhttp.CommandRequest) (workerhttp.Attempt, error) {
	panic("unexpected command")
}

func (chatWorker) ForceStop(context.Context, workerhttp.MutationIdentity, workerhttp.ForceStopRequest) (workerhttp.Attempt, error) {
	panic("unexpected force stop")
}

func (chatWorker) Supersede(context.Context, workerhttp.MutationIdentity, workerhttp.SupersedeRequest) (workerhttp.Attempt, error) {
	panic("unexpected supersede")
}

type chatDescriber struct{}

func (chatDescriber) Describe(context.Context, string) (string, error) { return "## This project", nil }

// The chat routes list, start, rename, and archive a project's chats and
// talk in one of them, while the scope's own routes keep working on the
// most recently used chat.
func TestTheScopesRoutesActOnTheMostRecentChat(t *testing.T) {
	projects := &recordingProjectService{getByIDResult: project.Project{ID: "prj_test"}}
	agents := testAgents()
	service, err := conversations.NewService(conversations.Config{
		Root: t.TempDir(), WorkspaceRoot: t.TempDir(), Projects: projects, Agents: agents,
		Describer: chatDescriber{},
		Workers:   func(string) (workerhttp.Service, error) { return chatWorker{}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.SaveSettings(t.Context(), conversations.Settings{Agent: "codex", Model: "gpt-6-luna"}); err != nil {
		t.Fatal(err)
	}
	handler := NewWithWorkspaceRealWorkflowDeletionAndModels(projects, &recordingFeatureService{}, nil, nil, nil, nil,
		nil, nil, nil, nil, nil, nil, service, agents)
	const scope = "/api/v1/assistant/conversations/prj_test"
	call := func(method, path, body string) (int, map[string]any) {
		t.Helper()
		request := httptest.NewRequest(method, path, strings.NewReader(body))
		if method == http.MethodPost && strings.HasSuffix(path, "/messages") {
			request.Header.Set("Idempotency-Key", path+body)
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		answer := map[string]any{}
		_ = json.Unmarshal(recorder.Body.Bytes(), &answer)
		return recorder.Code, answer
	}

	code, first := call(http.MethodPost, scope+"/chats", `{"title":"Stack"}`)
	if code != http.StatusCreated || first["chat"] != conversations.MainChat || first["title"] != "Stack" {
		t.Fatalf("create = %d %v", code, first)
	}
	code, second := call(http.MethodPost, scope+"/chats", "")
	if code != http.StatusCreated || second["chat"] == conversations.MainChat {
		t.Fatalf("create = %d %v", code, second)
	}
	other := second["chat"].(string)
	// The newest chat is the most recently used: the old routes act on it.
	if code, got := call(http.MethodGet, scope, ""); code != http.StatusOK || got["chat"] != other {
		t.Fatalf("get = %d %v", code, got)
	}
	if code, got := call(http.MethodPost, scope+"/messages", `{"message":"Hello there"}`); code != http.StatusAccepted ||
		got["chat"] != other || got["status"] != "running" || got["title"] != "Hello there" {
		t.Fatalf("send = %d %v", code, got)
	}
	// Writing in a chat makes it the most recently used.
	if code, got := call(http.MethodPost, scope+"/chats/main/messages", `{"message":"Which database?"}`); code != http.StatusAccepted ||
		got["chat"] != conversations.MainChat {
		t.Fatalf("send in a chat = %d %v", code, got)
	}
	if code, got := call(http.MethodGet, scope, ""); code != http.StatusOK || got["chat"] != conversations.MainChat {
		t.Fatalf("get = %d %v", code, got)
	}
	if code, got := call(http.MethodGet, scope+"/chats/"+other, ""); code != http.StatusOK ||
		len(got["messages"].([]any)) != 1 {
		t.Fatalf("get a chat = %d %v", code, got)
	}
	if code, got := call(http.MethodPatch, scope+"/chats/"+other, `{"title":"Greetings"}`); code != http.StatusOK ||
		got["title"] != "Greetings" {
		t.Fatalf("rename = %d %v", code, got)
	}
	if code, got := call(http.MethodDelete, scope+"/chats/main", ""); code != http.StatusOK || got["archived"] != true {
		t.Fatalf("archive = %d %v", code, got)
	}
	if code, got := call(http.MethodGet, scope, ""); code != http.StatusOK || got["chat"] != other {
		t.Fatalf("after archiving, get = %d %v", code, got)
	}
	if code, got := call(http.MethodGet, scope+"/chats", ""); code != http.StatusOK || len(got["chats"].([]any)) != 1 {
		t.Fatalf("list = %d %v", code, got)
	}
	if code, got := call(http.MethodGet, scope+"/chats?archived=true", ""); code != http.StatusOK || len(got["chats"].([]any)) != 2 {
		t.Fatalf("list with the archived = %d %v", code, got)
	}
	if code, _ := call(http.MethodGet, scope+"/chats/chat_0000000000000000", ""); code != http.StatusNotFound {
		t.Fatalf("a missing chat answered %d", code)
	}
	if code, _ := call(http.MethodPost, scope+"/chats/chat_0000000000000000/messages", `{"message":"Hi"}`); code != http.StatusNotFound {
		t.Fatalf("a message to a missing chat answered %d", code)
	}
}
