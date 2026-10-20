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
	"log"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/agent"
	"github.com/EinarLogiOskars/commitarium/internal/featureartifact"
	"github.com/EinarLogiOskars/commitarium/internal/modelguide"
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
	// ErrNoAgents and ErrNotSetUp say, in the user's words, why the assistant
	// cannot answer yet (ADR-027).
	ErrNoAgents = errors.New("the assistant needs an AI account to work with. Add one on the Agents page, then come back here")
	ErrNotSetUp = errors.New("the assistant is not set up yet. Choose which of your agents it works with in the assistant settings")
)

// TopLevel is the scope of the Commitarium-wide conversation; a project's
// conversation is scoped by its ID.
const TopLevel = "commitarium"

// Settings are the assistant's app-wide choices: its agent and model, and the
// models it must ask before using (ASSISTANT_PLAN.md decision 14).
//
// With a model guide (ADR-031), AskBefore lists the models the guide says
// need approval; the families the user stored before are migrated into the
// guide's overrides once, and AskBeforeMigrated records that.
type Settings struct {
	Agent             string   `json:"agent"`
	Model             string   `json:"model"`
	AskBefore         []string `json:"ask_before"`
	AskBeforeMigrated bool     `json:"ask_before_migrated,omitempty"`
	// MaxEffort is the highest reasoning effort the assistant may choose
	// (ADR-031); the user's own choices are not limited.
	MaxEffort string    `json:"max_effort"`
	UpdatedAt time.Time `json:"updated_at"`
}

// DefaultMaxEffort caps the assistant's efforts until the user chooses.
const DefaultMaxEffort = "high"

// DefaultAskBefore are the premium model families marked by default.
var DefaultAskBefore = []string{"fable", "astra"}

// RequiresApproval reports whether a model belongs to a family the user must
// approve before the assistant may use it. Without a model guide it is the
// whole check; Service.RequiresApproval asks the guide when there is one.
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
	// ProjectID names the project a create_app or plan_project action
	// created.
	ProjectID string `json:"project_id,omitempty"`
	// AutoTried marks an action autopilot took over, applied or not.
	AutoTried bool `json:"auto_tried,omitempty"`
	// Queued marks an action autopilot holds until the work it follows
	// (After) has merged.
	Queued bool `json:"queued,omitempty"`
	// HoldStart marks a work order autopilot creates without starting it,
	// because it follows work that has not merged: it shows as a draft at
	// once, and a queued start action starts it once that work has merged.
	HoldStart bool      `json:"hold_start,omitempty"`
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
	// SignIn is set while the last turn waits for its agent's login: the
	// message is kept and sent once the agent works (sign_in.go).
	SignIn *SignInWait `json:"sign_in,omitempty"`
	// Focus is the item the user opened the assistant on (focus.go).
	Focus *Focus `json:"focus,omitempty"`
	// Developer is whether the user's app shows Developer mode, as its last
	// message said: without it, a new app stays self-contained, and only
	// with it may the assistant plan a project (ADR-035).
	Developer bool `json:"developer,omitempty"`
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
	// LastTurn is the prompt the last turn started on, kept to start it
	// again when its agent's login failed (sign_in.go).
	LastTurn *turnInput `json:"last_turn,omitempty"`
	// Watched is the project's work as last seen, by work order, and
	// PendingEvents what the assistant has yet to hear (events.go).
	Watched       map[string]string `json:"watched,omitempty"`
	PendingEvents []string          `json:"pending_events,omitempty"`
	// AutoContinues counts the blocked work orders autopilot continued, by
	// work order; past maxAutoContinues the next one waits for the user.
	AutoContinues map[string]int `json:"auto_continues,omitempty"`
	// Refusals counts autopilot's refused actions since one last applied;
	// past maxRefusals they no longer wake the assistant.
	Refusals int `json:"refusals,omitempty"`
	// BuildsSince is when the user last switched the project to building
	// everything: cards dismissed before it no longer stand (gaps.go).
	BuildsSince *time.Time `json:"builds_since,omitempty"`
	// Gaps are what the gap check last told the assistant was missing, by
	// kind, so it tells each at most once per gapEvery (gaps.go).
	Gaps map[string]gapTold `json:"gaps,omitempty"`
	// PreviewFailedAt is when the app's preview last failed to start, as the
	// desktop reported it; a preview work order merged since fixes it.
	PreviewFailedAt *time.Time `json:"preview_failed_at,omitempty"`
	// Helpers are the helper sessions the assistant started (helpers.go).
	Helpers []Helper `json:"helpers,omitempty"`
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
	// Guide, when set, decides which models need the user's approval
	// (ADR-031); Models lists the models the connected agents offer.
	Guide  ModelGuide
	Models func(ctx context.Context) []ModelChoice
	// ChatUsage, when set, records each turn's token usage per scope, so a
	// project's cost includes the assistant's own turns.
	ChatUsage ChatUsageRecorder
	// SignIns, when set, shares which agents need a sign-in with the work
	// orders waiting on them (sign_in.go).
	SignIns SignIns
	// RecoveryExhausted, when set, reports whether a work order's latest run
	// ran out of recovery (ADR-032): autopilot may then close it, even on a
	// goal item, so the assistant can replace it.
	RecoveryExhausted func(ctx context.Context, workOrderID string) bool
}

// ChatUsage is one assistant turn's usage.
type ChatUsage struct {
	Scope, AttemptID, Agent, Model string
	Usage                          workerhttp.TokenUsage
	RecordedAt                     time.Time
}

// ChatUsageRecorder stores the assistant's turns' usage.
type ChatUsageRecorder interface {
	RecordAssistantChatUsage(ctx context.Context, usage ChatUsage) error
}

// ModelGuide is the official model guide merged with the user's overrides.
type ModelGuide interface {
	AsOf() string
	RequiresApproval(ctx context.Context, model string) (bool, error)
	Entries(ctx context.Context, extra []string) ([]modelguide.Entry, error)
	MigrateAskBefore(ctx context.Context, families, models []string) error
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

// Settings returns the assistant's settings with the default premium
// families. The agent and model are the user's choice (ADR-027): until they
// choose, both are empty and the assistant asks to be set up.
func (service *Service) Settings(ctx context.Context) (Settings, error) {
	settings, err := service.storedSettings(ctx)
	if err != nil || service.config.Guide == nil {
		return settings, err
	}
	offered := service.offeredModels(ctx)
	if !settings.AskBeforeMigrated {
		settings = service.migrateAskBefore(ctx, settings, offered)
	}
	entries, err := service.config.Guide.Entries(ctx, offered)
	if err != nil {
		return Settings{}, fmt.Errorf("%w: read the model guide", ErrUnavailable)
	}
	settings.AskBefore = make([]string, 0)
	for _, entry := range entries {
		if entry.NeedsApproval() {
			settings.AskBefore = append(settings.AskBefore, entry.ID)
		}
	}
	return settings, nil
}

// storedSettings reads the settings file as it is stored.
func (service *Service) storedSettings(ctx context.Context) (Settings, error) {
	settings := Settings{AskBefore: append([]string(nil), DefaultAskBefore...), MaxEffort: DefaultMaxEffort}
	contents, err := os.ReadFile(service.settingsPath())
	switch {
	case errors.Is(err, os.ErrNotExist):
		return service.earlierDefault(ctx, settings)
	case err != nil:
		return Settings{}, ErrUnavailable
	default:
		if json.Unmarshal(contents, &settings) != nil {
			return Settings{}, ErrUnavailable
		}
	}
	if settings.MaxEffort == "" {
		settings.MaxEffort = DefaultMaxEffort
	}
	return settings, nil
}

// offeredModels lists the models the connected agents offer.
func (service *Service) offeredModels(ctx context.Context) []string {
	if service.config.Models == nil {
		return nil
	}
	choices := service.config.Models(ctx)
	models := make([]string, 0, len(choices))
	for _, choice := range choices {
		models = append(models, choice.ID)
	}
	return models
}

// migrateAskBefore moves the families the user stored into the model guide's
// overrides, once (ADR-031). Families that are still the defaults need
// nothing; others wait until the agents' models are known, so no choice is
// lost.
func (service *Service) migrateAskBefore(ctx context.Context, settings Settings, offered []string) Settings {
	defaults := slices.Equal(slices.Sorted(slices.Values(settings.AskBefore)), slices.Sorted(slices.Values(DefaultAskBefore)))
	if !defaults {
		if len(offered) == 0 {
			return settings
		}
		if err := service.config.Guide.MigrateAskBefore(ctx, settings.AskBefore, offered); err != nil {
			log.Printf("migrate the assistant's ask-before families: %v", err)
			return settings
		}
	}
	settings.AskBeforeMigrated = true
	if _, err := os.Stat(service.settingsPath()); err == nil {
		if err := writeJSON(service.settingsPath(), settings); err != nil {
			log.Printf("record the ask-before migration: %v", err)
		}
	}
	return settings
}

// RequiresApproval reports whether the assistant must ask the user before
// using a model: the model guide's Premium and unrated models (ADR-031), or,
// without a guide, the settings' families. When the guide cannot be read it
// asks.
func (service *Service) RequiresApproval(ctx context.Context, model string) bool {
	if service.config.Guide == nil {
		settings, err := service.Settings(ctx)
		return err != nil || settings.RequiresApproval(model)
	}
	needed, err := service.config.Guide.RequiresApproval(ctx, model)
	return err != nil || needed
}

// earlierDefault keeps an assistant that already answered on the default it
// had before ADR-027, when it took the first agent on its provider's default
// model without the user saving a choice: that choice is saved once, so it
// keeps working. A new assistant has none.
func (service *Service) earlierDefault(ctx context.Context, settings Settings) (Settings, error) {
	if !service.answeredBefore() {
		return settings, nil
	}
	agents, err := service.config.Agents.List(ctx)
	if err != nil {
		return Settings{}, err
	}
	if len(agents) == 0 || service.config.DefaultModel == nil {
		return settings, nil
	}
	model := service.config.DefaultModel(ctx, agents[0].Provider)
	if model == "" {
		return settings, nil
	}
	settings.Agent, settings.Model, settings.UpdatedAt = agents[0].ID, model, service.now()
	return settings, writeJSON(service.settingsPath(), settings)
}

// answeredBefore reports whether any conversation has had a turn.
func (service *Service) answeredBefore() bool {
	entries, err := os.ReadDir(filepath.Join(service.config.Root, "conversations"))
	if err != nil {
		return false
	}
	for _, entry := range entries {
		scope, ok := strings.CutSuffix(entry.Name(), ".json")
		if !ok || !scopePattern.MatchString(scope) {
			continue
		}
		if stored, err := service.read(scope); err == nil && stored.Turn > 0 {
			return true
		}
	}
	return false
}

// Readiness says whether the assistant can answer: ErrNoAgents when no agent
// exists yet, ErrNotSetUp when none is chosen for it (or the chosen one is
// gone), nil when it is set up.
func (service *Service) Readiness(ctx context.Context) (Settings, error) {
	settings, err := service.Settings(ctx)
	if err != nil {
		return Settings{}, err
	}
	agents, err := service.config.Agents.List(ctx)
	if err != nil {
		return Settings{}, err
	}
	if len(agents) == 0 {
		return settings, ErrNoAgents
	}
	for _, candidate := range agents {
		if candidate.ID == settings.Agent && settings.Model != "" {
			return settings, nil
		}
	}
	return settings, ErrNotSetUp
}

// SaveSettings stores the assistant's agent, model, and premium families.
func (service *Service) SaveSettings(ctx context.Context, settings Settings) (Settings, error) {
	settings.Agent, settings.Model = strings.TrimSpace(settings.Agent), strings.TrimSpace(settings.Model)
	if !project.ValidAgentID(settings.Agent) || settings.Model == "" {
		return Settings{}, fmt.Errorf("%w: choose an agent and a model", ErrInvalid)
	}
	if settings.MaxEffort = strings.TrimSpace(settings.MaxEffort); settings.MaxEffort == "" {
		settings.MaxEffort = DefaultMaxEffort
	}
	if !project.ValidEffortCeiling(settings.MaxEffort) {
		return Settings{}, fmt.Errorf("%w: the highest effort must be low, medium, high, xhigh, max, or ultra", ErrInvalid)
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
	if service.config.Guide != nil {
		// The model guide decides approvals (ADR-031); the stored families
		// only matter until they are migrated.
		stored, err := service.storedSettings(ctx)
		if err != nil {
			return Settings{}, err
		}
		settings.AskBefore, settings.AskBeforeMigrated = stored.AskBefore, stored.AskBeforeMigrated
	}
	families := make([]string, 0, len(settings.AskBefore))
	for _, family := range settings.AskBefore {
		if family = strings.ToLower(strings.TrimSpace(family)); family != "" && len(family) <= 64 {
			families = append(families, family)
		}
	}
	settings.AskBefore = families
	settings.UpdatedAt = service.now()
	if err := writeJSON(service.settingsPath(), settings); err != nil || service.config.Guide == nil {
		return settings, err
	}
	return service.Settings(ctx)
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

// SetDeveloperMode records whether the user's app shows Developer mode.
func (service *Service) SetDeveloperMode(ctx context.Context, scope string, on bool) error {
	if err := service.checkScope(ctx, scope); err != nil {
		return err
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	stored, err := service.read(scope)
	if err != nil || stored.Developer == on {
		return err
	}
	stored.Developer = on
	return service.write(stored)
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
	// Helper is a helper the action started, whose questions and results
	// come back to the assistant (helpers.go).
	Helper *Helper
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
		if status == ActionApplied && created.Helper != nil {
			stored.follow(*created.Helper)
		}
		if status == ActionApplied && (action.Type == featureartifact.AssistantAcceptBrief ||
			action.Type == featureartifact.AssistantApplyStack) {
			// An accepted result ends its helper's work.
			for index := range stored.Helpers {
				if stored.Helpers[index].ID == action.Helper {
					stored.Helpers[index].Done = true
				}
			}
		}
		if status == ActionApplied && action.Type == featureartifact.AssistantCreateWorkOrder && stored.Goal.active() {
			// The goal item follows its new work order, a replacement too, at
			// once rather than at the next look at the vision.
			stored.Goal.follow(action.VisionItemID, action.WorkOrderID)
		}
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
