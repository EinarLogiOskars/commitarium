package worker

import (
	"fmt"
	"sync"
	"time"
)

// UsageWindow is one of the provider account's usage limits as the agent
// last reported it while working: how much of it is used, when it resets,
// and whether work is held at it.
type UsageWindow struct {
	Name        string
	UsedPercent *float64
	ResetsAt    *time.Time
	Limited     bool
}

// UsageTracker keeps the latest usage windows an adapter's sessions saw.
// Providers report them only while they work, so they can be old.
type UsageTracker struct {
	mu         sync.Mutex
	windows    []UsageWindow
	observedAt *time.Time
}

// Record replaces the windows with the latest report.
func (tracker *UsageTracker) Record(windows []UsageWindow) {
	now := time.Now().UTC()
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	tracker.windows = append([]UsageWindow(nil), windows...)
	tracker.observedAt = &now
}

// Merge updates one named window, keeping the others.
func (tracker *UsageTracker) Merge(window UsageWindow) {
	now := time.Now().UTC()
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	for index := range tracker.windows {
		if tracker.windows[index].Name == window.Name {
			tracker.windows[index] = window
			tracker.observedAt = &now
			return
		}
	}
	tracker.windows = append(tracker.windows, window)
	tracker.observedAt = &now
}

// Latest is the last report and when it was seen; nil before any.
func (tracker *UsageTracker) Latest() ([]UsageWindow, *time.Time) {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	return append([]UsageWindow(nil), tracker.windows...), tracker.observedAt
}

// WindowName names a usage window by its length.
func WindowName(minutes int) string {
	switch {
	case minutes <= 0:
		return ""
	case minutes%(7*24*60) == 0:
		if weeks := minutes / (7 * 24 * 60); weeks > 1 {
			return fmt.Sprintf("%d-week", weeks)
		}
		return "Weekly"
	case minutes%(24*60) == 0:
		return fmt.Sprintf("%d-day", minutes/(24*60))
	case minutes%60 == 0:
		return fmt.Sprintf("%d-hour", minutes/60)
	default:
		return fmt.Sprintf("%d-minute", minutes)
	}
}
