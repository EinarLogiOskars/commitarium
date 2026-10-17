package vision

import (
	"context"
	"strings"
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
)

type signInsStub struct{ marked []string }

func (stub *signInsStub) MarkAgentNeedsSignIn(_ context.Context, agentID, _ string) error {
	stub.marked = append(stub.marked, agentID)
	return nil
}

// A vision turn that failed on its agent's login says to sign in, and
// records the agent as needing it (LOGIN_RECOVERY_PLAN.md); other failures
// keep their own reply.
func TestAVisionTurnThatFailedOnItsLoginSaysToSignIn(t *testing.T) {
	signIns := &signInsStub{}
	service := &Service{}
	service.SetSignIns(signIns)
	login := &workerhttp.TerminalResult{Outcome: workerhttp.OutcomeFailed, Error: &workerhttp.ProtocolError{
		Code: workerhttp.ErrorProviderLoginRequired, Message: "OAuth session expired",
	}}
	reply, ok := service.signInReply(t.Context(), "claude", login)
	if !ok || !strings.Contains(reply, "sign in again") || len(signIns.marked) != 1 || signIns.marked[0] != "claude" {
		t.Fatalf("reply %q ok=%t marked=%v", reply, ok, signIns.marked)
	}
	other := &workerhttp.TerminalResult{Outcome: workerhttp.OutcomeFailed, Error: &workerhttp.ProtocolError{
		Code: workerhttp.ErrorInternal, Message: "overloaded",
	}}
	if _, ok := service.signInReply(t.Context(), "claude", other); ok || len(signIns.marked) != 1 {
		t.Fatalf("a provider error was taken for a login failure: marked=%v", signIns.marked)
	}
}
