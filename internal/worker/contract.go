package worker

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

type Role string

const (
	RoleLead       Role = "lead"
	RoleConsultant Role = "consultant"
	RoleCoder      Role = "coder"
	RoleReviewer   Role = "reviewer"
)

type SessionRequest struct {
	SessionID         string
	AttemptID         string
	FeatureID         string
	Role              Role
	Instructions      string
	OutputContract    OutputContract
	LaunchEnvironment LaunchEnvironment
}

// OutputContract asks a provider adapter to return one small, workflow-owned
// structured result instead of treating its final response as unconstrained
// prose. The zero value preserves the normal conversational behavior.
type OutputContract string

const (
	OutputContractPlanningLead         OutputContract = "planning_lead"
	OutputContractImplementationLead   OutputContract = "implementation_lead"
	OutputContractImplementationReview OutputContract = "implementation_reviewer"
)

// LaunchEnvironment is the worker-resolved view of the profile and workspace
// assigned to one provider attempt. Variables are explicit NAME=VALUE entries;
// a real adapter must not inherit the worker service's environment implicitly.
//
// Secret values are deliberately not part of the coordinator-to-worker HTTP
// request or worker journal. A trusted worker-local resolver may add narrowly
// scoped values immediately before launch; callers must not persist or expose
// the resolved environment.
type LaunchEnvironment struct {
	AgentProfileID   string
	ProjectID        string
	FeatureID        string
	Role             Role
	WorkspaceID      string
	WorkingDirectory string
	Variables        []string
}

type ResumeRequest struct {
	SessionRequest
	ProviderSessionID string
	Recovery          RecoveryContext
}

// RecoveryContext is the durable state a resumed provider must reconcile
// before it performs more work. Provider adapters may enrich this briefing
// with repository and forge observations that are specific to their runtime.
type RecoveryContext struct {
	Briefing        string
	CompletedEvents []Event
	PendingCommands []Command
	PreviousState   string
	WorkflowPhase   string
}

type CommandType string

const (
	CommandMessage  CommandType = "message"
	CommandPause    CommandType = "pause"
	CommandContinue CommandType = "continue"
	CommandStop     CommandType = "stop"
)

// Command IDs make user and control actions safe to retry across transport failures.
type Command struct {
	ID      string
	Type    CommandType
	Message string
}

type EventType string

const (
	EventUserMessage        EventType = "user_message"
	EventMessage            EventType = "message"
	EventPlanSubmitted      EventType = "plan_submitted"
	EventActivity           EventType = "activity"
	EventInputRequired      EventType = "input_required"
	EventPauseAcknowledged  EventType = "pause_acknowledged"
	EventContinued          EventType = "continued"
	EventRecoveryAssessment EventType = "recovery_assessment"
)

type RecoveryAssessment struct {
	Consistent         bool
	RequiresUserReview bool
}

// Event contains public session activity, not private model reasoning. Most
// events come from a worker; the coordinator also records user messages in the
// same ordered conversation.
type Event struct {
	Type               EventType
	Text               string
	RecoveryAssessment *RecoveryAssessment
}

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

type Result struct {
	Outcome           Outcome
	Disposition       Disposition
	ProviderSessionID string
	Summary           string
	Publication       *ImplementationPublication
	Review            *ReviewPublication
}

// ImplementationPublication contains only the external identities that the
// coordinator must verify after an agent says it finished implementation. It
// deliberately excludes credentials and mutable repository content.
type ImplementationPublication struct {
	CommitID          string
	PullRequestNumber int64
}

// ReviewPublication identifies the exact formal Forgejo review that a reviewer
// says it submitted for one implementation commit. Result.Disposition carries
// approval versus requested changes.
type ReviewPublication struct {
	CommitID          string
	PullRequestNumber int64
	ReviewID          int64
}

// Adapter translates the provider-neutral session protocol to a provider CLI.
type Adapter interface {
	Start(ctx context.Context, request SessionRequest) (Session, error)
	Resume(ctx context.Context, request ResumeRequest) (Session, error)
}

// Session is a running or resumable provider conversation. Stop is cooperative.
// Events must close when the session finishes so the coordinator can collect
// the final result without guessing whether more observable output is coming.
type Session interface {
	ProviderSessionID() string
	Events() <-chan Event
	Send(ctx context.Context, command Command) error
	Wait(ctx context.Context) (Result, error)
}

// ForceStoppableSession is an optional extension implemented only when a
// provider session owns an operating-system process handle. Keeping it separate
// from Session prevents simulated or remotely managed providers from claiming a
// safety guarantee they cannot provide.
type ForceStoppableSession interface {
	Session
	ForceStop(ctx context.Context, reason string) error
}

var ErrInvalidSessionRequest = errors.New("invalid worker session request")
var ErrInvalidLaunchEnvironment = errors.New("invalid worker launch environment")
var ErrInvalidCommand = errors.New("invalid worker command")
var ErrInvalidResult = errors.New("invalid worker result")

var (
	launchIDPattern        = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	environmentNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	commitIDPattern        = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)
)

func (role Role) IsValid() bool {
	switch role {
	case RoleLead, RoleConsultant, RoleCoder, RoleReviewer:
		return true
	default:
		return false
	}
}

func (request SessionRequest) Validate() error {
	switch {
	case strings.TrimSpace(request.SessionID) == "":
		return fmt.Errorf("%w: session ID is required", ErrInvalidSessionRequest)
	case strings.TrimSpace(request.AttemptID) == "":
		return fmt.Errorf("%w: attempt ID is required", ErrInvalidSessionRequest)
	case strings.TrimSpace(request.FeatureID) == "":
		return fmt.Errorf("%w: feature ID is required", ErrInvalidSessionRequest)
	case !request.Role.IsValid():
		return fmt.Errorf("%w: role %q is not recognized", ErrInvalidSessionRequest, request.Role)
	case strings.TrimSpace(request.Instructions) == "":
		return fmt.Errorf("%w: instructions are required", ErrInvalidSessionRequest)
	case request.OutputContract != "" &&
		request.OutputContract != OutputContractPlanningLead &&
		request.OutputContract != OutputContractImplementationLead &&
		request.OutputContract != OutputContractImplementationReview:
		return fmt.Errorf("%w: output contract %q is not recognized", ErrInvalidSessionRequest, request.OutputContract)
	case (request.OutputContract == OutputContractPlanningLead ||
		request.OutputContract == OutputContractImplementationLead) && request.Role != RoleLead:
		return fmt.Errorf("%w: lead output contract requires the lead role", ErrInvalidSessionRequest)
	case request.OutputContract == OutputContractImplementationReview && request.Role != RoleReviewer:
		return fmt.Errorf("%w: reviewer output contract requires the reviewer role", ErrInvalidSessionRequest)
	}
	if !request.LaunchEnvironment.IsZero() {
		if err := request.LaunchEnvironment.Validate(); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidSessionRequest, err)
		}
		if request.LaunchEnvironment.FeatureID != request.FeatureID {
			return fmt.Errorf("%w: launch feature does not match session feature", ErrInvalidSessionRequest)
		}
		if request.LaunchEnvironment.Role != request.Role {
			return fmt.Errorf("%w: launch role does not match session role", ErrInvalidSessionRequest)
		}
	}
	return nil
}

func (environment LaunchEnvironment) IsZero() bool {
	return environment.AgentProfileID == "" &&
		environment.ProjectID == "" &&
		environment.FeatureID == "" &&
		environment.Role == "" &&
		environment.WorkspaceID == "" &&
		environment.WorkingDirectory == "" &&
		environment.Variables == nil
}

func (environment LaunchEnvironment) Validate() error {
	for _, field := range []struct {
		name  string
		value string
	}{
		{name: "agent profile ID", value: environment.AgentProfileID},
		{name: "project ID", value: environment.ProjectID},
		{name: "feature ID", value: environment.FeatureID},
		{name: "workspace ID", value: environment.WorkspaceID},
	} {
		if !launchIDPattern.MatchString(field.value) {
			return fmt.Errorf("%w: %s is invalid", ErrInvalidLaunchEnvironment, field.name)
		}
	}
	if !environment.Role.IsValid() {
		return fmt.Errorf("%w: role %q is not recognized", ErrInvalidLaunchEnvironment, environment.Role)
	}
	if !filepath.IsAbs(environment.WorkingDirectory) || strings.ContainsRune(environment.WorkingDirectory, '\x00') {
		return fmt.Errorf("%w: working directory must be absolute and cannot contain NUL", ErrInvalidLaunchEnvironment)
	}
	if environment.Variables == nil {
		return fmt.Errorf("%w: explicit variables are required", ErrInvalidLaunchEnvironment)
	}
	seen := make(map[string]struct{}, len(environment.Variables))
	for _, variable := range environment.Variables {
		name, _, found := strings.Cut(variable, "=")
		if !found || !environmentNamePattern.MatchString(name) || strings.ContainsRune(variable, '\x00') {
			return fmt.Errorf("%w: variables must be NAME=VALUE entries without NUL", ErrInvalidLaunchEnvironment)
		}
		if _, exists := seen[name]; exists {
			return fmt.Errorf("%w: variable %q is duplicated", ErrInvalidLaunchEnvironment, name)
		}
		seen[name] = struct{}{}
	}
	return nil
}

// Clone prevents callers from sharing the Variables backing array across
// attempt boundaries.
func (environment LaunchEnvironment) Clone() LaunchEnvironment {
	if environment.Variables != nil {
		variables := make([]string, len(environment.Variables))
		copy(variables, environment.Variables)
		environment.Variables = variables
	}
	return environment
}

func (environment LaunchEnvironment) Equal(other LaunchEnvironment) bool {
	return environment.AgentProfileID == other.AgentProfileID &&
		environment.ProjectID == other.ProjectID &&
		environment.FeatureID == other.FeatureID &&
		environment.Role == other.Role &&
		environment.WorkspaceID == other.WorkspaceID &&
		environment.WorkingDirectory == other.WorkingDirectory &&
		(environment.Variables == nil) == (other.Variables == nil) &&
		slices.Equal(environment.Variables, other.Variables)
}

func (request SessionRequest) Clone() SessionRequest {
	request.LaunchEnvironment = request.LaunchEnvironment.Clone()
	return request
}

func (request SessionRequest) Equal(other SessionRequest) bool {
	return request.SessionID == other.SessionID &&
		request.AttemptID == other.AttemptID &&
		request.FeatureID == other.FeatureID &&
		request.Role == other.Role &&
		request.Instructions == other.Instructions &&
		request.OutputContract == other.OutputContract &&
		request.LaunchEnvironment.Equal(other.LaunchEnvironment)
}

func (request ResumeRequest) Validate() error {
	if err := request.SessionRequest.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(request.ProviderSessionID) == "" {
		return fmt.Errorf("%w: provider session ID is required", ErrInvalidSessionRequest)
	}
	return nil
}

func (command Command) Validate() error {
	if strings.TrimSpace(command.ID) == "" {
		return fmt.Errorf("%w: command ID is required", ErrInvalidCommand)
	}
	switch command.Type {
	case CommandMessage:
		if strings.TrimSpace(command.Message) == "" {
			return fmt.Errorf("%w: message is required", ErrInvalidCommand)
		}
	case CommandPause, CommandContinue, CommandStop:
		if strings.TrimSpace(command.Message) != "" {
			return fmt.Errorf("%w: control command cannot contain a message", ErrInvalidCommand)
		}
	default:
		return fmt.Errorf("%w: type %q is not recognized", ErrInvalidCommand, command.Type)
	}
	return nil
}

func (outcome Outcome) IsValid() bool {
	return outcome == OutcomeCompleted || outcome == OutcomeStopped || outcome == OutcomeFailed
}

func (disposition Disposition) IsValid() bool {
	switch disposition {
	case DispositionSucceeded,
		DispositionChangesRequested,
		DispositionInputRequired:
		return true
	default:
		return false
	}
}

func (result Result) Validate() error {
	switch {
	case !result.Outcome.IsValid():
		return fmt.Errorf("%w: outcome %q is not recognized", ErrInvalidResult, result.Outcome)
	case strings.TrimSpace(result.ProviderSessionID) == "":
		return fmt.Errorf("%w: provider session ID is required", ErrInvalidResult)
	case result.Outcome == OutcomeCompleted && !result.Disposition.IsValid():
		return fmt.Errorf("%w: disposition %q is not recognized", ErrInvalidResult, result.Disposition)
	case result.Outcome == OutcomeStopped && result.Disposition != "":
		return fmt.Errorf("%w: stopped session cannot have a disposition", ErrInvalidResult)
	case result.Outcome == OutcomeFailed && result.Disposition != "":
		return fmt.Errorf("%w: failed session cannot have a disposition", ErrInvalidResult)
	case result.Publication != nil && result.Review != nil:
		return fmt.Errorf("%w: result cannot contain implementation and review publications", ErrInvalidResult)
	case result.Publication != nil &&
		(result.Outcome != OutcomeCompleted || result.Disposition != DispositionSucceeded):
		return fmt.Errorf("%w: implementation publication requires a successful completed session", ErrInvalidResult)
	case result.Review != nil && result.Outcome != OutcomeCompleted:
		return fmt.Errorf("%w: review publication requires a completed session", ErrInvalidResult)
	case result.Review != nil && result.Disposition != DispositionSucceeded &&
		result.Disposition != DispositionChangesRequested:
		return fmt.Errorf("%w: review publication requires approval or requested changes", ErrInvalidResult)
	case result.Publication != nil:
		if err := result.Publication.Validate(); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidResult, err)
		}
	case result.Review != nil:
		if err := result.Review.Validate(); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidResult, err)
		}
	default:
		return nil
	}
	return nil
}

func (publication ImplementationPublication) Validate() error {
	if !commitIDPattern.MatchString(publication.CommitID) {
		return errors.New("implementation commit ID must be a lowercase SHA-1 or SHA-256 object ID")
	}
	if publication.PullRequestNumber < 1 {
		return errors.New("implementation pull request number must be positive")
	}
	return nil
}

func (review ReviewPublication) Validate() error {
	if !commitIDPattern.MatchString(review.CommitID) {
		return errors.New("review commit ID must be a lowercase SHA-1 or SHA-256 object ID")
	}
	if review.PullRequestNumber < 1 {
		return errors.New("review pull request number must be positive")
	}
	if review.ReviewID < 1 {
		return errors.New("review ID must be positive")
	}
	return nil
}
