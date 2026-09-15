package toolchain

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

	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
)

var (
	ErrAssistantUnavailable = errors.New("toolchain setup assistant is unavailable")
	ErrAssistantNotFound    = errors.New("toolchain setup assistant session not found")
	ErrAssistantConflict    = errors.New("toolchain setup assistant request conflicts with durable state")
	ErrAssistantNotReady    = errors.New("toolchain setup assistant is not ready for that action")
)

var assistantIDPattern = regexp.MustCompile(`^tcs_[a-f0-9]{24}$`)

type AssistantStatus string

const (
	AssistantStatusRunning        AssistantStatus = "running"
	AssistantStatusWaitingForUser AssistantStatus = "waiting_for_user"
	AssistantStatusProposalReady  AssistantStatus = "proposal_ready"
	AssistantStatusApplied        AssistantStatus = "applied"
	AssistantStatusFailed         AssistantStatus = "failed"
)

type AssistantWorker struct {
	Service        workerhttp.Service
	AgentProfileID string
}

type AssistantSession struct {
	ID        string                `json:"id"`
	ProjectID string                `json:"project_id"`
	Provider  project.AgentProvider `json:"provider"`
	Model     string                `json:"model"`
	Status    AssistantStatus       `json:"status"`
	Message   string                `json:"message,omitempty"`
	Proposal  *Suggestion           `json:"proposal,omitempty"`
	Messages  []AssistantMessage    `json:"messages"`
	CreatedAt time.Time             `json:"created_at"`
	UpdatedAt time.Time             `json:"updated_at"`
}

type AssistantMessage struct {
	Role       string    `json:"role"`
	Text       string    `json:"text"`
	OccurredAt time.Time `json:"occurred_at"`
}

type assistantRecord struct {
	AssistantSession
	AttemptID         string                       `json:"attempt_id"`
	ProviderSessionID string                       `json:"provider_session_id,omitempty"`
	Turn              int                          `json:"turn"`
	InitialKey        string                       `json:"initial_key"`
	InitialDigest     string                       `json:"initial_digest"`
	PendingKey        string                       `json:"pending_key"`
	PendingDigest     string                       `json:"pending_digest"`
	Mutations         map[string]assistantMutation `json:"mutations"`
}

type assistantMutation struct {
	Digest    string `json:"digest"`
	AttemptID string `json:"attempt_id"`
}

type Assistant struct {
	root          string
	workspaceRoot string
	projects      ProjectReader
	toolchains    *Manager
	workers       map[project.AgentProvider]AssistantWorker
	now           func() time.Time
	mu            sync.Mutex
}

func NewAssistant(root, workspaceRoot string, projects ProjectReader, toolchains *Manager, workers map[project.AgentProvider]AssistantWorker) (*Assistant, error) {
	root = filepath.Clean(strings.TrimSpace(root))
	workspaceRoot = filepath.Clean(strings.TrimSpace(workspaceRoot))
	if !filepath.IsAbs(root) || !filepath.IsAbs(workspaceRoot) || projects == nil || toolchains == nil || len(workers) == 0 {
		return nil, fmt.Errorf("%w: roots, projects, toolchains, and workers are required", ErrAssistantUnavailable)
	}
	for provider, configured := range workers {
		if !provider.IsValid() || configured.Service == nil || strings.TrimSpace(configured.AgentProfileID) == "" {
			return nil, fmt.Errorf("%w: invalid worker for %q", ErrAssistantUnavailable, provider)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "assistants"), 0o700); err != nil {
		return nil, fmt.Errorf("%w: create assistant storage", ErrAssistantUnavailable)
	}
	return &Assistant{root: root, workspaceRoot: workspaceRoot, projects: projects, toolchains: toolchains,
		workers: workers, now: func() time.Time { return time.Now().UTC() }}, nil
}

func (assistant *Assistant) Start(ctx context.Context, projectID string, provider project.AgentProvider, model, message, idempotencyKey string) (AssistantSession, bool, error) {
	assistant.mu.Lock()
	defer assistant.mu.Unlock()
	stored, err := assistant.projects.GetByID(ctx, projectID)
	if err != nil {
		return AssistantSession{}, false, err
	}
	worker, ok := assistant.workers[provider]
	message, model, key := strings.TrimSpace(message), strings.TrimSpace(model), strings.TrimSpace(idempotencyKey)
	if !ok || message == "" || key == "" || !validAssistantModel(model) {
		return AssistantSession{}, false, fmt.Errorf("%w: provider, exact model, message, and idempotency key are required", ErrInvalidManifest)
	}
	sessionID := assistantID(stored.ID, key)
	digest := assistantDigest(string(provider), model, message)
	record, err := assistant.readRecord(sessionID)
	created := false
	if errors.Is(err, ErrAssistantNotFound) {
		now := assistant.now()
		record = assistantRecord{AssistantSession: AssistantSession{ID: sessionID, ProjectID: stored.ID,
			Provider: provider, Model: model, Status: AssistantStatusRunning,
			Messages: []AssistantMessage{{Role: "user", Text: message, OccurredAt: now}}, CreatedAt: now, UpdatedAt: now},
			AttemptID: sessionID + ":turn:1", Turn: 1, InitialKey: key, InitialDigest: digest,
			PendingKey: key, PendingDigest: digest,
			Mutations: map[string]assistantMutation{key: {Digest: digest, AttemptID: sessionID + ":turn:1"}}}
		if err := assistant.writeRecord(record); err != nil {
			return AssistantSession{}, false, err
		}
		created = true
	} else if err != nil {
		return AssistantSession{}, false, err
	} else if record.InitialKey != key || record.InitialDigest != digest || record.ProjectID != stored.ID {
		return AssistantSession{}, false, ErrAssistantConflict
	}
	if !created && record.ProviderSessionID != "" {
		if record.Status == AssistantStatusRunning {
			record, err = assistant.refresh(ctx, record)
			if err != nil {
				return AssistantSession{}, false, err
			}
		}
		return record.AssistantSession, false, nil
	}
	if err := assistant.ensureWorkspace(record.ID); err != nil {
		return AssistantSession{}, false, err
	}
	attempt, _, err := worker.Service.PutAttempt(ctx, workerhttp.MutationIdentity{
		AttemptReference: workerhttp.AttemptReference{SessionID: record.ID, AttemptID: record.AttemptID}, IdempotencyKey: key,
	}, workerhttp.PutAttemptRequest{Mode: workerhttp.AttemptModeStart, Assignment: workerhttp.Assignment{
		AgentProfileID: worker.AgentProfileID, Model: model, ProjectID: stored.ID, FeatureID: record.ID,
		Role: workerhttp.RoleConsultant, WorkspaceID: record.ID,
	}, Instructions: assistantInitialInstructions(stored.Name, message), OutputContract: workerhttp.OutputContractToolchainSetup})
	if err != nil {
		return AssistantSession{}, created, err
	}
	record.ProviderSessionID = attempt.ProviderSessionID
	record.UpdatedAt = assistant.now()
	if err := assistant.writeRecord(record); err != nil {
		return AssistantSession{}, created, err
	}
	return record.AssistantSession, created, nil
}

func (assistant *Assistant) Get(ctx context.Context, projectID, sessionID string) (AssistantSession, error) {
	assistant.mu.Lock()
	defer assistant.mu.Unlock()
	if _, err := assistant.projects.GetByID(ctx, projectID); err != nil {
		return AssistantSession{}, err
	}
	record, err := assistant.readRecord(sessionID)
	if err != nil || record.ProjectID != projectID {
		if err == nil {
			err = ErrAssistantNotFound
		}
		return AssistantSession{}, err
	}
	if record.Status == AssistantStatusRunning {
		record, err = assistant.refresh(ctx, record)
		if err != nil {
			return AssistantSession{}, err
		}
	}
	return record.AssistantSession, nil
}

func (assistant *Assistant) Reply(ctx context.Context, projectID, sessionID, message, idempotencyKey string) (AssistantSession, bool, error) {
	assistant.mu.Lock()
	defer assistant.mu.Unlock()
	if _, err := assistant.projects.GetByID(ctx, projectID); err != nil {
		return AssistantSession{}, false, err
	}
	record, err := assistant.readRecord(sessionID)
	if err != nil || record.ProjectID != projectID {
		if err == nil {
			err = ErrAssistantNotFound
		}
		return AssistantSession{}, false, err
	}
	if record.Status == AssistantStatusRunning {
		record, err = assistant.refresh(ctx, record)
		if err != nil {
			return AssistantSession{}, false, err
		}
	}
	message, key := strings.TrimSpace(message), strings.TrimSpace(idempotencyKey)
	if message == "" || key == "" {
		return AssistantSession{}, false, ErrInvalidManifest
	}
	digest := assistantDigest(message)
	prior, replay := record.Mutations[key]
	if replay {
		if prior.Digest != digest {
			return AssistantSession{}, false, ErrAssistantConflict
		}
		if prior.AttemptID != record.AttemptID {
			return record.AssistantSession, false, nil
		}
	} else {
		if record.Status != AssistantStatusWaitingForUser || record.ProviderSessionID == "" {
			return AssistantSession{}, false, ErrAssistantNotReady
		}
		record.Turn++
		record.AttemptID = fmt.Sprintf("%s:turn:%d", record.ID, record.Turn)
		record.PendingKey, record.PendingDigest = key, digest
		if record.Mutations == nil {
			record.Mutations = make(map[string]assistantMutation)
		}
		record.Mutations[key] = assistantMutation{Digest: digest, AttemptID: record.AttemptID}
		record.Status, record.Message, record.Proposal = AssistantStatusRunning, "", nil
		record.UpdatedAt = assistant.now()
		record.Messages = append(record.Messages, AssistantMessage{Role: "user", Text: message, OccurredAt: record.UpdatedAt})
		if err := assistant.writeRecord(record); err != nil {
			return AssistantSession{}, false, err
		}
	}
	configured := assistant.workers[record.Provider]
	attempt, created, err := configured.Service.PutAttempt(ctx, workerhttp.MutationIdentity{
		AttemptReference: workerhttp.AttemptReference{SessionID: record.ID, AttemptID: record.AttemptID}, IdempotencyKey: key,
	}, workerhttp.PutAttemptRequest{Mode: workerhttp.AttemptModeResume, Assignment: workerhttp.Assignment{
		AgentProfileID: configured.AgentProfileID, Model: record.Model, ProjectID: record.ProjectID,
		FeatureID: record.ID, Role: workerhttp.RoleConsultant, WorkspaceID: record.ID,
	}, ProviderSessionID: record.ProviderSessionID, Instructions: assistantReplyInstructions(message), OutputContract: workerhttp.OutputContractToolchainSetup})
	if err != nil {
		return AssistantSession{}, false, err
	}
	record.ProviderSessionID, record.UpdatedAt = attempt.ProviderSessionID, assistant.now()
	if err := assistant.writeRecord(record); err != nil {
		return AssistantSession{}, created, err
	}
	return record.AssistantSession, created, nil
}

func (assistant *Assistant) Apply(ctx context.Context, projectID, sessionID string) (Manifest, error) {
	assistant.mu.Lock()
	defer assistant.mu.Unlock()
	record, err := assistant.readRecord(sessionID)
	if err != nil || record.ProjectID != projectID {
		if err == nil {
			err = ErrAssistantNotFound
		}
		return Manifest{}, err
	}
	if record.Status == AssistantStatusRunning {
		record, err = assistant.refresh(ctx, record)
		if err != nil {
			return Manifest{}, err
		}
	}
	if record.Status == AssistantStatusApplied {
		return assistant.toolchains.Get(ctx, projectID)
	}
	if record.Status != AssistantStatusProposalReady || record.Proposal == nil {
		return Manifest{}, ErrAssistantNotReady
	}
	manifest, err := assistant.toolchains.Configure(ctx, projectID, Manifest{
		Source: SourceAssistant, Tools: record.Proposal.Tools, Services: record.Proposal.Services,
	})
	if err != nil {
		return Manifest{}, err
	}
	record.Status, record.UpdatedAt = AssistantStatusApplied, assistant.now()
	if err := assistant.writeRecord(record); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func (assistant *Assistant) refresh(ctx context.Context, record assistantRecord) (assistantRecord, error) {
	configured, ok := assistant.workers[record.Provider]
	if !ok {
		return record, ErrAssistantUnavailable
	}
	attempt, err := configured.Service.GetAttempt(ctx, workerhttp.AttemptReference{SessionID: record.ID, AttemptID: record.AttemptID})
	if err != nil {
		return record, err
	}
	record.ProviderSessionID = attempt.ProviderSessionID
	if attempt.State == workerhttp.AttemptStateIndeterminate {
		record.Status, record.Message = AssistantStatusFailed, "The setup assistant turn could not be safely recovered. Start a new setup conversation or use the stack picker."
		record.UpdatedAt = assistant.now()
		record.Messages = append(record.Messages, AssistantMessage{Role: "assistant", Text: record.Message, OccurredAt: record.UpdatedAt})
		return record, assistant.writeAndReturn(record)
	}
	if attempt.State != workerhttp.AttemptStateTerminal {
		return record, assistant.writeAndReturn(record)
	}
	if attempt.Result == nil || attempt.Result.Outcome != workerhttp.OutcomeCompleted {
		record.Status, record.Message = AssistantStatusFailed, "The setup assistant turn did not complete safely."
	} else if attempt.Result.Disposition == workerhttp.DispositionInputRequired {
		record.Status, record.Message = AssistantStatusWaitingForUser, attempt.Result.Summary
	} else if attempt.Result.ToolchainProposal != nil {
		manifest, normalizeErr := NormalizeManifest(Manifest{Source: SourceAssistant,
			Tools: attempt.Result.ToolchainProposal.Tools, Services: attempt.Result.ToolchainProposal.Services})
		if normalizeErr != nil {
			record.Status, record.Message = AssistantStatusFailed, "The setup assistant proposed an unsupported or non-explicit toolchain."
		} else {
			record.Status, record.Message = AssistantStatusProposalReady, attempt.Result.Summary
			record.Proposal = &Suggestion{Tools: manifest.Tools, Services: manifest.Services, Evidence: []string{"setup assistant"}, Confidence: "assistant"}
		}
	} else {
		record.Status, record.Message = AssistantStatusFailed, "The setup assistant returned no usable proposal."
	}
	record.UpdatedAt = assistant.now()
	record.Messages = append(record.Messages, AssistantMessage{Role: "assistant", Text: record.Message, OccurredAt: record.UpdatedAt})
	return record, assistant.writeAndReturn(record)
}

func (assistant *Assistant) writeAndReturn(record assistantRecord) error {
	return assistant.writeRecord(record)
}

func (assistant *Assistant) ensureWorkspace(sessionID string) error {
	path := filepath.Join(assistant.workspaceRoot, sessionID)
	if filepath.Dir(path) != assistant.workspaceRoot {
		return ErrAssistantUnavailable
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("%w: create setup workspace", ErrAssistantUnavailable)
	}
	return nil
}

func (assistant *Assistant) readRecord(sessionID string) (assistantRecord, error) {
	if !assistantIDPattern.MatchString(sessionID) {
		return assistantRecord{}, ErrAssistantNotFound
	}
	contents, err := os.ReadFile(filepath.Join(assistant.root, "assistants", sessionID+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return assistantRecord{}, ErrAssistantNotFound
	}
	if err != nil {
		return assistantRecord{}, ErrAssistantUnavailable
	}
	var record assistantRecord
	if json.Unmarshal(contents, &record) != nil {
		return assistantRecord{}, ErrAssistantUnavailable
	}
	return record, nil
}

func (assistant *Assistant) writeRecord(record assistantRecord) error {
	encoded, err := json.Marshal(record)
	if err != nil {
		return ErrAssistantUnavailable
	}
	if err := atomicWrite(filepath.Join(assistant.root, "assistants", record.ID+".json"), append(encoded, '\n'), 0o600); err != nil {
		return fmt.Errorf("%w: persist session", ErrAssistantUnavailable)
	}
	return nil
}

func assistantID(projectID, key string) string {
	digest := sha256.Sum256([]byte(projectID + "\x00" + key))
	return "tcs_" + hex.EncodeToString(digest[:12])
}

func assistantDigest(parts ...string) string {
	digest := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(digest[:])
}

func validAssistantModel(model string) bool {
	_, err := (project.AgentModels{Lead: model, Reviewer: model}).Normalize()
	return err == nil
}

func assistantInitialInstructions(projectName, message string) string {
	return "You are Commitarium's project setup assistant. Help the user choose a practical software stack " +
		"for project " + projectName + ". This is consultation only: do not edit files, run commands, install " +
		"software, or begin implementation. Supported runtime keys are bun, deno, go, java, node, php, python, " +
		"ruby, and rust. Use explicit versions only, never latest or system. Services such as PostgreSQL may be " +
		"recorded as requirements but are not provisioned in this release. Ask only the smallest useful question " +
		"when a material choice remains; otherwise propose the exact tools and any service requirements. " +
		"The user said:\n\n" + message
}

func assistantReplyInstructions(message string) string {
	return "Continue the same project stack consultation using the existing conversation. Do not edit files, " +
		"run commands, install software, or begin implementation. Ask only if a material ambiguity remains; " +
		"otherwise return an exact supported toolchain proposal. The user replied:\n\n" + message
}
