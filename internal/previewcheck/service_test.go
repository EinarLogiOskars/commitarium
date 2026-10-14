package previewcheck

import (
	"errors"
	"strings"
	"testing"
	"time"
)

const commit = "0123456789abcdef0123456789abcdef01234567"

func testService(now *time.Time) *Service {
	return NewService(NewMemoryStore()).WithClock(func() time.Time { return *now })
}

func TestRequestIsIdempotentPerCommit(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	service := testService(&now)
	first, created, err := service.Request(t.Context(), "prj", "fea", "run", commit)
	if err != nil || !created || first.Status != StatusPending || first.Attempt != 1 {
		t.Fatalf("first request = %+v, %v, %v", first, created, err)
	}
	again, created, err := service.Request(t.Context(), "prj", "fea", "run", commit)
	if err != nil || created || again.ID != first.ID {
		t.Fatalf("repeated request = %+v, %v, %v", again, created, err)
	}
	if _, _, err := service.Request(t.Context(), "prj", "fea", "run", "deadbeef"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("short commit error = %v", err)
	}
}

func TestOverdueCheckIsReplacedByANewAttempt(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	service := testService(&now)
	first, _, _ := service.Request(t.Context(), "prj", "fea", "run", commit)
	now = now.Add(ClaimTimeout)
	if !service.Overdue(first) {
		t.Fatal("an unclaimed check past the claim timeout is not overdue")
	}
	second, created, err := service.Request(t.Context(), "prj", "fea", "run", commit)
	if err != nil || !created || second.Attempt != 2 || second.ID == first.ID {
		t.Fatalf("replacement = %+v, %v, %v", second, created, err)
	}
	expired, _ := service.Get(t.Context(), first.ID)
	if expired.Status != StatusExpired || expired.CompletedAt == nil {
		t.Fatalf("overdue check = %+v", expired)
	}
	pending, _ := service.Pending(t.Context())
	if len(pending) != 1 || pending[0].ID != second.ID {
		t.Fatalf("pending = %+v", pending)
	}
}

func TestClaimAndCompleteReplayTheStoredCheck(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	service := testService(&now)
	check, _, _ := service.Request(t.Context(), "prj", "fea", "run", commit)
	if _, _, err := service.Complete(t.Context(), check.ID, true, Evidence{Stage: StageReady}); !errors.Is(err, ErrConflict) {
		t.Fatalf("completing an unclaimed check = %v", err)
	}
	claimed, changed, err := service.Claim(t.Context(), check.ID)
	if err != nil || !changed || claimed.Status != StatusRunning || claimed.ClaimedAt == nil {
		t.Fatalf("claim = %+v, %v, %v", claimed, changed, err)
	}
	if again, changed, _ := service.Claim(t.Context(), check.ID); changed || again.Status != StatusRunning {
		t.Fatalf("second claim = %+v, %v", again, changed)
	}
	evidence := Evidence{Stage: StageStart, Reason: "api exited", Services: []ServiceEvidence{
		{Service: "api", State: "exited", ExitCode: 1, Logs: "boom\n"},
	}}
	done, changed, err := service.Complete(t.Context(), check.ID, false, evidence)
	if err != nil || !changed || done.Status != StatusFailed || done.Evidence == nil {
		t.Fatalf("complete = %+v, %v, %v", done, changed, err)
	}
	replayed, changed, err := service.Complete(t.Context(), check.ID, true, Evidence{Stage: StageReady})
	if err != nil || changed || replayed.Status != StatusFailed {
		t.Fatalf("replayed completion = %+v, %v, %v", replayed, changed, err)
	}
	if failures, _ := service.Failures(t.Context(), "run"); failures != 1 {
		t.Fatalf("failures = %d", failures)
	}
	if err := done.Validate(); err != nil {
		t.Fatalf("completed check is invalid: %v", err)
	}
}

func TestCompleteRejectsUnknownStages(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	service := testService(&now)
	check, _, _ := service.Request(t.Context(), "prj", "fea", "run", commit)
	_, _, _ = service.Claim(t.Context(), check.ID)
	if _, _, err := service.Complete(t.Context(), check.ID, false, Evidence{Stage: "launch"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown stage error = %v", err)
	}
}

func TestEvidenceKeepsTheEndOfLongLogs(t *testing.T) {
	logs := strings.Repeat("early line\n", 3000) + "the real error\n"
	bounded := Evidence{Stage: StageStart, Services: []ServiceEvidence{{Service: "api", Logs: logs}}}.Bounded()
	kept := bounded.Services[0].Logs
	if len(kept) > maxLogBytes || !strings.HasSuffix(kept, "the real error\n") || !strings.HasPrefix(kept, "early line") {
		t.Fatalf("bounded logs keep %d bytes: %q…", len(kept), kept[:20])
	}
}

func TestEvidenceSaysWhatFailsInPlainWordsAndInDetail(t *testing.T) {
	evidence := Evidence{Stage: StageStart, Reason: "service api exited with code 1", Open: "web:5173", Services: []ServiceEvidence{
		{Service: "web", State: "running", Logs: "ready\n"},
		{Service: "api", State: "exited", ExitCode: 1, Logs: "ModuleNotFoundError: fastapi\n"},
	}}
	if plain := evidence.Plain(); plain != `the "api" part of the app stops right after it starts (exit code 1)` {
		t.Fatalf("plain = %q", plain)
	}
	technical := evidence.Technical()
	if strings.Index(technical, `Service "api"`) > strings.Index(technical, `Service "web"`) ||
		!strings.Contains(technical, "ModuleNotFoundError: fastapi") || !strings.Contains(technical, "Open target: web:5173") {
		t.Fatalf("technical = %s", technical)
	}
	cases := map[string]Evidence{
		`the "db" part of the app starts but reports that it is not working`:      {Stage: StageStart, Services: []ServiceEvidence{{Service: "db", State: "running", Health: "unhealthy"}}},
		`the "web" part of the app keeps crashing and restarting`:                 {Stage: StageStart, Services: []ServiceEvidence{{Service: "web", State: "restarting"}}},
		"the app could not be built":                                              {Stage: StageBuild},
		"the app's setup file does not follow the rules Commitarium runs apps by": {Stage: StageSetup},
		`the "web" part of the app never started answering`:                       {Stage: StageStart, Open: "web:5173"},
	}
	for want, evidence := range cases {
		if got := evidence.Plain(); got != want {
			t.Errorf("plain(%+v) = %q, want %q", evidence, got, want)
		}
	}
}
