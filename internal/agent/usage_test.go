package agent

import (
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
)

func TestUsageSaysWhenAnAgentIsLimitedAndHowMuchIsLeft(t *testing.T) {
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	soon, later, past := now.Add(time.Hour), now.Add(24*time.Hour), now.Add(-time.Minute)
	percent := func(value float64) *float64 { return &value }

	fresh := Usage{Windows: []workerhttp.UsageWindow{
		{Name: "5h", UsedPercent: percent(30), ResetsAt: &soon},
		{Name: "weekly", UsedPercent: percent(75), ResetsAt: &later},
	}}
	if limited, _ := fresh.LimitedUntil(now); limited {
		t.Fatal("an agent with room is limited")
	}
	if left, known := fresh.Remaining(now); !known || left != 25 {
		t.Fatalf("left = %v known=%t, want the tightest window's 25", left, known)
	}

	full := Usage{Windows: []workerhttp.UsageWindow{
		{Name: "5h", UsedPercent: percent(100), ResetsAt: &soon},
		{Name: "weekly", Limited: true, ResetsAt: &later},
	}}
	if limited, until := full.LimitedUntil(now); !limited || until == nil || !until.Equal(later) {
		t.Fatalf("limited=%t until=%v, want limited until the latest reset", limited, until)
	}

	reset := Usage{Windows: []workerhttp.UsageWindow{{Name: "5h", UsedPercent: percent(100), Limited: true, ResetsAt: &past}}}
	if limited, _ := reset.LimitedUntil(now); limited {
		t.Fatal("a limit that has reset still counts")
	}
	if left, known := reset.Remaining(now); !known || left != 100 {
		t.Fatalf("after its reset left = %v known=%t", left, known)
	}
	if _, known := (Usage{}).Remaining(now); known {
		t.Fatal("an agent that never reported has a known remainder")
	}
}
