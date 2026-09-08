package workerhttp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestClientAndServerRoundTrip(t *testing.T) {
	attempt := validServerAttempt()
	service := &recordingService{attempt: attempt, created: true}
	workerServer := newTestServer(t, testServerConfig(), service)
	client := newTestClient(t, workerServer, time.Second)
	ctx := context.Background()

	health, err := client.Health(ctx)
	if err != nil {
		t.Fatalf("get health: %v", err)
	}
	if err := health.Validate(); err != nil {
		t.Fatalf("validate health: %v", err)
	}

	capabilities, err := client.Capabilities(ctx)
	if err != nil {
		t.Fatalf("get capabilities: %v", err)
	}
	if capabilities.Provider != ProviderCodex || capabilities.ProtocolVersion != ProtocolVersion {
		t.Fatalf("unexpected capabilities: %+v", capabilities)
	}

	identity := MutationIdentity{
		AttemptReference: attempt.AttemptReference,
		IdempotencyKey:   "idem_start",
	}
	startRequest := validPutAttemptRequest()
	createdAttempt, created, err := client.PutAttempt(ctx, identity, startRequest)
	if err != nil {
		t.Fatalf("put attempt: %v", err)
	}
	if !created || !reflect.DeepEqual(createdAttempt, attempt) {
		t.Fatalf("created = %t, attempt = %+v; want created attempt %+v", created, createdAttempt, attempt)
	}
	if service.putIdentity != identity || !reflect.DeepEqual(service.putRequest, startRequest) {
		t.Fatalf("worker received identity=%+v request=%+v", service.putIdentity, service.putRequest)
	}
	service.created = false
	_, created, err = client.PutAttempt(ctx, identity, startRequest)
	if err != nil {
		t.Fatalf("replay attempt request: %v", err)
	}
	if created {
		t.Fatal("replayed attempt was incorrectly reported as newly created")
	}

	gotAttempt, err := client.GetAttempt(ctx, attempt.AttemptReference)
	if err != nil {
		t.Fatalf("get attempt: %v", err)
	}
	if !reflect.DeepEqual(gotAttempt, attempt) || service.getReference != attempt.AttemptReference {
		t.Fatalf("got attempt=%+v reference=%+v", gotAttempt, service.getReference)
	}

	commandIdentity := identity
	commandIdentity.IdempotencyKey = "idem_pause"
	command := CommandRequest{Type: CommandPause}
	commandAttempt, err := client.SendCommand(ctx, commandIdentity, command)
	if err != nil {
		t.Fatalf("send command: %v", err)
	}
	if !reflect.DeepEqual(commandAttempt, attempt) || service.commandIdentity != commandIdentity || service.commandRequest != command {
		t.Fatalf("unexpected command round trip: attempt=%+v identity=%+v request=%+v", commandAttempt, service.commandIdentity, service.commandRequest)
	}

	forceIdentity := identity
	forceIdentity.IdempotencyKey = "idem_force"
	forceRequest := ForceStopRequest{Reason: "cooperative stop deadline expired"}
	forceAttempt, err := client.ForceStop(ctx, forceIdentity, forceRequest)
	if err != nil {
		t.Fatalf("force stop: %v", err)
	}
	if !reflect.DeepEqual(forceAttempt, attempt) || service.forceIdentity != forceIdentity || service.forceRequest != forceRequest {
		t.Fatalf("unexpected force-stop round trip: attempt=%+v identity=%+v request=%+v", forceAttempt, service.forceIdentity, service.forceRequest)
	}
}

func TestClientResumesExistingProviderSession(t *testing.T) {
	attempt := validServerAttempt()
	attempt.Mode = AttemptModeResume
	service := &recordingService{attempt: attempt, created: true}
	workerServer := newTestServer(t, testServerConfig(), service)
	client := newTestClient(t, workerServer, time.Second)
	identity := validMutationIdentity()
	request := validPutAttemptRequest()
	request.Mode = AttemptModeResume
	request.ProviderSessionID = attempt.ProviderSessionID

	got, created, err := client.PutAttempt(context.Background(), identity, request)
	if err != nil {
		t.Fatalf("resume attempt: %v", err)
	}
	if !created || !reflect.DeepEqual(got, attempt) {
		t.Fatalf("created=%t attempt=%+v, want %+v", created, got, attempt)
	}
	if service.putRequest.ProviderSessionID != attempt.ProviderSessionID {
		t.Fatalf("worker provider session ID=%q, want %q", service.putRequest.ProviderSessionID, attempt.ProviderSessionID)
	}
}

func TestClientSendsCredentialsOnlyToProtectedRoutes(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case APIBasePath + "/health":
			if got := request.Header.Get("Authorization"); got != "" {
				t.Errorf("health Authorization=%q, want empty", got)
			}
			_, _ = fmt.Fprintln(w, `{"status":"ok","protocol_version":"v1"}`)
		case APIBasePath + "/capabilities":
			if got := request.Header.Get("Authorization"); got != "Bearer "+testBearerToken {
				t.Errorf("capabilities Authorization=%q", got)
			}
			_, _ = fmt.Fprintln(w, `{"protocol_version":"v1","provider":"codex","capabilities":["start"],"max_concurrent_attempts":1}`)
		default:
			t.Errorf("unexpected path %q", request.URL.Path)
		}
	})
	client := newTestClient(t, handler, time.Second)
	if _, err := client.Health(context.Background()); err != nil {
		t.Fatalf("get health: %v", err)
	}
	if _, err := client.Capabilities(context.Background()); err != nil {
		t.Fatalf("get capabilities: %v", err)
	}
}

func TestNewClientValidatesConfigurationWithoutMutatingHTTPClient(t *testing.T) {
	originalHTTPClient := &http.Client{}
	valid := testClientConfig("http://worker:8081", time.Second)
	valid.HTTPClient = originalHTTPClient
	if _, err := NewClient(valid); err != nil {
		t.Fatalf("create valid client: %v", err)
	}
	if originalHTTPClient.CheckRedirect != nil {
		t.Fatal("NewClient mutated the caller's HTTP client")
	}

	tests := []struct {
		name   string
		config ClientConfig
	}{
		{name: "missing URL", config: withClientBaseURL(valid, "")},
		{name: "unsupported URL scheme", config: withClientBaseURL(valid, "ftp://worker")},
		{name: "URL without host", config: withClientBaseURL(valid, "http:///worker")},
		{name: "URL with credentials", config: withClientBaseURL(valid, "http://user:secret@worker")},
		{name: "URL with path", config: withClientBaseURL(valid, "http://worker/prefix")},
		{name: "URL with query", config: withClientBaseURL(valid, "http://worker?debug=true")},
		{name: "URL with empty query", config: withClientBaseURL(valid, "http://worker?")},
		{name: "URL with fragment", config: withClientBaseURL(valid, "http://worker#fragment")},
		{name: "missing token", config: withClientBearerToken(valid, "")},
		{name: "token containing whitespace", config: withClientBearerToken(valid, "bad token")},
		{name: "zero timeout", config: withClientTimeout(valid, 0)},
		{name: "negative timeout", config: withClientTimeout(valid, -time.Second)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewClient(test.config)
			if !errors.Is(err, ErrInvalidClientConfig) {
				t.Fatalf("expected error %v, got %v", ErrInvalidClientConfig, err)
			}
		})
	}
}

func TestClientMutationRequestCannotBeReplayedByHTTPTransport(t *testing.T) {
	client := newTestClient(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), time.Second)
	request, cancel, err := client.newRequest(
		context.Background(),
		http.MethodPut,
		attemptPath(validAttemptReference()),
		true,
		"idem_start",
		validPutAttemptRequest(),
	)
	defer cancel()
	if err != nil {
		t.Fatalf("create mutation request: %v", err)
	}
	if request.GetBody != nil {
		t.Fatal("mutation request body can be replayed automatically")
	}
	if got := request.Header.Get(IdempotencyKeyHeader); got != "idem_start" {
		t.Fatalf("Idempotency-Key=%q, want idem_start", got)
	}
}

func TestClientRejectsInvalidRequestBeforeNetworkCall(t *testing.T) {
	var calls atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	})
	client := newTestClient(t, handler, time.Second)

	_, err := client.GetAttempt(context.Background(), AttemptReference{})
	if !errors.Is(err, ErrInvalidContract) {
		t.Fatalf("expected error %v, got %v", ErrInvalidContract, err)
	}
	_, _, err = client.PutAttempt(context.Background(), MutationIdentity{}, PutAttemptRequest{})
	if !errors.Is(err, ErrInvalidContract) {
		t.Fatalf("expected error %v, got %v", ErrInvalidContract, err)
	}
	_, err = client.SendCommand(context.Background(), validMutationIdentity(), CommandRequest{})
	if !errors.Is(err, ErrInvalidContract) {
		t.Fatalf("expected error %v, got %v", ErrInvalidContract, err)
	}
	_, err = client.ForceStop(context.Background(), validMutationIdentity(), ForceStopRequest{})
	if !errors.Is(err, ErrInvalidContract) {
		t.Fatalf("expected error %v, got %v", ErrInvalidContract, err)
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("network calls=%d, want 0", got)
	}
}

func TestClientReturnsTypedRemoteError(t *testing.T) {
	service := &recordingService{
		attempt: validServerAttempt(),
		getErr: NewServiceError(
			ErrorAttemptActive,
			"another attempt may still be active",
			false,
			errors.New("journal conflict"),
		),
	}
	workerServer := newTestServer(t, testServerConfig(), service)
	client := newTestClient(t, workerServer, time.Second)

	_, err := client.GetAttempt(context.Background(), validAttemptReference())
	if !errors.Is(err, ErrRemote) {
		t.Fatalf("expected error %v, got %v", ErrRemote, err)
	}
	if errors.Is(err, ErrRequestFailed) {
		t.Fatalf("remote rejection was incorrectly reported as a connection failure: %v", err)
	}
	var remoteError *RemoteError
	if !errors.As(err, &remoteError) {
		t.Fatalf("expected RemoteError, got %T", err)
	}
	if remoteError.StatusCode != http.StatusConflict || remoteError.ProtocolError.Code != ErrorAttemptActive || remoteError.ProtocolError.Retryable {
		t.Fatalf("unexpected remote error: %+v", remoteError)
	}
}

func TestClientRejectsMalformedOrContradictoryResponses(t *testing.T) {
	validHealth := `{"status":"ok","protocol_version":"v1"}`
	tests := []struct {
		name        string
		status      int
		contentType string
		body        string
	}{
		{name: "wrong content type", status: http.StatusOK, contentType: "text/plain", body: validHealth},
		{name: "malformed JSON", status: http.StatusOK, contentType: "application/json", body: `{"status":`},
		{name: "unknown field", status: http.StatusOK, contentType: "application/json", body: `{"status":"ok","protocol_version":"v1","extra":true}`},
		{name: "multiple values", status: http.StatusOK, contentType: "application/json", body: validHealth + `{}`},
		{name: "oversized", status: http.StatusOK, contentType: "application/json", body: strings.Repeat("x", int(MaxResponseBodyBytes)+1)},
		{name: "wrong protocol", status: http.StatusOK, contentType: "application/json", body: `{"status":"ok","protocol_version":"v2"}`},
		{name: "unexpected success status", status: http.StatusCreated, contentType: "application/json", body: validHealth},
		{
			name:        "error code and status disagree",
			status:      http.StatusNotFound,
			contentType: "application/json",
			body:        `{"error":{"code":"attempt_active","message":"active","retryable":false}}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", test.contentType)
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.body))
			})
			client := newTestClient(t, handler, time.Second)

			_, err := client.Health(context.Background())
			if !errors.Is(err, ErrInvalidResponse) {
				t.Fatalf("expected error %v, got %v", ErrInvalidResponse, err)
			}
		})
	}
}

func TestClientRejectsAttemptForDifferentSession(t *testing.T) {
	attempt := validServerAttempt()
	attempt.SessionID = "ses_different"
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintln(w, marshalTestJSON(t, attempt))
	})
	client := newTestClient(t, handler, time.Second)

	_, err := client.GetAttempt(context.Background(), validAttemptReference())
	if !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("expected error %v, got %v", ErrInvalidResponse, err)
	}
}

func TestClientRejectsCreatedAttemptWithoutLocation(t *testing.T) {
	attempt := validServerAttempt()
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprintln(w, marshalTestJSON(t, attempt))
	})
	client := newTestClient(t, handler, time.Second)

	_, _, err := client.PutAttempt(
		context.Background(),
		validMutationIdentity(),
		validPutAttemptRequest(),
	)
	if !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("expected error %v, got %v", ErrInvalidResponse, err)
	}
}

func TestClientTimeoutPreservesUncertainNetworkOutcome(t *testing.T) {
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		<-request.Context().Done()
		return nil, request.Context().Err()
	})
	client := newTestClientWithTransport(t, transport, 20*time.Millisecond)

	_, err := client.Health(context.Background())
	if !errors.Is(err, ErrRequestFailed) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected request failure and deadline exceeded, got %v", err)
	}
}

func TestClientReportsConnectionFailure(t *testing.T) {
	connectionError := errors.New("connection refused")
	transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, connectionError
	})
	client := newTestClientWithTransport(t, transport, time.Second)

	_, err := client.Health(context.Background())
	if !errors.Is(err, ErrRequestFailed) {
		t.Fatalf("expected error %v, got %v", ErrRequestFailed, err)
	}
}

func TestClientRefusesRedirectWithoutForwardingCredentials(t *testing.T) {
	var targetCalls atomic.Int32
	var targetAuthorization atomic.Value
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Host == "redirect-target.test" {
			targetCalls.Add(1)
			targetAuthorization.Store(request.Header.Get("Authorization"))
			return responseForHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			}), request), nil
		}
		if got := request.Header.Get("Authorization"); got != "Bearer "+testBearerToken {
			t.Errorf("source Authorization=%q", got)
		}
		return responseForHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "http://redirect-target.test/capabilities", http.StatusTemporaryRedirect)
		}), request), nil
	})
	client := newTestClientWithTransport(t, transport, time.Second)

	_, err := client.Capabilities(context.Background())
	if !errors.Is(err, ErrRedirectRefused) {
		t.Fatalf("expected error %v, got %v", ErrRedirectRefused, err)
	}
	if got := targetCalls.Load(); got != 0 {
		t.Fatalf("redirect target calls=%d, want 0", got)
	}
	if value := targetAuthorization.Load(); value != nil {
		t.Fatalf("redirect target received Authorization=%q", value)
	}
}

func testClientConfig(baseURL string, timeout time.Duration) ClientConfig {
	return ClientConfig{
		BaseURL:        baseURL,
		BearerToken:    testBearerToken,
		RequestTimeout: timeout,
	}
}

func newTestClient(t *testing.T, handler http.Handler, timeout time.Duration) *Client {
	t.Helper()
	return newTestClientWithTransport(t, handlerTransport{handler: handler}, timeout)
}

func newTestClientWithTransport(t *testing.T, transport http.RoundTripper, timeout time.Duration) *Client {
	t.Helper()
	config := testClientConfig("http://worker.test", timeout)
	config.HTTPClient = &http.Client{Transport: transport}
	client, err := NewClient(config)
	if err != nil {
		t.Fatalf("create worker HTTP client: %v", err)
	}
	return client
}

type handlerTransport struct {
	handler http.Handler
}

func (transport handlerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return responseForHandler(transport.handler, request), nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (roundTrip roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

func responseForHandler(handler http.Handler, request *http.Request) *http.Response {
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	response := recorder.Result()
	response.Request = request
	if response.Body == nil {
		response.Body = io.NopCloser(strings.NewReader(""))
	}
	return response
}

func withClientBaseURL(config ClientConfig, baseURL string) ClientConfig {
	config.BaseURL = baseURL
	return config
}

func withClientBearerToken(config ClientConfig, bearerToken string) ClientConfig {
	config.BearerToken = bearerToken
	return config
}

func withClientTimeout(config ClientConfig, timeout time.Duration) ClientConfig {
	config.RequestTimeout = timeout
	return config
}
