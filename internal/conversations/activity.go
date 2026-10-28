package conversations

import (
	"context"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/EinarLogiOskars/commitarium/internal/redact"
	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
)

// While a turn runs, the user sees what the assistant is doing in a line
// that replaces itself: its worker's activity stream, read as it comes and
// put in plain words. Only the latest step is kept, in memory: once the turn
// ends it is gone. The worker redacts the secrets it knows before an event
// leaves it; the detail is redacted again by shape, as a check's reason is.

// Activity is the assistant's latest step while it answers.
type Activity struct {
	// Label is the step in plain words ("Searching the repository").
	Label string `json:"label"`
	// Detail names what the step looked at (a path, a command, a search),
	// for Developer mode.
	Detail string `json:"detail,omitempty"`
	// StartedAt is when the turn started, its retries included.
	StartedAt *time.Time `json:"started_at,omitempty"`
	// Retry is the automatic retry running, from 1, or 0.
	Retry int `json:"retry,omitempty"`
}

// ActivityStream is a worker attempt's event stream.
type ActivityStream interface {
	NextFrame() (workerhttp.StreamFrame, error)
	Close() error
}

type liveActivity struct {
	attemptID, label, detail string
}

// activityLifetime bounds how long one turn's stream is read.
const activityLifetime = time.Hour

// maxActivityDetail bounds a step's detail.
const maxActivityDetail = 160

// followActivity reads the turn's activity stream in the background until
// the turn ends or another replaces it.
func (service *Service) followActivity(ctx context.Context, scope, agentID string, reference workerhttp.AttemptReference) {
	service.activityMu.Lock()
	if service.activity == nil {
		service.activity = map[string]liveActivity{}
	}
	service.activity[scope] = liveActivity{attemptID: reference.AttemptID}
	service.activityMu.Unlock()
	if service.config.ActivityStreams == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), activityLifetime)
		defer cancel()
		stream, err := service.config.ActivityStreams(ctx, agentID, reference)
		if err != nil {
			return
		}
		defer stream.Close()
		for {
			frame, err := stream.NextFrame()
			if err != nil || (frame.Event != nil && frame.Event.Type == workerhttp.EventAttemptTerminal) {
				return
			}
			label, detail := describeFrame(frame)
			if label == "" {
				continue
			}
			if !service.noteActivity(scope, reference.AttemptID, label, detail) {
				return
			}
		}
	}()
}

// noteActivity keeps a step of the scope's turn; false once another turn
// replaced it.
func (service *Service) noteActivity(scope, attemptID, label, detail string) bool {
	service.activityMu.Lock()
	defer service.activityMu.Unlock()
	if service.activity[scope].attemptID != attemptID {
		return false
	}
	service.activity[scope] = liveActivity{attemptID: attemptID, label: label, detail: detail}
	return true
}

// currentActivity is what the user sees of a running turn, or nil.
func (service *Service) currentActivity(stored record) *Activity {
	if stored.Status != StatusRunning {
		return nil
	}
	activity := &Activity{Label: "Thinking", StartedAt: stored.TurnStartedAt}
	if retry := stored.Retry; retry != nil {
		activity.Retry = retry.Count
		if retry.DueAt != nil {
			activity.Label = "Trying again"
			if retry.Correction != "" {
				activity.Label = "Fixing its reply"
			}
			return activity
		}
	}
	service.activityMu.Lock()
	live, ok := service.activity[stored.Scope]
	service.activityMu.Unlock()
	if ok && live.attemptID == stored.AttemptID && live.label != "" {
		activity.Label, activity.Detail = live.label, live.detail
	}
	return activity
}

// describeFrame puts a worker event in plain words, with its detail, or ""
// for one that says nothing new (a step finished).
func describeFrame(frame workerhttp.StreamFrame) (string, string) {
	if frame.Preview != nil {
		return "Writing its reply", ""
	}
	event := frame.Event
	if event == nil {
		return "", ""
	}
	switch event.Type {
	case workerhttp.EventMessage:
		return "Writing its reply", ""
	case workerhttp.EventActivity:
	default:
		return "", ""
	}
	if activity := event.Activity; activity != nil {
		switch activity.Kind {
		case workerhttp.ActivityKindNarration:
			return "Thinking it through", detail(firstLine(event.Text))
		case workerhttp.ActivityKindCommand:
			return describeCommand(activity.Command)
		case workerhttp.ActivityKindFileChange:
			return "Working on files", detail(activity.Path)
		}
		return "", ""
	}
	return describeToolText(event.Text)
}

// toolTexts are how the Claude Code and Codex adapters announce a tool
// starting, by what follows the agent's name; what comes after names its
// target.
var toolTexts = []struct{ prefix, label, detail string }{
	{"started reading ", "Reading a file", "Looking at "},
	{"started searching the workspace", "Searching the repository", "Searching"},
	{"started looking for files", "Searching the repository", "Looking for files"},
	{"started inspecting the workspace", "Searching the repository", ""},
	{"started a web search", "Looking something up on the web", "Searching the web for "},
	{"started a web request", "Looking something up on the web", "Reading"},
	{"started delegated agent work", "Working through a part of it", ""},
	{"started a command or tool", "Using a tool", ""},
	{"started a tool call", "Using a tool", ""},
	{"updated its plan", "Planning its reply", ""},
	{"started working", "Getting started", ""},
}

func describeToolText(text string) (string, string) {
	_, rest, found := strings.Cut(strings.TrimSpace(text), " ")
	if !found {
		return "", ""
	}
	rest = strings.TrimSuffix(rest, ".")
	for _, known := range toolTexts {
		target, ok := strings.CutPrefix(rest, known.prefix)
		if !ok {
			continue
		}
		target = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(target), ":"))
		if target == "" || known.detail == "" {
			return known.label, ""
		}
		joined := known.detail + target
		if !strings.HasSuffix(known.detail, " ") {
			joined = known.detail + " " + target
		}
		return known.label, detail(joined)
	}
	return "", ""
}

// describeCommand puts a shell command in plain words: what Codex reads and
// searches with is a command.
func describeCommand(command string) (string, string) {
	inner := unwrapShell(command)
	fields := strings.Fields(inner)
	if len(fields) == 0 {
		return "Running a command", detail(command)
	}
	switch path.Base(fields[0]) {
	case "rg", "grep", "egrep", "ag", "ack", "fd", "find", "ls", "tree":
		return "Searching the repository", detail(inner)
	case "cat", "sed", "head", "tail", "nl", "less", "more", "bat", "wc", "awk", "jq":
		if file := lastPath(fields[1:]); file != "" {
			return "Reading a file", detail("Looking at " + file)
		}
		return "Reading a file", detail(inner)
	case "git":
		if len(fields) > 1 && (fields[1] == "grep" || fields[1] == "ls-files") {
			return "Searching the repository", detail(inner)
		}
		return "Looking at the project's history", detail(inner)
	case "curl", "wget":
		return "Looking something up on the web", detail(inner)
	}
	return "Running a command", detail(inner)
}

// unwrapShell takes the command a shell wrapper runs ("bash -lc '…'"), and
// the first step of a chain that is not a cd.
func unwrapShell(command string) string {
	command = strings.TrimSpace(command)
	fields := strings.Fields(command)
	if len(fields) >= 3 {
		switch path.Base(fields[0]) {
		case "bash", "zsh", "sh":
			if fields[1] == "-lc" || fields[1] == "-c" {
				_, rest, _ := strings.Cut(command, fields[1])
				command = strings.Trim(strings.TrimSpace(rest), `'"`)
			}
		}
	}
	for _, step := range strings.Split(command, "&&") {
		step = strings.TrimSpace(step)
		if step != "" && !strings.HasPrefix(step, "cd ") {
			command = step
			break
		}
	}
	if before, _, found := strings.Cut(command, "|"); found {
		command = strings.TrimSpace(before)
	}
	return command
}

// lastPath is the last argument that looks like a file.
func lastPath(arguments []string) string {
	for index := len(arguments) - 1; index >= 0; index-- {
		argument := strings.Trim(arguments[index], `'"`)
		if argument != "" && !strings.HasPrefix(argument, "-") && strings.ContainsAny(argument, "./") &&
			!strings.ContainsAny(argument, ",;{}") {
			return argument
		}
	}
	return ""
}

// detail redacts and bounds a step's detail.
func detail(text string) string {
	return bounded(strings.Join(strings.Fields(redact.Secrets(text)), " "), maxActivityDetail)
}

// bounded cuts text to at most limit bytes, on a character boundary.
func bounded(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut] + "…"
}
