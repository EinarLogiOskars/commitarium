// Package conversations is the assistant (see ASSISTANT_PLAN.md): one
// continuous conversation for the top level and one per project, in which the
// assistant answers, proposes actions the user approves one by one, and keeps
// notes for itself. Underneath, provider sessions are disposable: a long one is
// replaced by a fresh one that starts from the project's state, the recent
// conversation, and the notes.
package conversations

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/agent"
	"github.com/EinarLogiOskars/commitarium/internal/featureartifact"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
	"github.com/EinarLogiOskars/commitarium/internal/workspace"
)

var (
	ErrUnavailable = errors.New("the assistant is unavailable")
	ErrNotFound    = errors.New("no such assistant conversation or action")
	ErrBusy        = errors.New("the assistant is still answering")
	ErrInvalid     = errors.New("invalid assistant request")
	ErrNotPending  = errors.New("the action is no longer waiting for approval")
)

// TopLevel is the scope of the Commitarium-wide conversation; a project's
// conversation is scoped by its ID.
const TopLevel = "commitarium"

// Settings are the assistant's app-wide choices: its agent and model, and the
// model families it must ask before using (ASSISTANT_PLAN.md decision 14).
type Settings struct {
	Agent     string    `json:"agent"`
	Model     string    `json:"model"`
	AskBefore []string  `json:"ask_before"`
	UpdatedAt time.Time `json:"updated_at"`
}

// DefaultAskBefore are the premium model families marked by default.
var DefaultAskBefore = []string{"fable", "astra"}

// RequiresApproval reports whether a model belongs to a family the user must
// approve before the assistant may use it.
func (settings Settings) RequiresApproval(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "" {
		return false
	}
	for _, family := range settings.AskBefore {
		if family = strings.ToLower(strings.TrimSpace(family)); family != "" && strings.Contains(model, family) {
			return true
		}
	}
	return false
}

type Status string

const (
	StatusIdle    Status = "idle"
	StatusRunning Status = "running"
)

type Message struct {
	ID string `json:"id"`
	// Role is "user", "assistant", or "event" (a note from Commitarium, such
	// as an approved action's outcome).
	Role       string    `json:"role"`
	Text       string    `json:"text"`
	OccurredAt time.Time `json:"occurred_at"`
}

type ActionStatus string

const (
	ActionProposed  ActionStatus = "proposed"
	ActionApplied   ActionStatus = "applied"
	ActionDismissed ActionStatus = "dismissed"
	ActionFailed    ActionStatus = "failed"
)

// Action is one proposed action, shown as a card the user approves.
type Action struct {
	ID        string `json:"id"`
	MessageID string `json:"message_id"`
	featureartifact.AssistantAction
	Status ActionStatus `json:"status"`
	// Premium names the models in it that need explicit approval.
	Premium []string `json:"premium,omitempty"`
	Result  string   `json:"result,omitempty"`
	// ProjectID names the project a create_app action created.
	ProjectID string `json:"project_id,omitempty"`
	// AutoTried marks an action autopilot took over, applied or not.
	AutoTried bool      `json:"auto_tried,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Conversation is what the user sees of a scope's conversation.
type Conversation struct {
	Scope    string    `json:"scope"`
	Status   Status    `json:"status"`
	Agent    string    `json:"agent"`
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	Actions  []Action  `json:"actions"`
	Notes    string    `json:"notes"`
	// Goal is the autopilot goal, the latest one even once it ended.
	Goal *Goal `json:"goal,omitempty"`
	// Queued are the user's messages sent while the assistant answered; they
	// start its next turn.
	Queued []Message `json:"queued,omitempty"`
	// Launch is a new app's launch while it is set up (launch.go).
	Launch    *Launch   `json:"launch,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

type record struct {
	Conversation
	ProviderSessionID string `json:"provider_session_id,omitempty"`
	// Session counts provider sessions; SessionTurns counts turns in the
	// current one.
	Session      int               `json:"session"`
	SessionTurns int               `json:"session_turns"`
	Turn         int               `json:"turn"`
	AttemptID    string            `json:"attempt_id,omitempty"`
	SessionAgent string            `json:"session_agent,omitempty"`
	SessionModel string            `json:"session_model,omitempty"`
	Keys         map[string]string `json:"keys,omitempty"`
	// Watched is the project's work as last seen, by work order, and
	// PendingEvents what the assistant has yet to hear (events.go).
	Watched       map[string]string `json:"watched,omitempty"`
	PendingEvents []string          `json:"pending_events,omitempty"`
	// AutoContinues counts the blocked work orders autopilot continued, by
	// work order; past maxAutoContinues the next one waits for the user.
	AutoContinues map[string]int `json:"auto_continues,omitempty"`
}

// maxSessionTurns bounds a provider session; the next turn starts fresh.
const maxSessionTurns = 30

// ProjectReader reads projects for the assistant's picture of its scope.
type ProjectReader interface {
	GetByID(context.Context, string) (project.Project, error)
	List(context.Context) ([]project.Project, error)
	GetRepositoryOverview(context.Context, string) (project.RepositoryOverview, error)
}

type AgentLister interface {
	List(context.Context) ([]agent.Agent, error)
}

// Describer gives the assistant a plain-text picture of its scope: the
// project, its vision and work orders, the models it may choose, and the
// other projects.
type Describer interface {
	Describe(ctx context.Context, scope string) (string, error)
}

type Checkouts interface {
	Ensure(ctx context.Context, spec workspace.CheckoutSpec) error
}

type Branches interface {
	GetBranch(ctx context.Context, owner, repository, branch string) (workspace.Branch, error)
}

type Config struct {
	// Root holds the conversations and settings; WorkspaceRoot the
	// assistant's working directories, shared with the workers.
	Root, WorkspaceRoot string
	Projects            ProjectReader
	Agents              AgentLister
	Describer           Describer
	Workers             func(agentID string) (workerhttp.Service, error)
	// DefaultModel is a provider's default model, for the assistant's
	// default agent.
	DefaultModel func(ctx context.Context, provider project.AgentProvider) string
	Checkouts    Checkouts
	Branches     Branches
	// Visions and Features, when set, let the user hand the assistant an
	// autopilot goal.
	Visions  VisionReader
	Features FeatureLister
}

type Service struct {
	config Config
	now    func() time.Time
	mu     sync.Mutex
	// nudge asks the watcher to look now, as when a turn proposed actions
	// autopilot may take over.
	nudge chan struct{}
}

var scopePattern = regexp.MustCompile(`^(commitarium|prj_[A-Za-z0-9]{1,64})$`)

func NewService(config Config) (*Service, error) {
	config.Root = filepath.Clean(strings.TrimSpace(config.Root))
	config.WorkspaceRoot = filepath.Clean(strings.TrimSpace(config.WorkspaceRoot))
	if !filepath.IsAbs(config.Root) || !filepath.IsAbs(config.WorkspaceRoot) || config.Projects == nil ||
		config.Agents == nil || config.Describer == nil || config.Workers == nil {
		return nil, fmt.Errorf("%w: roots, projects, agents, a describer, and workers are required", ErrUnavailable)
	}
	if err := os.MkdirAll(filepath.Join(config.Root, "conversations"), 0o700); err != nil {
		return nil, fmt.Errorf("%w: create the assistant's storage", ErrUnavailable)
	}
	return &Service{config: config, now: func() time.Time { return time.Now().UTC() }, nudge: make(chan struct{}, 1)}, nil
}

// Settings returns the assistant's settings, filling in the default agent
// (the first connected one, on its provider's default model) and the default
// premium families.
func (service *Service) Settings(ctx context.Context) (Settings, error) {
	settings := Settings{AskBefore: append([]string(nil), DefaultAskBefore...)}
	contents, err := os.ReadFile(service.settingsPath())
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return Settings{}, ErrUnavailable
	default:
		if json.Unmarshal(contents, &settings) != nil {
			return Settings{}, ErrUnavailable
		}
	}
	if settings.Agent == "" {
		agents, err := service.config.Agents.List(ctx)
		if err != nil {
			return Settings{}, err
		}
		if len(agents) > 0 {
			settings.Agent = agents[0].ID
			if settings.Model == "" && service.config.DefaultModel != nil {
				settings.Model = service.config.DefaultModel(ctx, agents[0].Provider)
			}
		}
	}
	return settings, nil
}

// SaveSettings stores the assistant's agent, model, and premium families.
func (service *Service) SaveSettings(ctx context.Context, settings Settings) (Settings, error) {
	settings.Agent, settings.Model = strings.TrimSpace(settings.Agent), strings.TrimSpace(settings.Model)
	if !project.ValidAgentID(settings.Agent) || settings.Model == "" {
		return Settings{}, fmt.Errorf("%w: choose an agent and a model", ErrInvalid)
	}
	agents, err := service.config.Agents.List(ctx)
	if err != nil {
		return Settings{}, err
	}
	known := false
	for _, candidate := range agents {
		known = known || candidate.ID == settings.Agent
	}
	if !known {
		return Settings{}, fmt.Errorf("%w: agent %q is not connected", ErrInvalid, settings.Agent)
	}
	families := make([]string, 0, len(settings.AskBefore))
	for _, family := range settings.AskBefore {
		if family = strings.ToLower(strings.TrimSpace(family)); family != "" && len(family) <= 64 {
			families = append(families, family)
		}
	}
	settings.AskBefore = families
	settings.UpdatedAt = service.now()
	return settings, writeJSON(service.settingsPath(), settings)
}

func (service *Service) settingsPath() string {
	return filepath.Join(service.config.Root, "settings.json")
}

// Get returns a scope's conversation, folding in a finished turn.
func (service *Service) Get(ctx context.Context, scope string) (Conversation, error) {
	if err := service.checkScope(ctx, scope); err != nil {
		return Conversation{}, err
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	stored, err := service.read(scope)
	if err != nil {
		return Conversation{}, err
	}
	if stored.Status == StatusRunning {
		if stored, err = service.refresh(ctx, stored); err != nil {
			return Conversation{}, err
		}
	}
	return stored.Conversation, nil
}

// SaveNotes replaces a scope's notes with the user's edit.
func (service *Service) SaveNotes(ctx context.Context, scope, notes string) (Conversation, error) {
	if err := service.checkScope(ctx, scope); err != nil {
		return Conversation{}, err
	}
	notes = strings.TrimSpace(notes)
	if len(notes) > featureartifact.MaxAssistantNoteBytes {
		return Conversation{}, fmt.Errorf("%w: the notes are too long", ErrInvalid)
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	stored, err := service.read(scope)
	if err != nil {
		return Conversation{}, err
	}
	stored.Notes = notes
	stored.UpdatedAt = service.now()
	return stored.Conversation, service.write(stored)
}

// Action returns one of a scope's actions.
func (service *Service) Action(ctx context.Context, scope, actionID string) (Action, error) {
	conversation, err := service.Get(ctx, scope)
	if err != nil {
		return Action{}, err
	}
	for _, action := range conversation.Actions {
		if action.ID == actionID {
			return action, nil
		}
	}
	return Action{}, ErrNotFound
}

// Resolve records the outcome of a proposed action: applied with what it did,
// failed with why, or dismissed. Only a proposed action can be resolved.
// Created names what an applied action created, so the app can open it.
type Created struct {
	WorkOrderID, ProjectID string
}

// Resolve records an action's outcome and what it created.
func (service *Service) Resolve(ctx context.Context, scope, actionID string, status ActionStatus, result string, created Created) (Conversation, error) {
	if status != ActionApplied && status != ActionFailed && status != ActionDismissed {
		return Conversation{}, ErrInvalid
	}
	if err := service.checkScope(ctx, scope); err != nil {
		return Conversation{}, err
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	stored, err := service.read(scope)
	if err != nil {
		return Conversation{}, err
	}
	for index := range stored.Actions {
		action := &stored.Actions[index]
		if action.ID != actionID {
			continue
		}
		if action.Status != ActionProposed {
			return Conversation{}, ErrNotPending
		}
		// The outcome shows on the action's card; the assistant reads the
		// work it changed from the project's state on its next turn.
		action.Status, action.Result, action.UpdatedAt = status, strings.TrimSpace(result), service.now()
		if created.WorkOrderID != "" {
			action.WorkOrderID = created.WorkOrderID
		}
		action.ProjectID = created.ProjectID
		stored.UpdatedAt = action.UpdatedAt
		return stored.Conversation, service.write(stored)
	}
	return Conversation{}, ErrNotFound
}

func (service *Service) checkScope(ctx context.Context, scope string) error {
	if !scopePattern.MatchString(scope) {
		return ErrNotFound
	}
	if scope == TopLevel {
		return nil
	}
	_, err := service.config.Projects.GetByID(ctx, scope)
	return err
}

func (service *Service) read(scope string) (record, error) {
	contents, err := os.ReadFile(service.path(scope))
	if errors.Is(err, os.ErrNotExist) {
		return record{Conversation: Conversation{
			Scope: scope, Status: StatusIdle, Messages: []Message{}, Actions: []Action{},
		}}, nil
	}
	if err != nil {
		return record{}, ErrUnavailable
	}
	var stored record
	if json.Unmarshal(contents, &stored) != nil || stored.Scope != scope {
		return record{}, ErrUnavailable
	}
	return stored, nil
}

func (service *Service) write(stored record) error {
	return writeJSON(service.path(stored.Scope), stored)
}

func (service *Service) path(scope string) string {
	return filepath.Join(service.config.Root, "conversations", scope+".json")
}

// DeleteProject forgets a project's conversation.
func (service *Service) DeleteProject(_ context.Context, projectID string) error {
	if !scopePattern.MatchString(projectID) || projectID == TopLevel {
		return nil
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	if err := os.Remove(service.path(projectID)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return ErrUnavailable
	}
	return nil
}

func messageID(stored record, index int) string {
	return fmt.Sprintf("msg_%s_%d", stored.Scope, index+1)
}

func conversationID(scope string, session int) string {
	digest := sha256.Sum256([]byte("assistant\x00" + scope + "\x00" + fmt.Sprint(session)))
	return "asc_" + hex.EncodeToString(digest[:12])
}

func writeJSON(path string, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return ErrUnavailable
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, encoded, 0o600); err != nil {
		return ErrUnavailable
	}
	if err := os.Rename(temporary, path); err != nil {
		return ErrUnavailable
	}
	return nil
}
