package workerservice

import (
	"context"
	"errors"
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
		closed := make(chan workerhttp.Event)
		close(closed)
		return workerhttp.EventStream{
			Replay: replay, ReplayThrough: boundary, Live: closed, Close: func() {},
		}, nil
	}

	service.nextSubID++
	subscriberID := service.nextSubID
	live := make(chan workerhttp.Event, service.bufferSize)
	if service.subscribers[reference] == nil {
		service.subscribers[reference] = make(map[uint64]chan workerhttp.Event)
	}
	service.subscribers[reference][subscriberID] = live
	var once sync.Once
	closeSubscription := func() {
		once.Do(func() {
			service.eventMu.Lock()
			defer service.eventMu.Unlock()
			service.removeSubscriberLocked(reference, subscriberID)
		})
	}
	return workerhttp.EventStream{
		Replay: replay, ReplayThrough: boundary, Live: live, Close: closeSubscription,
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
	for {
		select {
		case <-service.lifetime.Done():
			return
		case event, open := <-providerSession.Events():
			if !open {
				service.finishAttempt(reference, providerSession)
				return
			}
			if err := service.recordProviderEvent(reference, event); err != nil {
				return
			}
		}
	}
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
		OccurredAt:         service.timestamp(),
		Redaction:          normalized.Redaction,
		Truncation:         normalized.Truncation,
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
			if attempt, getErr := service.journal.GetAttempt(context.WithoutCancel(service.lifetime), reference); getErr == nil {
				_ = service.markIndeterminate(context.WithoutCancel(service.lifetime), attempt)
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
		Outcome:     workerhttp.Outcome(result.Outcome),
		Disposition: workerhttp.Disposition(result.Disposition),
		Summary:     result.Summary,
	}
	validationTime := service.timestamp()
	terminalCandidate := attempt
	terminalCandidate.State = workerhttp.AttemptStateTerminal
	terminalCandidate.UpdatedAt = validationTime
	terminalCandidate.EndedAt = &validationTime
	terminalCandidate.Result = &terminalResult
	if err := terminalCandidate.Validate(); err != nil {
		_ = service.markIndeterminateLocked(service.lifetime, attempt)
		return
	}
	terminalEvent := workerhttp.Event{
		AttemptReference: reference,
		Sequence:         attempt.LatestEventSequence + 1,
		Type:             workerhttp.EventAttemptTerminal,
		Text:             result.Summary,
		OccurredAt:       service.timestamp(),
		Redaction:        workerhttp.RedactionMetadata{},
	}
	recorded, created, err := service.journal.AppendEvent(service.lifetime, workerjournal.EventAppend{
		Event: terminalEvent, AcceptedAt: service.timestamp(),
	})
	if err != nil {
		_ = service.markIndeterminateLocked(service.lifetime, attempt)
		return
	}
	if created {
		service.publishLocked(recorded)
	}
	current, err := service.journal.GetAttempt(service.lifetime, reference)
	if err != nil {
		return
	}
	_, err = service.journal.TransitionAttempt(service.lifetime, workerjournal.AttemptTransition{
		Reference:         reference,
		Expected:          current.State,
		State:             workerhttp.AttemptStateTerminal,
		ProviderSessionID: result.ProviderSessionID,
		OccurredAt:        service.timestamp(),
		Result:            &terminalResult,
	})
	if err != nil {
		_ = service.markIndeterminateLocked(service.lifetime, current)
		return
	}
	service.closeSubscribersLocked(reference)
}

func (service *Service) hasActiveSession(reference workerhttp.AttemptReference) bool {
	service.activeMu.RLock()
	defer service.activeMu.RUnlock()
	_, ok := service.active[reference]
	return ok
}

func (service *Service) publishLocked(event workerhttp.Event) {
	for subscriberID, events := range service.subscribers[event.AttemptReference] {
		select {
		case events <- event:
		default:
			delete(service.subscribers[event.AttemptReference], subscriberID)
			close(events)
		}
	}
	if len(service.subscribers[event.AttemptReference]) == 0 {
		delete(service.subscribers, event.AttemptReference)
	}
}

func (service *Service) closeSubscribersLocked(reference workerhttp.AttemptReference) {
	for subscriberID, events := range service.subscribers[reference] {
		delete(service.subscribers[reference], subscriberID)
		close(events)
	}
	delete(service.subscribers, reference)
}

func (service *Service) removeSubscriberLocked(
	reference workerhttp.AttemptReference,
	subscriberID uint64,
) {
	events, ok := service.subscribers[reference][subscriberID]
	if !ok {
		return
	}
	delete(service.subscribers[reference], subscriberID)
	close(events)
	if len(service.subscribers[reference]) == 0 {
		delete(service.subscribers, reference)
	}
}
