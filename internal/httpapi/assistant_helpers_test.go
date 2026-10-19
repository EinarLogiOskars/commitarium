package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/conversations"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/featureartifact"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/toolchain"
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
	return &helperVisions{session: vision.Session{ID: "vis_1", ProjectID: "prj_test", Agent: "codex", Status: vision.StatusWaitingForUser,
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
	if err != nil || state.Status != conversations.HelperAsks || state.Text != "For families?" || state.Turn != 2 ||
		state.Agent != "codex" {
		t.Fatalf("state = %+v, %v", state, err)
	}
	visions.notFound = true
	if state, err := read(t.Context(), "prj_test", helper); err != nil || state.Status != conversations.HelperFinished {
		t.Fatalf("a gone helper: %+v, %v", state, err)
	}
}

func briefHandler(stub ConversationService, clarifier *recordingWorkOrderAssistant) http.Handler {
	features := &recordingFeatureService{getResult: feature.Feature{
		ID: "fea_sort", ProjectID: "prj_test", Title: "Sort the shelf", State: feature.StateDraft,
	}}
	features.createResult = features.getResult
	projects := &recordingProjectService{getByIDResult: project.Project{ID: "prj_test"}}
	return NewWithWorkspaceRealWorkflowDeletionAndModels(projects, features, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
		stub, clarifier, testAgents())
}

func approveCard(t *testing.T, handler http.Handler, body string) {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost,
		"/api/v1/assistant/conversations/prj_test/actions/act_1/approve", strings.NewReader(body)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d: %s", recorder.Code, recorder.Body.String())
	}
}

// write_brief starts the brief writer on the assistant's agent with what
// the assistant gathered, and follows it as a helper; so does a new work
// order whose brief is written first, which is created without starting.
func TestWritingABriefStartsTheBriefWriter(t *testing.T) {
	for name, card := range map[string]featureartifact.AssistantAction{
		"write_brief": {Type: featureartifact.AssistantWriteBrief, Reason: "Settled", WorkOrderID: "fea_sort",
			Description: "Sort by date, newest first."},
		"a new work order with a brief": {Type: featureartifact.AssistantCreateWorkOrder, Reason: "Asked",
			Title: "Sort the shelf", Description: "Sort by date, newest first.", Steps: "planned", Brief: true,
			LeadAgent: "codex", LeadModel: "gpt-6-luna"},
	} {
		t.Run(name, func(t *testing.T) {
			stub := &helperConversations{conversationStub: &conversationStub{
				settings: conversations.Settings{Agent: "codex", Model: "gpt-6-luna"},
				action:   conversations.Action{ID: "act_1", Status: conversations.ActionProposed, AssistantAction: card},
			}}
			clarifier := &recordingWorkOrderAssistant{}
			approveCard(t, briefHandler(stub, clarifier), `{"start":true}`)
			if clarifier.agent != "codex" || clarifier.model != "gpt-6-luna" || clarifier.description != "Sort by date, newest first." {
				t.Fatalf("brief writer on %s %s with %q", clarifier.agent, clarifier.model, clarifier.description)
			}
			if helper := stub.started; helper == nil || helper.Kind != conversations.HelperBrief || helper.ID != "ast_test" ||
				helper.WorkOrderID != "fea_sort" || stub.resolvedResult != "Writing the brief for “Sort the shelf”." {
				t.Fatalf("helper = %+v, result = %q", helper, stub.resolvedResult)
			}
		})
	}
}

// A brief card's Accept makes the work order Ready through its own accept.
func TestAcceptingABriefCardMakesTheWorkOrderReady(t *testing.T) {
	stub := &helperConversations{conversationStub: &conversationStub{action: conversations.Action{
		ID: "act_1", Status: conversations.ActionProposed, AssistantAction: featureartifact.AssistantAction{
			Type: featureartifact.AssistantAcceptBrief, Reason: "Written", WorkOrderID: "fea_sort", Title: "Sort the shelf",
			Helper: "ast_test", Description: "Sort the shelf by date.",
		},
	}}}
	clarifier := &recordingWorkOrderAssistant{}
	approveCard(t, briefHandler(stub, clarifier), `{}`)
	if clarifier.acceptKey != "assistant-act_1" || stub.resolvedResult != "“Sort the shelf” is ready to start." {
		t.Fatalf("accept key %q, result %q", clarifier.acceptKey, stub.resolvedResult)
	}
}

func stackHandler(stub ConversationService, designer *toolchainAssistantStub, overview project.RepositoryOverview) http.Handler {
	projects := &recordingProjectService{getByIDResult: project.Project{ID: "prj_test"}, overviewResult: overview}
	stacks := &recordingToolchainService{manifest: toolchain.Manifest{ProjectID: "prj_test", Status: toolchain.StatusNeedsSetup}}
	return NewWithWorkspaceRealWorkflowDeletionAndModels(projects, &recordingFeatureService{}, nil, nil, nil, nil, nil, nil, nil,
		nil, nil, nil, stub, stacks, designer, testAgents())
}

// design_stack starts the stack designer on the assistant's agent: it
// suggests a stack from what the app needs, or checks the stack of the code
// an imported repository has.
func TestDesigningAStackStartsTheStackDesigner(t *testing.T) {
	for name, test := range map[string]struct {
		overview project.RepositoryOverview
		purpose  toolchain.AssistantPurpose
		result   string
	}{
		"a new app": {purpose: toolchain.AssistantPurposeDesignStack, result: "Designing the stack."},
		"an imported repository": {overview: project.RepositoryOverview{Tree: []project.RepositoryTreeEntry{{Path: "go.mod"}}},
			purpose: toolchain.AssistantPurposeVerifyRepository, result: "Checking the stack the code uses."},
	} {
		t.Run(name, func(t *testing.T) {
			stub := &helperConversations{conversationStub: &conversationStub{
				settings: conversations.Settings{Agent: "codex", Model: "gpt-6-luna"},
				action: conversations.Action{ID: "act_1", Status: conversations.ActionProposed, AssistantAction: featureartifact.AssistantAction{
					Type: featureartifact.AssistantDesignStack, Reason: "No stack", Description: "A meal planner with accounts.",
				}},
			}}
			designer := &toolchainAssistantStub{session: toolchain.AssistantSession{ID: "tcs_1", Status: toolchain.AssistantStatusRunning}}
			approveCard(t, stackHandler(stub, designer, test.overview), `{}`)
			if designer.purpose != test.purpose || designer.agent != "codex" || designer.opening != "A meal planner with accounts." {
				t.Fatalf("designer on %s for %s with %q", designer.agent, designer.purpose, designer.opening)
			}
			if helper := stub.started; helper == nil || helper.ID != "tcs_1" || helper.Kind != conversations.HelperStack ||
				stub.resolvedResult != test.result {
				t.Fatalf("helper = %+v, result = %q", helper, stub.resolvedResult)
			}
		})
	}
}

// A stack card's Apply configures the suggested stack through the Stack
// page's own apply.
func TestApplyingAStackCardConfiguresTheStack(t *testing.T) {
	stub := &helperConversations{conversationStub: &conversationStub{action: conversations.Action{
		ID: "act_1", Status: conversations.ActionProposed, AssistantAction: featureartifact.AssistantAction{
			Type: featureartifact.AssistantApplyStack, Reason: "Suggested", Helper: "tcs_1", Description: "**Tools:** node 24",
		},
	}}}
	designer := &toolchainAssistantStub{manifest: toolchain.Manifest{ProjectID: "prj_test", Status: toolchain.StatusConfigured,
		Tools: map[string]string{"node": "24.1.0", "python": "3.14.7"}}}
	approveCard(t, stackHandler(stub, designer, project.RepositoryOverview{}), `{}`)
	if designer.applied != 1 || stub.resolvedResult != "The stack is set up: node 24.1.0, python 3.14.7." {
		t.Fatalf("applied %d, result %q", designer.applied, stub.resolvedResult)
	}
}

// The watcher reads a stack designer's suggestion as a result for a card.
func TestTheHelperReaderReadsAStackSuggestion(t *testing.T) {
	designer := &toolchainAssistantStub{session: toolchain.AssistantSession{ID: "tcs_1", ProjectID: "prj_test",
		Status: toolchain.AssistantStatusProposalReady, Message: "Node for the app, Postgres for accounts.",
		Proposal: &toolchain.Suggestion{Tools: map[string]string{"node": "24.1.0"}, Services: []string{"postgresql"}},
		Messages: []toolchain.AssistantMessage{{Role: "user"}, {Role: "assistant"}}}}
	read := HelperReader(stackHandler(&conversationStub{}, designer, project.RepositoryOverview{}))
	state, err := read(t.Context(), "prj_test", conversations.Helper{ID: "tcs_1", Kind: conversations.HelperStack})
	if err != nil || state.Status != conversations.HelperProposes || state.Turn != 2 ||
		state.Text != "**Tools:** node 24.1.0\n\n**Services:** postgresql\n\nNode for the app, Postgres for accounts." {
		t.Fatalf("state = %+v, %v", state, err)
	}
}
