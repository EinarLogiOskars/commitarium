// Package marketplace connects Commitarium to the app marketplace
// (MARKETPLACE_PLAN.md): a Forgejo server whose API Commitarium uses, plus a
// small sign-up service next to it. The user signs in once; the coordinator
// keeps an access token outside the backed-up state.
package marketplace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	ErrNotSignedIn        = errors.New("not signed in to the marketplace")
	ErrInvalidCredentials = errors.New("the marketplace username or password is wrong")
	ErrPendingApproval    = errors.New("the marketplace account is waiting for approval")
	ErrNotAdmin           = errors.New("only the marketplace admin can do this")
	ErrUnavailable        = errors.New("the marketplace is unavailable")
	ErrUserNotFound       = errors.New("marketplace user not found")
	ErrAppNotFound        = errors.New("marketplace app not found")
)

// SignupError is the sign-up service's own answer, such as a taken username.
type SignupError struct {
	Status  int
	Code    string
	Message string
}

func (err *SignupError) Error() string { return err.Message }

const (
	signupPath     = "/commitarium/v1/signup"
	maxResponse    = 1 << 20
	credentialFile = "marketplace.json"
)

// Account is who Commitarium is signed in as.
type Account struct {
	Username string `json:"username"`
	Admin    bool   `json:"admin"`
}

// Status is the marketplace's address and the signed-in account, if any.
type Status struct {
	URL     string   `json:"url"`
	Account *Account `json:"account"`
}

// PendingUser is an account waiting for the admin's approval.
type PendingUser struct {
	Username    string    `json:"username"`
	Email       string    `json:"email"`
	RequestedAt time.Time `json:"requested_at"`
}

// Credentials are what releasing needs to push as the signed-in user.
type Credentials struct {
	URL      string
	Username string
	Token    string
}

type storedCredentials struct {
	URL       string `json:"url"`
	Username  string `json:"username"`
	Admin     bool   `json:"admin"`
	Token     string `json:"token"`
	TokenName string `json:"token_name"`
}

type Service struct {
	baseURL string
	path    string
	client  *http.Client
	now     func() time.Time
	mu      sync.Mutex
}

// NewService talks to the marketplace at baseURL and keeps the token in
// credentialsRoot, a directory outside backups.
func NewService(baseURL, credentialsRoot string, timeout time.Duration) (*Service, error) {
	parsed, err := url.Parse(strings.TrimRight(strings.TrimSpace(baseURL), "/"))
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("marketplace URL must be an absolute http(s) URL")
	}
	if strings.TrimSpace(credentialsRoot) == "" {
		return nil, fmt.Errorf("marketplace credentials root is required")
	}
	if err := os.MkdirAll(credentialsRoot, 0o700); err != nil {
		return nil, fmt.Errorf("create marketplace credentials root: %w", err)
	}
	client := &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	return &Service{baseURL: parsed.String(), path: filepath.Join(credentialsRoot, credentialFile), client: client,
		now: func() time.Time { return time.Now().UTC() }}, nil
}

func (service *Service) Status(context.Context) (Status, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	status := Status{URL: service.baseURL}
	stored, err := service.read()
	if errors.Is(err, ErrNotSignedIn) {
		return status, nil
	}
	if err != nil {
		return Status{}, err
	}
	status.Account = &Account{Username: stored.Username, Admin: stored.Admin}
	return status, nil
}

// Credentials answers the signed-in user's token for pushing releases.
func (service *Service) Credentials(context.Context) (Credentials, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	stored, err := service.read()
	if err != nil {
		return Credentials{}, err
	}
	return Credentials{URL: service.baseURL, Username: stored.Username, Token: stored.Token}, nil
}

// SignUp asks for a new account; it waits for the admin's approval.
func (service *Service) SignUp(ctx context.Context, username, email, password string) error {
	status, body, err := service.call(ctx, http.MethodPost, signupPath, nil,
		map[string]string{"username": username, "email": email, "password": password})
	if err != nil {
		return err
	}
	if status == http.StatusAccepted {
		return nil
	}
	var rejection struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &rejection) == nil && rejection.Error.Code != "" && status < http.StatusInternalServerError {
		return &SignupError{Status: status, Code: rejection.Error.Code, Message: rejection.Error.Message}
	}
	return fmt.Errorf("%w: sign-up returned HTTP %d", ErrUnavailable, status)
}

// SignIn exchanges the username and password for an access token once; the
// password is not kept.
func (service *Service) SignIn(ctx context.Context, username, password string) (Status, error) {
	username = strings.TrimSpace(username)
	if username == "" || password == "" {
		return Status{}, ErrInvalidCredentials
	}
	basic := func(request *http.Request) { request.SetBasicAuth(username, password) }
	status, body, err := service.call(ctx, http.MethodGet, "/api/v1/user", basic, nil)
	if err != nil {
		return Status{}, err
	}
	if err := signInRejection(status, body); err != nil {
		return Status{}, err
	}
	var user struct {
		Login   string `json:"login"`
		IsAdmin bool   `json:"is_admin"`
	}
	if json.Unmarshal(body, &user) != nil || user.Login == "" {
		return Status{}, fmt.Errorf("%w: unreadable user", ErrUnavailable)
	}
	scopes := []string{"write:repository", "write:user"}
	if user.IsAdmin {
		scopes = append(scopes, "write:admin")
	}
	tokenName := "commitarium-" + service.now().Format("20060102-150405")
	status, body, err = service.call(ctx, http.MethodPost, "/api/v1/users/"+url.PathEscape(user.Login)+"/tokens", basic,
		map[string]any{"name": tokenName, "scopes": scopes})
	if err != nil {
		return Status{}, err
	}
	if err := signInRejection(status, body); err != nil {
		return Status{}, err
	}
	var token struct {
		SHA1 string `json:"sha1"`
	}
	if status != http.StatusCreated || json.Unmarshal(body, &token) != nil || token.SHA1 == "" {
		return Status{}, fmt.Errorf("%w: creating a token returned HTTP %d", ErrUnavailable, status)
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	if err := service.write(storedCredentials{URL: service.baseURL, Username: user.Login, Admin: user.IsAdmin,
		Token: token.SHA1, TokenName: tokenName}); err != nil {
		return Status{}, err
	}
	return Status{URL: service.baseURL, Account: &Account{Username: user.Login, Admin: user.IsAdmin}}, nil
}

// signInRejection reads Forgejo's refusal of a password. An account that is
// not activated yet gets 403 "This account is prohibited from signing in"
// (verified on Forgejo 15): it is waiting for approval.
func signInRejection(status int, body []byte) error {
	if status < 400 {
		return nil
	}
	var rejection struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(body, &rejection)
	message := strings.ToLower(rejection.Message)
	switch {
	case status == http.StatusForbidden &&
		(strings.Contains(message, "prohibited") || strings.Contains(message, "activat")):
		return ErrPendingApproval
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return ErrInvalidCredentials
	default:
		return fmt.Errorf("%w: sign-in returned HTTP %d", ErrUnavailable, status)
	}
}

// SignOut forgets the token. Forgejo only lets a password delete tokens, so
// the token stays listed in the account's settings until removed there.
func (service *Service) SignOut(context.Context) error {
	service.mu.Lock()
	defer service.mu.Unlock()
	if err := os.Remove(service.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: forget the marketplace token", ErrUnavailable)
	}
	return nil
}

// PendingUsers lists accounts waiting for approval. Admin only.
func (service *Service) PendingUsers(ctx context.Context) ([]PendingUser, error) {
	auth, err := service.adminAuth()
	if err != nil {
		return nil, err
	}
	pending := []PendingUser{}
	for page := 1; page <= 20; page++ {
		status, body, err := service.call(ctx, http.MethodGet,
			fmt.Sprintf("/api/v1/admin/users?limit=50&page=%d", page), auth, nil)
		if err != nil {
			return nil, err
		}
		if err := service.adminRejection(status); err != nil {
			return nil, err
		}
		var users []struct {
			Login   string    `json:"login"`
			Email   string    `json:"email"`
			Active  bool      `json:"active"`
			Created time.Time `json:"created"`
		}
		if json.Unmarshal(body, &users) != nil {
			return nil, fmt.Errorf("%w: unreadable user list", ErrUnavailable)
		}
		for _, user := range users {
			if !user.Active {
				pending = append(pending, PendingUser{Username: user.Login, Email: user.Email, RequestedAt: user.Created})
			}
		}
		if len(users) < 50 {
			break
		}
	}
	return pending, nil
}

// Approve activates a pending account. Admin only.
func (service *Service) Approve(ctx context.Context, username string) error {
	return service.adminUserChange(ctx, http.MethodPatch, username,
		map[string]any{"login_name": username, "source_id": 0, "active": true})
}

// Decline removes a pending account. Admin only.
func (service *Service) Decline(ctx context.Context, username string) error {
	return service.adminUserChange(ctx, http.MethodDelete, username, nil)
}

func (service *Service) adminUserChange(ctx context.Context, method, username string, payload any) error {
	auth, err := service.adminAuth()
	if err != nil {
		return err
	}
	status, _, err := service.call(ctx, method, "/api/v1/admin/users/"+url.PathEscape(strings.TrimSpace(username)), auth, payload)
	if err != nil {
		return err
	}
	if status == http.StatusNotFound {
		return ErrUserNotFound
	}
	return service.adminRejection(status)
}

func (service *Service) adminAuth() (func(*http.Request), error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	stored, err := service.read()
	if err != nil {
		return nil, err
	}
	if !stored.Admin {
		return nil, ErrNotAdmin
	}
	return func(request *http.Request) { request.Header.Set("Authorization", "token "+stored.Token) }, nil
}

func (service *Service) adminRejection(status int) error {
	switch {
	case status < 300:
		return nil
	case status == http.StatusUnauthorized:
		return ErrNotSignedIn
	case status == http.StatusForbidden:
		return ErrNotAdmin
	default:
		return fmt.Errorf("%w: admin request returned HTTP %d", ErrUnavailable, status)
	}
}

func (service *Service) call(ctx context.Context, method, path string, auth func(*http.Request), payload any) (int, []byte, error) {
	var reader io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return 0, nil, err
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, service.baseURL+path, reader)
	if err != nil {
		return 0, nil, err
	}
	request.Header.Set("Accept", "application/json")
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if auth != nil {
		auth(request)
	}
	response, err := service.client.Do(request)
	if err != nil {
		return 0, nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponse))
	if err != nil {
		return 0, nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return response.StatusCode, body, nil
}

// read answers the stored sign-in for this marketplace; one for another
// address counts as signed out. Callers hold service.mu.
func (service *Service) read() (storedCredentials, error) {
	contents, err := os.ReadFile(service.path)
	if errors.Is(err, os.ErrNotExist) {
		return storedCredentials{}, ErrNotSignedIn
	}
	if err != nil {
		return storedCredentials{}, fmt.Errorf("%w: read the marketplace token", ErrUnavailable)
	}
	var stored storedCredentials
	if json.Unmarshal(contents, &stored) != nil || stored.Token == "" || stored.Username == "" {
		return storedCredentials{}, fmt.Errorf("%w: the stored marketplace sign-in is unreadable", ErrUnavailable)
	}
	if stored.URL != service.baseURL {
		return storedCredentials{}, ErrNotSignedIn
	}
	return stored, nil
}

// write replaces the stored sign-in atomically. Callers hold service.mu.
func (service *Service) write(stored storedCredentials) error {
	encoded, err := json.Marshal(stored)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(service.path), ".marketplace-*.tmp")
	if err != nil {
		return fmt.Errorf("%w: store the marketplace token", ErrUnavailable)
	}
	defer os.Remove(temporary.Name())
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return fmt.Errorf("%w: store the marketplace token", ErrUnavailable)
	}
	if _, err := temporary.Write(encoded); err != nil {
		temporary.Close()
		return fmt.Errorf("%w: store the marketplace token", ErrUnavailable)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("%w: store the marketplace token", ErrUnavailable)
	}
	if err := os.Rename(temporary.Name(), service.path); err != nil {
		return fmt.Errorf("%w: store the marketplace token", ErrUnavailable)
	}
	return nil
}

// AppTopic marks a marketplace repository as a Commitarium app.
const AppTopic = "commitarium-app"

// AppRepository is an app's repository on the marketplace.
type AppRepository struct {
	Owner    string `json:"owner"`
	Name     string `json:"name"`
	CloneURL string `json:"clone_url"`
	WebURL   string `json:"web_url"`
}

// EnsureAppRepository answers the signed-in user's repository called name,
// creating it public and empty on the first release, and tags it as an app.
func (service *Service) EnsureAppRepository(ctx context.Context, name, description string) (AppRepository, error) {
	service.mu.Lock()
	stored, err := service.read()
	service.mu.Unlock()
	if err != nil {
		return AppRepository{}, err
	}
	auth := func(request *http.Request) { request.Header.Set("Authorization", "token "+stored.Token) }
	path := "/api/v1/repos/" + url.PathEscape(stored.Username) + "/" + url.PathEscape(name)
	status, body, err := service.call(ctx, http.MethodGet, path, auth, nil)
	if err != nil {
		return AppRepository{}, err
	}
	switch status {
	case http.StatusOK:
	case http.StatusNotFound:
		status, body, err = service.call(ctx, http.MethodPost, "/api/v1/user/repos", auth, map[string]any{
			"name": name, "description": description, "private": false, "auto_init": false, "default_branch": "main",
		})
		if err != nil {
			return AppRepository{}, err
		}
		if status != http.StatusCreated {
			return AppRepository{}, repositoryRejection("create the app's repository", status)
		}
	default:
		return AppRepository{}, repositoryRejection("read the app's repository", status)
	}
	var repository struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		CloneURL    string `json:"clone_url"`
		HTMLURL     string `json:"html_url"`
		Owner       struct {
			Login string `json:"login"`
		} `json:"owner"`
	}
	if json.Unmarshal(body, &repository) != nil || repository.CloneURL == "" {
		return AppRepository{}, fmt.Errorf("%w: unreadable repository", ErrUnavailable)
	}
	// The app's name is read from the description; one created by hand has none.
	if strings.TrimSpace(repository.Description) == "" && description != "" {
		status, _, err = service.call(ctx, http.MethodPatch,
			"/api/v1/repos/"+url.PathEscape(repository.Owner.Login)+"/"+url.PathEscape(repository.Name),
			auth, map[string]string{"description": description})
		if err != nil {
			return AppRepository{}, err
		}
		if status != http.StatusOK {
			return AppRepository{}, repositoryRejection("describe the app", status)
		}
	}
	status, _, err = service.call(ctx, http.MethodPut,
		"/api/v1/repos/"+url.PathEscape(repository.Owner.Login)+"/"+url.PathEscape(repository.Name)+"/topics/"+AppTopic,
		auth, nil)
	if err != nil {
		return AppRepository{}, err
	}
	if status != http.StatusNoContent && status != http.StatusOK {
		return AppRepository{}, repositoryRejection("mark the repository as an app", status)
	}
	return AppRepository{Owner: repository.Owner.Login, Name: repository.Name, CloneURL: repository.CloneURL,
		WebURL: repository.HTMLURL}, nil
}

func repositoryRejection(action string, status int) error {
	switch status {
	case http.StatusUnauthorized:
		return ErrNotSignedIn
	case http.StatusForbidden:
		return fmt.Errorf("%w: the marketplace refused to %s", ErrUnavailable, action)
	default:
		return fmt.Errorf("%w: %s returned HTTP %d", ErrUnavailable, action, status)
	}
}

// AppDescriptionSuffix ends the description a first release gives an app's
// repository, after the app's name.
const AppDescriptionSuffix = " — a Commitarium app"

// App is a released app on the marketplace.
type App struct {
	Owner       string    `json:"owner"`
	Name        string    `json:"name"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Version     string    `json:"version"`
	Stars       int       `json:"stars"`
	UpdatedAt   time.Time `json:"updated_at"`
	CloneURL    string    `json:"-"`
}

var versionTag = regexp.MustCompile(`^v([1-9][0-9]*)$`)

// Apps lists the marketplace's released apps: repositories with the app topic
// and at least one vN tag.
func (service *Service) Apps(ctx context.Context) ([]App, error) {
	service.mu.Lock()
	stored, err := service.read()
	service.mu.Unlock()
	if err != nil {
		return nil, err
	}
	auth := func(request *http.Request) { request.Header.Set("Authorization", "token "+stored.Token) }
	apps := []App{}
	for page := 1; page <= 10; page++ {
		status, body, err := service.call(ctx, http.MethodGet,
			fmt.Sprintf("/api/v1/repos/search?q=%s&topic=true&limit=50&page=%d", AppTopic, page), auth, nil)
		if err != nil {
			return nil, err
		}
		if status != http.StatusOK {
			return nil, repositoryRejection("list apps", status)
		}
		var found struct {
			Data []struct {
				Name        string    `json:"name"`
				Description string    `json:"description"`
				Stars       int       `json:"stars_count"`
				UpdatedAt   time.Time `json:"updated_at"`
				CloneURL    string    `json:"clone_url"`
				Empty       bool      `json:"empty"`
				Owner       struct {
					Login string `json:"login"`
				} `json:"owner"`
			} `json:"data"`
		}
		if json.Unmarshal(body, &found) != nil {
			return nil, fmt.Errorf("%w: unreadable app list", ErrUnavailable)
		}
		for _, repository := range found.Data {
			if repository.Empty {
				continue
			}
			version, err := service.latestVersion(ctx, auth, repository.Owner.Login, repository.Name)
			if err != nil {
				return nil, err
			}
			if version == "" {
				continue
			}
			title, description := strings.TrimSuffix(repository.Description, AppDescriptionSuffix), ""
			if manifest, ok := service.appManifest(ctx, auth, repository.Owner.Login, repository.Name); ok {
				title, description = manifest.Name, manifest.Description
			}
			if strings.TrimSpace(title) == "" {
				title = repository.Name
			}
			apps = append(apps, App{Owner: repository.Owner.Login, Name: repository.Name, Title: title,
				Description: description, Version: version, Stars: repository.Stars, UpdatedAt: repository.UpdatedAt,
				CloneURL: repository.CloneURL})
		}
		if len(found.Data) < 50 {
			break
		}
	}
	return apps, nil
}

// App answers one released app.
func (service *Service) App(ctx context.Context, owner, name string) (App, error) {
	apps, err := service.Apps(ctx)
	if err != nil {
		return App{}, err
	}
	for _, app := range apps {
		if strings.EqualFold(app.Owner, owner) && strings.EqualFold(app.Name, name) {
			return app, nil
		}
	}
	return App{}, ErrAppNotFound
}

// appManifest reads an app's released AppManifestPath, if it has one.
func (service *Service) appManifest(ctx context.Context, auth func(*http.Request), owner, name string) (AppManifest, bool) {
	status, body, err := service.call(ctx, http.MethodGet,
		"/api/v1/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(name)+"/raw/"+AppManifestPath+"?ref=main", auth, nil)
	if err != nil || status != http.StatusOK {
		return AppManifest{}, false
	}
	manifest, err := ParseAppManifest(body)
	return manifest, err == nil
}

func (service *Service) latestVersion(ctx context.Context, auth func(*http.Request), owner, name string) (string, error) {
	status, body, err := service.call(ctx, http.MethodGet,
		"/api/v1/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(name)+"/tags?limit=50", auth, nil)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", repositoryRejection("read an app's versions", status)
	}
	var tags []struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(body, &tags) != nil {
		return "", fmt.Errorf("%w: unreadable versions", ErrUnavailable)
	}
	highest := 0
	for _, tag := range tags {
		if match := versionTag.FindStringSubmatch(tag.Name); match != nil {
			if number, err := strconv.Atoi(match[1]); err == nil && number > highest {
				highest = number
			}
		}
	}
	if highest == 0 {
		return "", nil
	}
	return "v" + strconv.Itoa(highest), nil
}
