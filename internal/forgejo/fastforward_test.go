package forgejo

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"
)

func fastForwardClient(t *testing.T, merge int, calls *[]string, merged *map[string]any) *Client {
	t.Helper()
	client, err := NewClient(ClientConfig{
		BaseURL: "http://forgejo:3000", TokenFile: writeTestToken(t, "secret-test-token"),
		RequestTimeout: time.Second,
		HTTPClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			call := request.Method + " " + request.URL.Path
			*calls = append(*calls, call)
			switch call {
			case "POST /api/v1/repos/commitarium/todo/pulls":
				return jsonResponse(http.StatusCreated, `{"number":7}`), nil
			case "GET /api/v1/repos/commitarium/todo/pulls/7/reviews":
				return jsonResponse(http.StatusOK, `[]`), nil
			case "POST /api/v1/repos/commitarium/todo/pulls/7/merge":
				body, _ := io.ReadAll(request.Body)
				_ = json.Unmarshal(body, merged)
				return jsonResponse(merge, `{}`), nil
			}
			return jsonResponse(http.StatusOK, `{}`), nil
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestAFastForwardIsApprovedAndMergedOnlyAsAFastForward(t *testing.T) {
	var calls []string
	merged := map[string]any{}
	client := fastForwardClient(t, http.StatusOK, &calls, &merged)
	if err := client.FastForwardBranch(t.Context(), "commitarium", "todo",
		"main", "linked/main", "abc123", "Bring in"); err != nil {
		t.Fatalf("fast-forward: %v", err)
	}
	if merged["Do"] != "fast-forward-only" || merged["head_commit_id"] != "abc123" ||
		merged["delete_branch_after_merge"] != true {
		t.Fatalf("merge = %v", merged)
	}
	approved := false
	for _, call := range calls {
		if call == "POST /api/v1/repos/commitarium/todo/pulls/7/reviews" {
			approved = true
		}
	}
	if !approved {
		t.Fatalf("not approved: %v", calls)
	}
}

func TestARefusedFastForwardClosesItsPullRequest(t *testing.T) {
	var calls []string
	merged := map[string]any{}
	client := fastForwardClient(t, http.StatusMethodNotAllowed, &calls, &merged)
	err := client.FastForwardBranch(t.Context(), "commitarium", "todo",
		"main", "linked/main", "abc123", "Bring in")
	if !errors.Is(err, ErrNotFastForward) {
		t.Fatalf("err = %v", err)
	}
	if calls[len(calls)-1] != "PATCH /api/v1/repos/commitarium/todo/pulls/7" {
		t.Fatalf("calls = %v", calls)
	}
}
