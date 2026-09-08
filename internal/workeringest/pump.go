package workeringest

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
)

var (
	ErrStreamEndedActive       = errors.New("worker event stream ended while the attempt was active")
	ErrAttemptIndeterminate    = errors.New("worker attempt state is indeterminate")
	ErrAttemptStateConflict    = errors.New("worker attempt state conflicts with durable ingestion state")
	ErrTerminalEventsRemaining = errors.New("terminal worker attempt still has events to ingest")
)

type CheckpointReader interface {
	GetWorkerAttempt(context.Context, string) (execution.WorkerAttemptCheckpoint, error)
}

type EventIngester interface {
	IngestNext(context.Context, Reader) (execution.Event, bool, error)
}

type EventStream interface {
	Reader
	Close() error
}

type AttemptSource interface {
	OpenEventStream(
		context.Context,
		workerhttp.AttemptReference,
		int64,
	) (EventStream, error)
	GetAttempt(context.Context, workerhttp.AttemptReference) (workerhttp.Attempt, error)
}

// HTTPAttemptSource adapts workerhttp.Client's concrete EventReader return type
// to the small interface used by Pump and its controlled tests.
type HTTPAttemptSource struct {
	client *workerhttp.Client
}

func NewHTTPAttemptSource(client *workerhttp.Client) *HTTPAttemptSource {
	return &HTTPAttemptSource{client: client}
}

func (source *HTTPAttemptSource) OpenEventStream(
	ctx context.Context,
	reference workerhttp.AttemptReference,
	afterSequence int64,
) (EventStream, error) {
	return source.client.OpenEventStream(ctx, reference, afterSequence)
}

func (source *HTTPAttemptSource) GetAttempt(
	ctx context.Context,
	reference workerhttp.AttemptReference,
) (workerhttp.Attempt, error) {
	return source.client.GetAttempt(ctx, reference)
}

var _ CheckpointReader = (*execution.Service)(nil)
var _ EventIngester = (*Service)(nil)
var _ AttemptSource = (*HTTPAttemptSource)(nil)

type PumpResult struct {
	Reference            workerhttp.AttemptReference
	StartedAfterSequence int64
	LastAcceptedSequence int64
	NewEvents            int
	Attempt              workerhttp.Attempt
	AttemptInspected     bool
}

// Pump consumes one existing worker attempt. It does not reconnect, retry, or
// launch another process; those policy decisions belong to a later supervisor.
type Pump struct {
	checkpoints CheckpointReader
	ingester    EventIngester
	source      AttemptSource
}

func NewPump(
	checkpoints CheckpointReader,
	ingester EventIngester,
	source AttemptSource,
) *Pump {
	return &Pump{checkpoints: checkpoints, ingester: ingester, source: source}
}

func (pump *Pump) Run(
	ctx context.Context,
	sessionID string,
) (PumpResult, error) {
	checkpoint, err := pump.checkpoints.GetWorkerAttempt(ctx, sessionID)
	if err != nil {
		return PumpResult{}, fmt.Errorf("load worker event checkpoint: %w", err)
	}
	if err := checkpoint.Validate(); err != nil {
		return PumpResult{}, fmt.Errorf(
			"%w: invalid checkpoint: %v",
			ErrAttemptStateConflict,
			err,
		)
	}
	if checkpoint.SessionID != sessionID {
		return PumpResult{}, fmt.Errorf(
			"%w: checkpoint belongs to session %q, not %q",
			ErrAttemptStateConflict,
			checkpoint.SessionID,
			sessionID,
		)
	}
	reference := workerhttp.AttemptReference{
		SessionID: checkpoint.SessionID,
		AttemptID: checkpoint.AttemptID,
	}
	result := PumpResult{
		Reference:            reference,
		StartedAfterSequence: checkpoint.LastEventSequence,
		LastAcceptedSequence: checkpoint.LastEventSequence,
	}
	stream, err := pump.source.OpenEventStream(ctx, reference, checkpoint.LastEventSequence)
	if err != nil {
		return result, fmt.Errorf("open worker event stream: %w", err)
	}
	streamClosed := false
	closeStream := func() {
		if !streamClosed {
			streamClosed = true
			_ = stream.Close()
		}
	}
	defer closeStream()

	for {
		event, created, ingestErr := pump.ingester.IngestNext(ctx, stream)
		if ingestErr == nil {
			if event.SessionID != reference.SessionID ||
				event.WorkerAttemptID != reference.AttemptID ||
				event.WorkerEventSequence != result.LastAcceptedSequence+1 {
				return result, ErrAttemptStateConflict
			}
			result.LastAcceptedSequence = event.WorkerEventSequence
			if created {
				result.NewEvents++
			}
			continue
		}
		// Stop delivery before inspecting authoritative attempt state. This is
		// especially important when filtering or persistence, rather than the
		// network, caused ingestion to stop.
		closeStream()
		if ctxErr := ctx.Err(); ctxErr != nil {
			return result, ctxErr
		}
		if errors.Is(ingestErr, context.Canceled) ||
			errors.Is(ingestErr, context.DeadlineExceeded) {
			return result, ingestErr
		}

		attempt, inspectErr := pump.source.GetAttempt(ctx, reference)
		if inspectErr != nil {
			return result, errors.Join(
				fmt.Errorf("consume worker event stream: %w", ingestErr),
				fmt.Errorf("inspect worker attempt after stream ended: %w", inspectErr),
			)
		}
		result.Attempt = attempt
		result.AttemptInspected = true
		if err := validateInspectedAttempt(result); err != nil {
			if !errors.Is(ingestErr, io.EOF) {
				return result, errors.Join(
					fmt.Errorf("consume worker event stream: %w", ingestErr),
					err,
				)
			}
			return result, err
		}
		if !errors.Is(ingestErr, io.EOF) {
			return result, fmt.Errorf("consume worker event stream: %w", ingestErr)
		}
		return result, classifyStreamEnd(result)
	}
}

func validateInspectedAttempt(result PumpResult) error {
	if err := result.Attempt.Validate(); err != nil {
		return fmt.Errorf("%w: invalid inspected attempt: %v", ErrAttemptStateConflict, err)
	}
	if result.Attempt.AttemptReference != result.Reference ||
		result.Attempt.LatestEventSequence < result.LastAcceptedSequence {
		return ErrAttemptStateConflict
	}
	return nil
}

func classifyStreamEnd(result PumpResult) error {
	switch result.Attempt.State {
	case workerhttp.AttemptStateIndeterminate:
		return ErrAttemptIndeterminate
	case workerhttp.AttemptStateTerminal:
		if result.Attempt.LatestEventSequence > result.LastAcceptedSequence {
			return ErrTerminalEventsRemaining
		}
		return nil
	default:
		return ErrStreamEndedActive
	}
}
