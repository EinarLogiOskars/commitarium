package httpapi

import (
	"encoding/json"
	"log"
	"net/http"
)

type apiError struct {
	Code     string `json:"code"`
	Message  string `json:"message"`
	MaxBytes int64  `json:"max_bytes,omitempty"`
}

func writeContentTooLargeError(w http.ResponseWriter, maxBytes int64) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusRequestEntityTooLarge)
	response := errorResponse{Error: apiError{
		Code:     "content_too_large",
		Message:  "repository README exceeds the size limit",
		MaxBytes: maxBytes,
	}}
	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Printf("encode error response: %v", err)
	}
}

type errorResponse struct {
	Error apiError `json:"error"`
}

func writeError(
	w http.ResponseWriter,
	status int,
	code string,
	message string,
) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	response := errorResponse{
		Error: apiError{
			Code:    code,
			Message: message,
		},
	}

	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Printf("encode error response: %v", err)
	}
}
