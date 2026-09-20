package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/featureartifact"
)

func TestEnvironmentUsesOnlyTrustedWorkerIdentity(t *testing.T) {
	values := map[string]string{
		"COMMITARIUM_COORDINATOR_URL": "http://coordinator:8080/",
		"COMMITARIUM_PROJECT_ID":      "prj_test",
		"COMMITARIUM_FEATURE_ID":      "fea_test",
	}
	base, projectID, featureID, err := environment(func(name string) string { return values[name] })
	if err != nil {
		t.Fatalf("read helper environment: %v", err)
	}
	if base != "http://coordinator:8080" || projectID != "prj_test" || featureID != "fea_test" {
		t.Fatalf("unexpected environment %q %q %q", base, projectID, featureID)
	}
	values["COMMITARIUM_FEATURE_ID"] = "../other"
	if _, _, _, err := environment(func(name string) string { return values[name] }); err == nil {
		t.Fatal("expected path-like feature identity to be rejected")
	}
}

func TestTransitionReadsPlanAndRecordsExactStep(t *testing.T) {
	commitID := strings.Repeat("a", 40)
	var received transitionRequest
	var idempotencyKey string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/artifacts/implementation_plan"):
			_ = json.NewEncoder(w).Encode(artifactResponse{
				Revision: 2,
				Document: json.RawMessage(`{"plan_version":4,"title":"Plan","subtitle":"One step","steps":[]}`),
			})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/steps/api/transitions"):
			if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
				t.Errorf("decode transition: %v", err)
			}
			idempotencyKey = r.Header.Get("Idempotency-Key")
			_ = json.NewEncoder(w).Encode(artifactResponse{Revision: 3, Document: json.RawMessage(`{}`)})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	output := &strings.Builder{}
	if err := transition(
		server.Client(), server.URL, "prj_test", "fea_test", "api",
		featureartifact.StepCompleted, commitID, output,
	); err != nil {
		t.Fatalf("complete plan step: %v", err)
	}
	if received.PlanVersion != 4 || received.Status != featureartifact.StepCompleted ||
		received.CommitID != commitID || !strings.HasPrefix(idempotencyKey, "artifact-") {
		t.Fatalf("unexpected transition %+v key=%q", received, idempotencyKey)
	}
	if output.String() != "implementation plan revision 3: api completed\n" {
		t.Fatalf("unexpected output %q", output.String())
	}
}
