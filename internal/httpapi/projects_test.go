package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/project"
)

type recordingProjectImporterService struct {
	recordingProjectService
	importSpec    project.ImportSpec
	importBundle  string
	importResult  project.Project
	importCreated bool
	importErr     error
}

func (service *recordingProjectImporterService) Import(
	_ context.Context,
	spec project.ImportSpec,
	bundle io.Reader,
) (project.Project, bool, error) {
	service.importSpec = spec
	contents, _ := io.ReadAll(bundle)
	service.importBundle = string(contents)
	return service.importResult, service.importCreated, service.importErr
}

type recordingProjectService struct {
	calls                  int
	receivedName           string
	receivedRecoveryPolicy project.RecoveryPolicy
	receivedDialogueLimits project.DialogueLimits
	receivedAgentProviders project.AgentProviders
	receivedMergePolicy    project.MergePolicy
	result                 project.Project
	err                    error

	receivedID    string
	getByIDResult project.Project
	getByIDErr    error

	listResult []project.Project
	listErr    error

	updateProjectID string
	updateLimits    project.DialogueLimits
	updateResult    project.Project
	updateErr       error

	bindProjectID string
	bindOwner     string
	bindName      string
	bindResult    project.Project
	bindErr       error
}

type testErrorResponse struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (s *recordingProjectService) Create(
	_ context.Context,
	name string,
	recoveryPolicy project.RecoveryPolicy,
	dialogueLimits project.DialogueLimits,
	agentProviders project.AgentProviders,
	mergePolicy project.MergePolicy,
) (project.Project, error) {
	s.calls++
	s.receivedName = name
	s.receivedRecoveryPolicy = recoveryPolicy
	s.receivedDialogueLimits = dialogueLimits
	s.receivedAgentProviders = agentProviders
	s.receivedMergePolicy = mergePolicy
	return s.result, s.err
}

func (s *recordingProjectService) UpdateMergePolicy(
	_ context.Context,
	projectID string,
	policy project.MergePolicy,
) (project.Project, error) {
	s.updateProjectID = projectID
	s.receivedMergePolicy = policy
	return s.updateResult, s.updateErr
}

func (s *recordingProjectService) UpdateAgentProviders(
	_ context.Context,
	projectID string,
	providers project.AgentProviders,
) (project.Project, error) {
	s.updateProjectID = projectID
	s.receivedAgentProviders = providers
	return s.updateResult, s.updateErr
}

func (s *recordingProjectService) UpdateDialogueLimits(
	_ context.Context,
	projectID string,
	limits project.DialogueLimits,
) (project.Project, error) {
	s.updateProjectID = projectID
	s.updateLimits = limits
	return s.updateResult, s.updateErr
}

func (s *recordingProjectService) GetByID(
	_ context.Context,
	id string,
) (project.Project, error) {
	s.receivedID = id
	return s.getByIDResult, s.getByIDErr
}

func (s *recordingProjectService) List(context.Context) ([]project.Project, error) {
	return s.listResult, s.listErr
}

func (s *recordingProjectService) BindForgejoRepository(
	_ context.Context,
	projectID string,
	owner string,
	name string,
) (project.Project, error) {
	s.bindProjectID = projectID
	s.bindOwner = owner
	s.bindName = name
	return s.bindResult, s.bindErr
}

func projectImportRequest(t *testing.T, importID, metadata, bundle string) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("metadata", metadata); err != nil {
		t.Fatalf("write metadata: %v", err)
	}
	part, err := writer.CreateFormFile("bundle", "project.bundle")
	if err != nil {
		t.Fatalf("create bundle part: %v", err)
	}
	if _, err := io.WriteString(part, bundle); err != nil {
		t.Fatalf("write bundle: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart request: %v", err)
	}
	request := httptest.NewRequest(http.MethodPut, "/api/v1/project-imports/"+importID, &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	return request
}

func TestImportProject(t *testing.T) {
	fixedTime := time.Date(2026, time.September, 10, 12, 0, 0, 0, time.UTC)
	service := &recordingProjectImporterService{
		importResult: project.Project{
			ID: "prj_imported", Name: "Commitarium",
			RecoveryPolicy: project.RecoveryPolicyApprovalRequired,
			MergePolicy:    project.DefaultMergePolicy(),
			DialogueLimits: project.DefaultDialogueLimits(),
			ForgejoRepository: &project.ForgejoRepository{
				Owner: "commitarium", Name: "commitarium-aabbcc", DefaultBranch: "main", BoundAt: fixedTime,
			},
			CreatedAt: fixedTime,
		},
		importCreated: true,
	}
	recorder := httptest.NewRecorder()
	New(service, nil, nil, nil, nil, nil).ServeHTTP(recorder, projectImportRequest(
		t, "desktop-1", `{"name":"Commitarium","default_branch":"main"}`, "git bundle bytes",
	))
	if recorder.Code != http.StatusCreated {
		t.Fatalf("expected status %d, got %d: %s", http.StatusCreated, recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("Location") != "/api/v1/projects/prj_imported" {
		t.Fatalf("unexpected Location %q", recorder.Header().Get("Location"))
	}
	if service.importSpec.ImportID != "desktop-1" || service.importSpec.Name != "Commitarium" ||
		service.importSpec.DefaultBranch != "main" || service.importSpec.DialogueLimits != project.DefaultDialogueLimits() ||
		service.importSpec.AgentProviders != project.DefaultAgentProviders() ||
		service.importSpec.MergePolicy != "" ||
		service.importBundle != "git bundle bytes" {
		t.Fatalf("unexpected import request spec=%+v bundle=%q", service.importSpec, service.importBundle)
	}
	var response projectResponse
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.ForgejoRepository == nil || response.ForgejoRepository.Owner != "commitarium" {
		t.Fatalf("unexpected response %+v", response)
	}
}

func TestImportProjectReturnsOKForExactRetry(t *testing.T) {
	service := &recordingProjectImporterService{
		importResult: project.Project{ID: "prj_imported", DialogueLimits: project.DefaultDialogueLimits()},
	}
	recorder := httptest.NewRecorder()
	New(service, nil, nil, nil, nil, nil).ServeHTTP(recorder, projectImportRequest(
		t, "desktop-1", `{"name":"Commitarium","default_branch":"main"}`, "git bundle bytes",
	))
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, recorder.Code, recorder.Body.String())
	}
}

func TestImportProjectRejectsUnknownMetadataField(t *testing.T) {
	service := &recordingProjectImporterService{}
	recorder := httptest.NewRecorder()
	New(service, nil, nil, nil, nil, nil).ServeHTTP(recorder, projectImportRequest(
		t, "desktop-1", `{"name":"Commitarium","default_branch":"main","source_path":"/secret"}`, "bundle",
	))
	if recorder.Code != http.StatusBadRequest || service.importSpec.ImportID != "" {
		t.Fatalf("unexpected response status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestImportProjectMapsExpectedErrors(t *testing.T) {
	tests := []struct {
		err    error
		status int
		code   string
	}{
		{project.ErrInvalidImportID, http.StatusBadRequest, "invalid_project_import_id"},
		{project.ErrInvalidDefaultBranch, http.StatusBadRequest, "invalid_default_branch"},
		{project.ErrInvalidGitBundle, http.StatusBadRequest, "invalid_git_bundle"},
		{project.ErrImportConflict, http.StatusConflict, "project_import_conflict"},
		{project.ErrImportUnavailable, http.StatusServiceUnavailable, "project_import_unavailable"},
	}
	for _, test := range tests {
		t.Run(test.code, func(t *testing.T) {
			service := &recordingProjectImporterService{importErr: test.err}
			recorder := httptest.NewRecorder()
			New(service, nil, nil, nil, nil, nil).ServeHTTP(recorder, projectImportRequest(
				t, "desktop-1", `{"name":"Commitarium","default_branch":"main"}`, "bundle",
			))
			if recorder.Code != test.status {
				t.Fatalf("expected status %d, got %d: %s", test.status, recorder.Code, recorder.Body.String())
			}
			var response testErrorResponse
			if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil || response.Error.Code != test.code {
				t.Fatalf("unexpected error response %+v err=%v", response, err)
			}
		})
	}
}

func TestCreateProject(t *testing.T) {
	fixedTime := time.Date(
		2026,
		time.September,
		6,
		12,
		0,
		0,
		0,
		time.UTC,
	)

	service := &recordingProjectService{
		result: project.Project{
			ID:             "prj_test",
			Name:           "Commitarium",
			RecoveryPolicy: project.RecoveryPolicyAutomatic,
			DialogueLimits: project.DefaultDialogueLimits(),
			MergePolicy:    project.DefaultMergePolicy(),
			CreatedAt:      fixedTime,
		},
	}

	body := strings.NewReader(`{"name":"Commitarium","recovery_policy":"automatic"}`)

	req := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/projects",
		body,
	)
	req.Header.Set("Content-Type", "application/json")

	recorder := httptest.NewRecorder()
	New(service, nil, nil, nil, nil, nil).ServeHTTP(recorder, req)

	var response struct {
		ID             string                 `json:"id"`
		Name           string                 `json:"name"`
		RecoveryPolicy project.RecoveryPolicy `json:"recovery_policy"`
		DialogueLimits dialogueLimitsResponse `json:"dialogue_limits"`
		MergePolicy    project.MergePolicy    `json:"merge_policy"`
		CreatedAt      time.Time              `json:"created_at"`
	}

	res := recorder.Result()
	defer res.Body.Close()

	if res.StatusCode != http.StatusCreated {
		t.Fatalf("expected status code %d, got %d", http.StatusCreated, res.StatusCode)
	}

	expectedLocation := "/api/v1/projects/prj_test"

	if location := res.Header.Get("Location"); location != expectedLocation {
		t.Errorf(
			"expected Location header %q, got %q",
			expectedLocation,
			location,
		)
	}

	if contentType := res.Header.Get("Content-Type"); contentType != "application/json" {
		t.Errorf("expected application/json, got %q", contentType)
	}

	if err := json.NewDecoder(res.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if service.receivedName != "Commitarium" {
		t.Errorf("expected receivedName Commitarium, got %v", service.receivedName)
	}
	if service.receivedRecoveryPolicy != project.RecoveryPolicyAutomatic {
		t.Errorf("expected service recovery policy %q, got %q", project.RecoveryPolicyAutomatic, service.receivedRecoveryPolicy)
	}
	if service.receivedDialogueLimits != project.DefaultDialogueLimits() {
		t.Errorf("expected default dialogue limits, got %+v", service.receivedDialogueLimits)
	}
	if service.receivedAgentProviders != project.DefaultAgentProviders() {
		t.Errorf("expected default agent providers, got %+v", service.receivedAgentProviders)
	}
	if service.receivedMergePolicy != "" {
		t.Errorf("expected blank policy to be normalized by the service, got %q", service.receivedMergePolicy)
	}

	if response.ID != service.result.ID {
		t.Errorf("expected response ID %v, got %v", service.result.ID, response.ID)
	}

	if response.Name != service.result.Name {
		t.Errorf("expected response Name %v, got %v", service.result.Name, response.Name)
	}
	if response.RecoveryPolicy != project.RecoveryPolicyAutomatic {
		t.Errorf("expected recovery policy %q, got %q", project.RecoveryPolicyAutomatic, response.RecoveryPolicy)
	}
	if response.DialogueLimits.PlanningRounds != 6 || response.DialogueLimits.ImplementationReviewRounds != 6 {
		t.Errorf("unexpected dialogue limits %+v", response.DialogueLimits)
	}
	if response.MergePolicy != project.DefaultMergePolicy() {
		t.Errorf("unexpected merge policy %q", response.MergePolicy)
	}

	if response.CreatedAt != service.result.CreatedAt {
		t.Errorf("expected response CreatedAt %v, got %v", service.result.CreatedAt, response.CreatedAt)
	}
}

func TestCreateProjectAcceptsIndependentAgentProviders(t *testing.T) {
	want := project.AgentProviders{
		Lead: project.AgentProviderClaude, Reviewer: project.AgentProviderCodex,
	}
	service := &recordingProjectService{result: project.Project{
		ID: "prj_test", Name: "Commitarium", AgentProviders: want, CreatedAt: time.Now().UTC(),
	}}
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/projects",
		strings.NewReader(`{"name":"Commitarium","agent_providers":{"lead":"claude","reviewer":"codex"}}`),
	)
	recorder := httptest.NewRecorder()
	New(service, nil, nil, nil, nil, nil).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	if service.receivedAgentProviders != want {
		t.Fatalf("received providers = %+v, want %+v", service.receivedAgentProviders, want)
	}
	var response projectResponse
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.AgentProviders.Lead != want.Lead || response.AgentProviders.Reviewer != want.Reviewer {
		t.Fatalf("response providers = %+v", response.AgentProviders)
	}
}

func TestCreateAndUpdateProjectMergePolicy(t *testing.T) {
	service := &recordingProjectService{result: project.Project{
		ID: "prj_test", Name: "Commitarium",
		MergePolicy: project.MergePolicyAutoAfterGates, CreatedAt: time.Now().UTC(),
	}}
	create := httptest.NewRecorder()
	New(service, nil, nil, nil, nil, nil).ServeHTTP(create, httptest.NewRequest(
		http.MethodPost, "/api/v1/projects",
		strings.NewReader(`{"name":"Commitarium","merge_policy":"auto_after_gates"}`),
	))
	if create.Code != http.StatusCreated || service.receivedMergePolicy != project.MergePolicyAutoAfterGates {
		t.Fatalf("create policy status=%d received=%q body=%s", create.Code, service.receivedMergePolicy, create.Body.String())
	}
	service.updateResult = project.Project{
		ID: "prj_test", Name: "Commitarium",
		MergePolicy: project.MergePolicyRequireUserApproval, CreatedAt: time.Now().UTC(),
	}
	update := httptest.NewRecorder()
	New(service, nil, nil, nil, nil, nil).ServeHTTP(update, httptest.NewRequest(
		http.MethodPut, "/api/v1/projects/prj_test/merge-policy",
		strings.NewReader(`{"merge_policy":"require_user_approval"}`),
	))
	if update.Code != http.StatusOK || service.updateProjectID != "prj_test" ||
		service.receivedMergePolicy != project.MergePolicyRequireUserApproval {
		t.Fatalf("update policy status=%d project=%q policy=%q body=%s", update.Code, service.updateProjectID, service.receivedMergePolicy, update.Body.String())
	}
	var response projectResponse
	if err := json.NewDecoder(update.Body).Decode(&response); err != nil ||
		response.MergePolicy != project.MergePolicyRequireUserApproval {
		t.Fatalf("decode policy response: response=%+v err=%v", response, err)
	}
}

func TestUpdateProjectMergePolicyRejectsUnknownValue(t *testing.T) {
	service := &recordingProjectService{updateErr: project.ErrInvalidMergePolicy}
	recorder := httptest.NewRecorder()
	New(service, nil, nil, nil, nil, nil).ServeHTTP(recorder, httptest.NewRequest(
		http.MethodPut, "/api/v1/projects/prj_test/merge-policy",
		strings.NewReader(`{"merge_policy":"surprise_me"}`),
	))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response testErrorResponse
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil ||
		response.Error.Code != "invalid_merge_policy" {
		t.Fatalf("unexpected response %+v err=%v", response, err)
	}
}

func TestCreateProjectAcceptsExplicitUnlimitedDialogueLimit(t *testing.T) {
	limits := project.DialogueLimits{PlanningRounds: 0, ImplementationReviewRounds: 4}
	service := &recordingProjectService{result: project.Project{
		ID: "prj_test", Name: "Commitarium", DialogueLimits: limits, CreatedAt: time.Now().UTC(),
	}}
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/projects",
		strings.NewReader(`{"name":"Commitarium","dialogue_limits":{"planning_rounds":0,"implementation_review_rounds":4}}`),
	)
	recorder := httptest.NewRecorder()
	New(service, nil, nil, nil, nil, nil).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("expected status %d, got %d: %s", http.StatusCreated, recorder.Code, recorder.Body.String())
	}
	if service.receivedDialogueLimits != limits {
		t.Fatalf("expected limits %+v, got %+v", limits, service.receivedDialogueLimits)
	}
}

func TestCreateProjectRejectsIncompleteDialogueLimits(t *testing.T) {
	service := &recordingProjectService{}
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/projects",
		strings.NewReader(`{"name":"Commitarium","dialogue_limits":{"planning_rounds":3}}`),
	)
	recorder := httptest.NewRecorder()
	New(service, nil, nil, nil, nil, nil).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, recorder.Code)
	}
	var body errorResponse
	if err := json.NewDecoder(recorder.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Error.Code != "invalid_dialogue_limits" || service.calls != 0 {
		t.Fatalf("unexpected response %+v or service calls %d", body, service.calls)
	}
}

func TestUpdateProjectDialogueLimits(t *testing.T) {
	limits := project.DialogueLimits{PlanningRounds: 3, ImplementationReviewRounds: 0}
	service := &recordingProjectService{updateResult: project.Project{
		ID: "prj_test", Name: "Commitarium", DialogueLimits: limits, CreatedAt: time.Now().UTC(),
	}}
	request := httptest.NewRequest(
		http.MethodPut,
		"/api/v1/projects/prj_test/dialogue-limits",
		strings.NewReader(`{"planning_rounds":3,"implementation_review_rounds":0}`),
	)
	recorder := httptest.NewRecorder()
	New(service, nil, nil, nil, nil, nil).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, recorder.Code, recorder.Body.String())
	}
	if service.updateProjectID != "prj_test" || service.updateLimits != limits {
		t.Fatalf("unexpected update %q %+v", service.updateProjectID, service.updateLimits)
	}
	var body projectResponse
	if err := json.NewDecoder(recorder.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.DialogueLimits.PlanningRounds != 3 || body.DialogueLimits.ImplementationReviewRounds != 0 {
		t.Fatalf("unexpected response %+v", body)
	}
}

func TestUpdateProjectDialogueLimitsRejectsInvalidValues(t *testing.T) {
	for _, body := range []string{
		`{"planning_rounds":3}`,
		`{"planning_rounds":-1,"implementation_review_rounds":2}`,
	} {
		service := &recordingProjectService{}
		request := httptest.NewRequest(
			http.MethodPut, "/api/v1/projects/prj_test/dialogue-limits", strings.NewReader(body),
		)
		recorder := httptest.NewRecorder()
		New(service, nil, nil, nil, nil, nil).ServeHTTP(recorder, request)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("body %s: expected status %d, got %d", body, http.StatusBadRequest, recorder.Code)
		}
		if service.updateProjectID != "" {
			t.Fatalf("body %s reached service", body)
		}
	}
}

func TestUpdateProjectAgentProviders(t *testing.T) {
	want := project.AgentProviders{
		Lead: project.AgentProviderCodex, Reviewer: project.AgentProviderClaude,
	}
	service := &recordingProjectService{updateResult: project.Project{
		ID: "prj_test", Name: "Commitarium", AgentProviders: want, CreatedAt: time.Now().UTC(),
	}}
	request := httptest.NewRequest(
		http.MethodPut,
		"/api/v1/projects/prj_test/agent-providers",
		strings.NewReader(`{"lead":"codex","reviewer":"claude"}`),
	)
	recorder := httptest.NewRecorder()
	New(service, nil, nil, nil, nil, nil).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	if service.updateProjectID != "prj_test" || service.receivedAgentProviders != want {
		t.Fatalf("updated project=%q providers=%+v", service.updateProjectID, service.receivedAgentProviders)
	}
}

func TestUpdateProjectAgentProvidersRejectsIncompleteAssignment(t *testing.T) {
	service := &recordingProjectService{}
	request := httptest.NewRequest(
		http.MethodPut,
		"/api/v1/projects/prj_test/agent-providers",
		strings.NewReader(`{"lead":"codex"}`),
	)
	recorder := httptest.NewRecorder()
	New(service, nil, nil, nil, nil, nil).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestCreateProjectRejectsMalformedJSON(t *testing.T) {
	service := &recordingProjectService{}
	requestBody := strings.NewReader(`{"name":`)

	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/projects",
		requestBody,
	)
	request.Header.Set("Content-Type", "application/json")

	recorder := httptest.NewRecorder()
	New(service, nil, nil, nil, nil, nil).ServeHTTP(recorder, request)

	response := recorder.Result()
	defer response.Body.Close()

	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf(
			"expected status code %d, got %d",
			http.StatusBadRequest,
			response.StatusCode,
		)
	}

	if contentType := response.Header.Get("Content-Type"); contentType != "application/json" {
		t.Fatalf("expected application/json, got %q", contentType)
	}

	var responseBody testErrorResponse
	if err := json.NewDecoder(response.Body).Decode(&responseBody); err != nil {
		t.Fatalf("decode error response: %v", err)
	}

	if responseBody.Error.Code != "invalid_json" {
		t.Fatalf(
			"expected error code %q, got %q",
			"invalid_json",
			responseBody.Error.Code,
		)
	}

	if responseBody.Error.Message != "request body must contain valid JSON" {
		t.Errorf(
			"expected error message %q, got %q",
			"request body must contain valid JSON",
			responseBody.Error.Message,
		)
	}

	if service.calls != 0 {
		t.Fatalf(
			"expected project service not to be called, got %d calls",
			service.calls,
		)
	}
}

func TestCreateProjectRejectsEmptyName(t *testing.T) {
	service := &recordingProjectService{
		err: project.ErrNameRequired,
	}
	requestBody := strings.NewReader(`{"name":""}`)

	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/projects",
		requestBody,
	)

	recorder := httptest.NewRecorder()
	New(service, nil, nil, nil, nil, nil).ServeHTTP(recorder, request)

	response := recorder.Result()
	defer response.Body.Close()

	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf(
			"expected status code %d, got %d",
			http.StatusBadRequest,
			response.StatusCode,
		)
	}

	var body errorResponse

	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode error response: %v", err)
	}

	if body.Error.Code != "project_name_required" {
		t.Errorf(
			"expected error code %q, got %q",
			"project_name_required",
			body.Error.Code,
		)
	}

	if body.Error.Message != "project name is required" {
		t.Errorf(
			"expected error message %q, got %q",
			"project name is required",
			body.Error.Message,
		)
	}

	if service.calls != 1 {
		t.Fatalf("expected project service to be called once, got %d", service.calls)
	}
}

func TestCreateProjectRejectsInvalidRecoveryPolicy(t *testing.T) {
	service := &recordingProjectService{err: project.ErrInvalidRecoveryPolicy}
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/projects",
		strings.NewReader(`{"name":"Commitarium","recovery_policy":"reckless"}`),
	)
	recorder := httptest.NewRecorder()
	New(service, nil, nil, nil, nil, nil).ServeHTTP(recorder, request)

	response := recorder.Result()
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, response.StatusCode)
	}
	var body errorResponse
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if body.Error.Code != "invalid_recovery_policy" {
		t.Fatalf("unexpected error response %+v", body)
	}
}

func TestCreateProjectHandlesUnexpectedError(t *testing.T) {
	service := &recordingProjectService{
		err: errors.New("database connection failed"),
	}
	requestBody := strings.NewReader(`{"name":"Commitarium"}`)

	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/projects",
		requestBody,
	)

	recorder := httptest.NewRecorder()
	New(service, nil, nil, nil, nil, nil).ServeHTTP(recorder, request)

	response := recorder.Result()
	defer response.Body.Close()

	if response.StatusCode != http.StatusInternalServerError {
		t.Fatalf(
			"expected status code %d, got %d",
			http.StatusInternalServerError,
			response.StatusCode,
		)
	}

	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}

	var decodedBody errorResponse

	if err := json.Unmarshal(body, &decodedBody); err != nil {
		t.Fatalf("decode error response: %v", err)
	}

	if decodedBody.Error.Code != "internal_error" {
		t.Errorf(
			"expected error code %q, got %q",
			"internal_error",
			decodedBody.Error.Code,
		)
	}

	if decodedBody.Error.Message != "internal server error" {
		t.Errorf(
			"expected error message %q, got %q",
			"internal server error",
			decodedBody.Error.Message,
		)
	}

	if strings.Contains(string(body), service.err.Error()) {
		t.Fatal("response exposed the internal error")
	}
}

func TestGetProjectByID(t *testing.T) {
	expected := project.Project{
		ID:        "prj_test",
		Name:      "Commitarium",
		CreatedAt: time.Date(2026, time.September, 7, 12, 0, 0, 0, time.UTC),
	}

	projects := &recordingProjectService{
		getByIDResult: expected,
	}

	request := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/projects/prj_test",
		nil,
	)

	recorder := httptest.NewRecorder()
	New(projects, nil, nil, nil, nil, nil).ServeHTTP(recorder, request)

	response := recorder.Result()
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		t.Fatalf(
			"expected status code %d, got %d",
			http.StatusOK,
			response.StatusCode,
		)
	}

	if contentType := response.Header.Get("Content-Type"); contentType != "application/json" {
		t.Errorf("expected application/json, got %q", contentType)
	}

	var body projectResponse
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode project response: %v", err)
	}

	if projects.receivedID != expected.ID {
		t.Errorf(
			"expected service to receive ID %q, got %q",
			expected.ID,
			projects.receivedID,
		)
	}

	if body.ID != expected.ID {
		t.Errorf("expected ID %q, got %q", expected.ID, body.ID)
	}

	if body.Name != expected.Name {
		t.Errorf("expected name %q, got %q", expected.Name, body.Name)
	}

	if body.CreatedAt != expected.CreatedAt {
		t.Errorf(
			"expected creation time %v, got %v",
			expected.CreatedAt,
			body.CreatedAt,
		)
	}
}

func TestGetProjectByIDReturnsNotFound(t *testing.T) {
	service := &recordingProjectService{
		getByIDErr: project.ErrNotFound,
	}

	request := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/projects/prj_missing",
		nil,
	)

	recorder := httptest.NewRecorder()
	New(service, nil, nil, nil, nil, nil).ServeHTTP(recorder, request)

	response := recorder.Result()
	defer response.Body.Close()

	if response.StatusCode != http.StatusNotFound {
		t.Fatalf(
			"expected status code %d, got %d",
			http.StatusNotFound,
			response.StatusCode,
		)
	}

	if contentType := response.Header.Get("Content-Type"); contentType != "application/json" {
		t.Errorf("expected application/json, got %q", contentType)
	}

	var body errorResponse
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode error response: %v", err)
	}

	if body.Error.Code != "project_not_found" {
		t.Errorf(
			"expected error code %q, got %q",
			"project_not_found",
			body.Error.Code,
		)
	}

	if body.Error.Message != "project not found" {
		t.Errorf(
			"expected error message %q, got %q",
			"project not found",
			body.Error.Message,
		)
	}

	if service.receivedID != "prj_missing" {
		t.Errorf(
			"expected service to receive ID %q, got %q",
			"prj_missing",
			service.receivedID,
		)
	}
}

func TestListProjects(t *testing.T) {
	boundAt := time.Date(2026, time.September, 9, 18, 0, 0, 0, time.UTC)
	service := &recordingProjectService{listResult: []project.Project{
		{ID: "prj_one", Name: "One", RecoveryPolicy: project.RecoveryPolicyApprovalRequired, CreatedAt: boundAt.Add(-time.Hour)},
		{ID: "prj_two", Name: "Two", RecoveryPolicy: project.RecoveryPolicyAutomatic, CreatedAt: boundAt,
			ForgejoRepository: &project.ForgejoRepository{Owner: "owner", Name: "repo", DefaultBranch: "main", BoundAt: boundAt}},
	}}
	recorder := httptest.NewRecorder()
	New(service, nil, nil, nil, nil, nil).ServeHTTP(
		recorder, httptest.NewRequest(http.MethodGet, "/api/v1/projects", nil),
	)
	response := recorder.Result()
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, response.StatusCode)
	}
	var body []projectResponse
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode projects: %v", err)
	}
	if len(body) != 2 || body[0].ID != "prj_one" || body[0].ForgejoRepository != nil {
		t.Fatalf("unexpected project list %+v", body)
	}
	if body[1].ForgejoRepository == nil || body[1].ForgejoRepository.DefaultBranch != "main" {
		t.Fatalf("bound repository missing from project list %+v", body[1])
	}
}

func TestListProjectsHandlesUnexpectedError(t *testing.T) {
	service := &recordingProjectService{listErr: errors.New("database unavailable")}
	recorder := httptest.NewRecorder()
	New(service, nil, nil, nil, nil, nil).ServeHTTP(
		recorder, httptest.NewRequest(http.MethodGet, "/api/v1/projects", nil),
	)
	response := recorder.Result()
	defer response.Body.Close()
	if response.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected status %d, got %d", http.StatusInternalServerError, response.StatusCode)
	}
	var body errorResponse
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if body.Error.Code != "internal_error" {
		t.Fatalf("unexpected error response %+v", body)
	}
}

func TestBindForgejoRepository(t *testing.T) {
	boundAt := time.Date(2026, time.September, 9, 18, 0, 0, 0, time.UTC)
	service := &recordingProjectService{bindResult: project.Project{
		ID: "prj_test", Name: "Test", ForgejoRepository: &project.ForgejoRepository{
			Owner: "canonical", Name: "repository", DefaultBranch: "main", BoundAt: boundAt,
		},
	}}
	recorder := httptest.NewRecorder()
	New(service, nil, nil, nil, nil, nil).ServeHTTP(recorder, httptest.NewRequest(
		http.MethodPut, "/api/v1/projects/prj_test/forgejo-repository",
		strings.NewReader(`{"owner":"owner","name":"repo"}`),
	))
	response := recorder.Result()
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, response.StatusCode, body)
	}
	if service.bindProjectID != "prj_test" || service.bindOwner != "owner" || service.bindName != "repo" {
		t.Fatalf("unexpected bind request %q %q/%q", service.bindProjectID, service.bindOwner, service.bindName)
	}
	var body projectResponse
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode bound project: %v", err)
	}
	if body.ForgejoRepository == nil || body.ForgejoRepository.Owner != "canonical" ||
		body.ForgejoRepository.BoundAt != boundAt {
		t.Fatalf("unexpected bound project response %+v", body)
	}
}

func TestBindForgejoRepositoryRejectsMalformedJSON(t *testing.T) {
	service := &recordingProjectService{}
	recorder := httptest.NewRecorder()
	New(service, nil, nil, nil, nil, nil).ServeHTTP(recorder, httptest.NewRequest(
		http.MethodPut, "/api/v1/projects/prj_test/forgejo-repository", strings.NewReader(`{"owner":`),
	))
	response := recorder.Result()
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, response.StatusCode)
	}
	if service.bindProjectID != "" {
		t.Fatal("malformed request reached project service")
	}
}

func TestBindForgejoRepositoryMapsExpectedErrors(t *testing.T) {
	tests := []struct {
		err    error
		status int
		code   string
	}{
		{project.ErrNotFound, http.StatusNotFound, "project_not_found"},
		{project.ErrForgejoOwnerRequired, http.StatusBadRequest, "forgejo_owner_required"},
		{project.ErrForgejoRepositoryNameRequired, http.StatusBadRequest, "forgejo_repository_name_required"},
		{project.ErrInvalidForgejoRepositoryCoordinate, http.StatusBadRequest, "invalid_forgejo_repository"},
		{project.ErrForgejoRepositoryNotFound, http.StatusNotFound, "forgejo_repository_not_found"},
		{project.ErrForgejoRepositoryNotReady, http.StatusConflict, "forgejo_repository_not_ready"},
		{project.ErrForgejoRepositoryAlreadyBound, http.StatusConflict, "forgejo_repository_already_bound"},
		{project.ErrForgejoUnavailable, http.StatusServiceUnavailable, "forgejo_unavailable"},
		{errors.New("unexpected"), http.StatusInternalServerError, "internal_error"},
	}
	for _, test := range tests {
		t.Run(test.code, func(t *testing.T) {
			service := &recordingProjectService{bindErr: test.err}
			recorder := httptest.NewRecorder()
			New(service, nil, nil, nil, nil, nil).ServeHTTP(recorder, httptest.NewRequest(
				http.MethodPut, "/api/v1/projects/prj_test/forgejo-repository",
				strings.NewReader(`{"owner":"owner","name":"repo"}`),
			))
			response := recorder.Result()
			defer response.Body.Close()
			if response.StatusCode != test.status {
				t.Fatalf("expected status %d, got %d", test.status, response.StatusCode)
			}
			var body errorResponse
			if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
				t.Fatalf("decode error response: %v", err)
			}
			if body.Error.Code != test.code {
				t.Fatalf("expected code %q, got %+v", test.code, body)
			}
		})
	}
}
