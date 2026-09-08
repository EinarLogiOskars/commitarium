package workerhttp

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
)

const MaxRequestBodyBytes int64 = 128 * 1024

func (server *Server) decodeRequest(w http.ResponseWriter, r *http.Request, destination any) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		server.writeProtocolError(w, http.StatusUnsupportedMediaType, ProtocolError{
			Code:    ErrorUnsupportedMediaType,
			Message: "Content-Type must be application/json",
		})
		return false
	}

	r.Body = http.MaxBytesReader(w, r.Body, MaxRequestBodyBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		server.writeDecodeError(w, err)
		return false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			err = errors.New("request body contains multiple JSON values")
		}
		server.writeDecodeError(w, err)
		return false
	}
	return true
}

func (server *Server) writeDecodeError(w http.ResponseWriter, err error) {
	var maxBytesError *http.MaxBytesError
	if errors.As(err, &maxBytesError) {
		server.writeProtocolError(w, http.StatusRequestEntityTooLarge, ProtocolError{
			Code:    ErrorRequestTooLarge,
			Message: "request body exceeds the worker API limit",
		})
		return
	}
	server.writeProtocolError(w, http.StatusBadRequest, ProtocolError{
		Code:    ErrorInvalidRequest,
		Message: "request body must contain exactly one valid JSON object with no unknown fields",
	})
}

func (server *Server) writeServiceError(w http.ResponseWriter, err error) {
	var serviceError *ServiceError
	if errors.As(err, &serviceError) && serviceError != nil && serviceError.ProtocolError.Validate() == nil {
		if serviceError.ProtocolError.Code == ErrorUnauthorized {
			w.Header().Set("WWW-Authenticate", "Bearer")
		}
		server.writeProtocolError(
			w,
			statusForErrorCode(serviceError.ProtocolError.Code),
			serviceError.ProtocolError,
		)
		return
	}
	server.writeProtocolError(w, http.StatusInternalServerError, ProtocolError{
		Code:      ErrorInternal,
		Message:   "worker service failed unexpectedly",
		Retryable: true,
	})
}

func (server *Server) writeProtocolError(w http.ResponseWriter, status int, protocolError ProtocolError) {
	server.writeResponse(w, status, ErrorResponse{Error: protocolError})
}

func (server *Server) writeResponse(w http.ResponseWriter, status int, value any) {
	payload, err := json.Marshal(value)
	if err != nil {
		fallback := []byte(`{"error":{"code":"internal_error","message":"worker response could not be encoded","retryable":true}}`)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write(append(fallback, '\n'))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(append(payload, '\n'))
}

func statusForErrorCode(code ErrorCode) int {
	switch code {
	case ErrorInvalidRequest:
		return http.StatusBadRequest
	case ErrorUnauthorized:
		return http.StatusUnauthorized
	case ErrorNotFound:
		return http.StatusNotFound
	case ErrorMethodNotAllowed:
		return http.StatusMethodNotAllowed
	case ErrorUnsupportedMediaType:
		return http.StatusUnsupportedMediaType
	case ErrorRequestTooLarge:
		return http.StatusRequestEntityTooLarge
	case ErrorUnsupportedOperation:
		return http.StatusUnprocessableEntity
	case ErrorAttemptActive,
		ErrorAttemptConflict,
		ErrorStaleAttempt,
		ErrorProviderSessionMissing,
		ErrorConfigurationMismatch,
		ErrorIndeterminateState:
		return http.StatusConflict
	case ErrorProfileUnavailable, ErrorWorkspaceUnavailable:
		return http.StatusServiceUnavailable
	case ErrorRedactionFailed, ErrorInvalidEventStream, ErrorInternal:
		return http.StatusInternalServerError
	default:
		return http.StatusInternalServerError
	}
}
