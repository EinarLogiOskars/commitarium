package workerhttp

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode"
)

const IdempotencyKeyHeader = "Idempotency-Key"

var ErrInvalidServerConfig = errors.New("invalid worker HTTP server configuration")

// Service is the process-control boundary behind the worker HTTP API. The HTTP
// layer only authenticates, validates, and translates requests; a later
// supervisor implementation will own process and journal behavior.
type Service interface {
	PutAttempt(
		ctx context.Context,
		identity MutationIdentity,
		request PutAttemptRequest,
	) (attempt Attempt, created bool, err error)
	GetAttempt(ctx context.Context, reference AttemptReference) (Attempt, error)
	SendCommand(
		ctx context.Context,
		identity MutationIdentity,
		request CommandRequest,
	) (Attempt, error)
	ForceStop(
		ctx context.Context,
		identity MutationIdentity,
		request ForceStopRequest,
	) (Attempt, error)
}

type ServerConfig struct {
	BearerToken             string
	Provider                Provider
	Capabilities            []Capability
	MaxConcurrentAttempts   int
	EventSource             EventSource
	EventStreamHeartbeat    time.Duration
	EventStreamWriteTimeout time.Duration
}

// ServiceError lets process-control code select a stable protocol error
// without making the HTTP layer aware of provider-specific errors.
type ServiceError struct {
	ProtocolError ProtocolError
	Cause         error
}

func NewServiceError(
	code ErrorCode,
	message string,
	retryable bool,
	cause error,
) *ServiceError {
	return &ServiceError{
		ProtocolError: ProtocolError{
			Code:      code,
			Message:   message,
			Retryable: retryable,
		},
		Cause: cause,
	}
}

func (serviceError *ServiceError) Error() string {
	if serviceError == nil {
		return "<nil>"
	}
	return serviceError.ProtocolError.Message
}

func (serviceError *ServiceError) Unwrap() error {
	if serviceError == nil {
		return nil
	}
	return serviceError.Cause
}

type Server struct {
	service      Service
	eventSource  EventSource
	capabilities CapabilitiesResponse
	supported    map[Capability]struct{}
	tokenDigest  [sha256.Size]byte
	heartbeat    time.Duration
	writeTimeout time.Duration
	mux          *http.ServeMux
}

func NewServer(config ServerConfig, service Service) (http.Handler, error) {
	if service == nil {
		return nil, fmt.Errorf("%w: service is required", ErrInvalidServerConfig)
	}
	if config.BearerToken == "" || strings.IndexFunc(config.BearerToken, unicode.IsSpace) >= 0 {
		return nil, fmt.Errorf("%w: bearer token is required and cannot contain whitespace", ErrInvalidServerConfig)
	}

	capabilities := CapabilitiesResponse{
		ProtocolVersion:       ProtocolVersion,
		Provider:              config.Provider,
		Capabilities:          append([]Capability(nil), config.Capabilities...),
		MaxConcurrentAttempts: config.MaxConcurrentAttempts,
	}
	if err := capabilities.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidServerConfig, err)
	}

	supported := make(map[Capability]struct{}, len(capabilities.Capabilities))
	for _, capability := range capabilities.Capabilities {
		supported[capability] = struct{}{}
	}
	heartbeat, writeTimeout, err := validateEventStreamConfig(config, supported)
	if err != nil {
		return nil, err
	}
	server := &Server{
		service:      service,
		eventSource:  config.EventSource,
		capabilities: capabilities,
		supported:    supported,
		tokenDigest:  sha256.Sum256([]byte(config.BearerToken)),
		heartbeat:    heartbeat,
		writeTimeout: writeTimeout,
		mux:          http.NewServeMux(),
	}
	server.routes()
	return server, nil
}

func (server *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	server.mux.ServeHTTP(w, r)
}

func (server *Server) routes() {
	server.mux.HandleFunc(APIBasePath+"/health", server.requireMethod(http.MethodGet, server.health))
	server.mux.HandleFunc(APIBasePath+"/capabilities", server.authenticate(server.requireMethod(http.MethodGet, server.getCapabilities)))
	server.mux.HandleFunc(
		APIBasePath+"/sessions/{sessionID}/attempts/{attemptID}",
		server.authenticate(server.attempt),
	)
	server.mux.HandleFunc(
		APIBasePath+"/sessions/{sessionID}/attempts/{attemptID}/commands",
		server.authenticate(server.requireMethod(http.MethodPost, server.sendCommand)),
	)
	server.mux.HandleFunc(
		APIBasePath+"/sessions/{sessionID}/attempts/{attemptID}/force-stop",
		server.authenticate(server.requireMethod(http.MethodPost, server.forceStop)),
	)
	server.mux.HandleFunc(
		APIBasePath+"/sessions/{sessionID}/attempts/{attemptID}/events/stream",
		server.authenticate(server.requireMethod(http.MethodGet, server.streamEvents)),
	)
	server.mux.HandleFunc("/", server.notFound)
}

func (server *Server) health(w http.ResponseWriter, _ *http.Request) {
	server.writeResponse(w, http.StatusOK, HealthResponse{
		Status:          HealthStatusOK,
		ProtocolVersion: ProtocolVersion,
	})
}

func (server *Server) getCapabilities(w http.ResponseWriter, _ *http.Request) {
	server.writeResponse(w, http.StatusOK, server.capabilities)
}

func (server *Server) attempt(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		server.getAttempt(w, r)
	case http.MethodPut:
		server.putAttempt(w, r)
	default:
		server.writeMethodNotAllowed(w, http.MethodGet, http.MethodPut)
	}
}

func (server *Server) getAttempt(w http.ResponseWriter, r *http.Request) {
	reference, ok := server.readReference(w, r)
	if !ok {
		return
	}
	attempt, err := server.service.GetAttempt(r.Context(), reference)
	if err != nil {
		server.writeServiceError(w, err)
		return
	}
	if !server.validateAttemptResponse(w, reference, attempt) {
		return
	}
	server.writeResponse(w, http.StatusOK, attempt)
}

func (server *Server) putAttempt(w http.ResponseWriter, r *http.Request) {
	identity, ok := server.readMutationIdentity(w, r)
	if !ok {
		return
	}
	var request PutAttemptRequest
	if !server.decodeRequest(w, r, &request) {
		return
	}
	if err := request.Validate(identity); err != nil {
		server.writeProtocolError(w, http.StatusBadRequest, ProtocolError{
			Code:    ErrorInvalidRequest,
			Message: err.Error(),
		})
		return
	}
	capability := CapabilityStart
	if request.Mode == AttemptModeResume {
		capability = CapabilityResume
	}
	if !server.requireCapability(w, capability) {
		return
	}

	attempt, created, err := server.service.PutAttempt(r.Context(), identity, request)
	if err != nil {
		server.writeServiceError(w, err)
		return
	}
	if !server.validatePutAttemptResponse(w, identity.AttemptReference, request, attempt) {
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
		w.Header().Set("Location", attemptPath(identity.AttemptReference))
	}
	server.writeResponse(w, status, attempt)
}

func (server *Server) sendCommand(w http.ResponseWriter, r *http.Request) {
	identity, ok := server.readMutationIdentity(w, r)
	if !ok {
		return
	}
	var request CommandRequest
	if !server.decodeRequest(w, r, &request) {
		return
	}
	if err := request.Validate(identity); err != nil {
		server.writeProtocolError(w, http.StatusBadRequest, ProtocolError{
			Code:    ErrorInvalidRequest,
			Message: err.Error(),
		})
		return
	}
	if !server.requireCapability(w, capabilityForCommand(request.Type)) {
		return
	}

	attempt, err := server.service.SendCommand(r.Context(), identity, request)
	if err != nil {
		server.writeServiceError(w, err)
		return
	}
	if !server.validateAttemptResponse(w, identity.AttemptReference, attempt) {
		return
	}
	server.writeResponse(w, http.StatusAccepted, attempt)
}

func (server *Server) forceStop(w http.ResponseWriter, r *http.Request) {
	identity, ok := server.readMutationIdentity(w, r)
	if !ok {
		return
	}
	var request ForceStopRequest
	if !server.decodeRequest(w, r, &request) {
		return
	}
	if err := request.Validate(identity); err != nil {
		server.writeProtocolError(w, http.StatusBadRequest, ProtocolError{
			Code:    ErrorInvalidRequest,
			Message: err.Error(),
		})
		return
	}
	if !server.requireCapability(w, CapabilityForceStop) {
		return
	}

	attempt, err := server.service.ForceStop(r.Context(), identity, request)
	if err != nil {
		server.writeServiceError(w, err)
		return
	}
	if !server.validateAttemptResponse(w, identity.AttemptReference, attempt) {
		return
	}
	server.writeResponse(w, http.StatusOK, attempt)
}

func (server *Server) readReference(w http.ResponseWriter, r *http.Request) (AttemptReference, bool) {
	reference := AttemptReference{
		SessionID: r.PathValue("sessionID"),
		AttemptID: r.PathValue("attemptID"),
	}
	if err := reference.Validate(); err != nil {
		server.writeProtocolError(w, http.StatusBadRequest, ProtocolError{
			Code:    ErrorInvalidRequest,
			Message: err.Error(),
		})
		return AttemptReference{}, false
	}
	return reference, true
}

func (server *Server) readMutationIdentity(w http.ResponseWriter, r *http.Request) (MutationIdentity, bool) {
	reference, ok := server.readReference(w, r)
	if !ok {
		return MutationIdentity{}, false
	}
	values := r.Header.Values(IdempotencyKeyHeader)
	if len(values) != 1 {
		server.writeProtocolError(w, http.StatusBadRequest, ProtocolError{
			Code:    ErrorInvalidRequest,
			Message: IdempotencyKeyHeader + " header must be provided exactly once",
		})
		return MutationIdentity{}, false
	}
	identity := MutationIdentity{
		AttemptReference: reference,
		IdempotencyKey:   values[0],
	}
	if err := identity.Validate(); err != nil {
		server.writeProtocolError(w, http.StatusBadRequest, ProtocolError{
			Code:    ErrorInvalidRequest,
			Message: err.Error(),
		})
		return MutationIdentity{}, false
	}
	return identity, true
}

func (server *Server) validateAttemptResponse(
	w http.ResponseWriter,
	want AttemptReference,
	attempt Attempt,
) bool {
	if err := attempt.Validate(); err != nil || attempt.AttemptReference != want {
		server.writeProtocolError(w, http.StatusInternalServerError, ProtocolError{
			Code:      ErrorInternal,
			Message:   "worker service returned an invalid attempt",
			Retryable: true,
		})
		return false
	}
	return true
}

func (server *Server) validatePutAttemptResponse(
	w http.ResponseWriter,
	wantReference AttemptReference,
	wantRequest PutAttemptRequest,
	attempt Attempt,
) bool {
	if !server.validateAttemptResponse(w, wantReference, attempt) {
		return false
	}
	providerMismatch := wantRequest.Mode == AttemptModeResume &&
		attempt.ProviderSessionID != wantRequest.ProviderSessionID
	if attempt.Mode != wantRequest.Mode || attempt.Assignment != wantRequest.Assignment || providerMismatch {
		server.writeProtocolError(w, http.StatusInternalServerError, ProtocolError{
			Code:      ErrorInternal,
			Message:   "worker service returned an attempt for a different request",
			Retryable: true,
		})
		return false
	}
	return true
}

func (server *Server) requireCapability(w http.ResponseWriter, capability Capability) bool {
	if _, supported := server.supported[capability]; supported {
		return true
	}
	server.writeProtocolError(w, http.StatusUnprocessableEntity, ProtocolError{
		Code:    ErrorUnsupportedOperation,
		Message: fmt.Sprintf("worker does not support the %q capability", capability),
	})
	return false
}

func capabilityForCommand(command CommandType) Capability {
	switch command {
	case CommandMessage:
		return CapabilityMessage
	case CommandPause:
		return CapabilityPause
	case CommandContinue:
		return CapabilityContinue
	case CommandStop:
		return CapabilityCooperativeStop
	default:
		return ""
	}
}

func (server *Server) authenticate(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		values := r.Header.Values("Authorization")
		if len(values) != 1 || !server.validAuthorization(values[0]) {
			w.Header().Set("WWW-Authenticate", "Bearer")
			server.writeProtocolError(w, http.StatusUnauthorized, ProtocolError{
				Code:    ErrorUnauthorized,
				Message: "a valid bearer token is required",
			})
			return
		}
		next(w, r)
	}
}

func (server *Server) validAuthorization(value string) bool {
	parts := strings.Fields(value)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return false
	}
	suppliedDigest := sha256.Sum256([]byte(parts[1]))
	return subtle.ConstantTimeCompare(server.tokenDigest[:], suppliedDigest[:]) == 1
}

func (server *Server) requireMethod(method string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != method {
			server.writeMethodNotAllowed(w, method)
			return
		}
		next(w, r)
	}
}

func (server *Server) writeMethodNotAllowed(w http.ResponseWriter, methods ...string) {
	w.Header().Set("Allow", strings.Join(methods, ", "))
	server.writeProtocolError(w, http.StatusMethodNotAllowed, ProtocolError{
		Code:    ErrorMethodNotAllowed,
		Message: "HTTP method is not allowed for this endpoint",
	})
}

func (server *Server) notFound(w http.ResponseWriter, _ *http.Request) {
	server.writeProtocolError(w, http.StatusNotFound, ProtocolError{
		Code:    ErrorNotFound,
		Message: "worker API endpoint was not found",
	})
}

func attemptPath(reference AttemptReference) string {
	return APIBasePath + "/sessions/" + reference.SessionID + "/attempts/" + reference.AttemptID
}
