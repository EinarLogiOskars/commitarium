package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testOwnerToken = "owner-token-0123456789abcdefghijklmnopqrstuv"

func guarded(t *testing.T) (Callers, http.Handler, *int) {
	t.Helper()
	callers, err := NewCallers(testOwnerToken + "\n")
	if err != nil {
		t.Fatal(err)
	}
	reached := 0
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached++
		w.WriteHeader(http.StatusNoContent)
	})
	return callers, callers.Guard(next), &reached
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

func TestARunTokenOpensOnlyItsOwnWorkOrdersPlanAndTests(t *testing.T) {
	callers, handler, _ := guarded(t)
	token := callers.RunToken("prj_a", "ftr_b")
	if token == callers.RunToken("prj_a", "ftr_c") || token == testOwnerToken {
		t.Fatal("run tokens must differ per work order and from the owner token")
	}
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/projects/prj_a/features/ftr_b/artifacts/implementation_plan"},
		{http.MethodPost, "/api/v1/projects/prj_a/features/ftr_b/implementation-plan/steps/s1/transitions"},
		{http.MethodPost, "/api/v1/projects/prj_a/features/ftr_b/acceptance-tests/t1/transitions"},
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
	} {
		if code := call(handler, route.method, route.path, token); code != http.StatusForbidden {
			t.Errorf("%s %s with another scope's run token: %d, want 403", route.method, route.path, code)
		}
	}
}

func TestAShortOwnerTokenIsRefused(t *testing.T) {
	if _, err := NewCallers("short"); err == nil {
		t.Fatal("a short token passed")
	}
}
