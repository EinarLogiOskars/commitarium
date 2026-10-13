package marketplace

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type fakeMarketplace struct {
	users     map[string]fakeUser
	tokens    map[string]string
	scopes    []string
	activated []string
}

type fakeUser struct {
	password string
	admin    bool
	active   bool
}

func (market *fakeMarketplace) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	if r.URL.Path == signupPath {
		var request map[string]string
		_ = json.Unmarshal(body, &request)
		if _, taken := market.users[request["username"]]; taken {
			w.WriteHeader(http.StatusConflict)
			_, _ = io.WriteString(w, `{"error":{"code":"username_taken","message":"that username is taken"}}`)
			return
		}
		market.users[request["username"]] = fakeUser{password: request["password"]}
		w.WriteHeader(http.StatusAccepted)
		return
	}
	login := ""
	if username, password, ok := r.BasicAuth(); ok {
		user, exists := market.users[username]
		if !exists || user.password != password {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"message":"user's password is invalid"}`)
			return
		}
		if !user.active {
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"message":"user is not activated"}`)
			return
		}
		login = username
	} else if token := r.Header.Get("Authorization"); token != "" {
		login = market.tokens[token[len("token "):]]
	}
	if login == "" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/user":
		_ = json.NewEncoder(w).Encode(map[string]any{"login": login, "is_admin": market.users[login].admin})
	case r.Method == http.MethodPost && r.URL.Path == "/api/v1/users/"+login+"/tokens":
		var request struct {
			Scopes []string `json:"scopes"`
		}
		_ = json.Unmarshal(body, &request)
		market.scopes = request.Scopes
		market.tokens["token-"+login] = login
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{"sha1": "token-" + login})
	case !market.users[login].admin:
		w.WriteHeader(http.StatusForbidden)
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/admin/users":
		users := []map[string]any{}
		for name, user := range market.users {
			users = append(users, map[string]any{"login": name, "email": name + "@example.test", "active": user.active,
				"created": time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)})
		}
		_ = json.NewEncoder(w).Encode(users)
	case r.Method == http.MethodPatch:
		name := r.URL.Path[len("/api/v1/admin/users/"):]
		user := market.users[name]
		user.active = true
		market.users[name] = user
		market.activated = append(market.activated, name)
	}
}

func TestSigningUpWaitingAndApproval(t *testing.T) {
	market := &fakeMarketplace{users: map[string]fakeUser{"admin": {password: "admin-pass", admin: true, active: true}},
		tokens: map[string]string{}}
	server := httptest.NewServer(market)
	defer server.Close()
	root := t.TempDir()
	service, err := NewService(server.URL, root, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}

	if err := service.SignUp(t.Context(), "sigga", "sigga@example.test", "long enough"); err != nil {
		t.Fatalf("sign up: %v", err)
	}
	var taken *SignupError
	if err := service.SignUp(t.Context(), "sigga", "sigga@example.test", "long enough"); !errors.As(err, &taken) ||
		taken.Code != "username_taken" {
		t.Fatalf("second sign up error=%v", err)
	}
	if _, err := service.SignIn(t.Context(), "sigga", "long enough"); !errors.Is(err, ErrPendingApproval) {
		t.Fatalf("pending sign in error=%v", err)
	}
	if _, err := service.SignIn(t.Context(), "sigga", "wrong"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("wrong password error=%v", err)
	}
	if _, err := service.PendingUsers(t.Context()); !errors.Is(err, ErrNotSignedIn) {
		t.Fatalf("signed-out admin call error=%v", err)
	}

	status, err := service.SignIn(t.Context(), "admin", "admin-pass")
	if err != nil || status.Account == nil || !status.Account.Admin || len(market.scopes) != 3 {
		t.Fatalf("admin sign in status=%+v scopes=%v err=%v", status, market.scopes, err)
	}
	info, err := os.Stat(filepath.Join(root, credentialFile))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("stored token mode=%v err=%v", info, err)
	}
	pending, err := service.PendingUsers(t.Context())
	if err != nil || len(pending) != 1 || pending[0].Username != "sigga" {
		t.Fatalf("pending=%+v err=%v", pending, err)
	}
	if err := service.Approve(t.Context(), "sigga"); err != nil || len(market.activated) != 1 {
		t.Fatalf("approve err=%v activated=%v", err, market.activated)
	}

	status, err = service.SignIn(t.Context(), "sigga", "long enough")
	if err != nil || status.Account.Username != "sigga" || status.Account.Admin || len(market.scopes) != 2 {
		t.Fatalf("approved sign in status=%+v scopes=%v err=%v", status, market.scopes, err)
	}
	credentials, err := service.Credentials(t.Context())
	if err != nil || credentials.Token != "token-sigga" || credentials.URL != server.URL {
		t.Fatalf("credentials=%+v err=%v", credentials, err)
	}
	if _, err := service.PendingUsers(t.Context()); !errors.Is(err, ErrNotAdmin) {
		t.Fatalf("non-admin pending error=%v", err)
	}

	if err := service.SignOut(t.Context()); err != nil {
		t.Fatalf("sign out: %v", err)
	}
	if status, err := service.Status(t.Context()); err != nil || status.Account != nil {
		t.Fatalf("signed-out status=%+v err=%v", status, err)
	}
}

func TestASignInForAnotherMarketplaceIsIgnored(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, credentialFile),
		[]byte(`{"url":"https://elsewhere.example","username":"a","token":"t"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	service, err := NewService("https://market.example", root, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if status, err := service.Status(t.Context()); err != nil || status.Account != nil {
		t.Fatalf("status=%+v err=%v", status, err)
	}
}
