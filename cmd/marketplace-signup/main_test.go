package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeForgejo struct {
	calls       []string
	createCode  int
	createBody  string
	deactivated map[string]bool
	patchCode   int
}

func (forge *fakeForgejo) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	forge.calls = append(forge.calls, r.Method+" "+r.URL.Path)
	if r.Header.Get("Authorization") != "token admin-token" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/api/v1/admin/users":
		w.WriteHeader(forge.createCode)
		_, _ = io.WriteString(w, forge.createBody)
	case r.Method == http.MethodPatch:
		var edit map[string]any
		_ = json.Unmarshal(body, &edit)
		if active, ok := edit["active"].(bool); ok && !active {
			forge.deactivated[strings.TrimPrefix(r.URL.Path, "/api/v1/admin/users/")] = true
		}
		w.WriteHeader(forge.patchCode)
		_, _ = io.WriteString(w, `{}`)
	case r.Method == http.MethodDelete:
		w.WriteHeader(http.StatusNoContent)
	}
}

func newTestServer(t *testing.T, forge *fakeForgejo) (*server, *httptest.Server) {
	t.Helper()
	backend := httptest.NewServer(forge)
	t.Cleanup(backend.Close)
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("admin-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	return newServer(backend.URL, tokenFile, backend.Client(), func() time.Time { return now }), backend
}

func post(t *testing.T, handler http.Handler, address, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, signupPath, strings.NewReader(body))
	request.Header.Set("X-Forwarded-For", address)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

const validSignup = `{"username":"sigga","email":"sigga@example.test","password":"long enough"}`

func TestSignupCreatesAnInactiveAccount(t *testing.T) {
	forge := &fakeForgejo{createCode: http.StatusCreated, createBody: `{}`, patchCode: http.StatusOK, deactivated: map[string]bool{}}
	s, _ := newTestServer(t, forge)
	response := post(t, s.routes(), "203.0.113.1", validSignup)
	if response.Code != http.StatusAccepted || !strings.Contains(response.Body.String(), "pending_approval") {
		t.Fatalf("signup = %d %s", response.Code, response.Body)
	}
	if !forge.deactivated["sigga"] {
		t.Fatalf("account was not deactivated: calls %v", forge.calls)
	}
}

func TestSignupRemovesAnAccountItCouldNotDeactivate(t *testing.T) {
	forge := &fakeForgejo{createCode: http.StatusCreated, createBody: `{}`, patchCode: http.StatusInternalServerError, deactivated: map[string]bool{}}
	s, _ := newTestServer(t, forge)
	response := post(t, s.routes(), "203.0.113.1", validSignup)
	if response.Code != http.StatusServiceUnavailable || forge.calls[len(forge.calls)-1] != "DELETE /api/v1/admin/users/sigga" {
		t.Fatalf("signup = %d, calls %v", response.Code, forge.calls)
	}
}

func TestSignupExplainsRejections(t *testing.T) {
	cases := map[string]string{
		`{"message":"user already exists [name: sigga]"}`: "username_taken",
		`{"message":"e-mail already in use [email: x]"}`:  "email_taken",
	}
	for body, code := range cases {
		forge := &fakeForgejo{createCode: http.StatusUnprocessableEntity, createBody: body, deactivated: map[string]bool{}}
		s, _ := newTestServer(t, forge)
		response := post(t, s.routes(), "203.0.113.1", validSignup)
		if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), code) {
			t.Fatalf("%s: signup = %d %s", body, response.Code, response.Body)
		}
	}
	forge := &fakeForgejo{deactivated: map[string]bool{}}
	s, _ := newTestServer(t, forge)
	for body, code := range map[string]string{
		`{"username":"a","email":"a@b.c","password":"long enough"}`:          "invalid_username",
		`{"username":"sigga","email":"nope","password":"long enough"}`:       "invalid_email",
		`{"username":"sigga","email":"s@example.test","password":"short"}`:   "invalid_password",
		`{"username":"sigga","email":"s@example.test","password":"x","x":1}`: "invalid_request",
	} {
		if response := post(t, s.routes(), "203.0.113.1", body); response.Code != http.StatusBadRequest ||
			!strings.Contains(response.Body.String(), code) {
			t.Fatalf("%s: signup = %d %s", body, response.Code, response.Body)
		}
	}
	if len(forge.calls) != 0 {
		t.Fatalf("invalid requests reached Forgejo: %v", forge.calls)
	}
}

func TestSignupIsRateLimitedPerAddress(t *testing.T) {
	forge := &fakeForgejo{createCode: http.StatusCreated, createBody: `{}`, patchCode: http.StatusOK, deactivated: map[string]bool{}}
	s, _ := newTestServer(t, forge)
	for range signupsPerIP {
		if response := post(t, s.routes(), "203.0.113.1", validSignup); response.Code != http.StatusAccepted {
			t.Fatalf("signup = %d", response.Code)
		}
	}
	if response := post(t, s.routes(), "203.0.113.1", validSignup); response.Code != http.StatusTooManyRequests {
		t.Fatalf("over the limit = %d", response.Code)
	}
	if response := post(t, s.routes(), "203.0.113.2", validSignup); response.Code != http.StatusAccepted {
		t.Fatalf("another address = %d", response.Code)
	}
}
