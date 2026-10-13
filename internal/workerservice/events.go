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
	providerSession := live.current()
	previews := sessionPreviews(providerSession)
	for {
		select {
		case <-service.lifetime.Done():
			return
		case event, open := <-providerSession.Events():
			if !open {
				if corrected := service.resubmitRejectedResult(reference, live); corrected != nil {
					providerSession = corrected
					previews = sessionPreviews(corrected)
					continue
				}
				service.finishAttempt(reference, providerSession)
				return
			}
			if event.StreamID != "" {
				service.drainProviderPreviews(reference, &previews)
			}
			if err := service.recordProviderEvent(reference, event); err != nil {
				if service.forceStopAfterSupervisionFailure(providerSession) {
					service.finishAttempt(reference, providerSession)
				} else {
					service.activeMu.Lock()
					service.unfenced[reference] = struct{}{}
					service.activeMu.Unlock()
				}
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

func sessionPreviews(providerSession worker.Session) <-chan worker.MessagePreview {
	if previewSession, ok := providerSession.(worker.PreviewSession); ok {
		return previewSession.Previews()
	}
	return nil
}

// maxResultCorrections bounds how often one turn's rejected structured result
// goes back to the same provider conversation before the attempt fails.
const maxResultCorrections = 2

// resubmitRejectedResult hands a rejected structured result's validation error
// back to the same provider conversation and asks only for a corrected result.
// The correction stays inside the running attempt, so the coordinator sees
// neither a failure nor a blocker while it happens. It returns nil when the
// turn should end as it is.
func (service *Service) resubmitRejectedResult(
	reference workerhttp.AttemptReference,
	live *activeProviderSession,
) worker.Session {
	previous := live.current()
	// Wait before taking live.mu, so a hung provider can still be force-stopped.
	_, err := previous.Wait(service.lifetime)
	if !errors.Is(err, worker.ErrInvalidStructuredOutput) {
		return nil
	}
	live.mu.Lock()
	defer live.mu.Unlock()
	if live.corrections >= maxResultCorrections || live.launch.OutputContract == "" {
		return nil
	}
	attempt, getErr := service.journal.GetAttempt(service.lifetime, reference)
	if getErr != nil || attempt.State != workerhttp.AttemptStateRunning ||
		attempt.ProviderSessionID == "" || attempt.ProviderSessionID != previous.ProviderSessionID() {
		return nil
	}
	rejection := rejectedResultReason(err)
	if service.recordProviderEvent(reference, worker.Event{
		Type: worker.EventActivity, Text: "Commitarium rejected the structured result (" + rejection +
			") and asked the agent to correct it.",
		Activity: &worker.Activity{Kind: worker.ActivityKindNarration},
	}) != nil {
		return nil
	}
	corrected, resumeErr := service.provider.Resume(service.lifetime, worker.ResumeRequest{
		SessionRequest:    live.launch.Clone(),
		ProviderSessionID: attempt.ProviderSessionID,
		Recovery:          worker.RecoveryContext{Briefing: resultCorrectionPrompt(rejection)},
	})
	if resumeErr != nil || corrected == nil || corrected.ProviderSessionID() != attempt.ProviderSessionID {
		service.stopUnownedSession(corrected)
		return nil
	}
	live.corrections++
	live.session = corrected
	return corrected
}

// rejectedResultReason is the validation error without the provider and
// package prefixes the agent has no use for.
func rejectedResultReason(err error) string {
	message := err.Error()
	marker := worker.ErrInvalidStructuredOutput.Error() + ": "
	if index := strings.Index(message, marker); index >= 0 {
		message = message[index+len(marker):]
	}
	return strings.TrimSpace(message)
}

func resultCorrectionPrompt(rejection string) string {
	return "Commitarium could not accept your final structured result: " + rejection + ". " +
		"Do not redo or repeat any work, and do not repeat external effects such as commits, pushes, " +
		"pull requests or reviews. Return only a corrected final structured result for the same outcome, " +
		"following the output contract exactly."
}

func (service *Service) forceStopAfterSupervisionFailure(providerSession worker.Session) bool {
	forceStoppable, ok := providerSession.(worker.ForceStoppableSession)
	if !ok {
		return false
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(service.lifetime), service.forceStopTimeout)
	defer cancel()
	return forceStoppable.ForceStop(ctx, "worker supervision failed closed") == nil
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
		if result.ToolchainProposal.Run != nil {
			terminalResult.ToolchainProposal.Run = &workerhttp.ToolchainRunConfig{
				Open: workerhttp.ToolchainRunOpen{
					Service: result.ToolchainProposal.Run.Open.Service,
					Port:    result.ToolchainProposal.Run.Open.Port,
				},
			}
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
	terminalResult.HandoffBrief = result.HandoffBrief
	terminalResult.SessionTitle = result.SessionTitle
	terminalResult.VisionPlan = result.VisionPlan
	terminalResult.AssistantTurn = result.AssistantTurn
	if result.Usage != nil {
		usage := workerhttp.TokenUsage(*result.Usage)
		terminalResult.Usage = &usage
	}
	if result.Outcome == worker.OutcomeFailed {
		terminalResult.Error = &workerhttp.ProtocolError{
			Code:      workerhttp.ErrorInternal,
			Message:   result.Summary,
			Retryable: false,
		}
		if result.LoginRequired {
			terminalResult.Error.Code = workerhttp.ErrorProviderLoginRequired
			terminalResult.Error.Retryable = true
		}
		if result.UsageLimited && !result.LoginRequired {
			terminalResult.Error.Code = workerhttp.ErrorUsageLimit
			terminalResult.Error.Retryable = true
			if result.LimitResetsAt != nil {
				resetsAt := result.LimitResetsAt.UTC()
				terminalResult.Error.ResetsAt = &resetsAt
			}
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

func (service *Service) hasUnfencedSession(reference workerhttp.AttemptReference) bool {
	service.activeMu.RLock()
	defer service.activeMu.RUnlock()
	_, ok := service.unfenced[reference]
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
