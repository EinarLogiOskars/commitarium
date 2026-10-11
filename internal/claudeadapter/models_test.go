package claudeadapter

import (
	"strings"
	"testing"
)

func TestStartupModelReadsTheInitEvent(t *testing.T) {
	stream := strings.NewReader(`{"type":"rate_limit_event"}
not json
{"type":"system","subtype":"init","model":"claude-opus-5","tools":["Bash"]}
{"type":"assistant"}
`)
	model, err := startupModel(stream)
	if err != nil || model != "claude-opus-5" {
		t.Fatalf("model = %q err=%v", model, err)
	}
	if _, err := startupModel(strings.NewReader(`{"type":"result"}`)); err == nil {
		t.Fatal("a stream without a start-up event named a model")
	}
}
