package worker

import (
	"context"
	"errors"
	"fmt"
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
	SessionID    string
	FeatureID    string
	Role         Role
	Instructions string
}

type ResumeRequest struct {
	SessionRequest
	ProviderSessionID string
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
	EventMessage           EventType = "message"
	EventActivity          EventType = "activity"
	EventInputRequired     EventType = "input_required"
	EventPauseAcknowledged EventType = "pause_acknowledged"
	EventContinued         EventType = "continued"
)

// Event contains observable worker output, not private model reasoning.
// The coordinator will add durable IDs, timestamps, and ordering when it records it.
type Event struct {
	Type EventType
	Text string
}

type Outcome string

const (
	OutcomeCompleted Outcome = "completed"
	OutcomeStopped   Outcome = "stopped"
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
}

// Adapter translates the provider-neutral session protocol to a provider CLI.
type Adapter interface {
	Start(ctx context.Context, request SessionRequest) (Session, error)
	Resume(ctx context.Context, request ResumeRequest) (Session, error)
}

// Session is a running or resumable provider conversation. Stop is cooperative;
// forced termination belongs to the worker supervisor outside this interface.
// Events must close when the session finishes so the coordinator can collect
// the final result without guessing whether more observable output is coming.
type Session interface {
	Events() <-chan Event
	Send(ctx context.Context, command Command) error
	Wait(ctx context.Context) (Result, error)
}

var ErrInvalidSessionRequest = errors.New("invalid worker session request")
var ErrInvalidCommand = errors.New("invalid worker command")
var ErrInvalidResult = errors.New("invalid worker result")

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
	case strings.TrimSpace(request.FeatureID) == "":
		return fmt.Errorf("%w: feature ID is required", ErrInvalidSessionRequest)
	case !request.Role.IsValid():
		return fmt.Errorf("%w: role %q is not recognized", ErrInvalidSessionRequest, request.Role)
	case strings.TrimSpace(request.Instructions) == "":
		return fmt.Errorf("%w: instructions are required", ErrInvalidSessionRequest)
	default:
		return nil
	}
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
	return outcome == OutcomeCompleted || outcome == OutcomeStopped
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
	default:
		return nil
	}
}
