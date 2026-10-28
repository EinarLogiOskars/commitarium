package conversations

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
)

// framesStream hands out its frames, then waits for release before ending.
type framesStream struct {
	frames  chan workerhttp.StreamFrame
	release chan struct{}
}

func (stream *framesStream) NextFrame() (workerhttp.StreamFrame, error) {
	if frame, ok := <-stream.frames; ok {
		return frame, nil
	}
	<-stream.release
	return workerhttp.StreamFrame{}, io.EOF
}

func (*framesStream) Close() error { return nil }

func activityEvent(text string, activity *workerhttp.Activity) workerhttp.StreamFrame {
	return workerhttp.StreamFrame{Event: &workerhttp.Event{Type: workerhttp.EventActivity, Text: text, Activity: activity}}
}

// What the assistant's worker reports while it answers reaches the
// conversation as one plain line, with its detail for Developer mode.
func TestTheAssistantsActivityShowsWhileItAnswers(t *testing.T) {
	service, worker := newTestService(t)
	stream := &framesStream{frames: make(chan workerhttp.StreamFrame), release: make(chan struct{})}
	defer close(stream.release)
	opened := make(chan workerhttp.AttemptReference, 1)
	service.config.ActivityStreams = func(_ context.Context, agentID string, reference workerhttp.AttemptReference) (ActivityStream, error) {
		if agentID != "codex" {
			t.Errorf("stream opened on %q", agentID)
		}
		opened <- reference
		return stream, nil
	}
	conversation, err := service.Send(t.Context(), "prj_test", "How does sign-in work?", "msg-1")
	if err != nil || conversation.Activity != nil {
		t.Fatalf("send = %+v err=%v", conversation, err)
	}
	if reference := <-opened; reference.AttemptID != worker.attempt.AttemptID {
		t.Fatalf("stream opened for %+v", reference)
	}
	conversation, _ = service.Get(t.Context(), "prj_test")
	if conversation.Activity == nil || conversation.Activity.Label != "Thinking" ||
		conversation.Activity.StartedAt == nil || conversation.TurnStartedAt == nil {
		t.Fatalf("before any step: %+v", conversation.Activity)
	}
	for _, step := range []struct {
		frame         workerhttp.StreamFrame
		label, detail string
	}{
		{activityEvent("Claude started reading src/server/auth.ts.", nil), "Reading a file", "Looking at src/server/auth.ts"},
		{activityEvent("Codex ran command: /bin/zsh -lc 'rg -n session src'", &workerhttp.Activity{
			Kind: workerhttp.ActivityKindCommand, Command: "/bin/zsh -lc 'rg -n session src'"}),
			"Searching the repository", "rg -n session src"},
		{activityEvent("Claude finished inspecting the workspace.", nil), "Searching the repository", "rg -n session src"},
		{activityEvent("Codex ran command: curl -H 'Authorization: Bearer abc123' https://x", &workerhttp.Activity{
			Kind: workerhttp.ActivityKindCommand, Command: "curl -H 'Authorization: Bearer abc123' https://x"}),
			"Looking something up on the web", "curl -H 'Authorization: Bearer [REDACTED]' https://x"},
		{workerhttp.StreamFrame{Preview: &workerhttp.MessagePreview{Text: "Sign-in uses"}}, "Writing its reply", ""},
	} {
		stream.frames <- step.frame
		deadline := time.Now().Add(2 * time.Second)
		for {
			conversation, _ = service.Get(t.Context(), "prj_test")
			if activity := conversation.Activity; activity != nil && activity.Label == step.label && activity.Detail == step.detail {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("after %+v: activity = %+v, want %q / %q", step.frame, conversation.Activity, step.label, step.detail)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	close(stream.frames)
	worker.finish(replied("It uses a session cookie."))
	if conversation, _ = service.Get(t.Context(), "prj_test"); conversation.Activity != nil {
		t.Fatalf("the activity outlived the turn: %+v", conversation.Activity)
	}
}

func TestStepsInPlainWords(t *testing.T) {
	for _, test := range []struct{ command, label, detail string }{
		{"sed -n 1,200p src/server/auth.ts", "Reading a file", "Looking at src/server/auth.ts"},
		{"bash -lc \"cd web && cat package.json\"", "Reading a file", "Looking at package.json"},
		{"git log --oneline -5", "Looking at the project's history", "git log --oneline -5"},
		{"git grep -n login", "Searching the repository", "git grep -n login"},
		{"go test ./...", "Running a command", "go test ./..."},
	} {
		if label, detail := describeCommand(test.command); label != test.label || detail != test.detail {
			t.Errorf("%q = %q / %q, want %q / %q", test.command, label, detail, test.label, test.detail)
		}
	}
	for text, want := range map[string][2]string{
		"Claude started searching the workspace for useSession.": {"Searching the repository", "Searching for useSession"},
		"Claude started a web search: VAT rate Iceland.":         {"Looking something up on the web", "Searching the web for VAT rate Iceland"},
		"Codex started a web search.":                            {"Looking something up on the web", ""},
		"Claude finished a web request.":                         {"", ""},
	} {
		if label, detail := describeToolText(text); label != want[0] || detail != want[1] {
			t.Errorf("%q = %q / %q, want %q", text, label, detail, want)
		}
	}
}
