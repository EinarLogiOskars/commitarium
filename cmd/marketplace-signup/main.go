// Command marketplace-signup runs next to the marketplace's Forgejo and lets
// Commitarium create accounts without showing Forgejo's pages. Forgejo has no
// public sign-up API, so this service holds an admin token and creates each
// account inactive; the marketplace admin activates it. Every other
// marketplace call goes to Forgejo's own API.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	signupPath     = "/commitarium/v1/signup"
	healthPath     = "/commitarium/v1/health"
	maxRequestBody = 4 * 1024
	maxForgejoBody = 64 * 1024
	signupsPerIP   = 5
	signupsTotal   = 50
	signupWindow   = time.Hour
)

var (
	usernamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{1,38}[A-Za-z0-9]$`)
	emailPattern    = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)
)

func main() {
	if err := run(); err != nil {
		log.Fatalf("marketplace-signup: %v", err)
	}
}

func run() error {
	forgejoURL := strings.TrimRight(strings.TrimSpace(os.Getenv("FORGEJO_URL")), "/")
	if forgejoURL == "" {
		forgejoURL = "http://forgejo:3000"
	}
	tokenFile := strings.TrimSpace(os.Getenv("FORGEJO_ADMIN_TOKEN_FILE"))
	if tokenFile == "" {
		return errors.New("FORGEJO_ADMIN_TOKEN_FILE is required")
	}
	if _, err := readToken(tokenFile); err != nil {
		return err
	}
	address := strings.TrimSpace(os.Getenv("LISTEN_ADDRESS"))
	if address == "" {
		address = ":8090"
	}
	server := newServer(forgejoURL, tokenFile, &http.Client{Timeout: 15 * time.Second}, time.Now)
	log.Printf("listening on %s", address)
	return (&http.Server{Addr: address, Handler: server.routes(), ReadHeaderTimeout: 10 * time.Second}).ListenAndServe()
}

func readToken(path string) (string, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read the Forgejo admin token: %w", err)
	}
	token := strings.TrimSpace(string(contents))
	if token == "" || strings.ContainsAny(token, " \t\r\n") {
		return "", errors.New("the Forgejo admin token file must hold one token")
	}
	return token, nil
}

type server struct {
	forgejoURL string
	tokenFile  string
	client     *http.Client
	now        func() time.Time

	mu      sync.Mutex
	recent  []time.Time
	perAddr map[string][]time.Time
}

func newServer(forgejoURL, tokenFile string, client *http.Client, now func() time.Time) *server {
	return &server{forgejoURL: forgejoURL, tokenFile: tokenFile, client: client, now: now,
		perAddr: map[string][]time.Time{}}
}

func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+healthPath, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("POST "+signupPath, s.signup)
	return mux
}

type signupRequest struct {
	Username string `json:"username"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (s *server) signup(w http.ResponseWriter, r *http.Request) {
	var request signupRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBody))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "send username, email, and password as JSON")
		return
	}
	request.Username, request.Email = strings.TrimSpace(request.Username), strings.TrimSpace(request.Email)
	switch {
	case !usernamePattern.MatchString(request.Username):
		writeError(w, http.StatusBadRequest, "invalid_username",
			"a username is 3 to 40 letters, digits, dots, dashes or underscores")
		return
	case !emailPattern.MatchString(request.Email) || len(request.Email) > 254:
		writeError(w, http.StatusBadRequest, "invalid_email", "enter a valid email address")
		return
	case len(request.Password) < 8 || len(request.Password) > 255:
		writeError(w, http.StatusBadRequest, "invalid_password", "a password is at least 8 characters")
		return
	}
	if !s.allow(clientAddress(r)) {
		writeError(w, http.StatusTooManyRequests, "too_many_signups", "too many accounts were requested; try again later")
		return
	}
	token, err := readToken(s.tokenFile)
	if err != nil {
		log.Printf("signup: %v", err)
		writeError(w, http.StatusServiceUnavailable, "signup_unavailable", "sign-up is unavailable right now")
		return
	}

	status, body, err := s.forgejo(r, token, http.MethodPost, "/api/v1/admin/users", map[string]any{
		"username": request.Username, "login_name": request.Username, "email": request.Email,
		"password": request.Password, "must_change_password": false, "send_notify": false,
		"source_id": 0, "visibility": "public",
	})
	if err != nil {
		log.Printf("signup %q: create: %v", request.Username, err)
		writeError(w, http.StatusServiceUnavailable, "signup_unavailable", "sign-up is unavailable right now")
		return
	}
	if status != http.StatusCreated {
		writeForgejoRejection(w, request.Username, status, body)
		return
	}
	// Forgejo creates admin-made accounts active; the request waits for the
	// marketplace admin instead.
	status, body, err = s.forgejo(r, token, http.MethodPatch, "/api/v1/admin/users/"+url.PathEscape(request.Username),
		map[string]any{"login_name": request.Username, "source_id": 0, "active": false})
	if err != nil || status != http.StatusOK {
		log.Printf("signup %q: deactivate: status %d err %v body %s", request.Username, status, err, body)
		// Never leave an account usable without approval.
		if _, _, deleteErr := s.forgejo(r, token, http.MethodDelete,
			"/api/v1/admin/users/"+url.PathEscape(request.Username), nil); deleteErr != nil {
			log.Printf("signup %q: remove after failed deactivation: %v", request.Username, deleteErr)
		}
		writeError(w, http.StatusServiceUnavailable, "signup_unavailable", "sign-up is unavailable right now")
		return
	}
	log.Printf("signup %q: waiting for approval", request.Username)
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "pending_approval", "username": request.Username})
}

func writeForgejoRejection(w http.ResponseWriter, username string, status int, body []byte) {
	var rejection struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(body, &rejection)
	message := strings.ToLower(rejection.Message)
	switch {
	case status == http.StatusUnprocessableEntity && strings.Contains(message, "e-mail"),
		status == http.StatusUnprocessableEntity && strings.Contains(message, "email"):
		writeError(w, http.StatusConflict, "email_taken", "that email address already has an account")
	case status == http.StatusUnprocessableEntity && strings.Contains(message, "exist"):
		writeError(w, http.StatusConflict, "username_taken", "that username is taken")
	case status == http.StatusUnprocessableEntity || status == http.StatusBadRequest:
		writeError(w, http.StatusBadRequest, "invalid_request", "the account details were not accepted: "+rejection.Message)
	default:
		log.Printf("signup %q: create returned HTTP %d: %s", username, status, body)
		writeError(w, http.StatusServiceUnavailable, "signup_unavailable", "sign-up is unavailable right now")
	}
}

func (s *server) forgejo(r *http.Request, token, method, path string, payload any) (int, []byte, error) {
	var reader io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return 0, nil, err
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(r.Context(), method, s.forgejoURL+path, reader)
	if err != nil {
		return 0, nil, err
	}
	request.Header.Set("Authorization", "token "+token)
	request.Header.Set("Accept", "application/json")
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := s.client.Do(request)
	if err != nil {
		return 0, nil, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxForgejoBody))
	return response.StatusCode, body, err
}

// allow rate-limits sign-ups per client address and overall. Approval is the
// real gate; this only keeps the user list from being flooded.
func (s *server) allow(address string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	cutoff := now.Add(-signupWindow)
	s.recent = within(s.recent, cutoff)
	for key, times := range s.perAddr {
		if kept := within(times, cutoff); len(kept) > 0 {
			s.perAddr[key] = kept
		} else {
			delete(s.perAddr, key)
		}
	}
	if len(s.recent) >= signupsTotal || len(s.perAddr[address]) >= signupsPerIP {
		return false
	}
	s.recent = append(s.recent, now)
	s.perAddr[address] = append(s.perAddr[address], now)
	return true
}

func within(times []time.Time, cutoff time.Time) []time.Time {
	kept := times[:0]
	for _, at := range times {
		if at.After(cutoff) {
			kept = append(kept, at)
		}
	}
	return kept
}

// clientAddress trusts the forwarded address Caddy sets in front of us.
func clientAddress(r *http.Request) string {
	if forwarded := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-For"), ",")[0]); forwarded != "" {
		return forwarded
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}
