// Package workerhttp defines the versioned wire contract and HTTP transport
// shared by the coordinator and provider workers. Transport-neutral
// orchestration types remain in internal/worker.
package workerhttp

import "time"

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
	ProtocolVersion       string       `json:"protocol_version"`
	Provider              Provider     `json:"provider"`
	Capabilities          []Capability `json:"capabilities"`
	MaxConcurrentAttempts int          `json:"max_concurrent_attempts"`
}

// AttemptReference identifies one supervised provider process. SessionID is
// the durable coordinator conversation; AttemptID fences one concrete process
// incarnation within that conversation.
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
	ProjectID      string `json:"project_id"`
	FeatureID      string `json:"feature_id"`
	Role           Role   `json:"role"`
	WorkspaceID    string `json:"workspace_id"`
}

type PutAttemptRequest struct {
	Mode              AttemptMode    `json:"mode"`
	Assignment        Assignment     `json:"assignment"`
	Instructions      string         `json:"instructions"`
	OutputContract    OutputContract `json:"output_contract,omitempty"`
	ProviderSessionID string         `json:"provider_session_id,omitempty"`
}

type OutputContract string

const (
	OutputContractPlanningLead         OutputContract = "planning_lead"
	OutputContractImplementationLead   OutputContract = "implementation_lead"
	OutputContractImplementationReview OutputContract = "implementation_reviewer"
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

const (
	DispositionSucceeded        Disposition = "succeeded"
	DispositionChangesRequested Disposition = "changes_requested"
	DispositionInputRequired    Disposition = "input_required"
)

type TerminalResult struct {
	Outcome     Outcome                    `json:"outcome"`
	Disposition Disposition                `json:"disposition,omitempty"`
	Summary     string                     `json:"summary"`
	Publication *ImplementationPublication `json:"publication,omitempty"`
	Review      *ReviewPublication         `json:"review,omitempty"`
	Error       *ProtocolError             `json:"error,omitempty"`
}

type ImplementationPublication struct {
	CommitID          string `json:"commit_id"`
	PullRequestNumber int64  `json:"pull_request_number"`
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

type Event struct {
	AttemptReference
	Sequence           int64               `json:"sequence"`
	Type               EventType           `json:"type"`
	Text               string              `json:"text"`
	OccurredAt         time.Time           `json:"occurred_at"`
	Redaction          RedactionMetadata   `json:"redaction"`
	Truncation         *TruncationMetadata `json:"truncation,omitempty"`
	RecoveryAssessment *RecoveryAssessment `json:"recovery_assessment,omitempty"`
}

type ErrorCode string

const (
	ErrorInvalidRequest         ErrorCode = "invalid_request"
	ErrorUnauthorized           ErrorCode = "unauthorized"
	ErrorNotFound               ErrorCode = "not_found"
	ErrorMethodNotAllowed       ErrorCode = "method_not_allowed"
	ErrorUnsupportedMediaType   ErrorCode = "unsupported_media_type"
	ErrorRequestTooLarge        ErrorCode = "request_too_large"
	ErrorUnsupportedOperation   ErrorCode = "unsupported_operation"
	ErrorAttemptActive          ErrorCode = "attempt_active"
	ErrorAttemptConflict        ErrorCode = "attempt_conflict"
	ErrorStaleAttempt           ErrorCode = "stale_attempt"
	ErrorProviderSessionMissing ErrorCode = "provider_session_missing"
	ErrorProfileUnavailable     ErrorCode = "profile_unavailable"
	ErrorWorkspaceUnavailable   ErrorCode = "workspace_unavailable"
	ErrorConfigurationMismatch  ErrorCode = "configuration_mismatch"
	ErrorIndeterminateState     ErrorCode = "indeterminate_state"
	ErrorRedactionFailed        ErrorCode = "redaction_failed"
	ErrorInvalidEventStream     ErrorCode = "invalid_event_stream"
	ErrorInternal               ErrorCode = "internal_error"
)

type ProtocolError struct {
	Code      ErrorCode `json:"code"`
	Message   string    `json:"message"`
	Retryable bool      `json:"retryable"`
}

type ErrorResponse struct {
	Error ProtocolError `json:"error"`
}
