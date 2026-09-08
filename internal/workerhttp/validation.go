package workerhttp

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

const (
	maxProviderSessionIDBytes = 1024
	maxReasonBytes            = 4096
	maxSummaryBytes           = 64 * 1024
	maxErrorMessageBytes      = 4096
)

var (
	ErrInvalidContract = errors.New("invalid worker HTTP contract value")
	safeIDPattern      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	digestPattern      = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

func (provider Provider) IsValid() bool {
	return provider == ProviderCodex || provider == ProviderClaudeCode
}

func (capability Capability) IsValid() bool {
	switch capability {
	case CapabilityStart,
		CapabilityResume,
		CapabilityMessage,
		CapabilityPause,
		CapabilityContinue,
		CapabilityCooperativeStop,
		CapabilityForceStop,
		CapabilityEventReplay:
		return true
	default:
		return false
	}
}

func (response HealthResponse) Validate() error {
	if response.Status != HealthStatusOK {
		return invalid("health status %q is not recognized", response.Status)
	}
	return validateProtocolVersion(response.ProtocolVersion)
}

func (response CapabilitiesResponse) Validate() error {
	if err := validateProtocolVersion(response.ProtocolVersion); err != nil {
		return err
	}
	if !response.Provider.IsValid() {
		return invalid("provider %q is not recognized", response.Provider)
	}
	if len(response.Capabilities) == 0 {
		return invalid("at least one capability is required")
	}
	seen := make(map[Capability]struct{}, len(response.Capabilities))
	for _, capability := range response.Capabilities {
		if !capability.IsValid() {
			return invalid("capability %q is not recognized", capability)
		}
		if _, exists := seen[capability]; exists {
			return invalid("capability %q is duplicated", capability)
		}
		seen[capability] = struct{}{}
	}
	if response.MaxConcurrentAttempts < 1 {
		return invalid("maximum concurrent attempts must be positive")
	}
	return nil
}

func (reference AttemptReference) Validate() error {
	if err := validateID("session ID", reference.SessionID); err != nil {
		return err
	}
	return validateID("attempt ID", reference.AttemptID)
}

func (identity MutationIdentity) Validate() error {
	if err := identity.AttemptReference.Validate(); err != nil {
		return err
	}
	return validateID("idempotency key", identity.IdempotencyKey)
}

func (mode AttemptMode) IsValid() bool {
	return mode == AttemptModeStart || mode == AttemptModeResume
}

func (role Role) IsValid() bool {
	switch role {
	case RoleLead, RoleConsultant, RoleCoder, RoleReviewer:
		return true
	default:
		return false
	}
}

func (assignment Assignment) Validate() error {
	for _, field := range []struct {
		name  string
		value string
	}{
		{name: "agent profile ID", value: assignment.AgentProfileID},
		{name: "project ID", value: assignment.ProjectID},
		{name: "feature ID", value: assignment.FeatureID},
		{name: "workspace ID", value: assignment.WorkspaceID},
	} {
		if err := validateID(field.name, field.value); err != nil {
			return err
		}
	}
	if !assignment.Role.IsValid() {
		return invalid("role %q is not recognized", assignment.Role)
	}
	if assignment.ConfigurationRevision < 1 {
		return invalid("configuration revision must be positive")
	}
	if !digestPattern.MatchString(assignment.MaterializationDigest) {
		return invalid("materialization digest must be a lowercase sha256 digest")
	}
	return nil
}

func (request PutAttemptRequest) Validate(identity MutationIdentity) error {
	if err := identity.Validate(); err != nil {
		return err
	}
	if !request.Mode.IsValid() {
		return invalid("attempt mode %q is not recognized", request.Mode)
	}
	if err := request.Assignment.Validate(); err != nil {
		return err
	}
	if err := validateRequiredText("instructions", request.Instructions, MaxInstructionsBytes); err != nil {
		return err
	}
	providerSessionID := strings.TrimSpace(request.ProviderSessionID)
	if len(providerSessionID) > maxProviderSessionIDBytes {
		return invalid("provider session ID exceeds %d bytes", maxProviderSessionIDBytes)
	}
	switch request.Mode {
	case AttemptModeStart:
		if providerSessionID != "" {
			return invalid("start attempt cannot include a provider session ID")
		}
	case AttemptModeResume:
		if providerSessionID == "" {
			return invalid("resume attempt requires a provider session ID")
		}
	}
	return nil
}

func (state AttemptState) IsValid() bool {
	switch state {
	case AttemptStateStarting,
		AttemptStateRunning,
		AttemptStatePauseRequested,
		AttemptStatePaused,
		AttemptStateStopRequested,
		AttemptStateTerminal,
		AttemptStateIndeterminate:
		return true
	default:
		return false
	}
}

func (state AttemptState) IsTerminal() bool {
	return state == AttemptStateTerminal
}

// MayStillBeActive deliberately includes indeterminate. Uncertainty fences a
// replacement attempt until reconciliation or user review proves it is safe.
func (state AttemptState) MayStillBeActive() bool {
	return state.IsValid() && state != AttemptStateTerminal
}

func (state AttemptState) CanTransitionTo(next AttemptState) bool {
	if !state.IsValid() || !next.IsValid() || state == next || state.IsTerminal() {
		return false
	}
	switch state {
	case AttemptStateStarting:
		return next == AttemptStateRunning ||
			next == AttemptStateStopRequested ||
			next == AttemptStateTerminal ||
			next == AttemptStateIndeterminate
	case AttemptStateRunning:
		return next == AttemptStatePauseRequested ||
			next == AttemptStateStopRequested ||
			next == AttemptStateTerminal ||
			next == AttemptStateIndeterminate
	case AttemptStatePauseRequested:
		return next == AttemptStatePaused ||
			next == AttemptStateRunning ||
			next == AttemptStateStopRequested ||
			next == AttemptStateTerminal ||
			next == AttemptStateIndeterminate
	case AttemptStatePaused:
		return next == AttemptStateRunning ||
			next == AttemptStateStopRequested ||
			next == AttemptStateTerminal ||
			next == AttemptStateIndeterminate
	case AttemptStateStopRequested:
		return next == AttemptStateTerminal || next == AttemptStateIndeterminate
	case AttemptStateIndeterminate:
		return next == AttemptStateTerminal
	default:
		return false
	}
}

func (attempt Attempt) Validate() error {
	if err := attempt.AttemptReference.Validate(); err != nil {
		return err
	}
	if !attempt.Mode.IsValid() {
		return invalid("attempt mode %q is not recognized", attempt.Mode)
	}
	if err := attempt.Assignment.Validate(); err != nil {
		return err
	}
	if !attempt.State.IsValid() {
		return invalid("attempt state %q is not recognized", attempt.State)
	}
	if attempt.LatestEventSequence < 0 {
		return invalid("latest event sequence cannot be negative")
	}
	if err := validateTimeline(attempt.StartedAt, attempt.UpdatedAt, attempt.EndedAt); err != nil {
		return err
	}
	providerSessionID := strings.TrimSpace(attempt.ProviderSessionID)
	if len(providerSessionID) > maxProviderSessionIDBytes {
		return invalid("provider session ID exceeds %d bytes", maxProviderSessionIDBytes)
	}
	if attempt.Mode == AttemptModeResume && providerSessionID == "" {
		return invalid("resume attempt requires a provider session ID")
	}
	if requiresProviderSessionID(attempt.State) && providerSessionID == "" {
		return invalid("state %q requires a provider session ID", attempt.State)
	}
	if attempt.State.IsTerminal() {
		if attempt.EndedAt == nil {
			return invalid("terminal attempt requires an end time")
		}
		if attempt.Result == nil {
			return invalid("terminal attempt requires a result")
		}
		if err := attempt.Result.Validate(); err != nil {
			return err
		}
		if attempt.Result.Outcome == OutcomeCompleted && providerSessionID == "" {
			return invalid("completed attempt requires a provider session ID")
		}
		return nil
	}
	if attempt.EndedAt != nil {
		return invalid("nonterminal attempt cannot have an end time")
	}
	if attempt.Result != nil {
		return invalid("nonterminal attempt cannot have a result")
	}
	return nil
}

func (outcome Outcome) IsValid() bool {
	return outcome == OutcomeCompleted || outcome == OutcomeStopped || outcome == OutcomeFailed
}

func (disposition Disposition) IsValid() bool {
	switch disposition {
	case DispositionSucceeded, DispositionChangesRequested, DispositionInputRequired:
		return true
	default:
		return false
	}
}

func (result TerminalResult) Validate() error {
	if !result.Outcome.IsValid() {
		return invalid("outcome %q is not recognized", result.Outcome)
	}
	if err := validateRequiredText("terminal summary", result.Summary, maxSummaryBytes); err != nil {
		return err
	}
	switch result.Outcome {
	case OutcomeCompleted:
		if !result.Disposition.IsValid() {
			return invalid("completed outcome requires a disposition")
		}
		if result.Error != nil {
			return invalid("completed outcome cannot include an error")
		}
	case OutcomeStopped:
		if result.Disposition != "" {
			return invalid("stopped outcome cannot include a disposition")
		}
		if result.Error != nil {
			return invalid("stopped outcome cannot include an error")
		}
	case OutcomeFailed:
		if result.Disposition != "" {
			return invalid("failed outcome cannot include a disposition")
		}
		if result.Error == nil {
			return invalid("failed outcome requires an error")
		}
		if err := result.Error.Validate(); err != nil {
			return err
		}
	}
	return nil
}

func (command CommandType) IsValid() bool {
	switch command {
	case CommandMessage, CommandPause, CommandContinue, CommandStop:
		return true
	default:
		return false
	}
}

func (request CommandRequest) Validate(identity MutationIdentity) error {
	if err := identity.Validate(); err != nil {
		return err
	}
	if !request.Type.IsValid() {
		return invalid("command type %q is not recognized", request.Type)
	}
	if request.Type == CommandMessage {
		return validateRequiredText("command message", request.Message, MaxEventTextBytes)
	}
	if strings.TrimSpace(request.Message) != "" {
		return invalid("control command cannot contain a message")
	}
	return nil
}

func (request ForceStopRequest) Validate(identity MutationIdentity) error {
	if err := identity.Validate(); err != nil {
		return err
	}
	return validateRequiredText("force-stop reason", request.Reason, maxReasonBytes)
}

func (eventType EventType) IsValid() bool {
	switch eventType {
	case EventMessage,
		EventActivity,
		EventInputRequired,
		EventPauseAcknowledged,
		EventContinued,
		EventRecoveryAssessment,
		EventAttemptTerminal,
		EventRedactionFailure:
		return true
	default:
		return false
	}
}

func (category RedactionCategory) IsValid() bool {
	switch category {
	case RedactionCredential,
		RedactionAuthentication,
		RedactionPrivateKey,
		RedactionConnectionString,
		RedactionInternalPath:
		return true
	default:
		return false
	}
}

func (metadata RedactionMetadata) Validate() error {
	if metadata.Count < 0 {
		return invalid("redaction count cannot be negative")
	}
	if metadata.Count == 0 && len(metadata.Categories) != 0 {
		return invalid("redaction categories require a positive count")
	}
	if metadata.Count > 0 && len(metadata.Categories) == 0 {
		return invalid("positive redaction count requires at least one category")
	}
	seen := make(map[RedactionCategory]struct{}, len(metadata.Categories))
	for _, category := range metadata.Categories {
		if !category.IsValid() {
			return invalid("redaction category %q is not recognized", category)
		}
		if _, exists := seen[category]; exists {
			return invalid("redaction category %q is duplicated", category)
		}
		seen[category] = struct{}{}
	}
	return nil
}

func (metadata TruncationMetadata) Validate() error {
	if metadata.RetainedBytes < 0 {
		return invalid("retained byte count cannot be negative")
	}
	if metadata.OriginalBytes <= metadata.RetainedBytes {
		return invalid("original byte count must exceed retained byte count")
	}
	if metadata.RetainedBytes > MaxEventTextBytes {
		return invalid("retained byte count exceeds %d bytes", MaxEventTextBytes)
	}
	return nil
}

func (event Event) Validate() error {
	if err := event.AttemptReference.Validate(); err != nil {
		return err
	}
	if event.Sequence < 1 {
		return invalid("event sequence must be positive")
	}
	if !event.Type.IsValid() {
		return invalid("event type %q is not recognized", event.Type)
	}
	if err := validateRequiredText("event text", event.Text, MaxEventTextBytes); err != nil {
		return err
	}
	if event.OccurredAt.IsZero() {
		return invalid("event occurrence time is required")
	}
	if !isUTC(event.OccurredAt) {
		return invalid("event occurrence time must be UTC")
	}
	if err := event.Redaction.Validate(); err != nil {
		return err
	}
	if event.Truncation != nil {
		if err := event.Truncation.Validate(); err != nil {
			return err
		}
	}
	if event.Type == EventRecoveryAssessment {
		if event.RecoveryAssessment == nil {
			return invalid("recovery assessment event requires assessment data")
		}
	} else if event.RecoveryAssessment != nil {
		return invalid("event type %q cannot include recovery assessment data", event.Type)
	}
	return nil
}

func (code ErrorCode) IsValid() bool {
	switch code {
	case ErrorInvalidRequest,
		ErrorUnauthorized,
		ErrorNotFound,
		ErrorAttemptActive,
		ErrorAttemptConflict,
		ErrorStaleAttempt,
		ErrorProviderSessionMissing,
		ErrorProfileUnavailable,
		ErrorWorkspaceUnavailable,
		ErrorConfigurationMismatch,
		ErrorIndeterminateState,
		ErrorRedactionFailed,
		ErrorInternal:
		return true
	default:
		return false
	}
}

func (protocolError ProtocolError) Validate() error {
	if !protocolError.Code.IsValid() {
		return invalid("error code %q is not recognized", protocolError.Code)
	}
	return validateRequiredText("error message", protocolError.Message, maxErrorMessageBytes)
}

func (response ErrorResponse) Validate() error {
	return response.Error.Validate()
}

func requiresProviderSessionID(state AttemptState) bool {
	switch state {
	case AttemptStateRunning,
		AttemptStatePauseRequested,
		AttemptStatePaused:
		return true
	default:
		return false
	}
}

func validateProtocolVersion(version string) error {
	if version != ProtocolVersion {
		return invalid("protocol version %q is not supported", version)
	}
	return nil
}

func validateID(name string, value string) error {
	if !safeIDPattern.MatchString(value) {
		return invalid("%s must be a path-safe identifier of at most 128 bytes", name)
	}
	return nil
}

func validateRequiredText(name string, value string, maximumBytes int) error {
	if strings.TrimSpace(value) == "" {
		return invalid("%s is required", name)
	}
	if len(value) > maximumBytes {
		return invalid("%s exceeds %d bytes", name, maximumBytes)
	}
	return nil
}

func validateTimeline(startedAt time.Time, updatedAt time.Time, endedAt *time.Time) error {
	if startedAt.IsZero() {
		return invalid("attempt start time is required")
	}
	if !isUTC(startedAt) {
		return invalid("attempt start time must be UTC")
	}
	if !isUTC(updatedAt) {
		return invalid("attempt update time must be UTC")
	}
	if endedAt != nil && !isUTC(*endedAt) {
		return invalid("attempt end time must be UTC")
	}
	if updatedAt.Before(startedAt) {
		return invalid("attempt update time precedes start time")
	}
	if endedAt != nil && endedAt.Before(startedAt) {
		return invalid("attempt end time precedes start time")
	}
	if endedAt != nil && updatedAt.Before(*endedAt) {
		return invalid("attempt update time precedes end time")
	}
	return nil
}

func isUTC(value time.Time) bool {
	_, offset := value.Zone()
	return offset == 0
}

func invalid(format string, arguments ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidContract, fmt.Sprintf(format, arguments...))
}
