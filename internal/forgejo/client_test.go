package forgejo

import (
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/project"
)

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
