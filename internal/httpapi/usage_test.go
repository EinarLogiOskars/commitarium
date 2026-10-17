package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/modelguide"
	"github.com/EinarLogiOskars/commitarium/internal/project"
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

type projectUsageStub []execution.ProjectAttemptUsage

func (stub projectUsageStub) ProjectAttemptUsage(context.Context, string) ([]execution.ProjectAttemptUsage, error) {
	return stub, nil
}

// API-key turns are priced in dollars, by the provider's own cost or the
// guide's price; subscription and unknown turns stay in tokens (ADR-031).
func TestProjectUsagePricesAPITurnsAndListsTheAssistant(t *testing.T) {
	guide, err := modelguide.NewService(&modelGuideMemory{overrides: map[string]modelguide.Override{}})
	if err != nil {
		t.Fatal(err)
	}
	claudeCost := 0.5
	attempt := func(feature, role, model, billing string, cost *float64, input, output int64) execution.ProjectAttemptUsage {
		return execution.ProjectAttemptUsage{FeatureID: feature, RoleAttemptUsage: execution.RoleAttemptUsage{
			Role: role, Model: model, Billing: billing, CostUSD: cost,
			Usage: execution.TokenUsage{InputTokens: input, OutputTokens: output},
		}}
	}
	usage := projectUsageStub{
		attempt("fea_a", "lead", "claude-sonnet-5-5", "api_key", &claudeCost, 100, 10),
		attempt("fea_b", "lead", "gpt-6.1-sol", "api_key", nil, 1_000_000, 100_000), // $2 + $1
		attempt("fea_b", "reviewer", "gpt-6.1-sol", "subscription", nil, 50, 5),
		attempt("fea_b", "lead", "brand-new", "api_key", nil, 7, 1),
		attempt("", "assistant", "gpt-6.1-sol", "", nil, 20, 2),
	}
	features := &recordingFeatureService{listResult: []feature.Feature{{ID: "fea_a", Title: "A"}, {ID: "fea_b", Title: "B"}}}
	handler := newAPI(&recordingProjectService{getByIDResult: project.Project{ID: "prj_test"}}, features,
		nil, nil, nil, nil, nil, nil, nil, nil, guide, usage)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/projects/prj_test/usage", nil))
	var body projectUsageResponse
	if recorder.Code != http.StatusOK || json.Unmarshal(recorder.Body.Bytes(), &body) != nil {
		t.Fatalf("status %d: %s", recorder.Code, recorder.Body.String())
	}
	if body.Cost.APICostUSD < 3.4999 || body.Cost.APICostUSD > 3.5001 || body.Cost.PricesAsOf == "" ||
		body.Cost.APIUnpriced.InputTokens != 7 || body.Cost.Subscription.InputTokens != 50 ||
		body.Cost.Unknown.InputTokens != 20 {
		t.Fatalf("cost = %+v", body.Cost)
	}
	if len(body.WorkOrders) != 2 || body.WorkOrders[0].ID != "fea_b" || body.WorkOrders[0].Title != "B" ||
		*body.WorkOrders[0].CostUSD < 2.9999 {
		t.Fatalf("work orders = %+v", body.WorkOrders)
	}
	if body.Assistant.InputTokens != 20 || body.Assistant.CostUSD != nil {
		t.Fatalf("assistant = %+v", body.Assistant)
	}
	if body.Models[0].Model != "gpt-6.1-sol" {
		t.Fatalf("models = %+v", body.Models)
	}
}
