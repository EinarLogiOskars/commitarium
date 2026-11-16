package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Hex, as bootstrap makes it, so it looks like a run token too.
const testOwnerToken = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// guarded wraps a mux with the coordinator's real route patterns, so the
// guard is judged against how the mux routes, and records which matched.
func guarded(t *testing.T) (Callers, http.Handler, *int) {
	t.Helper()
	callers, err := NewCallers(testOwnerToken + "\n")
	if err != nil {
		t.Fatal(err)
	}
	reached := 0
	mux := http.NewServeMux()
	for _, pattern := range []string{
		"GET /health",
		"GET /api/v1/projects",
		"GET /api/v1/projects/{projectID}",
		"POST /api/v1/agents",
		"PUT /api/v1/assistant/settings",
		"POST /api/v1/assistant/conversations/{scope}/actions/{actionID}/approve",
		"POST /api/v1/marketplace/apps/{owner}/{name}/install",
		"GET /api/v1/projects/{projectID}/features/{id}",
		"POST /api/v1/projects/{projectID}/features/{id}/transitions",
		"GET /api/v1/projects/{projectID}/features/{id}/artifacts/{kind}",
		"PUT /api/v1/projects/{projectID}/features/{id}/artifacts/{kind}",
		"POST /api/v1/projects/{projectID}/features/{id}/implementation-plan/steps/{stepID}/transitions",
	} {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, _ *http.Request) {
			reached++
			w.WriteHeader(http.StatusNoContent)
		})
	}
	return callers, callers.Guard(mux), &reached
}

func call(handler http.Handler, method, path, token string) int {
	request := httptest.NewRequest(method, path, strings.NewReader("{}"))
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder.Code
}

func TestTheCoordinatorRefusesCallersWithoutItsToken(t *testing.T) {
	_, handler, reached := guarded(t)
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/projects"},
		{http.MethodPost, "/api/v1/agents"},
		{http.MethodPut, "/api/v1/assistant/settings"},
		{http.MethodPost, "/api/v1/assistant/conversations/prj_a/actions/act_1/approve"},
		{http.MethodPost, "/api/v1/marketplace/apps/someone/app/install"},
		{http.MethodGet, "/api/v1/projects/prj_a/features/ftr_b/artifacts/implementation_plan"},
	} {
		if code := call(handler, route.method, route.path, ""); code != http.StatusUnauthorized {
			t.Errorf("%s %s without a token: %d, want 401", route.method, route.path, code)
		}
		if code := call(handler, route.method, route.path, "not-the-token"); code != http.StatusUnauthorized {
			t.Errorf("%s %s with a wrong token: %d, want 401", route.method, route.path, code)
		}
	}
	if *reached != 0 {
		t.Fatalf("%d refused calls reached the API", *reached)
	}
	if code := call(handler, http.MethodGet, "/health", ""); code != http.StatusNoContent {
		t.Fatalf("health without a token: %d", code)
	}
}

func TestTheOwnerTokenOpensEverything(t *testing.T) {
	_, handler, _ := guarded(t)
	if code := call(handler, http.MethodPost, "/api/v1/assistant/conversations/prj_a/actions/act_1/approve", testOwnerToken); code != http.StatusNoContent {
		t.Fatalf("approve with the owner token: %d", code)
	}
}

func TestARunTokenOpensOnlyItsOwnWorkOrdersPlan(t *testing.T) {
	callers, handler, _ := guarded(t)
	token := callers.RunToken("prj_a", "ftr_b")
	if token == callers.RunToken("prj_a", "ftr_c") || token == testOwnerToken {
		t.Fatal("run tokens must differ per work order and from the owner token")
	}
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/projects/prj_a/features/ftr_b/artifacts/implementation_plan"},
		{http.MethodPost, "/api/v1/projects/prj_a/features/ftr_b/implementation-plan/steps/s1/transitions"},
	} {
		if code := call(handler, route.method, route.path, token); code != http.StatusNoContent {
			t.Errorf("%s %s with its run token: %d", route.method, route.path, code)
		}
	}
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/projects/prj_a/features/ftr_c/artifacts/implementation_plan"},
		{http.MethodGet, "/api/v1/projects/prj_x/features/ftr_b/artifacts/implementation_plan"},
		{http.MethodPut, "/api/v1/projects/prj_a/features/ftr_b/artifacts/implementation_plan"},
		{http.MethodPost, "/api/v1/projects/prj_a/features/ftr_b/transitions"},
		{http.MethodGet, "/api/v1/projects/prj_a/features/ftr_b"},
		{http.MethodPost, "/api/v1/assistant/conversations/prj_a/actions/act_1/approve"},
		{http.MethodGet, "/api/v1/projects/prj_a/features/ftr_b/artifacts/.."},
		{http.MethodHead, "/api/v1/projects/prj_a/features/ftr_b/artifacts/implementation_plan"},
		// The acceptance-test route is gone, and its old path opens nothing.
		{http.MethodPost, "/api/v1/projects/prj_a/features/ftr_b/acceptance-tests/t1/transitions"},
		// An encoded slash stays in one segment for the mux, so these reach
		// other routes than they seem to.
		{http.MethodPost, "/api/v1/projects/prj_a/features/ftr_b%2Fimplementation-plan%2Fsteps%2Fs1/transitions"},
		{http.MethodGet, "/api/v1/projects/prj_a/features/ftr_b%2Fartifacts%2Fx"},
		{http.MethodGet, "/api/v1/projects/prj_a%2Ffeatures%2Fftr_b%2Fartifacts%2Fx"},
	} {
		if code := call(handler, route.method, route.path, token); code != http.StatusUnauthorized {
			t.Errorf("%s %s with another scope's run token: %d, want 401", route.method, route.path, code)
		}
	}
}

func TestAShortOwnerTokenIsRefused(t *testing.T) {
	if _, err := NewCallers("short"); err == nil {
		t.Fatal("a short token passed")
	}
}
