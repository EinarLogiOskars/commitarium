package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/conversations"
	"github.com/EinarLogiOskars/commitarium/internal/featureartifact"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
)

// answeringWorker answers every turn at once, proposing actions.
type answeringWorker struct {
	chatWorker
	actions []featureartifact.AssistantAction
}

func (worker answeringWorker) GetAttempt(_ context.Context, reference workerhttp.AttemptReference) (workerhttp.Attempt, error) {
	return workerhttp.Attempt{AttemptReference: reference, State: workerhttp.AttemptStateTerminal, Result: &workerhttp.TerminalResult{
		Outcome: workerhttp.OutcomeCompleted, Disposition: workerhttp.DispositionSucceeded,
		Summary: "A bank importer is a real project.", AssistantTurn: &featureartifact.AssistantTurn{Actions: worker.actions},
	}}, nil
}

// wakeRecorder is the assistant, recording what each project's assistant is
// woken with.
type wakeRecorder struct {
	*conversations.Service
	woken map[string]string
}

func (recorder *wakeRecorder) Wake(_ context.Context, scope, _, text string) error {
	recorder.woken[scope] = text
	return nil
}

type looseChats struct {
	service  *wakeRecorder
	projects *recordingProjectService
	visions  *planningVisions
	handler  http.Handler
}

// newLooseChats is the API on a real assistant whose turns answer at once
// with actions, and whose new projects are prj_bank.
func newLooseChats(t *testing.T, actions ...featureartifact.AssistantAction) looseChats {
	t.Helper()
	projects := &recordingProjectService{result: project.Project{ID: "prj_bank", Name: "Bank import"},
		getByIDResult: project.Project{ID: "prj_bank", Name: "Bank import"}}
	agents := testAgents()
	service, err := conversations.NewService(conversations.Config{
		Root: t.TempDir(), WorkspaceRoot: t.TempDir(), Projects: projects, Agents: agents,
		Describer: chatDescriber{},
		Workers:   func(string) (workerhttp.Service, error) { return answeringWorker{actions: actions}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.SaveSettings(t.Context(), conversations.Settings{Agent: "codex", Model: "gpt-6-luna"}); err != nil {
		t.Fatal(err)
	}
	recorder := &wakeRecorder{Service: service, woken: map[string]string{}}
	visions := &planningVisions{}
	// New apps are made through the same projects.
	apps := appProjects{&assistantModeProjects{recordingProjectService: projects}}
	handler := NewWithWorkspaceRealWorkflowDeletionAndModels(apps, &recordingFeatureService{}, nil, nil, nil, nil,
		nil, nil, nil, nil, nil, nil, recorder, agents, visions)
	return looseChats{service: recorder, projects: projects, visions: visions, handler: handler}
}

func (loose looseChats) call(t *testing.T, method, path, body string) (int, conversations.Conversation, string) {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if strings.HasSuffix(path, "/messages") {
		request.Header.Set("Idempotency-Key", path+body)
	}
	recorder := httptest.NewRecorder()
	loose.handler.ServeHTTP(recorder, request)
	var conversation conversations.Conversation
	_ = json.Unmarshal(recorder.Body.Bytes(), &conversation)
	return recorder.Code, conversation, recorder.Body.String()
}

// talk starts a loose chat and says something in it, answering its chat.
func (loose looseChats) talk(t *testing.T, message string) string {
	t.Helper()
	const top = "/api/v1/assistant/conversations/" + conversations.TopLevel
	code, created, body := loose.call(t, http.MethodPost, top+"/chats", "")
	if code != http.StatusCreated {
		t.Fatalf("create chat: %d %s", code, body)
	}
	if code, _, body := loose.call(t, http.MethodPost, top+"/chats/"+created.Chat+"/messages",
		`{"message":`+quote(message)+`}`); code != http.StatusOK && code != http.StatusAccepted {
		t.Fatalf("send: %d %s", code, body)
	}
	if code, answered, body := loose.call(t, http.MethodGet, top+"/chats/"+created.Chat, ""); code != http.StatusOK ||
		answered.Status != conversations.StatusIdle {
		t.Fatalf("get: %d %s", code, body)
	}
	return created.Chat
}

func quote(text string) string {
	encoded, _ := json.Marshal(text)
	return string(encoded)
}

// "Make it a project" makes a loose chat a project only in Developer mode:
// then the project is created, the chat moves into it as its first chat,
// and its assistant is woken to plan it with the chat's transcript.
func TestALooseChatBecomesAProject(t *testing.T) {
	loose := newLooseChats(t)
	chat := loose.talk(t, "Could something read my bank's CSVs every night?")
	route := "/api/v1/assistant/conversations/commitarium/chats/" + chat + "/project"

	if code, _, body := loose.call(t, http.MethodPost, route, `{"name":"Bank import"}`); code != http.StatusConflict ||
		!strings.Contains(body, "developer_mode_required") || loose.projects.receivedName != "" {
		t.Fatalf("a project was made outside Developer mode: %d %s", code, body)
	}
	if err := loose.service.SetDeveloperMode(t.Context(), conversations.TopLevel, true); err != nil {
		t.Fatal(err)
	}
	code, moved, body := loose.call(t, http.MethodPost, route, `{"name":"Bank import"}`)
	if code != http.StatusCreated {
		t.Fatalf("status %d: %s", code, body)
	}
	if loose.projects.receivedName != "Bank import" || len(loose.visions.goals) != 1 || loose.visions.goals[0] != "Bank import" {
		t.Fatalf("name=%q goals=%v", loose.projects.receivedName, loose.visions.goals)
	}
	if moved.Scope != "prj_bank" || moved.Chat != conversations.MainChat || len(moved.Messages) < 2 ||
		moved.Messages[0].Text != "Could something read my bank's CSVs every night?" {
		t.Fatalf("moved = %s", body)
	}
	woken := loose.service.woken["prj_bank"]
	if !strings.Contains(woken, "plan this project with you") || !strings.Contains(woken, "grew from the user's chat") ||
		!strings.Contains(woken, "user: Could something read my bank's CSVs every night?") ||
		!strings.Contains(woken, "assistant: A bank importer is a real project.") {
		t.Fatalf("the assistant was woken with %q", woken)
	}
	if left, err := loose.service.Chats(t.Context(), conversations.TopLevel, true); err != nil || len(left) != 0 {
		t.Fatalf("the top level kept %+v err=%v", left, err)
	}
	if code, _, _ := loose.call(t, http.MethodPost, route, `{"name":"Bank import"}`); code != http.StatusNotFound {
		t.Fatalf("a moved chat moved again: %d", code)
	}
}

// Approving a create_app card in a loose chat, in Developer mode, moves the
// chat into the new app as its chat, with its applied card; the app's
// launch is told there and its vision is written from the brief.
func TestApprovingCreateAppMovesTheChat(t *testing.T) {
	loose := newLooseChats(t, featureartifact.AssistantAction{
		Type: featureartifact.AssistantCreateApp, Reason: "Clear and small", Title: "Pep Talk",
		Description: "A random motivational quote each time it opens.", AppBrief: "About 150 bundled quotes.",
	})
	if err := loose.service.SetDeveloperMode(t.Context(), conversations.TopLevel, true); err != nil {
		t.Fatal(err)
	}
	chat := loose.talk(t, "A tiny app that gives a motivational quote every time it opens?")
	_, conversation, _ := loose.call(t, http.MethodGet, "/api/v1/assistant/conversations/commitarium/chats/"+chat, "")
	if len(conversation.Actions) != 1 {
		t.Fatalf("actions = %+v", conversation.Actions)
	}
	code, moved, body := loose.call(t, http.MethodPost,
		"/api/v1/assistant/conversations/commitarium/actions/"+conversation.Actions[0].ID+"/approve", `{}`)
	if code != http.StatusOK {
		t.Fatalf("status %d: %s", code, body)
	}
	if moved.Scope != "prj_bank" || len(moved.Actions) != 1 || moved.Actions[0].Status != conversations.ActionApplied ||
		moved.Messages[0].Text != "A tiny app that gives a motivational quote every time it opens?" {
		t.Fatalf("moved = %s", body)
	}
	if moved.Launch == nil || !strings.Contains(moved.Notes, "About 150 bundled quotes.") {
		t.Fatalf("the moved chat has launch %+v and notes %q", moved.Launch, moved.Notes)
	}
	if len(loose.visions.described) != 1 || !strings.Contains(loose.visions.described[0], "About 150 bundled quotes.") {
		t.Fatalf("the vision was described as %q", loose.visions.described)
	}
	if chats, _ := loose.service.Chats(t.Context(), "prj_bank", true); len(chats) != 1 {
		t.Fatalf("the app has chats %+v", chats)
	}
	if left, _ := loose.service.Chats(t.Context(), conversations.TopLevel, true); len(left) != 0 {
		t.Fatalf("the top level kept %+v", left)
	}
}

// Approving a plan_project card in a loose chat does the same: the chat,
// with its applied card, moves into the new project, whose assistant hears
// the brief in its notes and the chat as planning starts.
func TestApprovingPlanProjectMovesTheChat(t *testing.T) {
	loose := newLooseChats(t, featureartifact.AssistantAction{
		Type: featureartifact.AssistantPlanProject, Reason: "A real project", Title: "Bank import",
		Description: "Reads the bank's CSV exports every night.", AppBrief: "Landsbankinn exports, nightly.",
	})
	if err := loose.service.SetDeveloperMode(t.Context(), conversations.TopLevel, true); err != nil {
		t.Fatal(err)
	}
	chat := loose.talk(t, "Could something read my bank's CSVs every night?")
	_, conversation, _ := loose.call(t, http.MethodGet, "/api/v1/assistant/conversations/commitarium/chats/"+chat, "")
	if len(conversation.Actions) != 1 {
		t.Fatalf("actions = %+v", conversation.Actions)
	}
	actionID := conversation.Actions[0].ID
	code, moved, body := loose.call(t, http.MethodPost,
		"/api/v1/assistant/conversations/commitarium/actions/"+actionID+"/approve", `{}`)
	if code != http.StatusOK {
		t.Fatalf("status %d: %s", code, body)
	}
	if moved.Scope != "prj_bank" || len(moved.Actions) != 1 || moved.Actions[0].Status != conversations.ActionApplied ||
		moved.Actions[0].ProjectID != "prj_bank" {
		t.Fatalf("moved = %s", body)
	}
	if moved.Notes == "" || !strings.Contains(moved.Notes, "Landsbankinn exports, nightly.") {
		t.Fatalf("notes = %q", moved.Notes)
	}
	if woken := loose.service.woken["prj_bank"]; !strings.Contains(woken, "user: Could something read my bank's CSVs every night?") {
		t.Fatalf("the assistant was woken with %q", woken)
	}
	if left, _ := loose.service.Chats(t.Context(), conversations.TopLevel, true); len(left) != 0 {
		t.Fatalf("the top level kept %+v", left)
	}
}
