package forgejo

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"
)

func TestAPushMirrorPushesOnEveryCommitAndAtOnce(t *testing.T) {
	var calls []string
	var added map[string]any
	client, err := NewClient(ClientConfig{
		BaseURL: "http://forgejo:3000", TokenFile: writeTestToken(t, "secret-test-token"),
		RequestTimeout: time.Second,
		HTTPClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			calls = append(calls, request.Method+" "+request.URL.Path)
			if request.URL.Path == "/api/v1/repos/commitarium/todo/push_mirrors" {
				body, _ := io.ReadAll(request.Body)
				_ = json.Unmarshal(body, &added)
				return jsonResponse(http.StatusOK, `{}`), nil
			}
			return jsonResponse(http.StatusOK, ``), nil
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.AddPushMirror(t.Context(), "commitarium", "todo",
		"https://github.com/you/todo.git", "", "ghp_token"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if len(calls) != 2 || calls[1] != "POST /api/v1/repos/commitarium/todo/push_mirrors-sync" {
		t.Fatalf("calls = %v", calls)
	}
	if added["sync_on_commit"] != true || added["remote_password"] != "ghp_token" ||
		added["remote_username"] != "commitarium" {
		t.Fatalf("mirror = %v", added)
	}
}

func TestAPushMirrorNeedsAPlainHTTPSAddressAndAToken(t *testing.T) {
	client, err := NewClient(ClientConfig{
		BaseURL: "http://forgejo:3000", TokenFile: writeTestToken(t, "secret-test-token"),
		RequestTimeout: time.Second,
		HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			t.Fatal("a bad mirror reached Forgejo")
			return nil, nil
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, address := range []string{
		"git@github.com:you/todo.git",
		"http://github.com/you/todo.git",
		"https://user:pass@github.com/you/todo.git",
		"https://github.com/",
	} {
		err := client.AddPushMirror(t.Context(), "commitarium", "todo", address, "", "token")
		if !errors.Is(err, ErrInvalidMirror) {
			t.Fatalf("%s: %v", address, err)
		}
	}
	err = client.AddPushMirror(t.Context(), "commitarium", "todo", "https://github.com/you/todo.git", "", " ")
	if !errors.Is(err, ErrInvalidMirror) {
		t.Fatalf("no token: %v", err)
	}
}
