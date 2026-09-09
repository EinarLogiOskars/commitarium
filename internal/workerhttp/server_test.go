package workerhttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

const testBearerToken = "worker-test-token"

type contextKey string

type recordingService struct {
	attempt Attempt
	created bool

	putErr     error
	getErr     error
	commandErr error
	forceErr   error

	putCalls     int
	getCalls     int
	commandCalls int
	forceCalls   int

	putIdentity     MutationIdentity
	putRequest      PutAttemptRequest
	getReference    AttemptReference
	commandIdentity MutationIdentity
	commandRequest  CommandRequest
	forceIdentity   MutationIdentity
	forceRequest    ForceStopRequest
	contextValue    any
}

func (service *recordingService) PutAttempt(
	ctx context.Context,
	identity MutationIdentity,
	request PutAttemptRequest,
) (Attempt, bool, error) {
	service.putCalls++
	service.putIdentity = identity
	service.putRequest = request
	service.contextValue = ctx.Value(contextKey("test"))
	return service.attempt, service.created, service.putErr
}

func (service *recordingService) GetAttempt(
	_ context.Context,
	reference AttemptReference,
) (Attempt, error) {
	service.getCalls++
	service.getReference = reference
	return service.attempt, service.getErr
}

func (service *recordingService) SendCommand(
	_ context.Context,
	identity MutationIdentity,
	request CommandRequest,
) (Attempt, error) {
	service.commandCalls++
	service.commandIdentity = identity
	service.commandRequest = request
	return service.attempt, service.commandErr
}

func (service *recordingService) ForceStop(
	_ context.Context,
	identity MutationIdentity,
	request ForceStopRequest,
) (Attempt, error) {
	service.forceCalls++
	service.forceIdentity = identity
	service.forceRequest = request
	return service.attempt, service.forceErr
}

func TestNewServerValidatesConfiguration(t *testing.T) {
	validConfig := testServerConfig()
	service := &recordingService{attempt: validServerAttempt()}

	tests := []struct {
		name    string
		config  ServerConfig
		service Service
	}{
		{name: "missing service", config: validConfig},
		{name: "missing bearer token", config: withServerBearerToken(validConfig, ""), service: service},
		{name: "token with surrounding whitespace", config: withServerBearerToken(validConfig, " token"), service: service},
		{name: "token with internal whitespace", config: withServerBearerToken(validConfig, "bad token"), service: service},
		{name: "unknown provider", config: withServerProvider(validConfig, "unknown"), service: service},
		{name: "no capabilities", config: withServerCapabilities(validConfig, nil), service: service},
		{name: "duplicate capabilities", config: withServerCapabilities(validConfig, []Capability{CapabilityStart, CapabilityStart}), service: service},
		{name: "nonpositive concurrency", config: withServerConcurrency(validConfig, 0), service: service},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewServer(test.config, test.service)
			if !errors.Is(err, ErrInvalidServerConfig) {
				t.Fatalf("expected error %v, got %v", ErrInvalidServerConfig, err)
			}
		})
	}
}

func TestHealthAndCapabilities(t *testing.T) {
	config := testServerConfig()
	server := newTestServer(t, config, &recordingService{attempt: validServerAttempt()})

	healthRequest := httptest.NewRequest(http.MethodGet, APIBasePath+"/health", nil)
	healthResponse := serve(server, healthRequest)
	if healthResponse.Code != http.StatusOK {
		t.Fatalf("health status = %d, want %d", healthResponse.Code, http.StatusOK)
	}
	var health HealthResponse
	decodeTestResponse(t, healthResponse, &health)
	if err := health.Validate(); err != nil {
		t.Fatalf("validate health response: %v", err)
	}

	// The server copies configuration so later caller mutation cannot silently
	// change the advertised protocol.
	config.Capabilities[0] = CapabilityForceStop
	capabilitiesRequest := authorizedRequest(http.MethodGet, APIBasePath+"/capabilities", "")
	capabilitiesResponse := serve(server, capabilitiesRequest)
	if capabilitiesResponse.Code != http.StatusOK {
		t.Fatalf("capabilities status = %d, want %d", capabilitiesResponse.Code, http.StatusOK)
	}
	var capabilities CapabilitiesResponse
	decodeTestResponse(t, capabilitiesResponse, &capabilities)
	if err := capabilities.Validate(); err != nil {
		t.Fatalf("validate capabilities response: %v", err)
	}
	if capabilities.Provider != ProviderCodex || !reflect.DeepEqual(capabilities.Capabilities, []Capability{CapabilityStart, CapabilityResume, CapabilityPause, CapabilityForceStop}) {
		t.Fatalf("unexpected capabilities response: %+v", capabilities)
	}
}

func TestAuthenticationProtectsWorkerEndpoints(t *testing.T) {
	service := &recordingService{attempt: validServerAttempt()}
	server := newTestServer(t, testServerConfig(), service)
	path := attemptPath(validAttemptReference())

	tests := []struct {
		name          string
		authorization []string
		wantStatus    int
	}{
		{name: "missing", wantStatus: http.StatusUnauthorized},
		{name: "wrong scheme", authorization: []string{"Basic " + testBearerToken}, wantStatus: http.StatusUnauthorized},
		{name: "wrong token", authorization: []string{"Bearer wrong-token"}, wantStatus: http.StatusUnauthorized},
		{name: "duplicate", authorization: []string{"Bearer " + testBearerToken, "Bearer " + testBearerToken}, wantStatus: http.StatusUnauthorized},
		{name: "valid scheme case insensitive", authorization: []string{"bearer " + testBearerToken}, wantStatus: http.StatusOK},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, path, nil)
			for _, value := range test.authorization {
				request.Header.Add("Authorization", value)
			}
			response := serve(server, request)
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d", response.Code, test.wantStatus)
			}
			if test.wantStatus == http.StatusUnauthorized {
				assertProtocolError(t, response, ErrorUnauthorized)
				if got := response.Header().Get("WWW-Authenticate"); got != "Bearer" {
					t.Fatalf("WWW-Authenticate = %q, want Bearer", got)
				}
			}
		})
	}

	if service.getCalls != 1 {
		t.Fatalf("service GetAttempt calls = %d, want 1", service.getCalls)
	}
}

func TestPutAttemptForwardsIdentityAndReturnsCreatedAttempt(t *testing.T) {
	attempt := validServerAttempt()
	service := &recordingService{attempt: attempt, created: true}
	server := newTestServer(t, testServerConfig(), service)
	requestBody := validPutAttemptRequest()
	request := authorizedJSONRequest(t, http.MethodPut, attemptPath(attempt.AttemptReference), requestBody)
	request.Header.Set("Content-Type", "application/json; charset=utf-8")
	request.Header.Set(IdempotencyKeyHeader, "idem_start")
	request = request.WithContext(context.WithValue(request.Context(), contextKey("test"), "forwarded"))

	response := serve(server, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d; body=%s", response.Code, http.StatusCreated, response.Body.String())
	}
	if got := response.Header().Get("Location"); got != attemptPath(attempt.AttemptReference) {
		t.Fatalf("Location = %q, want %q", got, attemptPath(attempt.AttemptReference))
	}
	var responseAttempt Attempt
	decodeTestResponse(t, response, &responseAttempt)
	if !reflect.DeepEqual(responseAttempt, attempt) {
		t.Fatalf("response attempt = %+v, want %+v", responseAttempt, attempt)
	}

	wantIdentity := MutationIdentity{AttemptReference: attempt.AttemptReference, IdempotencyKey: "idem_start"}
	if service.putCalls != 1 || service.putIdentity != wantIdentity {
		t.Fatalf("PutAttempt call = %d, identity = %+v", service.putCalls, service.putIdentity)
	}
	if !reflect.DeepEqual(service.putRequest, requestBody) {
		t.Fatalf("PutAttempt request = %+v, want %+v", service.putRequest, requestBody)
	}
	if service.contextValue != "forwarded" {
		t.Fatalf("request context value = %v, want forwarded", service.contextValue)
	}
}

func TestPutAttemptReplayReturnsExistingAttempt(t *testing.T) {
	attempt := validServerAttempt()
	service := &recordingService{attempt: attempt, created: false}
	server := newTestServer(t, testServerConfig(), service)
	request := authorizedJSONRequest(t, http.MethodPut, attemptPath(attempt.AttemptReference), validPutAttemptRequest())
	request.Header.Set(IdempotencyKeyHeader, "idem_replay")

	response := serve(server, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	if got := response.Header().Get("Location"); got != "" {
		t.Fatalf("replayed response Location = %q, want empty", got)
	}
}

func TestPutAttemptRejectsMismatchedServiceResponse(t *testing.T) {
	attempt := validServerAttempt()
	attempt.Assignment.ProjectID = "prj_other"
	service := &recordingService{attempt: attempt, created: true}
	server := newTestServer(t, testServerConfig(), service)
	request := authorizedJSONRequest(t, http.MethodPut, attemptPath(attempt.AttemptReference), validPutAttemptRequest())
	request.Header.Set(IdempotencyKeyHeader, "idem_start")

	response := serve(server, request)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusInternalServerError)
	}
	assertProtocolError(t, response, ErrorInternal)
}

func TestGetAttemptForwardsPathIdentity(t *testing.T) {
	attempt := validServerAttempt()
	service := &recordingService{attempt: attempt}
	server := newTestServer(t, testServerConfig(), service)

	response := serve(server, authorizedRequest(http.MethodGet, attemptPath(attempt.AttemptReference), ""))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	if service.getCalls != 1 || service.getReference != attempt.AttemptReference {
		t.Fatalf("GetAttempt call = %d, reference = %+v", service.getCalls, service.getReference)
	}
}

func TestCommandAndForceStopForwardExactMutationIdentity(t *testing.T) {
	attempt := validServerAttempt()
	service := &recordingService{attempt: attempt}
	server := newTestServer(t, testServerConfig(), service)

	command := CommandRequest{Type: CommandPause}
	commandRequest := authorizedJSONRequest(t, http.MethodPost, attemptPath(attempt.AttemptReference)+"/commands", command)
	commandRequest.Header.Set(IdempotencyKeyHeader, "idem_pause")
	commandResponse := serve(server, commandRequest)
	if commandResponse.Code != http.StatusAccepted {
		t.Fatalf("command status = %d, want %d; body=%s", commandResponse.Code, http.StatusAccepted, commandResponse.Body.String())
	}
	wantCommandIdentity := MutationIdentity{AttemptReference: attempt.AttemptReference, IdempotencyKey: "idem_pause"}
	if service.commandCalls != 1 || service.commandIdentity != wantCommandIdentity || service.commandRequest != command {
		t.Fatalf("unexpected command call: count=%d identity=%+v request=%+v", service.commandCalls, service.commandIdentity, service.commandRequest)
	}

	forceStop := ForceStopRequest{Reason: "cooperative stop deadline expired"}
	forceRequest := authorizedJSONRequest(t, http.MethodPost, attemptPath(attempt.AttemptReference)+"/force-stop", forceStop)
	forceRequest.Header.Set(IdempotencyKeyHeader, "idem_force")
	forceResponse := serve(server, forceRequest)
	if forceResponse.Code != http.StatusOK {
		t.Fatalf("force-stop status = %d, want %d; body=%s", forceResponse.Code, http.StatusOK, forceResponse.Body.String())
	}
	wantForceIdentity := MutationIdentity{AttemptReference: attempt.AttemptReference, IdempotencyKey: "idem_force"}
	if service.forceCalls != 1 || service.forceIdentity != wantForceIdentity || service.forceRequest != forceStop {
		t.Fatalf("unexpected force-stop call: count=%d identity=%+v request=%+v", service.forceCalls, service.forceIdentity, service.forceRequest)
	}
}

func TestUnsupportedOperationDoesNotReachService(t *testing.T) {
	attempt := validServerAttempt()
	service := &recordingService{attempt: attempt}
	config := withServerCapabilities(testServerConfig(), []Capability{CapabilityStart})
	server := newTestServer(t, config, service)
	commandRequest := authorizedJSONRequest(
		t,
		http.MethodPost,
		attemptPath(attempt.AttemptReference)+"/commands",
		CommandRequest{Type: CommandPause},
	)
	commandRequest.Header.Set(IdempotencyKeyHeader, "idem_pause")

	response := serve(server, commandRequest)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUnprocessableEntity)
	}
	assertProtocolError(t, response, ErrorUnsupportedOperation)
	if service.commandCalls != 0 {
		t.Fatalf("SendCommand calls = %d, want 0", service.commandCalls)
	}
}

func TestCommandCapabilityMapping(t *testing.T) {
	tests := map[CommandType]Capability{
		CommandMessage:  CapabilityMessage,
		CommandPause:    CapabilityPause,
		CommandContinue: CapabilityContinue,
		CommandStop:     CapabilityCooperativeStop,
	}
	for command, want := range tests {
		if got := capabilityForCommand(command); got != want {
			t.Errorf("capabilityForCommand(%q) = %q, want %q", command, got, want)
		}
	}
	if got := capabilityForCommand("unknown"); got != "" {
		t.Errorf("unknown command capability = %q, want empty", got)
	}
}

func TestMutationsRejectAmbiguousOrInvalidRequests(t *testing.T) {
	attempt := validServerAttempt()
	validBody := marshalTestJSON(t, validPutAttemptRequest())
	path := attemptPath(attempt.AttemptReference)

	tests := []struct {
		name        string
		body        string
		contentType string
		keys        []string
		wantStatus  int
		wantCode    ErrorCode
	}{
		{name: "missing content type", body: validBody, keys: []string{"idem_test"}, wantStatus: http.StatusUnsupportedMediaType, wantCode: ErrorUnsupportedMediaType},
		{name: "wrong content type", body: validBody, contentType: "text/plain", keys: []string{"idem_test"}, wantStatus: http.StatusUnsupportedMediaType, wantCode: ErrorUnsupportedMediaType},
		{name: "malformed JSON", body: `{"mode":`, contentType: "application/json", keys: []string{"idem_test"}, wantStatus: http.StatusBadRequest, wantCode: ErrorInvalidRequest},
		{name: "unknown JSON field", body: strings.TrimSuffix(validBody, "}") + `,"surprise":true}`, contentType: "application/json", keys: []string{"idem_test"}, wantStatus: http.StatusBadRequest, wantCode: ErrorInvalidRequest},
		{name: "multiple JSON values", body: validBody + `{}`, contentType: "application/json", keys: []string{"idem_test"}, wantStatus: http.StatusBadRequest, wantCode: ErrorInvalidRequest},
		{name: "oversized body", body: validBody + strings.Repeat(" ", int(MaxRequestBodyBytes)), contentType: "application/json", keys: []string{"idem_test"}, wantStatus: http.StatusRequestEntityTooLarge, wantCode: ErrorRequestTooLarge},
		{name: "missing idempotency key", body: validBody, contentType: "application/json", wantStatus: http.StatusBadRequest, wantCode: ErrorInvalidRequest},
		{name: "duplicate idempotency key", body: validBody, contentType: "application/json", keys: []string{"idem_one", "idem_two"}, wantStatus: http.StatusBadRequest, wantCode: ErrorInvalidRequest},
		{name: "invalid request fields", body: `{}`, contentType: "application/json", keys: []string{"idem_test"}, wantStatus: http.StatusBadRequest, wantCode: ErrorInvalidRequest},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &recordingService{attempt: attempt}
			server := newTestServer(t, testServerConfig(), service)
			request := authorizedRequest(http.MethodPut, path, test.body)
			if test.contentType != "" {
				request.Header.Set("Content-Type", test.contentType)
			}
			for _, key := range test.keys {
				request.Header.Add(IdempotencyKeyHeader, key)
			}

			response := serve(server, request)
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", response.Code, test.wantStatus, response.Body.String())
			}
			assertProtocolError(t, response, test.wantCode)
			if service.putCalls != 0 {
				t.Fatalf("PutAttempt calls = %d, want 0", service.putCalls)
			}
		})
	}
}

func TestRoutesReturnJSONErrorsForWrongMethodAndUnknownPath(t *testing.T) {
	service := &recordingService{attempt: validServerAttempt()}
	server := newTestServer(t, testServerConfig(), service)

	wrongMethod := authorizedRequest(http.MethodPatch, attemptPath(validAttemptReference()), "")
	wrongMethodResponse := serve(server, wrongMethod)
	if wrongMethodResponse.Code != http.StatusMethodNotAllowed {
		t.Fatalf("wrong-method status = %d, want %d", wrongMethodResponse.Code, http.StatusMethodNotAllowed)
	}
	assertProtocolError(t, wrongMethodResponse, ErrorMethodNotAllowed)
	if got := wrongMethodResponse.Header().Get("Allow"); got != "GET, PUT" {
		t.Fatalf("Allow = %q, want %q", got, "GET, PUT")
	}

	unknownResponse := serve(server, httptest.NewRequest(http.MethodGet, APIBasePath+"/unknown", nil))
	if unknownResponse.Code != http.StatusNotFound {
		t.Fatalf("unknown-path status = %d, want %d", unknownResponse.Code, http.StatusNotFound)
	}
	assertProtocolError(t, unknownResponse, ErrorNotFound)

	invalidIdentityResponse := serve(server, authorizedRequest(
		http.MethodGet,
		APIBasePath+"/sessions/ses$invalid/attempts/att_test",
		"",
	))
	if invalidIdentityResponse.Code != http.StatusBadRequest {
		t.Fatalf("invalid-identity status = %d, want %d", invalidIdentityResponse.Code, http.StatusBadRequest)
	}
	assertProtocolError(t, invalidIdentityResponse, ErrorInvalidRequest)
	if service.getCalls != 0 {
		t.Fatalf("GetAttempt calls = %d, want 0", service.getCalls)
	}
}

func TestServiceErrorsHaveStableStatusAndDoNotLeakUnexpectedFailures(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   ErrorCode
	}{
		{
			name:       "known conflict",
			err:        NewServiceError(ErrorAttemptActive, "another attempt may still be active", false, errors.New("journal conflict")),
			wantStatus: http.StatusConflict,
			wantCode:   ErrorAttemptActive,
		},
		{
			name:       "known unavailable dependency",
			err:        NewServiceError(ErrorProfileUnavailable, "agent profile is unavailable", true, errors.New("volume unavailable")),
			wantStatus: http.StatusServiceUnavailable,
			wantCode:   ErrorProfileUnavailable,
		},
		{
			name:       "ordinary internal error",
			err:        errors.New("secret internal detail"),
			wantStatus: http.StatusInternalServerError,
			wantCode:   ErrorInternal,
		},
		{
			name:       "invalid service error",
			err:        NewServiceError("unknown", "secret internal detail", false, nil),
			wantStatus: http.StatusInternalServerError,
			wantCode:   ErrorInternal,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &recordingService{attempt: validServerAttempt(), getErr: test.err}
			server := newTestServer(t, testServerConfig(), service)
			response := serve(server, authorizedRequest(http.MethodGet, attemptPath(validAttemptReference()), ""))
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d", response.Code, test.wantStatus)
			}
			protocolError := assertProtocolError(t, response, test.wantCode)
			if test.wantCode == ErrorInternal && strings.Contains(protocolError.Message, "secret") {
				t.Fatalf("internal error leaked service detail: %q", protocolError.Message)
			}
		})
	}
}

func TestInvalidServiceAttemptIsNotPublished(t *testing.T) {
	attempt := validServerAttempt()
	attempt.AttemptID = "att_wrong"
	service := &recordingService{attempt: attempt}
	server := newTestServer(t, testServerConfig(), service)

	response := serve(server, authorizedRequest(http.MethodGet, attemptPath(validAttemptReference()), ""))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusInternalServerError)
	}
	assertProtocolError(t, response, ErrorInternal)
}

func TestStatusForErrorCode(t *testing.T) {
	tests := map[ErrorCode]int{
		ErrorInvalidRequest:         http.StatusBadRequest,
		ErrorUnauthorized:           http.StatusUnauthorized,
		ErrorNotFound:               http.StatusNotFound,
		ErrorMethodNotAllowed:       http.StatusMethodNotAllowed,
		ErrorUnsupportedMediaType:   http.StatusUnsupportedMediaType,
		ErrorRequestTooLarge:        http.StatusRequestEntityTooLarge,
		ErrorUnsupportedOperation:   http.StatusUnprocessableEntity,
		ErrorAttemptActive:          http.StatusConflict,
		ErrorAttemptConflict:        http.StatusConflict,
		ErrorStaleAttempt:           http.StatusConflict,
		ErrorProviderSessionMissing: http.StatusConflict,
		ErrorConfigurationMismatch:  http.StatusConflict,
		ErrorIndeterminateState:     http.StatusConflict,
		ErrorProfileUnavailable:     http.StatusServiceUnavailable,
		ErrorWorkspaceUnavailable:   http.StatusServiceUnavailable,
		ErrorRedactionFailed:        http.StatusInternalServerError,
		ErrorInvalidEventStream:     http.StatusInternalServerError,
		ErrorInternal:               http.StatusInternalServerError,
	}
	for code, want := range tests {
		if got := statusForErrorCode(code); got != want {
			t.Errorf("statusForErrorCode(%q) = %d, want %d", code, got, want)
		}
	}
	if got := statusForErrorCode("unknown"); got != http.StatusInternalServerError {
		t.Errorf("unknown error status = %d, want %d", got, http.StatusInternalServerError)
	}
}

func testServerConfig() ServerConfig {
	return ServerConfig{
		BearerToken: testBearerToken,
		Provider:    ProviderCodex,
		Capabilities: []Capability{
			CapabilityStart,
			CapabilityResume,
			CapabilityPause,
			CapabilityForceStop,
		},
		MaxConcurrentAttempts: 1,
	}
}

func newTestServer(t *testing.T, config ServerConfig, service Service) http.Handler {
	t.Helper()
	server, err := NewServer(config, service)
	if err != nil {
		t.Fatalf("create worker HTTP server: %v", err)
	}
	return server
}

func validServerAttempt() Attempt {
	now := time.Date(2026, time.September, 8, 12, 0, 0, 0, time.UTC)
	return Attempt{
		AttemptReference:    validAttemptReference(),
		Mode:                AttemptModeStart,
		Assignment:          validAssignment(),
		ProviderSessionID:   "provider_session_test",
		State:               AttemptStateRunning,
		LatestEventSequence: 1,
		StartedAt:           now,
		UpdatedAt:           now,
	}
}

func validPutAttemptRequest() PutAttemptRequest {
	return PutAttemptRequest{
		Mode:         AttemptModeStart,
		Assignment:   validAssignment(),
		Instructions: "Implement the accepted plan.",
	}
}

func authorizedRequest(method string, path string, body string) *http.Request {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+testBearerToken)
	return request
}

func authorizedJSONRequest(t *testing.T, method string, path string, body any) *http.Request {
	t.Helper()
	request := authorizedRequest(method, path, marshalTestJSON(t, body))
	request.Header.Set("Content-Type", "application/json")
	return request
}

func serve(handler http.Handler, request *http.Request) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func decodeTestResponse(t *testing.T, recorder *httptest.ResponseRecorder, destination any) {
	t.Helper()
	if contentType := recorder.Header().Get("Content-Type"); contentType != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", contentType)
	}
	if cacheControl := recorder.Header().Get("Cache-Control"); cacheControl != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", cacheControl)
	}
	if err := json.NewDecoder(recorder.Body).Decode(destination); err != nil {
		t.Fatalf("decode response: %v", err)
	}
}

func assertProtocolError(t *testing.T, recorder *httptest.ResponseRecorder, code ErrorCode) ProtocolError {
	t.Helper()
	var response ErrorResponse
	decodeTestResponse(t, recorder, &response)
	if response.Error.Code != code {
		t.Fatalf("error code = %q, want %q", response.Error.Code, code)
	}
	if err := response.Validate(); err != nil {
		t.Fatalf("validate error response: %v", err)
	}
	return response.Error
}

func marshalTestJSON(t *testing.T, value any) string {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal test JSON: %v", err)
	}
	return string(payload)
}

func withServerBearerToken(config ServerConfig, bearerToken string) ServerConfig {
	config.BearerToken = bearerToken
	return config
}

func withServerProvider(config ServerConfig, provider Provider) ServerConfig {
	config.Provider = provider
	return config
}

func withServerCapabilities(config ServerConfig, capabilities []Capability) ServerConfig {
	config.Capabilities = capabilities
	return config
}

func withServerConcurrency(config ServerConfig, concurrency int) ServerConfig {
	config.MaxConcurrentAttempts = concurrency
	return config
}
