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

type frameReader interface {
	NextFrame() (workerhttp.StreamFrame, error)
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

type PreviewPublisher interface {
	PublishWorkerPreview(execution.MessagePreview) error
}

var _ Reader = (*workerhttp.EventReader)(nil)
var _ Recorder = (*execution.Service)(nil)

type Service struct {
	recorder Recorder
	previews PreviewPublisher
	filter   Filter
}

func NewService(recorder Recorder, filter Filter) *Service {
	previews, _ := recorder.(PreviewPublisher)
	return &Service{recorder: recorder, previews: previews, filter: filter}
}

// IngestNext reads and handles exactly one worker event. Stream lifecycle and
// reconnect policy intentionally remain the caller's responsibility.
func (s *Service) IngestNext(
	ctx context.Context,
	reader Reader,
) (execution.Event, bool, error) {
	var event workerhttp.Event
	for {
		if framed, ok := reader.(frameReader); ok {
			frame, err := framed.NextFrame()
			if err != nil {
				return execution.Event{}, false, fmt.Errorf("read worker event: %w", err)
			}
			if frame.Preview != nil {
				if s.previews != nil {
					preview := execution.MessagePreview{
						SessionID: frame.Preview.SessionID, AttemptID: frame.Preview.AttemptID,
						StreamID: frame.Preview.StreamID, Text: frame.Preview.Text,
						OccurredAt: frame.Preview.OccurredAt,
					}
					if err := s.previews.PublishWorkerPreview(preview); err != nil {
						return execution.Event{}, false, fmt.Errorf("publish worker preview: %w", err)
					}
				}
				continue
			}
			if frame.Event == nil {
				return execution.Event{}, false, errors.New("read worker event: empty stream frame")
			}
			event = *frame.Event
			break
		}
		var err error
		event, err = reader.Next()
		if err != nil {
			return execution.Event{}, false, fmt.Errorf("read worker event: %w", err)
		}
		break
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
		Type:     worker.EventType(filtered.Type),
		Text:     filtered.Text,
		StreamID: filtered.StreamID,
	}
	if filtered.Activity != nil {
		visibleEvent.Activity = &worker.Activity{
			Kind:       worker.ActivityKind(filtered.Activity.Kind),
			Command:    filtered.Activity.Command,
			ExitCode:   filtered.Activity.ExitCode,
			DurationMS: filtered.Activity.DurationMS,
			Operation:  worker.FileOperation(filtered.Activity.Operation),
			Path:       filtered.Activity.Path,
			OldPath:    filtered.Activity.OldPath,
			Additions:  filtered.Activity.Additions,
			Deletions:  filtered.Activity.Deletions,
		}
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
