package orchestration

import (
	"context"
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
	"github.com/EinarLogiOskars/commitarium/internal/workeringest"
)

type routingWorkerStub struct{ puts, gets int }

func (stub *routingWorkerStub) PutAttempt(
	context.Context, workerhttp.MutationIdentity, workerhttp.PutAttemptRequest,
) (workerhttp.Attempt, bool, error) {
	stub.puts++
	return workerhttp.Attempt{}, true, nil
}

func (stub *routingWorkerStub) GetAttempt(
	context.Context, workerhttp.AttemptReference,
) (workerhttp.Attempt, error) {
	stub.gets++
	return workerhttp.Attempt{}, nil
}

type routingPumpStub struct{ runs int }

func (stub *routingPumpStub) Run(context.Context, string) (workeringest.PumpResult, error) {
	stub.runs++
	return workeringest.PumpResult{}, nil
}

func TestRoleRoutersKeepLeadAndReviewerOnTheirOwnWorkers(t *testing.T) {
	leadWorker, reviewerWorker := &routingWorkerStub{}, &routingWorkerStub{}
	workerRouter, err := NewRoleRoutedWorker(leadWorker, reviewerWorker)
	if err != nil {
		t.Fatalf("create worker router: %v", err)
	}
	identity := workerhttp.MutationIdentity{AttemptReference: workerhttp.AttemptReference{
		SessionID: "run_test:reviewer", AttemptID: "attempt_test",
	}}
	if _, _, err := workerRouter.PutAttempt(t.Context(), identity, workerhttp.PutAttemptRequest{
		Assignment: workerhttp.Assignment{Role: workerhttp.RoleReviewer},
	}); err != nil {
		t.Fatalf("route reviewer put: %v", err)
	}
	if _, err := workerRouter.GetAttempt(t.Context(), identity.AttemptReference); err != nil {
		t.Fatalf("route reviewer get: %v", err)
	}
	if leadWorker.puts != 0 || leadWorker.gets != 0 || reviewerWorker.puts != 1 || reviewerWorker.gets != 1 {
		t.Fatalf("worker routing lead=%+v reviewer=%+v", leadWorker, reviewerWorker)
	}

	leadPump, reviewerPump := &routingPumpStub{}, &routingPumpStub{}
	pumpRouter, err := NewRoleRoutedPump(leadPump, reviewerPump)
	if err != nil {
		t.Fatalf("create pump router: %v", err)
	}
	if _, err := pumpRouter.Run(t.Context(), "run_test:lead"); err != nil {
		t.Fatalf("route lead pump: %v", err)
	}
	if leadPump.runs != 1 || reviewerPump.runs != 0 {
		t.Fatalf("pump routing lead=%d reviewer=%d", leadPump.runs, reviewerPump.runs)
	}
}

func TestRoleRoutersRejectUnknownSessionsAndRoles(t *testing.T) {
	router, _ := NewRoleRoutedWorker(&routingWorkerStub{}, &routingWorkerStub{})
	if _, _, err := router.PutAttempt(t.Context(), workerhttp.MutationIdentity{}, workerhttp.PutAttemptRequest{
		Assignment: workerhttp.Assignment{Role: workerhttp.RoleCoder},
	}); err == nil {
		t.Fatal("expected unsupported role to fail")
	}
	if _, err := router.GetAttempt(t.Context(), workerhttp.AttemptReference{SessionID: "unknown"}); err == nil {
		t.Fatal("expected unsupported session to fail")
	}
	if _, _, err := router.PutAttempt(t.Context(), workerhttp.MutationIdentity{
		AttemptReference: workerhttp.AttemptReference{SessionID: "run_test:lead"},
	}, workerhttp.PutAttemptRequest{
		Assignment: workerhttp.Assignment{Role: workerhttp.RoleReviewer},
	}); err == nil {
		t.Fatal("expected mismatched session role to fail")
	}
}
