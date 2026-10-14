package conversations

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/featureartifact"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/toolchain"
	"github.com/EinarLogiOskars/commitarium/internal/vision"
)

func kinds(gaps []gap) []gapKind {
	found := make([]gapKind, 0, len(gaps))
	for _, item := range gaps {
		found = append(found, item.Kind)
	}
	return found
}

func sameKinds(a []gapKind, b ...gapKind) bool {
	if len(a) != len(b) {
		return false
	}
	for index := range a {
		if a[index] != b[index] {
			return false
		}
	}
	return true
}

var openPlan = featureartifact.VisionPlan{Priorities: []featureartifact.VisionPriority{
	{Level: 1, Items: []featureartifact.VisionItem{{ID: "later", Title: "Share the shelf"}}},
	{Level: 0, Items: []featureartifact.VisionItem{
		{ID: "done", Title: "Add a book", Done: true}, {ID: "sort", Title: "Sort the shelf", FeatureID: "fea_merged"},
		{ID: "count", Title: "Count the books"},
	}},
}}

// The gap check finds each thing a project lacks to run, in the order the
// assistant fixes it; the preview only in an app.
func TestTheGapCheckFindsWhatAProjectLacks(t *testing.T) {
	merged := feature.Feature{ID: "fea_merged", Title: "Sort the shelf", State: feature.StateCompleted}
	for name, test := range map[string]struct {
		state projectState
		want  []gapKind
		text  string
	}{
		"an empty project":       {projectState{}, []gapKind{gapVision}, "write_vision"},
		"a vision being written": {projectState{Writing: true}, []gapKind{}, ""},
		"code but nothing else": {projectState{HasCode: true, HasCompose: true},
			[]gapKind{gapVision, gapStack}, "set_up_stack"},
		"a vision without a stack": {projectState{Vision: &openPlan}, []gapKind{gapStack}, ""},
		"a stack being set up":     {projectState{Vision: &openPlan, SettingUp: true}, []gapKind{}, ""},
		"open items, autopilot off": {projectState{Vision: &openPlan, StackReady: true, Features: []feature.Feature{merged}},
			[]gapKind{gapAutopilot}, "2 open items and autopilot is off. Propose start_autopilot for P0"},
		"waiting work, nothing runs": {projectState{StackReady: true, Vision: &featureartifact.VisionPlan{}, Features: []feature.Feature{
			{ID: "fea_draft", Title: "Count the books", State: feature.StateDraft},
			{ID: "fea_ready", Title: "Dark shelf", State: feature.StateReady},
			{ID: "fea_session", Title: "Pairing", State: feature.StateDraft, Kind: feature.KindSession},
		}}, []gapKind{gapWaiting}, "“Count the books” (fea_draft, draft), “Dark shelf” (fea_ready, ready)"},
		"waiting work while work runs": {projectState{StackReady: true, Vision: &featureartifact.VisionPlan{}, Features: []feature.Feature{
			{ID: "fea_draft", Title: "Count the books", State: feature.StateDraft},
			{ID: "fea_run", Title: "Sort", State: feature.StateImplementing},
		}}, []gapKind{}, ""},
		"a project without a compose file": {projectState{StackReady: true, Vision: &featureartifact.VisionPlan{}, HasCode: true},
			[]gapKind{}, ""},
		"an app without a compose file": {projectState{App: true, StackReady: true, Vision: &featureartifact.VisionPlan{}, HasCode: true},
			[]gapKind{gapPreview}, "no compose file. Propose set_up_preview"},
		"an app whose preview is being set up": {projectState{App: true, StackReady: true, Vision: &featureartifact.VisionPlan{}, HasCode: true,
			Features: []feature.Feature{{ID: "fea_bg", Title: "Set up the preview", Background: true, State: feature.StateImplementing}},
			Runs:     map[string]execution.Run{"fea_bg": {Status: execution.RunStatusRunning}},
		}, []gapKind{}, ""},
		"an app whose preview setup is stuck": {projectState{App: true, StackReady: true, Vision: &featureartifact.VisionPlan{}, HasCode: true, HasCompose: true,
			Features: []feature.Feature{{ID: "fea_bg", Title: "Fix the preview", Background: true, State: feature.StateImplementing}},
			Runs: map[string]execution.Run{"fea_bg": {Status: execution.RunStatusWaitingForUser,
				WaitKind: execution.RunWaitKindBlocker, Reason: "The API does not start.\nTrace."}},
		}, []gapKind{gapPreview}, "“Fix the preview” (fea_bg), is stuck: The API does not start."},
	} {
		t.Run(name, func(t *testing.T) {
			found := findGaps(test.state, record{})
			if !sameKinds(kinds(found), test.want...) {
				t.Fatalf("gaps = %+v, want %v", found, test.want)
			}
			if test.text != "" && !strings.Contains(found[len(found)-1].Text, test.text) {
				t.Fatalf("gap text = %q, want %q", found[len(found)-1].Text, test.text)
			}
		})
	}
}

// A preview the desktop reported failing is a gap until preview work merged
// after it.
func TestAFailingPreviewIsAGapUntilAFixMerges(t *testing.T) {
	failedAt := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	state := projectState{App: true, StackReady: true, Vision: &featureartifact.VisionPlan{}, HasCode: true, HasCompose: true}
	stored := record{PreviewFailedAt: &failedAt}
	if !sameKinds(kinds(findGaps(state, stored)), gapPreview) {
		t.Fatal("a failing preview is no gap")
	}
	state.Features = []feature.Feature{{ID: "fea_fix", PreviewCheck: true, State: feature.StateCompleted, UpdatedAt: failedAt.Add(time.Minute)}}
	if gaps := findGaps(state, stored); len(gaps) != 0 {
		t.Fatalf("a fixed preview is still a gap: %+v", gaps)
	}
}

// A start card the user dismissed while the assistant assisted does not
// keep it from taking the work order over after the switch; one dismissed
// while it builds everything does.
func TestACardDismissedBeforeTheSwitchDoesNotBlockTakingOver(t *testing.T) {
	switched := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	state := projectState{StackReady: true, Vision: &featureartifact.VisionPlan{}, Features: []feature.Feature{
		{ID: "fea_ready", Title: "Add sort by", State: feature.StateReady},
	}}
	dismissed := func(at time.Time) record {
		return record{BuildsSince: &switched, Conversation: Conversation{Actions: []Action{{
			ID: "act_start", Status: ActionDismissed, UpdatedAt: at,
			AssistantAction: featureartifact.AssistantAction{Type: featureartifact.AssistantStartWorkOrder, WorkOrderID: "fea_ready"},
		}}}}
	}
	if !sameKinds(kinds(findGaps(state, dismissed(switched.Add(-time.Hour)))), gapWaiting) {
		t.Fatal("a card dismissed while assisting kept the work order from being taken over")
	}
	if gaps := findGaps(state, dismissed(switched.Add(time.Hour))); len(gaps) != 0 {
		t.Fatalf("a card dismissed while building everything was overruled: %+v", gaps)
	}
	if text := recentActions(dismissed(switched.Add(-time.Hour)).Actions, &switched); !strings.Contains(text, "no longer stands") {
		t.Fatalf("the assistant does not hear the dismissal no longer stands: %q", text)
	}
}

type gapProjects struct {
	projectsStub
	mode project.AssistantMode
}

func (stub gapProjects) GetByID(ctx context.Context, id string) (project.Project, error) {
	stored, err := stub.projectsStub.GetByID(ctx, id)
	stored.AssistantMode = stub.mode
	return stored, err
}

type toolchainStub struct{ status toolchain.Status }

func (stub toolchainStub) Get(context.Context, string) (toolchain.Manifest, error) {
	return toolchain.Manifest{Status: stub.status}, nil
}

type noVisionSession struct{}

func (noVisionSession) OpenSession(context.Context, string) (vision.Session, error) {
	return vision.Session{}, vision.ErrNotFound
}

func gapWatcher(t *testing.T, mode project.AssistantMode) (Watcher, *Service, *bool) {
	t.Helper()
	service, _ := newTestService(t)
	service.config.Projects = gapProjects{mode: mode}
	limited := false
	watcher := Watcher{
		Service: service, Features: &featuresStub{}, Runs: runsStub{}, Visions: missingVision{},
		Toolchains: toolchainStub{status: toolchain.StatusNeedsSetup}, VisionSessions: noVisionSession{},
		AgentLimited: func(context.Context, string) bool { return limited },
	}
	return watcher, service, &limited
}

// Building everything, the gap check wakes the assistant once with what
// is missing; at most once per gapEvery, at most maxGapTells times in a
// row, and never while it answers, has events waiting, or its agent is at
// its usage limit. Assisting, it never does.
func TestTheGapCheckWakesTheAssistantRarely(t *testing.T) {
	watcher, service, limited := gapWatcher(t, project.AssistantModeBuilds)
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	pending := func() []string {
		stored, err := service.read("prj_test")
		if err != nil {
			t.Fatal(err)
		}
		return stored.PendingEvents
	}
	clear := func() {
		stored, _ := service.read("prj_test")
		stored.PendingEvents = nil
		if err := service.write(stored); err != nil {
			t.Fatal(err)
		}
	}

	*limited = true
	watcher.gapCheck(t.Context(), "prj_test")
	if len(pending()) != 0 {
		t.Fatal("the gap check woke an assistant at its usage limit")
	}
	*limited = false
	watcher.gapCheck(t.Context(), "prj_test")
	if events := pending(); len(events) != 1 || !strings.Contains(events[0], "no vision yet") {
		t.Fatalf("pending = %v", events)
	}
	// Events wait: nothing more is told.
	now = now.Add(gapEvery)
	watcher.gapCheck(t.Context(), "prj_test")
	if len(pending()) != 1 {
		t.Fatal("the gap check told again while events waited")
	}
	clear()
	now = now.Add(-time.Minute)
	stored, _ := service.read("prj_test")
	stored.Gaps["vision"] = gapTold{At: now.Add(-gapEvery + time.Minute), Count: 1}
	_ = service.write(stored)
	watcher.gapCheck(t.Context(), "prj_test")
	if len(pending()) != 0 {
		t.Fatal("the same gap was told again within gapEvery")
	}
	// A turn running holds it back too.
	stored, _ = service.read("prj_test")
	stored.Status = StatusRunning
	_ = service.write(stored)
	now = now.Add(gapEvery)
	watcher.gapCheck(t.Context(), "prj_test")
	if len(pending()) != 0 {
		t.Fatal("the gap check told an assistant whose turn runs")
	}
	stored, _ = service.read("prj_test")
	stored.Status = StatusIdle
	_ = service.write(stored)
	for tell := 2; tell <= maxGapTells+1; tell++ {
		watcher.gapCheck(t.Context(), "prj_test")
		told := len(pending()) == 1
		if told != (tell <= maxGapTells) {
			t.Fatalf("tell %d: told %t", tell, told)
		}
		clear()
		now = now.Add(gapEvery)
	}

	assists, service, _ := gapWatcher(t, project.AssistantModeAssists)
	assists.gapCheck(t.Context(), "prj_test")
	if stored, _ := service.read("prj_test"); len(stored.PendingEvents) != 0 {
		t.Fatal("the gap check woke an assistant that assists")
	}
}

// Switching to building everything wakes the assistant at once, starts the
// project's conversation if it had none, and records the switch.
func TestHandingOverWakesTheAssistant(t *testing.T) {
	service, _ := newTestService(t)
	if err := service.HandOver(t.Context(), "prj_test"); err != nil {
		t.Fatal(err)
	}
	stored, err := service.read("prj_test")
	if err != nil || stored.BuildsSince == nil || len(stored.PendingEvents) != 1 ||
		!strings.Contains(stored.PendingEvents[0], "dismissed before this switch no longer stand") {
		t.Fatalf("handed over = %+v err=%v", stored.PendingEvents, err)
	}
	if scopes := service.projectScopes(); len(scopes) != 1 || scopes[0] != "prj_test" {
		t.Fatalf("the watcher does not look at the project: %v", scopes)
	}
}

// Work that follows other work waits for it to merge: autopilot creates it
// at once, so it shows as a draft, queues its start, says so once, and
// starts it after.
func TestAutopilotQueuesWorkThatFollowsOtherWork(t *testing.T) {
	service, features := queuedCountsAfterSort(t)
	if queued, err := service.QueuedStarts(t.Context(), "prj_test"); err != nil || queued["fea_count"] != "Add sort by" {
		t.Fatalf("queued starts = %v err=%v", queued, err)
	}
	if apply, _ := service.autopilotActions(t.Context(), "prj_test"); len(apply) != 0 {
		t.Fatalf("the counts started beside the sort: %+v", apply)
	}
	conversation, _ := service.Get(t.Context(), "prj_test")
	queued := 0
	for _, message := range conversation.Messages {
		if strings.Contains(message.Text, "queued") {
			queued++
		}
	}
	if queued != 1 {
		t.Fatalf("the queue was noted %d times", queued)
	}
	features.features[0].State = feature.StateCompleted
	apply, err := service.autopilotActions(t.Context(), "prj_test")
	if err != nil || len(apply) != 1 || apply[0].ID != "act_count_start" || apply[0].WorkOrderID != "fea_count" ||
		apply[0].Type != featureartifact.AssistantStartWorkOrder {
		t.Fatalf("after the sort merged, applied %+v err=%v", apply, err)
	}
}

// A queued work order the user starts themselves no longer waits, and
// autopilot does not start it again.
func TestAQueuedWorkOrderTheUserStartsNoLongerWaits(t *testing.T) {
	service, features := queuedCountsAfterSort(t)
	features.features[1].State = feature.StatePlanning
	if apply, _ := service.autopilotActions(t.Context(), "prj_test"); len(apply) != 0 {
		t.Fatalf("autopilot acted on a work order the user started: %+v", apply)
	}
	if queued, _ := service.QueuedStarts(t.Context(), "prj_test"); len(queued) != 0 {
		t.Fatalf("still queued: %v", queued)
	}
	conversation, _ := service.Get(t.Context(), "prj_test")
	if last := conversation.Messages[len(conversation.Messages)-1]; !strings.Contains(last.Text, "You started “Show counts”, so it no longer waits for “Add sort by”") {
		t.Fatalf("last note = %q", last.Text)
	}
	features.features[0].State = feature.StateCompleted
	if apply, _ := service.autopilotActions(t.Context(), "prj_test"); len(apply) != 0 {
		t.Fatalf("autopilot started it again after the sort merged: %+v", apply)
	}
}

// A held create autopilot could not apply becomes an ordinary card: the
// user's approval starts what it creates.
func TestARefusedHeldCreateStartsWhenTheUserApproves(t *testing.T) {
	service, _ := newTestService(t)
	stored, err := service.read("prj_test")
	if err != nil {
		t.Fatal(err)
	}
	action := Action{ID: "act_count", Status: ActionProposed, HoldStart: true, AssistantAction: featureartifact.AssistantAction{
		Type: featureartifact.AssistantCreateWorkOrder, Title: "Show counts", After: "Add sort by"}}
	stored.Actions = append(stored.Actions, action)
	if err := service.write(stored); err != nil {
		t.Fatal(err)
	}
	if err := service.autopilotOutcome("prj_test", action, errors.New("the agent is signed out")); err != nil {
		t.Fatal(err)
	}
	if card, err := service.Action(t.Context(), "prj_test", "act_count"); err != nil || card.HoldStart {
		t.Fatalf("card = %+v err=%v", card, err)
	}
}

// queuedCountsAfterSort has autopilot, building everything, take over two
// changes where the counts follow the sort: both are created, the sort runs,
// and the counts' start is queued.
func queuedCountsAfterSort(t *testing.T) (*Service, *featuresStub) {
	t.Helper()
	service, _ := newTestService(t)
	service.config.Projects = gapProjects{mode: project.AssistantModeBuilds}
	features := &featuresStub{}
	service.config.Features = features
	stored, err := service.read("prj_test")
	if err != nil {
		t.Fatal(err)
	}
	create := func(id, title, after string) Action {
		return Action{ID: id, Status: ActionProposed, AssistantAction: featureartifact.AssistantAction{
			Type: featureartifact.AssistantCreateWorkOrder, Reason: "Asked", Title: title, Steps: "quick", After: after}}
	}
	stored.Actions = append(stored.Actions, create("act_sort", "Add sort by", ""), create("act_count", "Show counts", "Add sort by"))
	if err := service.write(stored); err != nil {
		t.Fatal(err)
	}
	apply, err := service.autopilotActions(t.Context(), "prj_test")
	if err != nil || len(apply) != 2 || apply[0].ID != "act_sort" || apply[0].HoldStart ||
		apply[1].ID != "act_count" || !apply[1].HoldStart {
		t.Fatalf("applied %+v err=%v, want the sort and the counts created without starting", apply, err)
	}
	for _, created := range []struct{ action, workOrder string }{{"act_sort", "fea_sort"}, {"act_count", "fea_count"}} {
		if _, err := service.Resolve(t.Context(), "prj_test", created.action, ActionApplied, "Created",
			Created{WorkOrderID: created.workOrder}); err != nil {
			t.Fatal(err)
		}
	}
	for _, applied := range apply {
		if err := service.autopilotOutcome("prj_test", applied, nil); err != nil {
			t.Fatal(err)
		}
	}
	features.features = []feature.Feature{
		{ID: "fea_sort", Title: "Add sort by", State: feature.StateImplementing},
		{ID: "fea_count", Title: "Show counts", State: feature.StateDraft},
	}
	return service, features
}
