package workerservice

import (
	"slices"
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/worker"
)

func TestTheRunTokenReachesTheAgentsEnvironment(t *testing.T) {
	base := worker.LaunchEnvironment{Variables: []string{"COMMITARIUM_PROJECT_ID=prj_a"}}
	with := withRunToken(base, "abc")
	if !slices.Contains(with.Variables, "COMMITARIUM_RUN_CREDENTIAL=abc") {
		t.Fatalf("variables = %v", with.Variables)
	}
	if len(base.Variables) != 1 {
		t.Fatal("the resolved environment was changed in place")
	}
	if without := withRunToken(base, ""); len(without.Variables) != 1 {
		t.Fatalf("an empty token added a variable: %v", without.Variables)
	}
}
