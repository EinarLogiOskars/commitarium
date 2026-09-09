package forgejo

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/workspace"
)

const forgejoTestCommitID = "0123456789abcdef0123456789abcdef01234567"

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestClientVerifiesCanonicalRepositoryWithoutExposingToken(t *testing.T) {
	tokenFile := writeTestToken(t, "secret-test-token")
	client, err := NewClient(ClientConfig{
		BaseURL: "http://forgejo:3000", TokenFile: tokenFile,
		RequestTimeout: time.Second,
		HTTPClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			if request.Method != http.MethodGet ||
				request.URL.String() != "http://forgejo:3000/api/v1/repos/Commitarium/Example" {
				t.Fatalf("unexpected request %s %s", request.Method, request.URL)
			}
			if request.Header.Get("Authorization") != "token secret-test-token" {
				t.Fatal("request did not use the token-file credential")
			}
			return jsonResponse(http.StatusOK, `{
				"name":"example","default_branch":"main","empty":false,"archived":false,
				"owner":{"login":"commitarium"},"ignored":"safe to ignore"
			}`), nil
		})},
	})
	if err != nil {
		t.Fatalf("create client: %v", err)
	}

	repository, err := client.VerifyRepository(t.Context(), "Commitarium", "Example")
	if err != nil {
		t.Fatalf("verify repository: %v", err)
	}
	if repository.Owner != "commitarium" || repository.Name != "example" ||
		repository.DefaultBranch != "main" || !repository.BoundAt.IsZero() {
		t.Fatalf("unexpected verified repository %+v", repository)
	}
}

func TestClientMapsRepositoryVerificationFailures(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		token  bool
		want   error
	}{
		{name: "missing token file", token: false, want: project.ErrForgejoUnavailable},
		{name: "not found", token: true, status: http.StatusNotFound, body: `{}`, want: project.ErrForgejoRepositoryNotFound},
		{name: "forbidden", token: true, status: http.StatusForbidden, body: `{"message":"denied"}`, want: project.ErrForgejoUnavailable},
		{name: "empty repository", token: true, status: http.StatusOK, body: `{"name":"demo","owner":{"login":"owner"},"empty":true}`, want: project.ErrForgejoRepositoryNotReady},
		{name: "archived repository", token: true, status: http.StatusOK, body: `{"name":"demo","owner":{"login":"owner"},"default_branch":"main","archived":true}`, want: project.ErrForgejoRepositoryNotReady},
		{name: "different repository identity", token: true, status: http.StatusOK, body: `{"name":"other","owner":{"login":"owner"},"default_branch":"main"}`, want: project.ErrForgejoUnavailable},
		{name: "malformed response", token: true, status: http.StatusOK, body: `{`, want: project.ErrForgejoUnavailable},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tokenFile := filepath.Join(t.TempDir(), "forgejo-token")
			if test.token {
				if err := os.WriteFile(tokenFile, []byte("secret-test-token\n"), 0o600); err != nil {
					t.Fatalf("write token: %v", err)
				}
			}
			client, err := NewClient(ClientConfig{
				BaseURL: "http://forgejo:3000", TokenFile: tokenFile,
				RequestTimeout: time.Second,
				HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					return jsonResponse(test.status, test.body), nil
				})},
			})
			if err != nil {
				t.Fatalf("create client: %v", err)
			}
			_, err = client.VerifyRepository(t.Context(), "owner", "demo")
			if !errors.Is(err, test.want) {
				t.Fatalf("expected error %v, got %v", test.want, err)
			}
			if err != nil && strings.Contains(err.Error(), "secret-test-token") {
				t.Fatal("error exposed Forgejo token")
			}
		})
	}
}

func TestClientReadsRotatedTokenWithoutRestart(t *testing.T) {
	tokenFile := writeTestToken(t, "first-token")
	wantTokens := []string{"token first-token", "token second-token"}
	call := 0
	client, err := NewClient(ClientConfig{
		BaseURL: "http://forgejo:3000", TokenFile: tokenFile,
		RequestTimeout: time.Second,
		HTTPClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			if call >= len(wantTokens) || request.Header.Get("Authorization") != wantTokens[call] {
				t.Fatalf("call %d used unexpected authorization", call)
			}
			call++
			return jsonResponse(http.StatusOK, `{"name":"demo","owner":{"login":"owner"},"default_branch":"main"}`), nil
		})},
	})
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	if _, err := client.VerifyRepository(t.Context(), "owner", "demo"); err != nil {
		t.Fatalf("verify with first token: %v", err)
	}
	if err := os.WriteFile(tokenFile, []byte("second-token\n"), 0o600); err != nil {
		t.Fatalf("rotate token: %v", err)
	}
	if _, err := client.VerifyRepository(t.Context(), "owner", "demo"); err != nil {
		t.Fatalf("verify with rotated token: %v", err)
	}
	if call != 2 {
		t.Fatalf("expected two repository calls, got %d", call)
	}
}

func TestClientGetsBranch(t *testing.T) {
	client := newBranchTestClient(t, func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodGet ||
			request.URL.String() != "http://forgejo:3000/api/v1/repos/owner/repository/branches/feature%2Ftest" {
			t.Fatalf("unexpected request %s %s", request.Method, request.URL)
		}
		return branchResponse(http.StatusOK, "feature/test", forgejoTestCommitID), nil
	})

	branch, err := client.GetBranch(t.Context(), "owner", "repository", "feature/test")
	if err != nil {
		t.Fatalf("get branch: %v", err)
	}
	if branch.Name != "feature/test" || branch.CommitID != forgejoTestCommitID {
		t.Fatalf("unexpected branch %+v", branch)
	}
}

func TestClientCreatesBranchFromExactCommit(t *testing.T) {
	client := newBranchTestClient(t, func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodPost ||
			request.URL.String() != "http://forgejo:3000/api/v1/repos/owner/repository/branches" {
			t.Fatalf("unexpected request %s %s", request.Method, request.URL)
		}
		var body struct {
			NewBranchName string `json:"new_branch_name"`
			OldRefName    string `json:"old_ref_name"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatalf("decode branch request: %v", err)
		}
		if body.NewBranchName != "commitarium/fea_test" || body.OldRefName != forgejoTestCommitID {
			t.Fatalf("unexpected branch request %+v", body)
		}
		return branchResponse(http.StatusCreated, body.NewBranchName, body.OldRefName), nil
	})

	branch, err := client.EnsureBranch(
		t.Context(), "owner", "repository", "commitarium/fea_test", forgejoTestCommitID,
	)
	if err != nil || branch.Name != "commitarium/fea_test" || branch.CommitID != forgejoTestCommitID {
		t.Fatalf("create branch: branch=%+v err=%v", branch, err)
	}
}

func TestClientAdoptsMatchingBranchAfterCreationConflict(t *testing.T) {
	calls := 0
	client := newBranchTestClient(t, func(request *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return jsonResponse(http.StatusConflict, `{"message":"branch exists"}`), nil
		}
		if request.Method != http.MethodGet ||
			request.URL.Path != "/api/v1/repos/owner/repository/branches/commitarium/fea_test" {
			t.Fatalf("unexpected reconciliation request %s %s", request.Method, request.URL)
		}
		return branchResponse(http.StatusOK, "commitarium/fea_test", forgejoTestCommitID), nil
	})

	branch, err := client.EnsureBranch(
		t.Context(), "owner", "repository", "commitarium/fea_test", forgejoTestCommitID,
	)
	if err != nil || branch.CommitID != forgejoTestCommitID || calls != 2 {
		t.Fatalf("adopt branch: branch=%+v calls=%d err=%v", branch, calls, err)
	}
}

func TestClientRejectsConflictingExistingBranch(t *testing.T) {
	calls := 0
	client := newBranchTestClient(t, func(*http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return jsonResponse(http.StatusConflict, `{}`), nil
		}
		return branchResponse(
			http.StatusOK,
			"commitarium/fea_test",
			"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		), nil
	})

	_, err := client.EnsureBranch(
		t.Context(), "owner", "repository", "commitarium/fea_test", forgejoTestCommitID,
	)
	if !errors.Is(err, workspace.ErrBranchConflict) {
		t.Fatalf("expected %v, got %v", workspace.ErrBranchConflict, err)
	}
}

func TestNewClientRejectsUnsafeConfiguration(t *testing.T) {
	for _, config := range []ClientConfig{
		{},
		{BaseURL: "ftp://forgejo", TokenFile: "/token", RequestTimeout: time.Second},
		{BaseURL: "http://user:password@forgejo", TokenFile: "/token", RequestTimeout: time.Second},
		{BaseURL: "http://forgejo", RequestTimeout: time.Second},
		{BaseURL: "http://forgejo", TokenFile: "/token"},
	} {
		if _, err := NewClient(config); !errors.Is(err, ErrInvalidClientConfig) {
			t.Fatalf("expected error %v for %+v, got %v", ErrInvalidClientConfig, config, err)
		}
	}
}

func writeTestToken(t *testing.T, token string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "forgejo-token")
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		t.Fatalf("write token: %v", err)
	}
	return path
}

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func branchResponse(status int, name, commitID string) *http.Response {
	return jsonResponse(status, `{"name":"`+name+`","commit":{"id":"`+commitID+`"}}`)
}

func newBranchTestClient(
	t *testing.T,
	transport roundTripFunc,
) *Client {
	t.Helper()
	client, err := NewClient(ClientConfig{
		BaseURL: "http://forgejo:3000", TokenFile: writeTestToken(t, "test-token"),
		RequestTimeout: time.Second, HTTPClient: &http.Client{Transport: transport},
	})
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	return client
}
