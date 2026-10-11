package claudeadapter

import (
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/worker"
)

func TestFailedTurnWithExpiredLoginAsksForLogin(t *testing.T) {
	current := &session{providerSessionID: "claude-session"}
	_, result, err := current.translateResult(streamMessage{
		Subtype: "success", IsError: true,
		Result: "Failed to authenticate: OAuth session expired and could not be refreshed",
	})
	if err != nil || result == nil || result.Outcome != worker.OutcomeFailed || !result.LoginRequired {
		t.Fatalf("expired login result = %+v err=%v", result, err)
	}
	_, result, err = current.translateResult(streamMessage{Subtype: "error_max_turns", IsError: true, Result: "Reached max turns"})
	if err != nil || result == nil || result.LoginRequired {
		t.Fatalf("ordinary failure asked for login: %+v err=%v", result, err)
	}
}
