package claudeadapter

import (
	"testing"
	"time"
)

func TestUsageIsReadFromTheUsageCommand(t *testing.T) {
	now := time.Date(2026, time.November, 7, 16, 20, 0, 0, time.UTC)
	text := "You are currently using your subscription to power your Claude Code usage\n\n" +
		"Current session: 39% used · resets Nov 7, 6:49pm (UTC)\n" +
		"Current week (all models): 30% used · resets Nov 9, 5:59am (UTC)\n" +
		"Current week (Fable): 0% used · resets Nov 9, 6am (UTC)\n\n" +
		"What's contributing to your limits usage?\n  82% of your usage was at >150k context\n"
	windows := parseUsage(text, now)
	if len(windows) != 3 {
		t.Fatalf("windows = %+v", windows)
	}
	want := []struct {
		name   string
		used   float64
		resets time.Time
	}{
		{"5-hour", 39, time.Date(2026, time.November, 7, 18, 49, 0, 0, time.UTC)},
		{"Weekly", 30, time.Date(2026, time.November, 9, 5, 59, 0, 0, time.UTC)},
		{"Weekly (Fable)", 0, time.Date(2026, time.November, 9, 6, 0, 0, 0, time.UTC)},
	}
	for index, expected := range want {
		got := windows[index]
		if got.Name != expected.name || got.UsedPercent == nil || *got.UsedPercent != expected.used ||
			got.ResetsAt == nil || !got.ResetsAt.Equal(expected.resets) || got.Limited {
			t.Errorf("window %d = %+v, want %+v", index, got, expected)
		}
	}
	if limited := parseUsage("Current session: 100% used · resets Jan 1, 1am (UTC)", now); len(limited) != 1 ||
		!limited[0].Limited || limited[0].ResetsAt.Year() != 2027 {
		t.Fatalf("a used-up window over the new year = %+v", limited)
	}
}
