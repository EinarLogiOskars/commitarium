// Package workeringest connects the worker event transport to durable,
// coordinator-visible session activity.
package workeringest

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/worker"
	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
)

var ErrFilterChangedIdentity = errors.New("worker event filter changed immutable event identity")

// Reader is the narrow part of workerhttp.EventReader needed by ingestion.
// A controlled fake can implement it without opening an HTTP connection.
type Reader interface {
	Next() (workerhttp.Event, error)
}

// Filter is the coordinator's final fail-closed safety check before activity is
// persisted or published. It may redact or truncate Text and update the related
// metadata, but it cannot change which worker event is being acknowledged.
type Filter interface {
	Filter(context.Context, workerhttp.Event) (workerhttp.Event, error)
}

type FilterFunc func(context.Context, workerhttp.Event) (workerhttp.Event, error)

func (function FilterFunc) Filter(
	ctx context.Context,
	event workerhttp.Event,
) (workerhttp.Event, error) {
	return function(ctx, event)
}

type Recorder interface {
	RecordWorkerEvent(
		context.Context,
		string,
		string,
		int64,
		time.Time,
		worker.Event,
	) (execution.Event, bool, error)
}

var _ Reader = (*workerhttp.EventReader)(nil)
var _ Recorder = (*execution.Service)(nil)

type Service struct {
	recorder Recorder
	filter   Filter
}

func NewService(recorder Recorder, filter Filter) *Service {
	return &Service{recorder: recorder, filter: filter}
}

// IngestNext reads and handles exactly one worker event. Stream lifecycle and
// reconnect policy intentionally remain the caller's responsibility.
func (s *Service) IngestNext(
	ctx context.Context,
	reader Reader,
) (execution.Event, bool, error) {
	event, err := reader.Next()
	if err != nil {
		return execution.Event{}, false, fmt.Errorf("read worker event: %w", err)
	}
	if err := event.Validate(); err != nil {
		return execution.Event{}, false, fmt.Errorf("validate worker event: %w", err)
	}
	filtered, err := s.filter.Filter(ctx, event)
	if err != nil {
		return execution.Event{}, false, fmt.Errorf("filter worker event: %w", err)
	}
	if err := filtered.Validate(); err != nil {
		return execution.Event{}, false, fmt.Errorf("validate filtered worker event: %w", err)
	}
	if !sameImmutableIdentity(event, filtered) {
		return execution.Event{}, false, ErrFilterChangedIdentity
	}

	visibleEvent := worker.Event{
		Type: worker.EventType(filtered.Type),
		Text: filtered.Text,
	}
	if filtered.RecoveryAssessment != nil {
		visibleEvent.RecoveryAssessment = &worker.RecoveryAssessment{
			Consistent:         filtered.RecoveryAssessment.Consistent,
			RequiresUserReview: filtered.RecoveryAssessment.RequiresUserReview,
		}
	}
	recorded, created, err := s.recorder.RecordWorkerEvent(
		ctx,
		filtered.SessionID,
		filtered.AttemptID,
		filtered.Sequence,
		filtered.OccurredAt,
		visibleEvent,
	)
	if err != nil {
		return execution.Event{}, false, fmt.Errorf("persist worker event: %w", err)
	}
	return recorded, created, nil
}

func sameImmutableIdentity(before workerhttp.Event, after workerhttp.Event) bool {
	if before.AttemptReference != after.AttemptReference ||
		before.Sequence != after.Sequence ||
		before.Type != after.Type ||
		!before.OccurredAt.Equal(after.OccurredAt) {
		return false
	}
	if before.RecoveryAssessment == nil || after.RecoveryAssessment == nil {
		return before.RecoveryAssessment == nil && after.RecoveryAssessment == nil
	}
	return *before.RecoveryAssessment == *after.RecoveryAssessment
}
