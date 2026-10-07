package workerservice

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/EinarLogiOskars/commitarium/internal/worker"
	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
	"github.com/EinarLogiOskars/commitarium/internal/workerjournal"
)

func (service *Service) OpenEventStream(
	ctx context.Context,
	reference workerhttp.AttemptReference,
	afterSequence int64,
) (workerhttp.EventStream, error) {
	if err := reference.Validate(); err != nil || afterSequence < 0 {
		return workerhttp.EventStream{}, workerhttp.NewServiceError(
			workerhttp.ErrorInvalidRequest,
			"event replay request is invalid",
			false,
			err,
		)
	}
	if err := ctx.Err(); err != nil {
		return workerhttp.EventStream{}, err
	}

	// Event persistence/publication and replay/subscription share this lock.
	// That is the boundary which guarantees an event cannot fall into the gap
	// between the SQLite history query and registration for future events.
	service.eventMu.Lock()
	defer service.eventMu.Unlock()
	attempt, err := service.journal.GetAttempt(ctx, reference)
	if err != nil {
		return workerhttp.EventStream{}, service.journalError(err)
	}
	if afterSequence > attempt.LatestEventSequence {
		return workerhttp.EventStream{}, workerhttp.NewServiceError(
			workerhttp.ErrorInvalidRequest,
			"event cursor is ahead of durable worker history",
			false,
			nil,
		)
	}
	replay, err := service.journal.ListEventsAfter(ctx, reference, afterSequence)
	if err != nil {
		return workerhttp.EventStream{}, service.journalError(err)
	}
	boundary := attempt.LatestEventSequence
	if int64(len(replay)) != boundary-afterSequence {
		return workerhttp.EventStream{}, workerhttp.NewServiceError(
			workerhttp.ErrorInvalidEventStream,
			"durable event history is incomplete",
			false,
			nil,
		)
	}

	if attempt.State == workerhttp.AttemptStateTerminal ||
		attempt.State == workerhttp.AttemptStateIndeterminate ||
		!service.hasActiveSession(reference) {
		closedEvents := make(chan workerhttp.Event)
		closedPreviews := make(chan workerhttp.MessagePreview)
		close(closedEvents)
		close(closedPreviews)
		return workerhttp.EventStream{
			Replay: replay, ReplayThrough: boundary, Live: closedEvents,
			Previews: closedPreviews, Close: func() {},
		}, nil
	}

	service.nextSubID++
	subscriberID := service.nextSubID
	subscriber := &eventSubscriber{
		events:   make(chan workerhttp.Event, service.bufferSize),
		previews: make(chan workerhttp.MessagePreview, 1),
	}
	if service.subscribers[reference] == nil {
		service.subscribers[reference] = make(map[uint64]*eventSubscriber)
	}
	service.subscribers[reference][subscriberID] = subscriber
	var once sync.Once
	closeSubscription := func() {
		once.Do(func() {
			service.eventMu.Lock()
			defer service.eventMu.Unlock()
			service.removeSubscriberLocked(reference, subscriberID)
		})
	}
	return workerhttp.EventStream{
		Replay: replay, ReplayThrough: boundary, Live: subscriber.events,
		Previews: subscriber.previews, Close: closeSubscription,
	}, nil
}

func (service *Service) supervise(
	reference workerhttp.AttemptReference,
	live *activeProviderSession,
) {
	defer func() {
		service.removeActive(reference, live)
		close(live.finished)
	}()
	providerSession := live.session
	var previews <-chan worker.MessagePreview
	if previewSession, ok := providerSession.(worker.PreviewSession); ok {
		previews = previewSession.Previews()
	}
	for {
		select {
		case <-service.lifetime.Done():
			return
		case event, open := <-providerSession.Events():
			if !open {
				service.finishAttempt(reference, providerSession)
				return
			}
			if event.StreamID != "" {
				service.drainProviderPreviews(reference, &previews)
			}
			if err := service.recordProviderEvent(reference, event); err != nil {
				return
			}
		case preview, open := <-previews:
			if !open {
				previews = nil
				continue
			}
			service.publishProviderPreview(reference, preview)
		}
	}
}

func (service *Service) drainProviderPreviews(
	reference workerhttp.AttemptReference,
	previews *<-chan worker.MessagePreview,
) {
	for *previews != nil {
		select {
		case preview, open := <-*previews:
			if !open {
				*previews = nil
				return
			}
			service.publishProviderPreview(reference, preview)
		default:
			return
		}
	}
}

func (service *Service) publishProviderPreview(
	reference workerhttp.AttemptReference,
	providerPreview worker.MessagePreview,
) {
	if err := providerPreview.Validate(); err != nil {
		return
	}
	normalized, err := service.normalizer.Normalize(service.lifetime, worker.Event{
		Type: worker.EventMessage, Text: providerPreview.Text, StreamID: providerPreview.StreamID,
	})
	if err != nil || normalized.Type != workerhttp.EventMessage {
		return
	}
	preview := workerhttp.MessagePreview{
		AttemptReference: reference,
		StreamID:         providerPreview.StreamID,
		Text:             normalized.Text,
		OccurredAt:       service.timestamp(),
		Redaction:        normalized.Redaction,
		Truncation:       normalized.Truncation,
	}
	if err := preview.Validate(); err != nil {
		return
	}
	service.eventMu.Lock()
	defer service.eventMu.Unlock()
	service.publishPreviewLocked(preview)
}

func (service *Service) recordProviderEvent(
	reference workerhttp.AttemptReference,
	providerEvent worker.Event,
) error {
	service.eventMu.Lock()
	defer service.eventMu.Unlock()
	attempt, err := service.journal.GetAttempt(service.lifetime, reference)
	if err != nil {
		return service.journalError(err)
	}
	if attempt.State == workerhttp.AttemptStateTerminal || attempt.State == workerhttp.AttemptStateIndeterminate {
		return workerhttp.NewServiceError(
			workerhttp.ErrorIndeterminateState,
			"provider emitted activity after its attempt stopped accepting events",
			false,
			nil,
		)
	}
	normalized, err := service.normalizer.Normalize(service.lifetime, providerEvent)
	if err != nil {
		service.recordRedactionFailureLocked(reference, attempt.LatestEventSequence+1)
		_ = service.markIndeterminateLocked(service.lifetime, attempt)
		return workerhttp.NewServiceError(
			workerhttp.ErrorRedactionFailed,
			"provider activity normalization failed closed",
			false,
			err,
		)
	}
	event := workerhttp.Event{
		AttemptReference:   reference,
		Sequence:           attempt.LatestEventSequence + 1,
		Type:               normalized.Type,
		Text:               normalized.Text,
		StreamID:           normalized.StreamID,
		OccurredAt:         service.timestamp(),
		Redaction:          normalized.Redaction,
		Truncation:         normalized.Truncation,
		Activity:           normalized.Activity,
		RecoveryAssessment: normalized.RecoveryAssessment,
	}
	if err := event.Validate(); err != nil {
		service.recordRedactionFailureLocked(reference, attempt.LatestEventSequence+1)
		_ = service.markIndeterminateLocked(service.lifetime, attempt)
		return workerhttp.NewServiceError(
			workerhttp.ErrorRedactionFailed,
			"provider normalizer returned invalid activity",
			false,
			err,
		)
	}
	recorded, created, err := service.journal.AppendEvent(service.lifetime, workerjournal.EventAppend{
		Event: event, AcceptedAt: service.timestamp(),
	})
	if err != nil {
		_ = service.markIndeterminateLocked(service.lifetime, attempt)
		return service.journalError(err)
	}
	if created {
		service.publishLocked(recorded)
	}
	if err := service.applyEventStateLocked(attempt, recorded); err != nil {
		_ = service.markIndeterminateLocked(service.lifetime, attempt)
		return err
	}
	return nil
}

func (service *Service) recordRedactionFailureLocked(
	reference workerhttp.AttemptReference,
	sequence int64,
) {
	failure := workerhttp.Event{
		AttemptReference: reference,
		Sequence:         sequence,
		Type:             workerhttp.EventRedactionFailure,
		Text:             "Provider activity could not be safely normalized.",
		OccurredAt:       service.timestamp(),
		Redaction:        workerhttp.RedactionMetadata{},
	}
	recorded, created, err := service.journal.AppendEvent(service.lifetime, workerjournal.EventAppend{
		Event: failure, AcceptedAt: service.timestamp(),
	})
	if err == nil && created {
		service.publishLocked(recorded)
	}
}

func (service *Service) applyEventStateLocked(
	attempt workerhttp.Attempt,
	event workerhttp.Event,
) error {
	current, err := service.journal.GetAttempt(service.lifetime, attempt.AttemptReference)
	if err != nil {
		return service.journalError(err)
	}
	switch event.Type {
	case workerhttp.EventRecoveryAssessment:
		if current.State != workerhttp.AttemptStateRunning {
			return commandStateError(current.State, workerhttp.CommandPause)
		}
		current, err = service.transition(
			service.lifetime,
			current,
			workerhttp.AttemptStatePauseRequested,
		)
		if err != nil {
			return err
		}
		_, err = service.transition(service.lifetime, current, workerhttp.AttemptStatePaused)
		return err
	case workerhttp.EventPauseAcknowledged:
		if current.State != workerhttp.AttemptStatePauseRequested {
			return commandStateError(current.State, workerhttp.CommandPause)
		}
		_, err = service.transition(service.lifetime, current, workerhttp.AttemptStatePaused)
		return err
	case workerhttp.EventContinued:
		if current.State != workerhttp.AttemptStatePaused {
			return commandStateError(current.State, workerhttp.CommandContinue)
		}
		_, err = service.transition(service.lifetime, current, workerhttp.AttemptStateRunning)
		return err
	default:
		return nil
	}
}

func (service *Service) finishAttempt(
	reference workerhttp.AttemptReference,
	providerSession worker.Session,
) {
	result, err := providerSession.Wait(service.lifetime)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			ctx := context.WithoutCancel(service.lifetime)
			service.eventMu.Lock()
			defer service.eventMu.Unlock()
			if attempt, getErr := service.journal.GetAttempt(ctx, reference); getErr == nil {
				message := strings.TrimSpace(err.Error())
				if message == "" || len(message) > 4096 {
					message = "The provider process ended without returning a usable structured result."
				}
				_ = service.completeAttemptLocked(ctx, attempt, workerhttp.TerminalResult{
					Outcome: workerhttp.OutcomeFailed,
					Summary: "The provider process ended without returning a usable structured result.",
					Error: &workerhttp.ProtocolError{
						Code: workerhttp.ErrorIncompleteResult, Message: message, Retryable: true,
					},
				})
			}
		}
		return
	}
	service.eventMu.Lock()
	defer service.eventMu.Unlock()
	attempt, err := service.journal.GetAttempt(service.lifetime, reference)
	if err != nil {
		return
	}
	if attempt.ProviderSessionID != result.ProviderSessionID {
		_ = service.markIndeterminateLocked(service.lifetime, attempt)
		return
	}
	terminalResult := workerhttp.TerminalResult{
		Outcome:            workerhttp.Outcome(result.Outcome),
		Disposition:        workerhttp.Disposition(result.Disposition),
		Summary:            result.Summary,
		InterventionEffect: workerhttp.InterventionEffect(result.InterventionEffect),
	}
	if result.Publication != nil {
		terminalResult.Publication = &workerhttp.ImplementationPublication{
			CommitID:          result.Publication.CommitID,
			PullRequestNumber: result.Publication.PullRequestNumber,
		}
	}
	if result.Review != nil {
		terminalResult.Review = &workerhttp.ReviewPublication{
			CommitID:          result.Review.CommitID,
			PullRequestNumber: result.Review.PullRequestNumber,
			ReviewID:          result.Review.ReviewID,
		}
	}
	if result.ToolchainProposal != nil {
		terminalResult.ToolchainProposal = &workerhttp.ToolchainProposal{
			Tools: result.ToolchainProposal.Tools, Services: result.ToolchainProposal.Services,
		}
	}
	if result.EnvironmentRequest != nil {
		terminalResult.EnvironmentRequest = &workerhttp.EnvironmentRequest{
			SystemPackages: append([]string(nil), result.EnvironmentRequest.SystemPackages...),
			Reason:         result.EnvironmentRequest.Reason,
		}
	}
	terminalResult.GoalDraft = result.GoalDraft
	terminalResult.ImplementationPlan = result.ImplementationPlan
	terminalResult.AcceptanceTests = result.AcceptanceTests
	if result.Outcome == worker.OutcomeFailed {
		terminalResult.Error = &workerhttp.ProtocolError{
			Code:      workerhttp.ErrorInternal,
			Message:   result.Summary,
			Retryable: false,
		}
	}
	_ = service.completeAttemptLocked(service.lifetime, attempt, terminalResult)
}

// completeAttemptLocked records a known process ending. A missing or invalid
// provider result is a failed terminal attempt, not an indeterminate live
// process: the process has ended and only its required structured result is
// missing. The caller must hold eventMu.
func (service *Service) completeAttemptLocked(
	ctx context.Context,
	attempt workerhttp.Attempt,
	terminalResult workerhttp.TerminalResult,
) error {
	validationTime := service.timestamp()
	terminalCandidate := attempt
	terminalCandidate.State = workerhttp.AttemptStateTerminal
	terminalCandidate.UpdatedAt = validationTime
	terminalCandidate.EndedAt = &validationTime
	terminalCandidate.Result = &terminalResult
	if err := terminalCandidate.Validate(); err != nil {
		_ = service.markIndeterminateLocked(service.lifetime, attempt)
		return err
	}
	terminalEvent := workerhttp.Event{
		AttemptReference: attempt.AttemptReference,
		Sequence:         attempt.LatestEventSequence + 1,
		Type:             workerhttp.EventAttemptTerminal,
		Text:             terminalResult.Summary,
		OccurredAt:       service.timestamp(),
		Redaction:        workerhttp.RedactionMetadata{},
	}
	recorded, created, err := service.journal.AppendEvent(ctx, workerjournal.EventAppend{
		Event: terminalEvent, AcceptedAt: service.timestamp(),
	})
	if err != nil {
		_ = service.markIndeterminateLocked(service.lifetime, attempt)
		return err
	}
	if created {
		service.publishLocked(recorded)
	}
	current, err := service.journal.GetAttempt(ctx, attempt.AttemptReference)
	if err != nil {
		return err
	}
	_, err = service.journal.TransitionAttempt(ctx, workerjournal.AttemptTransition{
		Reference:         attempt.AttemptReference,
		Expected:          current.State,
		State:             workerhttp.AttemptStateTerminal,
		ProviderSessionID: attempt.ProviderSessionID,
		OccurredAt:        service.timestamp(),
		Result:            &terminalResult,
	})
	if err != nil {
		_ = service.markIndeterminateLocked(service.lifetime, current)
		return err
	}
	service.closeSubscribersLocked(attempt.AttemptReference)
	return nil
}

func (service *Service) hasActiveSession(reference workerhttp.AttemptReference) bool {
	service.activeMu.RLock()
	defer service.activeMu.RUnlock()
	_, ok := service.active[reference]
	return ok
}

func (service *Service) publishLocked(event workerhttp.Event) {
	for subscriberID, subscriber := range service.subscribers[event.AttemptReference] {
		select {
		case subscriber.events <- event:
		default:
			delete(service.subscribers[event.AttemptReference], subscriberID)
			close(subscriber.events)
			close(subscriber.previews)
		}
	}
	if len(service.subscribers[event.AttemptReference]) == 0 {
		delete(service.subscribers, event.AttemptReference)
	}
}

func (service *Service) publishPreviewLocked(preview workerhttp.MessagePreview) {
	for _, subscriber := range service.subscribers[preview.AttemptReference] {
		select {
		case subscriber.previews <- preview:
			continue
		default:
		}
		// Previews are cumulative. Replace a slow consumer's stale snapshot
		// without evicting it or interfering with durable event delivery.
		select {
		case <-subscriber.previews:
		default:
		}
		select {
		case subscriber.previews <- preview:
		default:
		}
	}
}

func (service *Service) closeSubscribersLocked(reference workerhttp.AttemptReference) {
	for subscriberID, subscriber := range service.subscribers[reference] {
		delete(service.subscribers[reference], subscriberID)
		close(subscriber.events)
		close(subscriber.previews)
	}
	delete(service.subscribers, reference)
}

func (service *Service) removeSubscriberLocked(
	reference workerhttp.AttemptReference,
	subscriberID uint64,
) {
	subscriber, ok := service.subscribers[reference][subscriberID]
	if !ok {
		return
	}
	delete(service.subscribers[reference], subscriberID)
	close(subscriber.events)
	close(subscriber.previews)
	if len(service.subscribers[reference]) == 0 {
		delete(service.subscribers, reference)
	}
}
