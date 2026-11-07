package main

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/worker"
)

func TestMissingUsageIsReadInTheBackgroundAtMostSoOften(t *testing.T) {
	var reads atomic.Int32
	done := make(chan struct{}, 4)
	reader := newUsageReader(func(context.Context) error {
		reads.Add(1)
		done <- struct{}{}
		return nil
	})
	<-done // the read at start
	var tracker worker.UsageTracker
	source := usageSource(&tracker, reader)
	if windows, observed := source(); len(windows) != 0 || observed != nil {
		t.Fatalf("usage before any report = %v, %v", windows, observed)
	}
	time.Sleep(20 * time.Millisecond)
	if got := reads.Load(); got != 1 {
		t.Fatalf("read %d times within usageReadEvery, want 1", got)
	}
	reader.mu.Lock()
	reader.last = time.Now().Add(-usageReadEvery - time.Second)
	reader.mu.Unlock()
	source()
	<-done
	if got := reads.Load(); got != 2 {
		t.Fatalf("missing usage was not read again: %d reads", got)
	}
	used := 12.0
	tracker.Record([]worker.UsageWindow{{Name: "Weekly", UsedPercent: &used}})
	reader.mu.Lock()
	reader.last = time.Now().Add(-usageReadEvery - time.Second)
	reader.mu.Unlock()
	if windows, _ := source(); len(windows) != 1 {
		t.Fatalf("windows = %v", windows)
	}
	time.Sleep(20 * time.Millisecond)
	if got := reads.Load(); got != 2 {
		t.Fatalf("fresh usage was read again: %d reads", got)
	}
}
