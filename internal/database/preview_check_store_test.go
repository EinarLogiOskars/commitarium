package database

import (
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/previewcheck"
)

func TestPreviewCheckStorePersistsLifecycleAndEvidence(t *testing.T) {
	db, executions := newTestExecutionStore(t)
	run, _ := createExecutionRecords(t, db, executions)
	now := run.StartedAt.Add(time.Minute)
	service := previewcheck.NewService(NewPreviewCheckStore(db)).WithClock(func() time.Time { return now })
	const commit = "0123456789abcdef0123456789abcdef01234567"

	check, created, err := service.Request(t.Context(), "prj_execution_test", run.FeatureID, run.ID, commit)
	if err != nil || !created || check.Status != previewcheck.StatusPending {
		t.Fatalf("request preview check = %#v, %t, %v", check, created, err)
	}
	pending, err := service.Pending(t.Context())
	if err != nil || len(pending) != 1 || pending[0].ID != check.ID {
		t.Fatalf("pending preview checks = %#v, %v", pending, err)
	}
	now = now.Add(time.Second)
	claimed, changed, err := service.Claim(t.Context(), check.ID)
	if err != nil || !changed || claimed.ClaimedAt == nil || !claimed.ClaimedAt.Equal(now) {
		t.Fatalf("claim preview check = %#v, %t, %v", claimed, changed, err)
	}
	now = now.Add(time.Second)
	evidence := previewcheck.Evidence{Stage: previewcheck.StageStart, Reason: "service api exited", Open: "web:5173",
		Services: []previewcheck.ServiceEvidence{{Service: "api", State: "exited", ExitCode: 1, Logs: "boom\n"}}}
	failed, changed, err := service.Complete(t.Context(), check.ID, false, evidence)
	if err != nil || !changed || failed.Status != previewcheck.StatusFailed || failed.CompletedAt == nil {
		t.Fatalf("complete preview check = %#v, %t, %v", failed, changed, err)
	}
	stored, err := service.Get(t.Context(), check.ID)
	if err != nil || stored.Evidence == nil || stored.Evidence.Services[0].Logs != "boom\n" ||
		stored.ClaimedAt == nil || stored.Evidence.Open != "web:5173" {
		t.Fatalf("stored preview check = %#v, %v", stored, err)
	}
	if unfinished, err := service.Unfinished(t.Context()); err != nil || len(unfinished) != 0 {
		t.Fatalf("unfinished preview checks = %#v, %v", unfinished, err)
	}
	if listed, err := service.ForRun(t.Context(), run.ID); err != nil || len(listed) != 1 {
		t.Fatalf("run preview checks = %#v, %v", listed, err)
	}
}
