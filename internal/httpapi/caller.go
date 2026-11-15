package httpapi

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
)

// Callers checks who calls the coordinator (COORDINATOR_ACCESS_PLAN.md).
// The owner token opens the whole API; the host, the desktop's main window
// and the gateway hold it, and no worker does. A run token, derived from the
// owner token for one project and work order, opens only what
// commitarium-artifact needs for that work order. Everything else, except
// GET /health, is refused with 401.
type Callers struct {
	owner []byte
}

// minOwnerToken keeps a test or a broken file from passing as a secret.
const minOwnerToken = 32

// NewCallers takes the owner token, as read from its file.
func NewCallers(ownerToken string) (Callers, error) {
	ownerToken = strings.TrimSpace(ownerToken)
	if len(ownerToken) < minOwnerToken || strings.ContainsAny(ownerToken, " \t\r\n") {
		return Callers{}, errors.New("the coordinator token must be at least 32 characters with no spaces")
	}
	return Callers{owner: []byte(ownerToken)}, nil
}

// RunToken is the token a turn's agent gets for its own work order. It is
// the same for every turn of that work order, so a resumed turn keeps it.
func (callers Callers) RunToken(projectID, featureID string) string {
	mac := hmac.New(sha256.New, callers.owner)
	mac.Write([]byte("commitarium-run\x00" + projectID + "\x00" + featureID))
	return hex.EncodeToString(mac.Sum(nil))
}

// Guard wraps the coordinator's routes.
func (callers Callers) Guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/health" {
			next.ServeHTTP(w, r)
			return
		}
		token, ok := bearer(r)
		if !ok {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeError(w, http.StatusUnauthorized, "unauthorized", "this needs the coordinator's token")
			return
		}
		if subtle.ConstantTimeCompare([]byte(token), callers.owner) == 1 {
			next.ServeHTTP(w, r)
			return
		}
		projectID, featureID, artifactRoute := runRoute(r)
		if artifactRoute && subtle.ConstantTimeCompare([]byte(token), []byte(callers.RunToken(projectID, featureID))) == 1 {
			next.ServeHTTP(w, r)
			return
		}
		// One answer for every refusal, so a window holding an old token
		// asks for it again.
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeError(w, http.StatusUnauthorized, "unauthorized", "this needs the coordinator's token")
	})
}

func bearer(r *http.Request) (string, bool) {
	header := r.Header.Get("Authorization")
	token, found := strings.CutPrefix(header, "Bearer ")
	token = strings.TrimSpace(token)
	return token, found && token != ""
}

// runRoute reports whether the request is one of commitarium-artifact's
// calls, and for which project and work order:
//
//	GET  /api/v1/projects/{p}/features/{f}/artifacts/{kind}
//	POST /api/v1/projects/{p}/features/{f}/implementation-plan/steps/{s}/transitions
//	POST /api/v1/projects/{p}/features/{f}/acceptance-tests/{t}/transitions
func runRoute(r *http.Request) (string, string, bool) {
	if r.URL.RawPath != "" {
		// The mux matches the escaped path, where an encoded slash stays
		// inside one segment: such a path is never one of these routes.
		return "", "", false
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return "", "", false
		}
	}
	if len(parts) < 7 || parts[0] != "api" || parts[1] != "v1" || parts[2] != "projects" || parts[4] != "features" {
		return "", "", false
	}
	projectID, featureID, rest := parts[3], parts[5], parts[6:]
	switch {
	case r.Method == http.MethodGet && len(rest) == 2 && rest[0] == "artifacts":
	case r.Method == http.MethodPost && len(rest) == 4 && rest[0] == "implementation-plan" &&
		rest[1] == "steps" && rest[3] == "transitions":
	case r.Method == http.MethodPost && len(rest) == 3 && rest[0] == "acceptance-tests" && rest[2] == "transitions":
	default:
		return "", "", false
	}
	return projectID, featureID, true
}
