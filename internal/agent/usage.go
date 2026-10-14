package agent

import (
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
)

// Usage is an agent's account usage as its worker last reported it. The
// coordinator keeps an agent at its limit from new work, and the assistant
// reads it to prefer the agent with more room (ADR-027).
type Usage struct {
	Agent   string                   `json:"agent"`
	Windows []workerhttp.UsageWindow `json:"windows"`
	// ObservedAt is when the agent last reported; nil until it has worked.
	ObservedAt *time.Time `json:"observed_at,omitempty"`
}

// LimitedUntil reports whether one of the account's limits is reached at now,
// and when the latest such limit resets, when known. A limit whose reset time
// has passed no longer counts.
func (usage Usage) LimitedUntil(now time.Time) (bool, *time.Time) {
	limited := false
	var until *time.Time
	for _, window := range usage.Windows {
		full := window.Limited || (window.UsedPercent != nil && *window.UsedPercent >= 100)
		if !full || (window.ResetsAt != nil && !window.ResetsAt.After(now)) {
			continue
		}
		limited = true
		if window.ResetsAt != nil && (until == nil || window.ResetsAt.After(*until)) {
			resets := *window.ResetsAt
			until = &resets
		}
	}
	return limited, until
}

// Remaining is the percentage left in the account's tightest limit; known is
// false when no limit reports a percentage. A limit whose reset time has
// passed counts as fresh.
func (usage Usage) Remaining(now time.Time) (percent float64, known bool) {
	percent = 100
	for _, window := range usage.Windows {
		if window.UsedPercent == nil {
			continue
		}
		known = true
		if window.ResetsAt != nil && !window.ResetsAt.After(now) {
			continue
		}
		if left := 100 - *window.UsedPercent; left < percent {
			percent = left
		}
	}
	if percent < 0 {
		percent = 0
	}
	return percent, known
}
