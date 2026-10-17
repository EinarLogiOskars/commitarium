package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
)

type usageStub []execution.RoleAttemptUsage

func (stub usageStub) FeatureAttemptUsage(context.Context, string) ([]execution.RoleAttemptUsage, error) {
	return stub, nil
}

// The cost breakdown names the models and efforts that spent each phase
// (ADR-031).
func TestFeatureUsageSplitsPhasesByModelAndEffort(t *testing.T) {
	features := &recordingFeatureService{getResult: feature.Feature{ID: "fea_test", ProjectID: "prj_test"}}
	usage := usageStub{
		{Role: "lead", SessionID: "run_1:lead", AttemptID: "run_1:lead:planning:1", Model: "claude-opus-5-5", Effort: "high",
			Usage: execution.TokenUsage{InputTokens: 10, OutputTokens: 1}},
		{Role: "lead", SessionID: "run_1:lead", AttemptID: "run_1:lead:planning:2", Model: "claude-opus-5-5", Effort: "high",
			Usage: execution.TokenUsage{InputTokens: 5}},
		{Role: "lead", SessionID: "run_1:lead", AttemptID: "run_1:lead:implementation:1", Model: "claude-sonnet-5-5",
			Usage: execution.TokenUsage{InputTokens: 100, OutputTokens: 20}},
	}
	handler := newAPI(nil, features, nil, nil, nil, nil, nil, nil, nil, nil, usage)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/projects/prj_test/features/fea_test/usage", nil))
	var body featureUsageResponse
	if recorder.Code != http.StatusOK || json.Unmarshal(recorder.Body.Bytes(), &body) != nil {
		t.Fatalf("status %d: %s", recorder.Code, recorder.Body.String())
	}
	if len(body.Phases) != 2 || body.Phases[0].Phase != "plan" || len(body.Phases[0].Models) != 1 ||
		body.Phases[0].Models[0].Model != "claude-opus-5-5" || body.Phases[0].Models[0].Effort != "high" ||
		body.Phases[0].Models[0].InputTokens != 15 {
		t.Fatalf("phases = %+v", body.Phases)
	}
	if implement := body.Phases[1]; implement.Phase != "implement" || implement.Models[0].Model != "claude-sonnet-5-5" ||
		implement.Models[0].Effort != "" {
		t.Fatalf("implement = %+v", implement)
	}
	if len(body.Models) != 2 || body.Models[0].Effort != "high" {
		t.Fatalf("models = %+v", body.Models)
	}
}
