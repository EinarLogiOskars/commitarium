package workerservice

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/worker"
	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
)

func TestPublishProviderPreviewNormalizesAndKeepsLatestSnapshot(t *testing.T) {
	reference := workerhttp.AttemptReference{SessionID: "ses_test", AttemptID: "att_test"}
	subscriber := &eventSubscriber{
		events:   make(chan workerhttp.Event, 1),
		previews: make(chan workerhttp.MessagePreview, 1),
	}
	fixedTime := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	service := &Service{
		normalizer: NormalizerFunc(func(_ context.Context, event worker.Event) (NormalizedEvent, error) {
			return NormalizedEvent{
				Type: workerhttp.EventMessage, Text: event.Text + " [safe]",
				Redaction: workerhttp.RedactionMetadata{},
			}, nil
		}),
		lifetime: context.Background(),
		now:      func() time.Time { return fixedTime },
		subscribers: map[workerhttp.AttemptReference]map[uint64]*eventSubscriber{
			reference: {1: subscriber},
		},
	}

	service.publishProviderPreview(reference, worker.MessagePreview{StreamID: "item_final", Text: "first"})
	service.publishProviderPreview(reference, worker.MessagePreview{StreamID: "item_final", Text: "second"})

	select {
	case preview := <-subscriber.previews:
		if preview.StreamID != "item_final" || preview.Text != "second [safe]" ||
			preview.AttemptReference != reference || !preview.OccurredAt.Equal(fixedTime) {
			t.Fatalf("unexpected preview %+v", preview)
		}
	default:
		t.Fatal("expected latest preview")
	}
	if len(subscriber.events) != 0 {
		t.Fatal("preview interfered with durable event channel")
	}
}

func TestPublishProviderPreviewFailsClosedWithoutClosingDurableDelivery(t *testing.T) {
	reference := workerhttp.AttemptReference{SessionID: "ses_test", AttemptID: "att_test"}
	subscriber := &eventSubscriber{
		events:   make(chan workerhttp.Event, 1),
		previews: make(chan workerhttp.MessagePreview, 1),
	}
	service := &Service{
		normalizer: NormalizerFunc(func(context.Context, worker.Event) (NormalizedEvent, error) {
			return NormalizedEvent{}, errors.New("redaction unavailable")
		}),
		lifetime: context.Background(),
		now:      time.Now,
		subscribers: map[workerhttp.AttemptReference]map[uint64]*eventSubscriber{
			reference: {1: subscriber},
		},
	}

	service.publishProviderPreview(reference, worker.MessagePreview{StreamID: "item_final", Text: "unsafe"})
	if len(subscriber.previews) != 0 {
		t.Fatal("unsafe preview was published")
	}
	service.publishLocked(workerhttp.Event{
		AttemptReference: reference, Sequence: 1, Type: workerhttp.EventMessage,
		Text: "safe final", OccurredAt: time.Now().UTC(), Redaction: workerhttp.RedactionMetadata{},
	})
	if len(subscriber.events) != 1 {
		t.Fatal("preview normalization failure disturbed durable subscriber")
	}
}
