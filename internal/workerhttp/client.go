package workerhttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
)

const MaxResponseBodyBytes int64 = 256 * 1024

var (
	ErrInvalidClientConfig = errors.New("invalid worker HTTP client configuration")
	ErrRequestFailed       = errors.New("worker HTTP request failed")
	ErrInvalidResponse     = errors.New("invalid worker HTTP response")
	ErrRedirectRefused     = errors.New("worker HTTP redirect refused")
	ErrRemote              = errors.New("worker returned a protocol error")
)

type ClientConfig struct {
	BaseURL        string
	BearerToken    string
	RequestTimeout time.Duration
	HTTPClient     *http.Client
}

// RemoteError is a deliberate protocol error returned by a reachable worker.
// It is distinct from a connection failure or malformed worker response.
type RemoteError struct {
	StatusCode    int
	ProtocolError ProtocolError
}

func (remoteError *RemoteError) Error() string {
	if remoteError == nil {
		return "<nil>"
	}
	return fmt.Sprintf(
		"worker returned HTTP %d with %s: %s",
		remoteError.StatusCode,
		remoteError.ProtocolError.Code,
		remoteError.ProtocolError.Message,
	)
}

func (remoteError *RemoteError) Unwrap() error {
	return ErrRemote
}

type Client struct {
	baseURL        string
	bearerToken    string
	requestTimeout time.Duration
	httpClient     *http.Client
}

var _ Service = (*Client)(nil)

func NewClient(config ClientConfig) (*Client, error) {
	baseURL, err := validateClientBaseURL(config.BaseURL)
	if err != nil {
		return nil, err
	}
	if config.BearerToken == "" || strings.IndexFunc(config.BearerToken, unicode.IsSpace) >= 0 {
		return nil, fmt.Errorf("%w: bearer token is required and cannot contain whitespace", ErrInvalidClientConfig)
	}
	if config.RequestTimeout <= 0 {
		return nil, fmt.Errorf("%w: request timeout must be positive", ErrInvalidClientConfig)
	}

	httpClient := config.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	clientCopy := &http.Client{
		Transport: httpClient.Transport,
		Jar:       httpClient.Jar,
		Timeout:   httpClient.Timeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	return &Client{
		baseURL:        baseURL,
		bearerToken:    config.BearerToken,
		requestTimeout: config.RequestTimeout,
		httpClient:     clientCopy,
	}, nil
}

func (client *Client) Health(ctx context.Context) (HealthResponse, error) {
	var response HealthResponse
	status, _, err := client.do(ctx, http.MethodGet, APIBasePath+"/health", false, "", nil, &response)
	if err != nil {
		return HealthResponse{}, err
	}
	if status != http.StatusOK {
		return HealthResponse{}, invalidClientResponse("health status", nil)
	}
	if err := response.Validate(); err != nil {
		return HealthResponse{}, invalidClientResponse("health response", err)
	}
	return response, nil
}

func (client *Client) Capabilities(ctx context.Context) (CapabilitiesResponse, error) {
	var response CapabilitiesResponse
	status, _, err := client.do(ctx, http.MethodGet, APIBasePath+"/capabilities", true, "", nil, &response)
	if err != nil {
		return CapabilitiesResponse{}, err
	}
	if status != http.StatusOK {
		return CapabilitiesResponse{}, invalidClientResponse("capabilities status", nil)
	}
	if err := response.Validate(); err != nil {
		return CapabilitiesResponse{}, invalidClientResponse("capabilities response", err)
	}
	return response, nil
}

func (client *Client) PutAttempt(
	ctx context.Context,
	identity MutationIdentity,
	request PutAttemptRequest,
) (Attempt, bool, error) {
	if err := request.Validate(identity); err != nil {
		return Attempt{}, false, err
	}
	path := attemptPath(identity.AttemptReference)
	var attempt Attempt
	status, headers, err := client.do(
		ctx,
		http.MethodPut,
		path,
		true,
		identity.IdempotencyKey,
		request,
		&attempt,
	)
	if err != nil {
		return Attempt{}, false, err
	}
	if status != http.StatusOK && status != http.StatusCreated {
		return Attempt{}, false, invalidClientResponse("start or resume status", nil)
	}

	if err := validateClientPutAttempt(identity.AttemptReference, request, attempt); err != nil {
		return Attempt{}, false, err
	}
	created := status == http.StatusCreated
	if created && headers.location != path {
		return Attempt{}, false, invalidClientResponse("created attempt Location header", nil)
	}
	return attempt, created, nil
}

func (client *Client) GetAttempt(
	ctx context.Context,
	reference AttemptReference,
) (Attempt, error) {
	if err := reference.Validate(); err != nil {
		return Attempt{}, err
	}
	var attempt Attempt
	status, _, err := client.do(
		ctx,
		http.MethodGet,
		attemptPath(reference),
		true,
		"",
		nil,
		&attempt,
	)
	if err != nil {
		return Attempt{}, err
	}
	if status != http.StatusOK {
		return Attempt{}, invalidClientResponse("attempt status", nil)
	}
	if err := validateClientAttempt(reference, attempt); err != nil {
		return Attempt{}, err
	}
	return attempt, nil
}

func (client *Client) SendCommand(
	ctx context.Context,
	identity MutationIdentity,
	request CommandRequest,
) (Attempt, error) {
	if err := request.Validate(identity); err != nil {
		return Attempt{}, err
	}
	var attempt Attempt
	status, _, err := client.do(
		ctx,
		http.MethodPost,
		attemptPath(identity.AttemptReference)+"/commands",
		true,
		identity.IdempotencyKey,
		request,
		&attempt,
	)
	if err != nil {
		return Attempt{}, err
	}
	if status != http.StatusAccepted {
		return Attempt{}, invalidClientResponse("command status", nil)
	}
	if err := validateClientAttempt(identity.AttemptReference, attempt); err != nil {
		return Attempt{}, err
	}
	return attempt, nil
}

func (client *Client) ForceStop(
	ctx context.Context,
	identity MutationIdentity,
	request ForceStopRequest,
) (Attempt, error) {
	if err := request.Validate(identity); err != nil {
		return Attempt{}, err
	}
	var attempt Attempt
	status, _, err := client.do(
		ctx,
		http.MethodPost,
		attemptPath(identity.AttemptReference)+"/force-stop",
		true,
		identity.IdempotencyKey,
		request,
		&attempt,
	)
	if err != nil {
		return Attempt{}, err
	}
	if status != http.StatusOK {
		return Attempt{}, invalidClientResponse("force-stop status", nil)
	}
	if err := validateClientAttempt(identity.AttemptReference, attempt); err != nil {
		return Attempt{}, err
	}
	return attempt, nil
}

type responseMetadata struct {
	location string
}

func (client *Client) do(
	ctx context.Context,
	method string,
	path string,
	authenticated bool,
	idempotencyKey string,
	body any,
	destination any,
) (int, responseMetadata, error) {
	request, cancel, err := client.newRequest(ctx, method, path, authenticated, idempotencyKey, body)
	if err != nil {
		return 0, responseMetadata{}, err
	}
	defer cancel()

	response, err := client.httpClient.Do(request)
	if err != nil {
		return 0, responseMetadata{}, fmt.Errorf("%w: %w", ErrRequestFailed, err)
	}
	defer response.Body.Close()
	if response.StatusCode >= http.StatusMultipleChoices && response.StatusCode < http.StatusBadRequest {
		return response.StatusCode, responseMetadata{}, fmt.Errorf(
			"%w: worker returned HTTP %d",
			ErrRedirectRefused,
			response.StatusCode,
		)
	}

	payload, err := readClientResponse(response)
	if err != nil {
		return response.StatusCode, responseMetadata{}, err
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return response.StatusCode, responseMetadata{}, decodeRemoteError(response.StatusCode, payload)
	}
	if err := decodeStrictJSON(payload, destination); err != nil {
		return response.StatusCode, responseMetadata{}, invalidClientResponse("success body", err)
	}
	return response.StatusCode, responseMetadata{
		location: response.Header.Get("Location"),
	}, nil
}

func (client *Client) newRequest(
	ctx context.Context,
	method string,
	path string,
	authenticated bool,
	idempotencyKey string,
	body any,
) (*http.Request, context.CancelFunc, error) {
	requestContext, cancel := context.WithTimeout(ctx, client.requestTimeout)
	var requestBody io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			cancel()
			return nil, func() {}, fmt.Errorf("%w: encode request: %v", ErrRequestFailed, err)
		}
		requestBody = bytes.NewReader(payload)
	}
	request, err := http.NewRequestWithContext(requestContext, method, client.baseURL+path, requestBody)
	if err != nil {
		cancel()
		return nil, func() {}, fmt.Errorf("%w: create request: %v", ErrRequestFailed, err)
	}
	// net/http may replay idempotent requests after some connection failures.
	// Mutations must instead return uncertainty to orchestration for inspection.
	if body != nil {
		request.GetBody = nil
	}
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if authenticated {
		request.Header.Set("Authorization", "Bearer "+client.bearerToken)
	}
	if idempotencyKey != "" {
		request.Header.Set(IdempotencyKeyHeader, idempotencyKey)
	}
	return request, cancel, nil
}

func readClientResponse(response *http.Response) ([]byte, error) {
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return nil, invalidClientResponse("Content-Type", err)
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, MaxResponseBodyBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w: read response body: %w", ErrRequestFailed, err)
	}
	if int64(len(payload)) > MaxResponseBodyBytes {
		return nil, invalidClientResponse("response body exceeds the client limit", nil)
	}
	return payload, nil
}

func decodeRemoteError(status int, payload []byte) error {
	var response ErrorResponse
	if err := decodeStrictJSON(payload, &response); err != nil {
		return invalidClientResponse("error body", err)
	}
	if err := response.Validate(); err != nil {
		return invalidClientResponse("protocol error", err)
	}
	if wantStatus := statusForErrorCode(response.Error.Code); wantStatus != status {
		return invalidClientResponse("protocol error status", nil)
	}
	return &RemoteError{StatusCode: status, ProtocolError: response.Error}
}

func decodeStrictJSON(payload []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func validateClientAttempt(reference AttemptReference, attempt Attempt) error {
	if err := attempt.Validate(); err != nil {
		return invalidClientResponse("attempt", err)
	}
	if attempt.AttemptReference != reference {
		return invalidClientResponse("attempt identity", nil)
	}
	return nil
}

func validateClientPutAttempt(
	reference AttemptReference,
	request PutAttemptRequest,
	attempt Attempt,
) error {
	if err := validateClientAttempt(reference, attempt); err != nil {
		return err
	}
	providerMismatch := request.Mode == AttemptModeResume &&
		attempt.ProviderSessionID != request.ProviderSessionID
	if attempt.Mode != request.Mode || attempt.Assignment != request.Assignment || providerMismatch {
		return invalidClientResponse("attempt request identity", nil)
	}
	return nil
}

func invalidClientResponse(part string, cause error) error {
	if cause == nil {
		return fmt.Errorf("%w: %s is invalid", ErrInvalidResponse, part)
	}
	return fmt.Errorf("%w: %s is invalid: %v", ErrInvalidResponse, part, cause)
}

func validateClientBaseURL(value string) (string, error) {
	parsed, err := url.Parse(value)
	if err != nil {
		return "", fmt.Errorf("%w: parse base URL: %v", ErrInvalidClientConfig, err)
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return "", fmt.Errorf("%w: base URL must use http or https and include a host", ErrInvalidClientConfig)
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return "", fmt.Errorf("%w: base URL cannot contain user information, a query, or a fragment", ErrInvalidClientConfig)
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return "", fmt.Errorf("%w: base URL cannot contain a path", ErrInvalidClientConfig)
	}
	parsed.Path = ""
	return strings.TrimSuffix(parsed.String(), "/"), nil
}
