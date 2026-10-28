// Package workerhttp defines the versioned wire contract and HTTP transport
// shared by the coordinator and provider workers. Transport-neutral
// orchestration types remain in internal/worker.
package workerhttp

import (
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/featureartifact"
)

const (
	ProtocolVersion      = "v1"
	APIBasePath          = "/internal/v1"
	MaxInstructionsBytes = 64 * 1024
	MaxEventTextBytes    = 64 * 1024
)

type Provider string

const (
	ProviderCodex      Provider = "codex"
	ProviderClaudeCode Provider = "claude_code"
)

type Capability string

const (
	CapabilityStart           Capability = "start"
	CapabilityResume          Capability = "resume"
	CapabilityMessage         Capability = "message"
	CapabilityPause           Capability = "pause"
	CapabilityContinue        Capability = "continue"
	CapabilityCooperativeStop Capability = "cooperative_stop"
	CapabilityForceStop       Capability = "force_stop"
	CapabilityEventReplay     Capability = "event_replay"
)

type HealthStatus string

const HealthStatusOK HealthStatus = "ok"

type HealthResponse struct {
	Status          HealthStatus `json:"status"`
	ProtocolVersion string       `json:"protocol_version"`
}

type CapabilitiesResponse struct {
	ProtocolVersion string       `json:"protocol_version"`
	Provider        Provider     `json:"provider"`
	Capabilities    []Capability `json:"capabilities"`
	// MaxConcurrentAttempts is the worker's limit on simultaneous attempts;
	// 0 means it does not limit them (ADR-016).
	MaxConcurrentAttempts int `json:"max_concurrent_attempts"`
}

type Model struct {
	ID                        string   `json:"id"`
	DisplayName               string   `json:"display_name"`
	DefaultReasoningEffort    string   `json:"default_reasoning_effort,omitempty"`
	SupportedReasoningEfforts []string `json:"supported_reasoning_efforts,omitempty"`
}

// UsageWindow is one of the provider account's usage limits as the agent
// last reported it while working.
type UsageWindow struct {
	Name        string     `json:"name"`
	UsedPercent *float64   `json:"used_percent,omitempty"`
	ResetsAt    *time.Time `json:"resets_at,omitempty"`
	Limited     bool       `json:"limited"`
}

// UsageResponse is the agent's latest usage report; ObservedAt is unset
// before the agent has worked since it started.
type UsageResponse struct {
	Provider   Provider      `json:"provider"`
	Windows    []UsageWindow `json:"windows"`
	ObservedAt *time.Time    `json:"observed_at,omitempty"`
}

// ActivityResponse is whether the worker is launching or running provider
// turns, and how many; restarting an idle worker cuts nothing off. CLIVersion
// is the provider CLI version the worker resolved at startup, so the desktop
// can tell whether a CLI installed since needs a restart.
type ActivityResponse struct {
	Busy       bool   `json:"busy"`
	Attempts   int    `json:"attempts"`
	CLIVersion string `json:"cli_version,omitempty"`
}

type ModelsResponse struct {
	ProtocolVersion string    `json:"protocol_version"`
	Provider        Provider  `json:"provider"`
	Models          []Model   `json:"models"`
	FetchedAt       time.Time `json:"fetched_at"`
}

// AttemptReference identifies one durable provider turn. SessionID is the
// coordinator conversation; AttemptID fences one logical turn across worker
// process replacement and reconnection to the same provider conversation.
type AttemptReference struct {
	SessionID string `json:"session_id"`
	AttemptID string `json:"attempt_id"`
}

// MutationIdentity combines path identity with the idempotency key supplied by
// the HTTP header. It is not itself serialized as a request body.
type MutationIdentity struct {
	AttemptReference
	IdempotencyKey string `json:"-"`
}

type AttemptMode string

const (
	AttemptModeStart  AttemptMode = "start"
	AttemptModeResume AttemptMode = "resume"
)

type Role string

const (
	RoleLead       Role = "lead"
	RoleConsultant Role = "consultant"
	RoleCoder      Role = "coder"
	RoleReviewer   Role = "reviewer"
)

type Assignment struct {
	AgentProfileID string `json:"agent_profile_id"`
	Model          string `json:"model,omitempty"`
	// Effort is the reasoning effort for the turn; empty keeps the model's
	// default (ADR-031).
	Effort      string `json:"effort,omitempty"`
	ProjectID   string `json:"project_id"`
	FeatureID   string `json:"feature_id"`
	Role        Role   `json:"role"`
	WorkspaceID string `json:"workspace_id"`
}

type PutAttemptRequest struct {
	Mode              AttemptMode     `json:"mode"`
	Assignment        Assignment      `json:"assignment"`
	Instructions      string          `json:"instructions"`
	OutputContract    OutputContract  `json:"output_contract,omitempty"`
	WorkspaceAccess   WorkspaceAccess `json:"workspace_access,omitempty"`
	ProviderSessionID string          `json:"provider_session_id,omitempty"`
	// Web lets a read-only turn search and read the web without asking,
	// for research (ADR-035).
	Web bool `json:"web,omitempty"`
}

type WorkspaceAccess string

const (
	WorkspaceAccessReadOnly  WorkspaceAccess = "read_only"
	WorkspaceAccessReadWrite WorkspaceAccess = "read_write"
)

type OutputContract string

const (
	OutputContractPlanningLead            OutputContract = "planning_lead"
	OutputContractGoalClarification       OutputContract = "goal_clarification"
	OutputContractImplementationLead      OutputContract = "implementation_lead"
	OutputContractAcceptanceTests         OutputContract = "acceptance_tests"
	OutputContractImplementationReview    OutputContract = "implementation_reviewer"
	OutputContractImplementationReadiness OutputContract = "implementation_lead_readiness"
	OutputContractPlanApproval            OutputContract = "plan_approval"
	OutputContractWorkOrderBrief          OutputContract = "work_order_brief"
	OutputContractIntervention            OutputContract = "intervention"
	OutputContractToolchainSetup          OutputContract = "toolchain_setup"
	OutputContractSessionOpening          OutputContract = "session_opening"
	OutputContractVisionPlan              OutputContract = "vision_plan"
	OutputContractAssistantTurn           OutputContract = "assistant_turn"
	OutputContractAssistantPlanningTurn   OutputContract = "assistant_planning_turn"
)

type AttemptState string

const (
	AttemptStateStarting       AttemptState = "starting"
	AttemptStateRunning        AttemptState = "running"
	AttemptStatePauseRequested AttemptState = "pause_requested"
	AttemptStatePaused         AttemptState = "paused"
	AttemptStateStopRequested  AttemptState = "stop_requested"
	AttemptStateTerminal       AttemptState = "terminal"
	AttemptStateIndeterminate  AttemptState = "indeterminate"
)

type Outcome string

const (
	OutcomeCompleted Outcome = "completed"
	OutcomeStopped   Outcome = "stopped"
	OutcomeFailed    Outcome = "failed"
)

type Disposition string

type InterventionEffect string

const (
	DispositionSucceeded        Disposition = "succeeded"
	DispositionChangesRequested Disposition = "changes_requested"
	DispositionInputRequired    Disposition = "input_required"
)

const (
	InterventionEffectGuidanceApplied       InterventionEffect = "guidance_applied"
	InterventionEffectClarificationRequired InterventionEffect = "clarification_required"
	InterventionEffectReplanningRequired    InterventionEffect = "replanning_required"
)

type TerminalResult struct {
	Outcome            Outcome                             `json:"outcome"`
	Disposition        Disposition                         `json:"disposition,omitempty"`
	Summary            string                              `json:"summary"`
	Publication        *ImplementationPublication          `json:"publication,omitempty"`
	Review             *ReviewPublication                  `json:"review,omitempty"`
	InterventionEffect InterventionEffect                  `json:"intervention_effect,omitempty"`
	ToolchainProposal  *ToolchainProposal                  `json:"toolchain_proposal,omitempty"`
	GoalDraft          *featureartifact.GoalDraft          `json:"goal_draft,omitempty"`
	ImplementationPlan *featureartifact.ImplementationPlan `json:"implementation_plan,omitempty"`
	AcceptanceTests    *featureartifact.AcceptanceTests    `json:"acceptance_tests,omitempty"`
	EnvironmentRequest *EnvironmentRequest                 `json:"environment_request,omitempty"`
	HandoffBrief       *featureartifact.HandoffBrief       `json:"handoff_brief,omitempty"`
	SessionTitle       string                              `json:"session_title,omitempty"`
	VisionPlan         *featureartifact.VisionPlan         `json:"vision_plan,omitempty"`
	AssistantTurn      *featureartifact.AssistantTurn      `json:"assistant_turn,omitempty"`
	Usage              *TokenUsage                         `json:"usage,omitempty"`
	Error              *ProtocolError                      `json:"error,omitempty"`
}

// TokenUsage is the provider-reported token consumption of one attempt.
// InputTokens excludes cached reads and cache writes.
type TokenUsage struct {
	InputTokens       int64 `json:"input_tokens"`
	CachedInputTokens int64 `json:"cached_input_tokens"`
	CacheWriteTokens  int64 `json:"cache_write_tokens"`
	OutputTokens      int64 `json:"output_tokens"`
	// Billing is "api_key" or "subscription" when the provider says how the
	// account pays; CostUSD the provider's own cost for the turn, when it
	// reports one.
	Billing string   `json:"billing,omitempty"`
	CostUSD *float64 `json:"cost_usd,omitempty"`
}

type ToolchainProposal struct {
	Tools    map[string]string   `json:"tools"`
	Services []string            `json:"services"`
	Run      *ToolchainRunConfig `json:"run,omitempty"`
}

type ToolchainRunConfig struct {
	Open ToolchainRunOpen `json:"open"`
}

type ToolchainRunOpen struct {
	Service string `json:"service"`
	Port    int    `json:"port"`
}

type EnvironmentRequest struct {
	SystemPackages []string `json:"system_packages"`
	Reason         string   `json:"reason"`
}

type ImplementationPublication struct {
	CommitID          string `json:"commit_id"`
	PullRequestNumber int64  `json:"pull_request_number"`
	// FindingAnswers answer a review's findings by number, in a review
	// response.
	FindingAnswers []FindingAnswer `json:"finding_answers,omitempty"`
}

// FindingAnswer is how the lead answered one numbered review finding: fixed,
// already_fixed, declined, or out_of_scope.
type FindingAnswer struct {
	Finding string `json:"finding"`
	Answer  string `json:"answer"`
}

// ChangesNothing reports whether answers, one per finding at least, all say
// the code needs no change: already fixed, declined, or out of scope. A
// review response that changes nothing then needs no new commit.
func ChangesNothing(answers []FindingAnswer) bool {
	for _, answer := range answers {
		if answer.Answer != "already_fixed" && answer.Answer != "declined" && answer.Answer != "out_of_scope" {
			return false
		}
	}
	return len(answers) > 0
}

type ReviewPublication struct {
	CommitID          string `json:"commit_id"`
	PullRequestNumber int64  `json:"pull_request_number"`
	ReviewID          int64  `json:"review_id"`
}

type Attempt struct {
	AttemptReference
	Mode                AttemptMode     `json:"mode"`
	Assignment          Assignment      `json:"assignment"`
	ProviderSessionID   string          `json:"provider_session_id,omitempty"`
	State               AttemptState    `json:"state"`
	LatestEventSequence int64           `json:"latest_event_sequence"`
	StartedAt           time.Time       `json:"started_at"`
	UpdatedAt           time.Time       `json:"updated_at"`
	EndedAt             *time.Time      `json:"ended_at,omitempty"`
	Result              *TerminalResult `json:"result,omitempty"`
}

type CommandType string

const (
	CommandMessage  CommandType = "message"
	CommandPause    CommandType = "pause"
	CommandContinue CommandType = "continue"
	CommandStop     CommandType = "stop"
)

type CommandRequest struct {
	Type    CommandType `json:"type"`
	Message string      `json:"message,omitempty"`
}

type ForceStopRequest struct {
	Reason string `json:"reason"`
}

// SupersedeRequest closes a previously indeterminate attempt only after the
// worker has proved that it no longer owns a live provider process for it.
type SupersedeRequest struct {
	Reason string `json:"reason"`
}

type EventType string

const (
	EventMessage            EventType = "message"
	EventPlanSubmitted      EventType = "plan_submitted"
	EventActivity           EventType = "activity"
	EventInputRequired      EventType = "input_required"
	EventPauseAcknowledged  EventType = "pause_acknowledged"
	EventContinued          EventType = "continued"
	EventRecoveryAssessment EventType = "recovery_assessment"
	EventAttemptTerminal    EventType = "attempt_terminal"
	EventRedactionFailure   EventType = "redaction_failure"
)

type RedactionCategory string

const (
	RedactionCredential       RedactionCategory = "credential"
	RedactionAuthentication   RedactionCategory = "authentication"
	RedactionPrivateKey       RedactionCategory = "private_key"
	RedactionConnectionString RedactionCategory = "connection_string"
	RedactionInternalPath     RedactionCategory = "internal_path"
)

type RedactionMetadata struct {
	Count      int                 `json:"count"`
	Categories []RedactionCategory `json:"categories,omitempty"`
}

type TruncationMetadata struct {
	OriginalBytes int `json:"original_bytes"`
	RetainedBytes int `json:"retained_bytes"`
}

type RecoveryAssessment struct {
	Consistent         bool `json:"consistent"`
	RequiresUserReview bool `json:"requires_user_review"`
}

type ActivityKind string

const (
	ActivityKindCommand    ActivityKind = "command"
	ActivityKindFileChange ActivityKind = "file_change"
	ActivityKindNarration  ActivityKind = "narration"
)

type FileOperation string

const (
	FileOperationCreated  FileOperation = "created"
	FileOperationModified FileOperation = "modified"
	FileOperationDeleted  FileOperation = "deleted"
	FileOperationRenamed  FileOperation = "renamed"
)

// Activity is structured observable metadata. Provider output and file
// contents are intentionally absent from this transport object.
type Activity struct {
	Kind       ActivityKind  `json:"kind"`
	Command    string        `json:"command,omitempty"`
	ExitCode   *int          `json:"exit_code,omitempty"`
	DurationMS *int64        `json:"duration_ms,omitempty"`
	Operation  FileOperation `json:"op,omitempty"`
	Path       string        `json:"path,omitempty"`
	OldPath    string        `json:"old_path,omitempty"`
	Additions  *int          `json:"additions,omitempty"`
	Deletions  *int          `json:"deletions,omitempty"`
}

type Event struct {
	AttemptReference
	Sequence           int64               `json:"sequence"`
	Type               EventType           `json:"type"`
	Text               string              `json:"text"`
	StreamID           string              `json:"stream_id,omitempty"`
	OccurredAt         time.Time           `json:"occurred_at"`
	Redaction          RedactionMetadata   `json:"redaction"`
	Truncation         *TruncationMetadata `json:"truncation,omitempty"`
	Activity           *Activity           `json:"activity,omitempty"`
	RecoveryAssessment *RecoveryAssessment `json:"recovery_assessment,omitempty"`
}

// MessagePreview is transient public prose for an in-progress final response.
// It deliberately has no durable sequence: SSE transports it without an id and
// reconnect cursors continue to refer only to Event.
type MessagePreview struct {
	AttemptReference
	StreamID   string              `json:"stream_id"`
	Text       string              `json:"text"`
	OccurredAt time.Time           `json:"occurred_at"`
	Redaction  RedactionMetadata   `json:"redaction"`
	Truncation *TruncationMetadata `json:"truncation,omitempty"`
}

type ErrorCode string

const (
	ErrorInvalidRequest          ErrorCode = "invalid_request"
	ErrorUnauthorized            ErrorCode = "unauthorized"
	ErrorNotFound                ErrorCode = "not_found"
	ErrorMethodNotAllowed        ErrorCode = "method_not_allowed"
	ErrorUnsupportedMediaType    ErrorCode = "unsupported_media_type"
	ErrorRequestTooLarge         ErrorCode = "request_too_large"
	ErrorUnsupportedOperation    ErrorCode = "unsupported_operation"
	ErrorAttemptActive           ErrorCode = "attempt_active"
	ErrorAttemptConflict         ErrorCode = "attempt_conflict"
	ErrorStaleAttempt            ErrorCode = "stale_attempt"
	ErrorProviderSessionMissing  ErrorCode = "provider_session_missing"
	ErrorProfileUnavailable      ErrorCode = "profile_unavailable"
	ErrorWorkspaceUnavailable    ErrorCode = "workspace_unavailable"
	ErrorToolchainUnavailable    ErrorCode = "toolchain_unavailable"
	ErrorModelCatalogUnavailable ErrorCode = "model_catalog_unavailable"
	ErrorConfigurationMismatch   ErrorCode = "configuration_mismatch"
	ErrorIncompleteResult        ErrorCode = "incomplete_result"
	ErrorProviderLoginRequired   ErrorCode = "provider_login_required"
	ErrorUsageLimit              ErrorCode = "usage_limit"
	ErrorUnsafeConcurrency       ErrorCode = "unsafe_concurrency"
	ErrorIndeterminateState      ErrorCode = "indeterminate_state"
	ErrorRedactionFailed         ErrorCode = "redaction_failed"
	ErrorInvalidEventStream      ErrorCode = "invalid_event_stream"
	ErrorInternal                ErrorCode = "internal_error"
)

type ProtocolError struct {
	Code      ErrorCode `json:"code"`
	Message   string    `json:"message"`
	Retryable bool      `json:"retryable"`
	// ResetsAt is when a usage_limit error's limit resets, when known.
	ResetsAt *time.Time `json:"resets_at,omitempty"`
}

type ErrorResponse struct {
	Error ProtocolError `json:"error"`
}
