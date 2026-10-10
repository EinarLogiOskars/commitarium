// Package assistant runs the project assistant's work-order clarification
// (ADR-015): a 1:1 conversation that turns a draft work order into a handoff
// brief before any lead or reviewer agent starts.
package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/featureartifact"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
	"github.com/EinarLogiOskars/commitarium/internal/workflow"
	"github.com/EinarLogiOskars/commitarium/internal/workspace"
)

var (
	ErrUnavailable = errors.New("project assistant is unavailable")
	ErrNotFound    = errors.New("project assistant session not found")
	ErrConflict    = errors.New("project assistant request conflicts with durable state")
	ErrNotReady    = errors.New("project assistant is not ready for that action")
	ErrNotDraft    = errors.New("only a draft work order can be clarified")
	ErrInvalid     = errors.New("invalid project assistant request")
)

type Status string

const (
	StatusRunning        Status = "running"
	StatusWaitingForUser Status = "waiting_for_user"
	StatusProposalReady  Status = "proposal_ready"
	StatusFailed         Status = "failed"
)

// Session is the user-visible state of a work order's clarification.
type Session struct {
	ID        string
	ProjectID string
	FeatureID string
	Agent     string // the agent whose worker runs the conversation (ADR-016)
	Model     string
	Status    Status
	Message   string
	Messages  []Message
	CreatedAt time.Time
	UpdatedAt time.Time
}

type Message struct {
	Role       string
	Text       string
	OccurredAt time.Time
}

// Record is the durable session, including what the coordinator needs to
// resume the provider conversation.
type Record struct {
	Session
	WorkspaceID       string
	BaseCommitID      string
	ProviderSessionID string
	AttemptID         string
	Turn              int
}

type Mutation struct {
	Digest    string
	AttemptID string
}

type Store interface {
	// CreateAssistantSession stores a new session with the user's opening
	// message, or returns the feature's existing session.
	CreateAssistantSession(ctx context.Context, record Record, opening Message) (Record, bool, error)
	GetAssistantSessionByFeature(ctx context.Context, featureID string) (Record, error)
	UpdateAssistantSession(ctx context.Context, record Record) error
	// FinishAssistantTurn stores the turn's outcome and, when present, the
	// assistant's message in one transaction.
	FinishAssistantTurn(ctx context.Context, record Record, message *Message) error
	GetAssistantMutation(ctx context.Context, sessionID, key string) (Mutation, bool, error)
	// BeginAssistantReply records the reply's idempotency key, the user's
	// message, and the advanced turn in one transaction.
	BeginAssistantReply(ctx context.Context, record Record, key string, mutation Mutation, message Message) error
	RecordAssistantUsage(ctx context.Context, sessionID, attemptID string, usage workerhttp.TokenUsage, recordedAt time.Time) error
}

type FeatureReader interface {
	GetByID(ctx context.Context, projectID string, featureID string) (feature.Feature, error)
}

type WorkspacePreparer interface {
	PrepareForClarification(ctx context.Context, projectID string, featureID string) (workspace.Workspace, bool, error)
}

type BriefWriter interface {
	UpsertHandoffBrief(ctx context.Context, featureID string, brief featureartifact.HandoffBrief, actor workflow.Actor, idempotencyKey string) (workflow.FeatureArtifact, error)
	GetFeatureArtifact(ctx context.Context, featureID string, kind featureartifact.Kind) (workflow.FeatureArtifact, error)
}

type FeatureTransitioner interface {
	TransitionFeature(ctx context.Context, featureID string, state feature.State, actor workflow.Actor, idempotencyKey string) (workflow.Event, error)
}

type WorkerService interface {
	PutAttempt(ctx context.Context, identity workerhttp.MutationIdentity, request workerhttp.PutAttemptRequest) (workerhttp.Attempt, bool, error)
	GetAttempt(ctx context.Context, reference workerhttp.AttemptReference) (workerhttp.Attempt, error)
}

type Worker struct {
	Service        WorkerService
	AgentProfileID string
}

type Config struct {
	Store       Store
	Features    FeatureReader
	Workspaces  WorkspacePreparer
	Briefs      BriefWriter
	Transitions FeatureTransitioner
	// Workers resolves an agent ID to its worker (ADR-016).
	Workers func(agentID string) (WorkerService, error)
	Now     func() time.Time
}

type Service struct {
	store       Store
	features    FeatureReader
	workspaces  WorkspacePreparer
	briefs      BriefWriter
	transitions FeatureTransitioner
	workers     func(agentID string) (WorkerService, error)
	now         func() time.Time
	mu          sync.Mutex
}

func NewService(config Config) (*Service, error) {
	if config.Store == nil || config.Features == nil || config.Workspaces == nil || config.Briefs == nil ||
		config.Transitions == nil || config.Workers == nil {
		return nil, fmt.Errorf("%w: store, features, workspaces, briefs, transitions, and workers are required", ErrUnavailable)
	}
	now := config.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &Service{
		store: config.Store, features: config.Features, workspaces: config.Workspaces,
		briefs: config.Briefs, transitions: config.Transitions, workers: config.Workers, now: now,
	}, nil
}

// Start opens the work order's clarification, or returns the existing one.
func (service *Service) Start(
	ctx context.Context,
	projectID string,
	featureID string,
	agentID string,
	model string,
) (Session, bool, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	storedFeature, err := service.features.GetByID(ctx, projectID, featureID)
	if err != nil {
		return Session{}, false, err
	}
	existing, err := service.store.GetAssistantSessionByFeature(ctx, featureID)
	if err == nil {
		if existing.Status == StatusRunning && existing.ProviderSessionID != "" {
			existing, err = service.refresh(ctx, existing)
		}
		return existing.Session, false, err
	}
	if !errors.Is(err, ErrNotFound) {
		return Session{}, false, err
	}
	if storedFeature.State != feature.StateDraft {
		return Session{}, false, ErrNotDraft
	}
	configured, ok := service.worker(agentID)
	model = strings.TrimSpace(model)
	if !ok {
		return Session{}, false, fmt.Errorf("%w: agent %q is not available", ErrInvalid, agentID)
	}
	prepared, _, err := service.workspaces.PrepareForClarification(ctx, projectID, featureID)
	if err != nil {
		return Session{}, false, err
	}
	now := service.now()
	request := workOrderText(storedFeature)
	id := sessionID(featureID)
	record, created, err := service.store.CreateAssistantSession(ctx, Record{
		Session: Session{
			ID: id, ProjectID: projectID, FeatureID: featureID, Agent: agentID, Model: model,
			Status: StatusRunning, CreatedAt: now, UpdatedAt: now,
		},
		WorkspaceID: prepared.ID, BaseCommitID: prepared.BaseCommitID,
		AttemptID: turnAttemptID(id, 1), Turn: 1,
	}, Message{Role: "user", Text: request, OccurredAt: now})
	if err != nil {
		return Session{}, false, err
	}
	attempt, _, err := configured.Service.PutAttempt(ctx, workerhttp.MutationIdentity{
		AttemptReference: workerhttp.AttemptReference{SessionID: record.ID, AttemptID: record.AttemptID},
		IdempotencyKey:   record.AttemptID + ":start",
	}, workerhttp.PutAttemptRequest{
		Mode:            workerhttp.AttemptModeStart,
		Assignment:      service.assignment(record, configured),
		Instructions:    initialInstructions(request),
		OutputContract:  workerhttp.OutputContractWorkOrderBrief,
		WorkspaceAccess: workerhttp.WorkspaceAccessReadOnly,
	})
	if err != nil {
		return Session{}, created, err
	}
	record.ProviderSessionID = attempt.ProviderSessionID
	record.UpdatedAt = service.now()
	if err := service.store.UpdateAssistantSession(ctx, record); err != nil {
		return Session{}, created, err
	}
	session, err := service.load(ctx, featureID)
	return session, created, err
}

// Get returns the session, first folding in a finished assistant turn.
func (service *Service) Get(ctx context.Context, projectID, featureID string) (Session, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	if _, err := service.features.GetByID(ctx, projectID, featureID); err != nil {
		return Session{}, err
	}
	record, err := service.store.GetAssistantSessionByFeature(ctx, featureID)
	if err != nil {
		return Session{}, err
	}
	if record.Status == StatusRunning {
		if record, err = service.refresh(ctx, record); err != nil {
			return Session{}, err
		}
	}
	return record.Session, nil
}

// Reply sends the user's message as the next assistant turn. The same
// idempotency key always maps to the same turn.
func (service *Service) Reply(
	ctx context.Context,
	projectID string,
	featureID string,
	message string,
	idempotencyKey string,
) (Session, bool, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	storedFeature, err := service.features.GetByID(ctx, projectID, featureID)
	if err != nil {
		return Session{}, false, err
	}
	message, key := strings.TrimSpace(message), strings.TrimSpace(idempotencyKey)
	if message == "" || key == "" {
		return Session{}, false, fmt.Errorf("%w: message and idempotency key are required", ErrInvalid)
	}
	record, err := service.store.GetAssistantSessionByFeature(ctx, featureID)
	if err != nil {
		return Session{}, false, err
	}
	if record.Status == StatusRunning {
		if record, err = service.refresh(ctx, record); err != nil {
			return Session{}, false, err
		}
	}
	digest := digestOf(message)
	mutation, found, err := service.store.GetAssistantMutation(ctx, record.ID, key)
	if err != nil {
		return Session{}, false, err
	}
	created := !found
	if found {
		if mutation.Digest != digest {
			return Session{}, false, ErrConflict
		}
		if mutation.AttemptID != record.AttemptID {
			// An earlier delivery of this reply ran and has been superseded.
			return record.Session, false, nil
		}
	} else {
		if storedFeature.State != feature.StateDraft {
			return Session{}, false, ErrNotDraft
		}
		if record.Status == StatusRunning || record.ProviderSessionID == "" {
			return Session{}, false, ErrNotReady
		}
		record.Turn++
		record.AttemptID = turnAttemptID(record.ID, record.Turn)
		record.Status, record.Message = StatusRunning, ""
		record.UpdatedAt = service.now()
		if err := service.store.BeginAssistantReply(
			ctx, record, key, Mutation{Digest: digest, AttemptID: record.AttemptID},
			Message{Role: "user", Text: message, OccurredAt: record.UpdatedAt},
		); err != nil {
			return Session{}, false, err
		}
	}
	configured, ok := service.worker(record.Agent)
	if !ok {
		return Session{}, false, ErrUnavailable
	}
	attempt, _, err := configured.Service.PutAttempt(ctx, workerhttp.MutationIdentity{
		AttemptReference: workerhttp.AttemptReference{SessionID: record.ID, AttemptID: record.AttemptID},
		IdempotencyKey:   key,
	}, workerhttp.PutAttemptRequest{
		Mode:              workerhttp.AttemptModeResume,
		Assignment:        service.assignment(record, configured),
		ProviderSessionID: record.ProviderSessionID,
		Instructions:      replyInstructions(message),
		OutputContract:    workerhttp.OutputContractWorkOrderBrief,
		WorkspaceAccess:   workerhttp.WorkspaceAccessReadOnly,
	})
	if err != nil {
		return Session{}, created, err
	}
	if attempt.ProviderSessionID != "" && attempt.ProviderSessionID != record.ProviderSessionID {
		record.ProviderSessionID = attempt.ProviderSessionID
		if err := service.store.UpdateAssistantSession(ctx, record); err != nil {
			return Session{}, created, err
		}
	}
	session, err := service.load(ctx, featureID)
	return session, created, err
}

// AcceptBrief makes a clarified draft Ready. It needs a handoff brief and an
// assistant that is not mid-turn. Accepting a Ready order again is a no-op.
func (service *Service) AcceptBrief(ctx context.Context, projectID, featureID, idempotencyKey string) (feature.Feature, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	storedFeature, err := service.features.GetByID(ctx, projectID, featureID)
	if err != nil {
		return feature.Feature{}, err
	}
	if storedFeature.State == feature.StateReady {
		return storedFeature, nil
	}
	if storedFeature.State != feature.StateDraft {
		return feature.Feature{}, ErrNotDraft
	}
	if _, err := service.briefs.GetFeatureArtifact(ctx, featureID, featureartifact.KindHandoffBrief); err != nil {
		if errors.Is(err, workflow.ErrArtifactNotFound) {
			return feature.Feature{}, ErrNotReady
		}
		return feature.Feature{}, err
	}
	record, err := service.store.GetAssistantSessionByFeature(ctx, featureID)
	if err == nil && record.Status == StatusRunning {
		if record, err = service.refresh(ctx, record); err != nil {
			return feature.Feature{}, err
		}
		if record.Status == StatusRunning {
			return feature.Feature{}, ErrNotReady
		}
	} else if err != nil && !errors.Is(err, ErrNotFound) {
		return feature.Feature{}, err
	}
	if _, err := service.transitions.TransitionFeature(
		ctx, featureID, feature.StateReady,
		workflow.Actor{Kind: workflow.ActorKindUser, ID: "local-user"}, idempotencyKey,
	); err != nil {
		return feature.Feature{}, err
	}
	return service.features.GetByID(ctx, projectID, featureID)
}

// Reopen returns a Ready order to Draft for more clarification. Reopening a
// draft again is a no-op.
func (service *Service) Reopen(ctx context.Context, projectID, featureID, idempotencyKey string) (feature.Feature, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	storedFeature, err := service.features.GetByID(ctx, projectID, featureID)
	if err != nil {
		return feature.Feature{}, err
	}
	if storedFeature.State == feature.StateDraft {
		return storedFeature, nil
	}
	if storedFeature.State != feature.StateReady {
		return feature.Feature{}, ErrNotReady
	}
	if _, err := service.transitions.TransitionFeature(
		ctx, featureID, feature.StateDraft,
		workflow.Actor{Kind: workflow.ActorKindUser, ID: "local-user"}, idempotencyKey,
	); err != nil {
		return feature.Feature{}, err
	}
	return service.features.GetByID(ctx, projectID, featureID)
}

// worker resolves an agent's worker; the agent's ID is its worker profile.
func (service *Service) worker(agentID string) (Worker, bool) {
	if !project.ValidAgentID(agentID) {
		return Worker{}, false
	}
	resolved, err := service.workers(agentID)
	if err != nil || resolved == nil {
		return Worker{}, false
	}
	return Worker{Service: resolved, AgentProfileID: agentID}, true
}

func (service *Service) assignment(record Record, configured Worker) workerhttp.Assignment {
	return workerhttp.Assignment{
		AgentProfileID: configured.AgentProfileID, Model: record.Model,
		ProjectID: record.ProjectID, FeatureID: record.FeatureID,
		Role: workerhttp.RoleConsultant, WorkspaceID: record.WorkspaceID,
	}
}

// refresh folds a finished turn into the session: a question waits for the
// user, a proposal becomes the work order's handoff brief.
func (service *Service) refresh(ctx context.Context, record Record) (Record, error) {
	configured, ok := service.worker(record.Agent)
	if !ok {
		return record, ErrUnavailable
	}
	attempt, err := configured.Service.GetAttempt(ctx, workerhttp.AttemptReference{SessionID: record.ID, AttemptID: record.AttemptID})
	if err != nil {
		return record, err
	}
	if attempt.ProviderSessionID != "" {
		record.ProviderSessionID = attempt.ProviderSessionID
	}
	switch attempt.State {
	case workerhttp.AttemptStateTerminal:
	case workerhttp.AttemptStateIndeterminate:
		return service.finish(ctx, record, StatusFailed,
			"The assistant's turn could not be safely recovered. Send a message to try again.")
	default:
		return record, nil
	}
	result := attempt.Result
	if result != nil && result.Usage != nil {
		if err := service.store.RecordAssistantUsage(ctx, record.ID, record.AttemptID, *result.Usage, service.now()); err != nil {
			return record, err
		}
	}
	switch {
	case result == nil || result.Outcome != workerhttp.OutcomeCompleted:
		return service.finish(ctx, record, StatusFailed,
			"The assistant's turn did not complete. Send a message to try again.")
	case result.Disposition == workerhttp.DispositionInputRequired:
		return service.finish(ctx, record, StatusWaitingForUser, result.Summary)
	case result.HandoffBrief != nil:
		brief := *result.HandoffBrief
		brief.BaseCommitID = record.BaseCommitID
		if _, err := service.briefs.UpsertHandoffBrief(
			ctx, record.FeatureID, brief,
			workflow.Actor{Kind: workflow.ActorKindAgent, ID: record.ID},
			record.AttemptID+":handoff-brief",
		); err != nil {
			return record, err
		}
		return service.finish(ctx, record, StatusProposalReady, result.Summary)
	default:
		return service.finish(ctx, record, StatusFailed,
			"The assistant returned no question or brief. Send a message to try again.")
	}
}

func (service *Service) finish(ctx context.Context, record Record, status Status, message string) (Record, error) {
	record.Status, record.Message = status, strings.TrimSpace(message)
	record.UpdatedAt = service.now()
	var reply *Message
	if record.Message != "" {
		reply = &Message{Role: "assistant", Text: record.Message, OccurredAt: record.UpdatedAt}
	}
	if err := service.store.FinishAssistantTurn(ctx, record, reply); err != nil {
		return record, err
	}
	stored, err := service.store.GetAssistantSessionByFeature(ctx, record.FeatureID)
	if err != nil {
		return record, err
	}
	return stored, nil
}

func (service *Service) load(ctx context.Context, featureID string) (Session, error) {
	record, err := service.store.GetAssistantSessionByFeature(ctx, featureID)
	return record.Session, err
}

func sessionID(featureID string) string {
	digest := sha256.Sum256([]byte("work-order-assistant\x00" + featureID))
	return "ast_" + hex.EncodeToString(digest[:12])
}

func turnAttemptID(sessionID string, turn int) string {
	return fmt.Sprintf("%s:turn:%d", sessionID, turn)
}

func digestOf(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func workOrderText(storedFeature feature.Feature) string {
	text := strings.TrimSpace(storedFeature.Title)
	if description := strings.TrimSpace(storedFeature.Description); description != "" {
		text += "\n\n" + description
	}
	return text
}

func initialInstructions(request string) string {
	return "You are the project assistant. Help the user turn the work order below into a handoff brief " +
		"that a lead agent and a reviewer agent will later plan and implement. The repository is checked " +
		"out read-only in your working directory: inspect it as needed, but do not modify files, install " +
		"anything, commit, or push. Work out what the user wants and why, where in the code it belongs, and " +
		"what is worth planning around. Ask only questions whose answers would change the work, and do not " +
		"ask what you can find in the code. While something important is unclear, return action 'ask' with " +
		"your question in message and leave goal empty and the lists empty. When the work is clear, return " +
		"action 'propose' with a short message and the brief: goal (what and why, self-contained, because " +
		"the agents will not see this conversation), areas (files, modules, or components to touch, each " +
		"with a few words on what changes), considerations (constraints, risks, existing patterns to " +
		"follow, testing notes), and open_questions (only what the agents should settle while planning; " +
		"empty if none). Do not write a commit-by-commit plan; the agents do that.\n\nWork order:\n" + request
}

func replyInstructions(message string) string {
	return "The user replied below. Continue the same conversation: ask the next question that matters, " +
		"or propose the brief. If you already proposed a brief and the user asks for changes, propose the " +
		"complete revised brief.\n\nUser:\n" + message
}
