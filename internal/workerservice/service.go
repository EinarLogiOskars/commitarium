// Package workerservice connects provider sessions to the durable worker
// journal and the provider-neutral worker HTTP service contract.
package workerservice

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/worker"
	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
	"github.com/EinarLogiOskars/commitarium/internal/workerjournal"
)

const (
	defaultSubscriberBuffer = 32
	defaultForceStopTimeout = 10 * time.Second
)

var ErrInvalidConfig = errors.New("invalid journal-backed worker service configuration")

// NormalizedEvent contains only activity that is safe to persist and publish.
// The service itself adds immutable attempt identity, sequence, and time data,
// so a provider-specific normalizer cannot accidentally change those fields.
type NormalizedEvent struct {
	Type               workerhttp.EventType
	Text               string
	Redaction          workerhttp.RedactionMetadata
	Truncation         *workerhttp.TruncationMetadata
	RecoveryAssessment *workerhttp.RecoveryAssessment
}

// Normalizer is the provider-output safety boundary. Real workers will supply
// a fail-closed redaction implementation; deterministic tests can supply a
// mapper whose scripted output is already known to be safe.
type Normalizer interface {
	Normalize(context.Context, worker.Event) (NormalizedEvent, error)
}

type NormalizerFunc func(context.Context, worker.Event) (NormalizedEvent, error)

func (function NormalizerFunc) Normalize(
	ctx context.Context,
	event worker.Event,
) (NormalizedEvent, error) {
	return function(ctx, event)
}

type Config struct {
	Journal             *workerjournal.Store
	Provider            worker.Adapter
	EnvironmentResolver EnvironmentResolver
	Normalizer          Normalizer
	Lifetime            context.Context
	Now                 func() time.Time
	SubscriberBuffer    int
	ForceStopTimeout    time.Duration
}

type activeProviderSession struct {
	session worker.Session
	// finished closes only after the normal watcher has persisted the final
	// attempt state, not merely after the operating-system process exits.
	finished chan struct{}
}

// Service owns live provider-session handles while the journal remains the
// durable authority exposed through inspection and replay.
type Service struct {
	journal             *workerjournal.Store
	provider            worker.Adapter
	environmentResolver EnvironmentResolver
	normalizer          Normalizer
	lifetime            context.Context
	now                 func() time.Time

	activeMu sync.RWMutex
	active   map[workerhttp.AttemptReference]*activeProviderSession

	eventMu          sync.Mutex
	subscribers      map[workerhttp.AttemptReference]map[uint64]chan workerhttp.Event
	nextSubID        uint64
	bufferSize       int
	forceStopTimeout time.Duration
}

var _ workerhttp.Service = (*Service)(nil)
var _ workerhttp.EventSource = (*Service)(nil)

// New creates one worker-service instance and performs its startup recovery
// transaction before it can serve requests. Any nonterminal journal entries
// came from a previous process incarnation and therefore become indeterminate.
func New(
	ctx context.Context,
	config Config,
) (*Service, workerjournal.RecoveryResult, error) {
	if config.Journal == nil {
		return nil, workerjournal.RecoveryResult{}, fmt.Errorf("%w: journal is required", ErrInvalidConfig)
	}
	if config.Provider == nil {
		return nil, workerjournal.RecoveryResult{}, fmt.Errorf("%w: provider is required", ErrInvalidConfig)
	}
	if config.EnvironmentResolver == nil {
		return nil, workerjournal.RecoveryResult{}, fmt.Errorf("%w: environment resolver is required", ErrInvalidConfig)
	}
	if config.Normalizer == nil {
		return nil, workerjournal.RecoveryResult{}, fmt.Errorf("%w: normalizer is required", ErrInvalidConfig)
	}
	if config.Lifetime == nil {
		return nil, workerjournal.RecoveryResult{}, fmt.Errorf("%w: lifetime context is required", ErrInvalidConfig)
	}
	if config.SubscriberBuffer < 0 {
		return nil, workerjournal.RecoveryResult{}, fmt.Errorf("%w: subscriber buffer cannot be negative", ErrInvalidConfig)
	}
	if config.ForceStopTimeout < 0 {
		return nil, workerjournal.RecoveryResult{}, fmt.Errorf("%w: force-stop timeout cannot be negative", ErrInvalidConfig)
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}
	bufferSize := config.SubscriberBuffer
	if bufferSize == 0 {
		bufferSize = defaultSubscriberBuffer
	}
	forceStopTimeout := config.ForceStopTimeout
	if forceStopTimeout == 0 {
		forceStopTimeout = defaultForceStopTimeout
	}
	service := &Service{
		journal:             config.Journal,
		provider:            config.Provider,
		environmentResolver: config.EnvironmentResolver,
		normalizer:          config.Normalizer,
		lifetime:            config.Lifetime,
		now:                 now,
		active:              make(map[workerhttp.AttemptReference]*activeProviderSession),
		subscribers:         make(map[workerhttp.AttemptReference]map[uint64]chan workerhttp.Event),
		bufferSize:          bufferSize,
		forceStopTimeout:    forceStopTimeout,
	}
	recovery, err := service.journal.RecoverInterrupted(ctx, service.timestamp())
	if err != nil {
		return nil, workerjournal.RecoveryResult{}, fmt.Errorf("recover worker journal at startup: %w", err)
	}
	return service, recovery, nil
}

func (service *Service) PutAttempt(
	ctx context.Context,
	identity workerhttp.MutationIdentity,
	request workerhttp.PutAttemptRequest,
) (workerhttp.Attempt, bool, error) {
	if err := request.Validate(identity); err != nil {
		return workerhttp.Attempt{}, false, err
	}
	if err := ctx.Err(); err != nil {
		return workerhttp.Attempt{}, false, err
	}
	digest, err := workerjournal.DigestRequest(request)
	if err != nil {
		return workerhttp.Attempt{}, false, fmt.Errorf("digest worker launch request: %w", err)
	}
	now := service.timestamp()
	attempt, created, err := service.journal.CreateAttempt(ctx, workerjournal.AttemptCreation{
		Attempt: workerhttp.Attempt{
			AttemptReference:  identity.AttemptReference,
			Mode:              request.Mode,
			Assignment:        request.Assignment,
			ProviderSessionID: request.ProviderSessionID,
			State:             workerhttp.AttemptStateStarting,
			StartedAt:         now,
			UpdatedAt:         now,
		},
		IdempotencyKey: identity.IdempotencyKey,
		RequestDigest:  digest,
	})
	if err != nil {
		return workerhttp.Attempt{}, false, service.journalError(err)
	}
	if !created {
		return attempt, false, nil
	}

	launchEnvironment, resolveErr := service.environmentResolver.Resolve(service.lifetime, request.Assignment)
	if resolveErr != nil {
		code, summary, retryable := environmentFailure(resolveErr)
		failed, terminalErr := service.failLaunch(ctx, attempt, code, summary, retryable, resolveErr)
		if terminalErr != nil {
			return workerhttp.Attempt{}, false, terminalErr
		}
		return failed, true, nil
	}
	if !launchEnvironmentMatches(launchEnvironment, request.Assignment) {
		resolveErr = fmt.Errorf("%w: resolver returned a different assignment", ErrConfigurationMismatch)
		failed, terminalErr := service.failLaunch(
			ctx,
			attempt,
			workerhttp.ErrorConfigurationMismatch,
			"launch environment does not match the requested assignment",
			false,
			resolveErr,
		)
		if terminalErr != nil {
			return workerhttp.Attempt{}, false, terminalErr
		}
		return failed, true, nil
	}
	if err := launchEnvironment.Validate(); err != nil {
		resolveErr = fmt.Errorf("%w: %v", ErrConfigurationMismatch, err)
		failed, terminalErr := service.failLaunch(
			ctx,
			attempt,
			workerhttp.ErrorConfigurationMismatch,
			"launch environment is invalid",
			false,
			resolveErr,
		)
		if terminalErr != nil {
			return workerhttp.Attempt{}, false, terminalErr
		}
		return failed, true, nil
	}
	providerSession, launchErr := service.launchProvider(
		request,
		identity.AttemptReference,
		launchEnvironment.Clone(),
	)
	if launchErr != nil {
		if providerSession != nil {
			uncertain, transitionErr := service.uncertainLaunch(ctx, attempt, providerSession.ProviderSessionID())
			if transitionErr != nil {
				return workerhttp.Attempt{}, false, transitionErr
			}
			return uncertain, true, nil
		}
		failed, terminalErr := service.failLaunch(
			ctx,
			attempt,
			workerhttp.ErrorInternal,
			"provider session could not be started",
			true,
			launchErr,
		)
		if terminalErr != nil {
			return workerhttp.Attempt{}, false, terminalErr
		}
		return failed, true, nil
	}
	providerSessionID := providerSession.ProviderSessionID()
	if strings.TrimSpace(providerSessionID) == "" {
		uncertain, transitionErr := service.uncertainLaunch(ctx, attempt, "")
		if transitionErr != nil {
			return workerhttp.Attempt{}, false, transitionErr
		}
		return uncertain, true, nil
	}
	if request.Mode == workerhttp.AttemptModeResume && providerSessionID != request.ProviderSessionID {
		uncertain, transitionErr := service.uncertainLaunch(ctx, attempt, "")
		if transitionErr != nil {
			return workerhttp.Attempt{}, false, transitionErr
		}
		return uncertain, true, nil
	}

	running, err := service.journal.TransitionAttempt(ctx, workerjournal.AttemptTransition{
		Reference:         attempt.AttemptReference,
		Expected:          workerhttp.AttemptStateStarting,
		State:             workerhttp.AttemptStateRunning,
		ProviderSessionID: providerSessionID,
		OccurredAt:        service.timestamp(),
	})
	if err != nil {
		return workerhttp.Attempt{}, false, service.journalError(err)
	}
	live := &activeProviderSession{session: providerSession, finished: make(chan struct{})}
	service.activeMu.Lock()
	service.active[running.AttemptReference] = live
	service.activeMu.Unlock()
	go service.supervise(running.AttemptReference, live)
	return running, true, nil
}

func (service *Service) uncertainLaunch(
	ctx context.Context,
	attempt workerhttp.Attempt,
	providerSessionID string,
) (workerhttp.Attempt, error) {
	uncertain, err := service.journal.TransitionAttempt(ctx, workerjournal.AttemptTransition{
		Reference:         attempt.AttemptReference,
		Expected:          workerhttp.AttemptStateStarting,
		State:             workerhttp.AttemptStateIndeterminate,
		ProviderSessionID: providerSessionID,
		OccurredAt:        service.timestamp(),
	})
	if err != nil {
		return workerhttp.Attempt{}, service.journalError(err)
	}
	return uncertain, nil
}

func (service *Service) GetAttempt(
	ctx context.Context,
	reference workerhttp.AttemptReference,
) (workerhttp.Attempt, error) {
	attempt, err := service.journal.GetAttempt(ctx, reference)
	if err != nil {
		return workerhttp.Attempt{}, service.journalError(err)
	}
	return attempt, nil
}

func (service *Service) SendCommand(
	ctx context.Context,
	identity workerhttp.MutationIdentity,
	request workerhttp.CommandRequest,
) (workerhttp.Attempt, error) {
	if err := request.Validate(identity); err != nil {
		return workerhttp.Attempt{}, err
	}
	digest, err := workerjournal.DigestRequest(request)
	if err != nil {
		return workerhttp.Attempt{}, fmt.Errorf("digest worker command: %w", err)
	}
	now := service.timestamp()
	mutation, created, err := service.journal.ClaimMutation(ctx, workerjournal.Mutation{
		MutationIdentity: identity,
		Kind:             mutationKind(request.Type),
		RequestDigest:    digest,
		Status:           workerjournal.MutationPending,
		RequestedAt:      now,
		UpdatedAt:        now,
	})
	if err != nil {
		return workerhttp.Attempt{}, service.journalError(err)
	}
	if !created {
		return service.replayMutation(ctx, mutation)
	}

	attempt, err := service.journal.GetAttempt(ctx, identity.AttemptReference)
	if err != nil {
		return workerhttp.Attempt{}, service.journalError(err)
	}
	if attempt.State == workerhttp.AttemptStateIndeterminate {
		_ = service.resolveMutation(ctx, identity, workerjournal.MutationIndeterminate)
		return workerhttp.Attempt{}, indeterminateError(errors.New("attempt is indeterminate"))
	}
	live, ok := service.activeSession(identity.AttemptReference)
	if !ok {
		_ = service.resolveMutation(ctx, identity, workerjournal.MutationIndeterminate)
		_ = service.markIndeterminate(ctx, attempt)
		return workerhttp.Attempt{}, indeterminateError(errors.New("live provider session is unavailable"))
	}

	prepared, err := service.prepareCommand(ctx, attempt, request.Type)
	if err != nil {
		_ = service.resolveMutation(ctx, identity, workerjournal.MutationRejected)
		return workerhttp.Attempt{}, err
	}
	command := worker.Command{
		ID:      identity.IdempotencyKey,
		Type:    worker.CommandType(request.Type),
		Message: request.Message,
	}
	if err := live.session.Send(ctx, command); err != nil {
		return workerhttp.Attempt{}, service.handleCommandFailure(ctx, identity, prepared, request.Type, err)
	}
	if err := service.resolveMutation(ctx, identity, workerjournal.MutationApplied); err != nil {
		return workerhttp.Attempt{}, err
	}
	return service.GetAttempt(ctx, identity.AttemptReference)
}

func (service *Service) ForceStop(
	ctx context.Context,
	identity workerhttp.MutationIdentity,
	request workerhttp.ForceStopRequest,
) (workerhttp.Attempt, error) {
	if err := request.Validate(identity); err != nil {
		return workerhttp.Attempt{}, err
	}
	if err := ctx.Err(); err != nil {
		return workerhttp.Attempt{}, err
	}
	digest, err := workerjournal.DigestRequest(request)
	if err != nil {
		return workerhttp.Attempt{}, fmt.Errorf("digest worker force-stop request: %w", err)
	}
	now := service.timestamp()
	mutation, created, err := service.journal.ClaimMutation(ctx, workerjournal.Mutation{
		MutationIdentity: identity,
		Kind:             workerjournal.MutationForceStop,
		RequestDigest:    digest,
		Status:           workerjournal.MutationPending,
		RequestedAt:      now,
		UpdatedAt:        now,
	})
	if err != nil {
		return workerhttp.Attempt{}, service.journalError(err)
	}
	if !created {
		return service.replayForceStop(ctx, mutation)
	}

	attempt, err := service.journal.GetAttempt(ctx, identity.AttemptReference)
	if err != nil {
		return workerhttp.Attempt{}, service.journalError(err)
	}
	if attempt.State == workerhttp.AttemptStateIndeterminate {
		_ = service.resolveMutation(context.WithoutCancel(ctx), identity, workerjournal.MutationIndeterminate)
		return workerhttp.Attempt{}, indeterminateError(errors.New("attempt is indeterminate"))
	}
	live, ok := service.activeSession(identity.AttemptReference)
	if !ok {
		return workerhttp.Attempt{}, service.handleMissingForceStopSession(ctx, identity, attempt)
	}
	forceStoppable, ok := live.session.(worker.ForceStoppableSession)
	if !ok {
		_ = service.resolveMutation(context.WithoutCancel(ctx), identity, workerjournal.MutationRejected)
		return workerhttp.Attempt{}, unsupportedForceStopError()
	}
	prepared, err := service.prepareForceStop(ctx, attempt)
	if err != nil {
		_ = service.resolveMutation(context.WithoutCancel(ctx), identity, workerjournal.MutationRejected)
		return workerhttp.Attempt{}, err
	}

	// Once the pending mutation is durable, an HTTP disconnect must not abandon
	// termination halfway through. The worker lifetime and this internal bound,
	// rather than the request context, own delivery and final persistence.
	operationCtx, cancel := context.WithTimeout(service.lifetime, service.forceStopTimeout)
	defer cancel()
	if err := forceStoppable.ForceStop(operationCtx, request.Reason); err != nil {
		return workerhttp.Attempt{}, service.handleForceStopFailure(identity, prepared, err)
	}
	select {
	case <-live.finished:
	case <-operationCtx.Done():
		return workerhttp.Attempt{}, service.handleForceStopFailure(identity, prepared, operationCtx.Err())
	}
	terminal, err := service.journal.GetAttempt(operationCtx, identity.AttemptReference)
	if err != nil {
		return workerhttp.Attempt{}, service.handleForceStopFailure(identity, prepared, err)
	}
	if terminal.State != workerhttp.AttemptStateTerminal {
		return workerhttp.Attempt{}, service.handleForceStopFailure(
			identity,
			terminal,
			errors.New("provider watcher did not record a terminal attempt"),
		)
	}
	if err := service.resolveMutation(operationCtx, identity, workerjournal.MutationApplied); err != nil {
		return workerhttp.Attempt{}, service.handleForceStopFailure(identity, terminal, err)
	}
	return terminal, nil
}

func (service *Service) launchProvider(
	request workerhttp.PutAttemptRequest,
	reference workerhttp.AttemptReference,
	launchEnvironment worker.LaunchEnvironment,
) (worker.Session, error) {
	sessionRequest := worker.SessionRequest{
		SessionID:         reference.SessionID,
		AttemptID:         reference.AttemptID,
		FeatureID:         request.Assignment.FeatureID,
		Role:              worker.Role(request.Assignment.Role),
		Instructions:      request.Instructions,
		OutputContract:    worker.OutputContract(request.OutputContract),
		LaunchEnvironment: launchEnvironment.Clone(),
	}
	if request.Mode == workerhttp.AttemptModeResume {
		return service.provider.Resume(service.lifetime, worker.ResumeRequest{
			SessionRequest:    sessionRequest,
			ProviderSessionID: request.ProviderSessionID,
			Recovery: worker.RecoveryContext{
				Briefing: request.Instructions,
			},
		})
	}
	return service.provider.Start(service.lifetime, sessionRequest)
}

func (service *Service) failLaunch(
	ctx context.Context,
	attempt workerhttp.Attempt,
	code workerhttp.ErrorCode,
	summary string,
	retryable bool,
	cause error,
) (workerhttp.Attempt, error) {
	failed, err := service.journal.TransitionAttempt(ctx, workerjournal.AttemptTransition{
		Reference:  attempt.AttemptReference,
		Expected:   workerhttp.AttemptStateStarting,
		State:      workerhttp.AttemptStateTerminal,
		OccurredAt: service.timestamp(),
		Result: &workerhttp.TerminalResult{
			Outcome: workerhttp.OutcomeFailed,
			Summary: summary,
			Error: &workerhttp.ProtocolError{
				Code:      code,
				Message:   summary,
				Retryable: retryable,
			},
		},
	})
	if err != nil {
		return workerhttp.Attempt{}, fmt.Errorf("record failed provider launch after %v: %w", cause, err)
	}
	return failed, nil
}

func (service *Service) replayMutation(
	ctx context.Context,
	mutation workerjournal.Mutation,
) (workerhttp.Attempt, error) {
	switch mutation.Status {
	case workerjournal.MutationPending, workerjournal.MutationApplied:
		return service.GetAttempt(ctx, mutation.AttemptReference)
	case workerjournal.MutationRejected:
		return workerhttp.Attempt{}, workerhttp.NewServiceError(
			workerhttp.ErrorAttemptConflict,
			"the original command was rejected for the attempt state",
			false,
			nil,
		)
	case workerjournal.MutationIndeterminate:
		return workerhttp.Attempt{}, indeterminateError(errors.New("command delivery is indeterminate"))
	default:
		return workerhttp.Attempt{}, fmt.Errorf("unsupported stored mutation status %q", mutation.Status)
	}
}

func (service *Service) replayForceStop(
	ctx context.Context,
	mutation workerjournal.Mutation,
) (workerhttp.Attempt, error) {
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for mutation.Status == workerjournal.MutationPending {
		select {
		case <-ctx.Done():
			return workerhttp.Attempt{}, ctx.Err()
		case <-ticker.C:
		}
		stored, err := service.journal.GetMutation(ctx, mutation.MutationIdentity)
		if err != nil {
			return workerhttp.Attempt{}, service.journalError(err)
		}
		mutation = stored
	}
	return service.replayMutation(ctx, mutation)
}

func (service *Service) prepareForceStop(
	ctx context.Context,
	attempt workerhttp.Attempt,
) (workerhttp.Attempt, error) {
	switch attempt.State {
	case workerhttp.AttemptStateRunning,
		workerhttp.AttemptStatePauseRequested,
		workerhttp.AttemptStatePaused:
		return service.transition(ctx, attempt, workerhttp.AttemptStateStopRequested)
	case workerhttp.AttemptStateStopRequested:
		return attempt, nil
	default:
		return workerhttp.Attempt{}, workerhttp.NewServiceError(
			workerhttp.ErrorAttemptConflict,
			fmt.Sprintf("forced termination is not valid while the attempt is %q", attempt.State),
			false,
			nil,
		)
	}
}

func (service *Service) handleMissingForceStopSession(
	ctx context.Context,
	identity workerhttp.MutationIdentity,
	attempt workerhttp.Attempt,
) error {
	current, err := service.journal.GetAttempt(ctx, attempt.AttemptReference)
	if err == nil && current.State == workerhttp.AttemptStateTerminal {
		_ = service.resolveMutation(context.WithoutCancel(ctx), identity, workerjournal.MutationRejected)
		return workerhttp.NewServiceError(
			workerhttp.ErrorAttemptConflict,
			"worker attempt finished before forced termination was delivered",
			false,
			nil,
		)
	}
	_ = service.resolveMutation(context.WithoutCancel(ctx), identity, workerjournal.MutationIndeterminate)
	_ = service.markIndeterminate(context.WithoutCancel(ctx), attempt)
	return indeterminateError(errors.New("live provider process handle is unavailable"))
}

func (service *Service) handleForceStopFailure(
	identity workerhttp.MutationIdentity,
	attempt workerhttp.Attempt,
	cause error,
) error {
	ctx := context.WithoutCancel(service.lifetime)
	_ = service.resolveMutation(ctx, identity, workerjournal.MutationIndeterminate)
	_ = service.markIndeterminate(ctx, attempt)
	return indeterminateError(cause)
}

func unsupportedForceStopError() error {
	return workerhttp.NewServiceError(
		workerhttp.ErrorUnsupportedOperation,
		"provider session does not own a force-stoppable process",
		false,
		nil,
	)
}

func (service *Service) prepareCommand(
	ctx context.Context,
	attempt workerhttp.Attempt,
	command workerhttp.CommandType,
) (workerhttp.Attempt, error) {
	switch command {
	case workerhttp.CommandMessage:
		if attempt.State != workerhttp.AttemptStateRunning &&
			attempt.State != workerhttp.AttemptStatePauseRequested &&
			attempt.State != workerhttp.AttemptStatePaused {
			return workerhttp.Attempt{}, commandStateError(attempt.State, command)
		}
		return attempt, nil
	case workerhttp.CommandPause:
		if attempt.State != workerhttp.AttemptStateRunning {
			return workerhttp.Attempt{}, commandStateError(attempt.State, command)
		}
		return service.transition(ctx, attempt, workerhttp.AttemptStatePauseRequested)
	case workerhttp.CommandContinue:
		if attempt.State != workerhttp.AttemptStatePaused {
			return workerhttp.Attempt{}, commandStateError(attempt.State, command)
		}
		return attempt, nil
	case workerhttp.CommandStop:
		if attempt.State != workerhttp.AttemptStateRunning &&
			attempt.State != workerhttp.AttemptStatePauseRequested &&
			attempt.State != workerhttp.AttemptStatePaused {
			return workerhttp.Attempt{}, commandStateError(attempt.State, command)
		}
		return service.transition(ctx, attempt, workerhttp.AttemptStateStopRequested)
	default:
		return workerhttp.Attempt{}, commandStateError(attempt.State, command)
	}
}

func (service *Service) handleCommandFailure(
	ctx context.Context,
	identity workerhttp.MutationIdentity,
	attempt workerhttp.Attempt,
	command workerhttp.CommandType,
	cause error,
) error {
	knownRejection := errors.Is(cause, worker.ErrInvalidCommand) ||
		errors.Is(cause, worker.ErrInvalidSessionState) ||
		errors.Is(cause, worker.ErrSessionFinished) ||
		errors.Is(cause, worker.ErrCommandConflict)
	if knownRejection {
		_ = service.resolveMutation(ctx, identity, workerjournal.MutationRejected)
		if command == workerhttp.CommandPause && attempt.State == workerhttp.AttemptStatePauseRequested {
			_, _ = service.transition(ctx, attempt, workerhttp.AttemptStateRunning)
		}
		return workerhttp.NewServiceError(
			workerhttp.ErrorAttemptConflict,
			"provider rejected the command for its current session state",
			false,
			cause,
		)
	}
	_ = service.resolveMutation(context.WithoutCancel(ctx), identity, workerjournal.MutationIndeterminate)
	_ = service.markIndeterminate(context.WithoutCancel(ctx), attempt)
	return indeterminateError(cause)
}

func (service *Service) resolveMutation(
	ctx context.Context,
	identity workerhttp.MutationIdentity,
	status workerjournal.MutationStatus,
) error {
	_, err := service.journal.ResolveMutation(ctx, workerjournal.MutationResolution{
		MutationIdentity: identity,
		Status:           status,
		OccurredAt:       service.timestamp(),
	})
	if err != nil {
		return service.journalError(err)
	}
	return nil
}

func (service *Service) transition(
	ctx context.Context,
	attempt workerhttp.Attempt,
	state workerhttp.AttemptState,
) (workerhttp.Attempt, error) {
	transitioned, err := service.journal.TransitionAttempt(ctx, workerjournal.AttemptTransition{
		Reference:  attempt.AttemptReference,
		Expected:   attempt.State,
		State:      state,
		OccurredAt: service.timestamp(),
	})
	if err != nil {
		return workerhttp.Attempt{}, service.journalError(err)
	}
	return transitioned, nil
}

func (service *Service) markIndeterminate(
	ctx context.Context,
	attempt workerhttp.Attempt,
) error {
	service.eventMu.Lock()
	defer service.eventMu.Unlock()
	return service.markIndeterminateLocked(ctx, attempt)
}

func (service *Service) markIndeterminateLocked(
	ctx context.Context,
	attempt workerhttp.Attempt,
) error {
	current, err := service.journal.GetAttempt(ctx, attempt.AttemptReference)
	if err != nil {
		return service.journalError(err)
	}
	if current.State == workerhttp.AttemptStateTerminal || current.State == workerhttp.AttemptStateIndeterminate {
		service.closeSubscribersLocked(current.AttemptReference)
		return nil
	}
	_, err = service.transition(ctx, current, workerhttp.AttemptStateIndeterminate)
	if err == nil {
		service.closeSubscribersLocked(current.AttemptReference)
	}
	return err
}

func (service *Service) activeSession(
	reference workerhttp.AttemptReference,
) (*activeProviderSession, bool) {
	service.activeMu.RLock()
	defer service.activeMu.RUnlock()
	session, ok := service.active[reference]
	return session, ok
}

func (service *Service) removeActive(
	reference workerhttp.AttemptReference,
	live *activeProviderSession,
) {
	service.activeMu.Lock()
	if service.active[reference] == live {
		delete(service.active, reference)
	}
	service.activeMu.Unlock()
}

func (service *Service) timestamp() time.Time {
	return service.now().UTC()
}

func (service *Service) journalError(err error) error {
	switch {
	case errors.Is(err, workerjournal.ErrNotFound):
		return workerhttp.NewServiceError(workerhttp.ErrorNotFound, "worker attempt was not found", false, err)
	case errors.Is(err, workerjournal.ErrAttemptActive):
		return workerhttp.NewServiceError(workerhttp.ErrorAttemptActive, "another attempt for this session may still be active", false, err)
	case errors.Is(err, workerjournal.ErrAttemptConflict),
		errors.Is(err, workerjournal.ErrMutationConflict),
		errors.Is(err, workerjournal.ErrStateConflict):
		return workerhttp.NewServiceError(workerhttp.ErrorAttemptConflict, "worker attempt conflicts with durable state", false, err)
	case errors.Is(err, workerjournal.ErrEventConflict),
		errors.Is(err, workerjournal.ErrEventSequence):
		return workerhttp.NewServiceError(workerhttp.ErrorInvalidEventStream, "worker event conflicts with durable history", false, err)
	default:
		return err
	}
}

func mutationKind(command workerhttp.CommandType) workerjournal.MutationKind {
	return workerjournal.MutationKind(command)
}

func commandStateError(
	state workerhttp.AttemptState,
	command workerhttp.CommandType,
) error {
	return workerhttp.NewServiceError(
		workerhttp.ErrorAttemptConflict,
		fmt.Sprintf("command %q is not valid while the attempt is %q", command, state),
		false,
		nil,
	)
}

func indeterminateError(cause error) error {
	return workerhttp.NewServiceError(
		workerhttp.ErrorIndeterminateState,
		"worker cannot prove whether the provider action completed",
		false,
		cause,
	)
}
