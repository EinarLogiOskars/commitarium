package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/conversations"
	"github.com/EinarLogiOskars/commitarium/internal/featureartifact"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/vision"
)

// helperConversations knows the helpers the assistant started and records
// the one an applied action starts.
type helperConversations struct {
	*conversationStub
	helpers map[string]conversations.Helper
	started *conversations.Helper
}

func (stub *helperConversations) LookUpHelper(_ context.Context, _, id string) (conversations.Helper, error) {
	if helper, ok := stub.helpers[id]; ok {
		return helper, nil
	}
	return conversations.Helper{}, conversations.ErrNotFound
}

func (stub *helperConversations) Resolve(ctx context.Context, scope, actionID string, status conversations.ActionStatus,
	result string, created conversations.Created) (conversations.Conversation, error) {
	stub.started = created.Helper
	return stub.conversationStub.Resolve(ctx, scope, actionID, status, result, created)
}

// helperVisions is a vision writer whose one session waits for an answer.
type helperVisions struct {
	VisionService
	session  vision.Session
	replies  []string
	started  []vision.Mode
	notFound bool
}

func (stub *helperVisions) Get(context.Context, string) (vision.Vision, error) {
	return vision.Vision{}, vision.ErrNotFound
}

func (stub *helperVisions) Start(_ context.Context, _, _, _ string, mode vision.Mode, _, _ string) (vision.Session, bool, error) {
	stub.started = append(stub.started, mode)
	return stub.session, true, nil
}

func (stub *helperVisions) GetSession(_ context.Context, _, id string) (vision.Session, error) {
	if stub.notFound || id != stub.session.ID {
		return vision.Session{}, vision.ErrNotFound
	}
	return stub.session, nil
}

func (stub *helperVisions) Reply(_ context.Context, _, id, message, _ string) (vision.Session, bool, error) {
	if id != stub.session.ID {
		return vision.Session{}, false, vision.ErrNotFound
	}
	stub.replies = append(stub.replies, message)
	return stub.session, true, nil
}

func waitingVision() *helperVisions {
	return &helperVisions{session: vision.Session{ID: "vis_1", ProjectID: "prj_test", Status: vision.StatusWaitingForUser,
		Messages: []vision.Message{{Role: "user", Text: "A meal planner."}, {Role: "assistant", Text: "For families?"}}}}
}

// write_vision records the vision writer as the assistant's helper.
func TestWritingTheVisionStartsAHelper(t *testing.T) {
	visions := waitingVision()
	stub := &helperConversations{conversationStub: &conversationStub{
		settings: conversations.Settings{Agent: "codex", Model: "gpt-6-luna"},
		action: conversations.Action{ID: "act_1", Status: conversations.ActionProposed, AssistantAction: featureartifact.AssistantAction{
			Type: featureartifact.AssistantWriteVision, Reason: "No vision", Description: "A meal planner.",
		}},
	}}
	projects := &recordingProjectService{getByIDResult: project.Project{ID: "prj_test"}}
	handler := NewWithWorkspaceRealWorkflowDeletionAndModels(projects, &recordingFeatureService{}, nil, nil, nil, nil, nil, nil, nil,
		nil, nil, nil, stub, visions, testAgents())
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost,
		"/api/v1/assistant/conversations/prj_test/actions/act_1/approve", strings.NewReader(`{}`)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d: %s", recorder.Code, recorder.Body.String())
	}
	if stub.started == nil || stub.started.ID != "vis_1" || stub.started.Kind != conversations.HelperVision ||
		len(visions.started) != 1 || visions.started[0] != vision.ModeDescribe {
		t.Fatalf("helper = %+v, started %v", stub.started, visions.started)
	}
}

// answer_helper replies to a helper the assistant started through the
// helper's own endpoint; a helper it did not start is refused.
func TestAnsweringAHelperRepliesToIt(t *testing.T) {
	for name, test := range map[string]struct {
		helper string
		code   int
	}{
		"its own helper":    {"vis_1", http.StatusOK},
		"the user's helper": {"vis_users", http.StatusNotFound},
	} {
		t.Run(name, func(t *testing.T) {
			visions := waitingVision()
			stub := &helperConversations{
				conversationStub: &conversationStub{action: conversations.Action{
					ID: "act_1", Status: conversations.ActionProposed, AssistantAction: featureartifact.AssistantAction{
						Type: featureartifact.AssistantAnswerHelper, Reason: "The vision says so", Helper: test.helper,
						Guidance: "Families of four.",
					},
				}},
				helpers: map[string]conversations.Helper{"vis_1": {ID: "vis_1", Kind: conversations.HelperVision}},
			}
			projects := &recordingProjectService{getByIDResult: project.Project{ID: "prj_test"}}
			handler := NewWithWorkspaceRealWorkflowDeletionAndModels(projects, &recordingFeatureService{}, nil, nil, nil, nil, nil,
				nil, nil, nil, nil, nil, stub, visions)
			if err := AssistantActionApplier(handler)(t.Context(), "prj_test", "act_1"); (err == nil) != (test.code == http.StatusOK) {
				t.Fatalf("apply: %v", err)
			}
			if test.code != http.StatusOK {
				if len(visions.replies) != 0 {
					t.Fatalf("answered a helper the assistant did not start: %v", visions.replies)
				}
				return
			}
			if len(visions.replies) != 1 || visions.replies[0] != "Families of four." ||
				stub.resolvedResult != "Answered the vision writer." {
				t.Fatalf("replies = %v, result = %q", visions.replies, stub.resolvedResult)
			}
		})
	}
}

// The watcher reads a helper's question through its endpoint; a helper
// whose session is gone is finished.
func TestTheHelperReaderReadsAQuestion(t *testing.T) {
	visions := waitingVision()
	projects := &recordingProjectService{getByIDResult: project.Project{ID: "prj_test"}}
	read := HelperReader(NewWithWorkspaceRealWorkflowDeletionAndModels(projects, &recordingFeatureService{}, nil, nil, nil, nil,
		nil, nil, nil, nil, nil, nil, visions))
	helper := conversations.Helper{ID: "vis_1", Kind: conversations.HelperVision}
	state, err := read(t.Context(), "prj_test", helper)
	if err != nil || state.Status != conversations.HelperAsks || state.Text != "For families?" || state.Turn != 2 {
		t.Fatalf("state = %+v, %v", state, err)
	}
	visions.notFound = true
	if state, err := read(t.Context(), "prj_test", helper); err != nil || state.Status != conversations.HelperFinished {
		t.Fatalf("a gone helper: %+v, %v", state, err)
	}
}
