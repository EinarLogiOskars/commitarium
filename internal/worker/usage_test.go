package worker

import "testing"

func TestUsageWindowsAreNamedByLengthAndMergedByName(t *testing.T) {
	for minutes, name := range map[int]string{300: "5-hour", 10080: "Weekly", 1440: "1-day", 0: ""} {
		if got := WindowName(minutes); got != name {
			t.Errorf("WindowName(%d) = %q, want %q", minutes, got, name)
		}
	}
	var tracker UsageTracker
	if windows, observed := tracker.Latest(); windows != nil && len(windows) != 0 || observed != nil {
		t.Fatalf("a new tracker reports %+v at %v", windows, observed)
	}
	low, high := 20.0, 80.0
	tracker.Merge(UsageWindow{Name: "5-hour", UsedPercent: &low})
	tracker.Merge(UsageWindow{Name: "Weekly", UsedPercent: &low})
	tracker.Merge(UsageWindow{Name: "5-hour", UsedPercent: &high})
	windows, observed := tracker.Latest()
	if len(windows) != 2 || *windows[0].UsedPercent != 80 || observed == nil {
		t.Fatalf("merged windows = %+v", windows)
	}
}
