package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/toolchain"
)

type recordingToolchainService struct {
	manifest   toolchain.Manifest
	suggestion toolchain.Suggestion
	err        error
	configured toolchain.Manifest
}

func (service *recordingToolchainService) Get(context.Context, string) (toolchain.Manifest, error) {
	return service.manifest, service.err
}

func (service *recordingToolchainService) Configure(_ context.Context, _ string, manifest toolchain.Manifest) (toolchain.Manifest, error) {
	service.configured = manifest
	return service.manifest, service.err
}

func (service *recordingToolchainService) Detect(context.Context, string) (toolchain.Suggestion, error) {
	return service.suggestion, service.err
}

func toolchainHandler(service ToolchainService) http.Handler {
	return newAPI(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, service)
}

func TestToolchainEndpointsExposePresetsAndProjectConfiguration(t *testing.T) {
	service := &recordingToolchainService{
		manifest: toolchain.Manifest{ProjectID: "prj_test", Status: toolchain.StatusConfigured,
			Source: toolchain.SourcePicker, Tools: map[string]string{"python": "3.14.7"},
			Services: []string{"postgresql"}, ServicesRunnable: false},
		suggestion: toolchain.Suggestion{Tools: map[string]string{"go": "1.27.1"}, Confidence: "high"},
	}
	handler := toolchainHandler(service)

	presets := httptest.NewRecorder()
	handler.ServeHTTP(presets, httptest.NewRequest(http.MethodGet, "/api/v1/toolchain-presets", nil))
	if presets.Code != http.StatusOK || !strings.Contains(presets.Body.String(), `"node-lts"`) {
		t.Fatalf("presets status=%d body=%s", presets.Code, presets.Body.String())
	}

	get := httptest.NewRecorder()
	handler.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/api/v1/projects/prj_test/toolchain", nil))
	if get.Code != http.StatusOK || !strings.Contains(get.Body.String(), `"services_runnable":false`) {
		t.Fatalf("get status=%d body=%s", get.Code, get.Body.String())
	}

	configure := httptest.NewRecorder()
	handler.ServeHTTP(configure, httptest.NewRequest(http.MethodPut, "/api/v1/projects/prj_test/toolchain",
		strings.NewReader(`{"source":"picker","tools":{"python":"3.14.7"},"services":["postgresql"]}`)))
	if configure.Code != http.StatusOK || service.configured.Tools["python"] != "3.14.7" ||
		service.configured.Source != toolchain.SourcePicker {
		t.Fatalf("configure status=%d captured=%+v body=%s", configure.Code, service.configured, configure.Body.String())
	}

	detect := httptest.NewRecorder()
	handler.ServeHTTP(detect, httptest.NewRequest(http.MethodPost, "/api/v1/projects/prj_test/toolchain/detect", nil))
	if detect.Code != http.StatusOK || !strings.Contains(detect.Body.String(), `"go":"1.27.1"`) {
		t.Fatalf("detect status=%d body=%s", detect.Code, detect.Body.String())
	}
}

func TestConfigureToolchainRejectsMalformedBodies(t *testing.T) {
	handler := toolchainHandler(&recordingToolchainService{})
	for _, body := range []string{
		`{"source":"picker","tools":{"python":"3.14.7"},"unexpected":true}`,
		`{"source":"picker","tools":{"python":"3.14.7"}} {}`,
		``,
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPut, "/api/v1/projects/prj_test/toolchain", strings.NewReader(body)))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("body %q returned %d: %s", body, response.Code, response.Body.String())
		}
	}
}

func TestDetectToolchainRequiresEmptyBody(t *testing.T) {
	response := httptest.NewRecorder()
	toolchainHandler(&recordingToolchainService{}).ServeHTTP(response,
		httptest.NewRequest(http.MethodPost, "/api/v1/projects/prj_test/toolchain/detect", strings.NewReader("{}")))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestToolchainErrorsUseStableCodes(t *testing.T) {
	for _, test := range []struct {
		err  error
		code int
		body string
	}{
		{project.ErrNotFound, http.StatusNotFound, "project_not_found"},
		{toolchain.ErrInvalidManifest, http.StatusBadRequest, "invalid_toolchain"},
		{toolchain.ErrUnavailable, http.StatusServiceUnavailable, "toolchain_unavailable"},
	} {
		service := &recordingToolchainService{err: test.err}
		response := httptest.NewRecorder()
		toolchainHandler(service).ServeHTTP(response,
			httptest.NewRequest(http.MethodGet, "/api/v1/projects/prj_test/toolchain", nil))
		if response.Code != test.code {
			t.Fatalf("error %v returned %d: %s", test.err, response.Code, response.Body.String())
		}
		var payload map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil || !strings.Contains(response.Body.String(), test.body) {
			t.Fatalf("error %v returned body %s", test.err, response.Body.String())
		}
	}

	response := httptest.NewRecorder()
	toolchainHandler(&recordingToolchainService{err: errors.New("boom")}).ServeHTTP(response,
		httptest.NewRequest(http.MethodGet, "/api/v1/projects/prj_test/toolchain", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("unexpected internal error response %d", response.Code)
	}
}

func TestWorkOrderCreationRequiresConfiguredProjectToolchain(t *testing.T) {
	features := &recordingFeatureService{createResult: feature.Feature{
		ID: "fea_test", ProjectID: "prj_test", Title: "Build it", State: feature.StateDraft,
		DialogueLimits: project.DefaultDialogueLimits(), AgentProviders: project.DefaultAgentProviders(),
		MergePolicy: project.DefaultMergePolicy(), AutonomyPolicy: project.DefaultAutonomyPolicy(),
	}}
	service := &recordingToolchainService{manifest: toolchain.Manifest{
		ProjectID: "prj_test", Status: toolchain.StatusNeedsSetup, Tools: map[string]string{}, Services: []string{},
	}}
	handler := newAPI(nil, features, nil, nil, nil, nil, nil, nil, nil, nil, service)

	blocked := httptest.NewRecorder()
	handler.ServeHTTP(blocked, httptest.NewRequest(http.MethodPost, "/api/v1/projects/prj_test/features",
		strings.NewReader(`{"title":"Build it"}`)))
	if blocked.Code != http.StatusConflict || !strings.Contains(blocked.Body.String(), "project_toolchain_required") || features.createCalls != 0 {
		t.Fatalf("blocked status=%d calls=%d body=%s", blocked.Code, features.createCalls, blocked.Body.String())
	}

	service.manifest.Status = toolchain.StatusConfigured
	service.manifest.Source = toolchain.SourcePicker
	service.manifest.Tools = map[string]string{"python": "3.14.7"}
	created := httptest.NewRecorder()
	handler.ServeHTTP(created, httptest.NewRequest(http.MethodPost, "/api/v1/projects/prj_test/features",
		strings.NewReader(`{"title":"Build it"}`)))
	if created.Code != http.StatusCreated || features.createCalls != 1 {
		t.Fatalf("created status=%d calls=%d body=%s", created.Code, features.createCalls, created.Body.String())
	}
}
