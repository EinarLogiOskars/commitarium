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

func TestClientCreatesDraftPullRequest(t *testing.T) {
	spec := testPullRequestSpec()
	calls := 0
	client := newBranchTestClient(t, func(request *http.Request) (*http.Response, error) {
		calls++
		switch calls {
		case 1:
			if request.Method != http.MethodGet || request.URL.Path != "/api/v1/repos/owner/repository/pulls" ||
				request.URL.Query().Get("state") != "all" ||
				request.URL.Query().Get("base") != spec.BaseBranch ||
				request.URL.Query().Get("head") != spec.HeadBranch {
				t.Fatalf("unexpected discovery request %s %s", request.Method, request.URL)
			}
			return jsonResponse(http.StatusOK, `[]`), nil
		case 2:
			if request.Method != http.MethodPost || request.URL.Path != "/api/v1/repos/owner/repository/pulls" {
				t.Fatalf("unexpected creation request %s %s", request.Method, request.URL)
			}
			var body struct {
				Base  string `json:"base"`
				Head  string `json:"head"`
				Title string `json:"title"`
				Body  string `json:"body"`
			}
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Fatalf("decode pull request request: %v", err)
			}
			if body.Base != spec.BaseBranch || body.Head != spec.HeadBranch ||
				body.Title != spec.Title || body.Body != spec.Body {
				t.Fatalf("unexpected pull request request %+v", body)
			}
			return pullRequestJSONResponse(t, http.StatusCreated, managedPullRequest(spec)), nil
		default:
			t.Fatalf("unexpected request %d: %s %s", calls, request.Method, request.URL)
			return nil, nil
		}
	})

	created, err := client.EnsureDraftPullRequest(t.Context(), "owner", "repository", spec)
	if err != nil || created.Number != 7 || !created.Draft || calls != 2 {
		t.Fatalf("create pull request: pull_request=%+v calls=%d err=%v", created, calls, err)
	}
}

func TestClientAdoptsDraftPullRequestAfterUncertainCreation(t *testing.T) {
	spec := testPullRequestSpec()
	client := newBranchTestClient(t, func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodGet {
			t.Fatalf("adoption unexpectedly tried %s", request.Method)
		}
		body := pullRequestJSON(t, managedPullRequest(spec))
		return jsonResponse(http.StatusOK, "["+body+"]"), nil
	})

	adopted, err := client.EnsureDraftPullRequest(t.Context(), "owner", "repository", spec)
	if err != nil || adopted.Number != 7 {
		t.Fatalf("adopt pull request: pull_request=%+v err=%v", adopted, err)
	}
}

func TestClientAdoptsPullRequestCreatedByConcurrentRequest(t *testing.T) {
	spec := testPullRequestSpec()
	calls := 0
	client := newBranchTestClient(t, func(request *http.Request) (*http.Response, error) {
		calls++
		switch calls {
		case 1:
			return jsonResponse(http.StatusOK, `[]`), nil
		case 2:
			if request.Method != http.MethodPost {
				t.Fatalf("expected creation request, got %s", request.Method)
			}
			return jsonResponse(http.StatusUnprocessableEntity, `{"message":"already exists"}`), nil
		case 3:
			body := pullRequestJSON(t, managedPullRequest(spec))
			return jsonResponse(http.StatusOK, "["+body+"]"), nil
		default:
			t.Fatalf("unexpected request %d", calls)
			return nil, nil
		}
	})

	adopted, err := client.EnsureDraftPullRequest(t.Context(), "owner", "repository", spec)
	if err != nil || adopted.Number != 7 || calls != 3 {
		t.Fatalf("adopt concurrent pull request: pull_request=%+v calls=%d err=%v", adopted, calls, err)
	}
}

func TestClientReconcilesRecordedPullRequestAfterWorkAdvances(t *testing.T) {
	spec := testPullRequestSpec()
	spec.ExistingNumber = 7
	advanced := managedPullRequest(spec)
	advanced.Title = "WIP: User-adjusted title"
	advanced.HeadCommitID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	client := newBranchTestClient(t, func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodGet ||
			request.URL.Path != "/api/v1/repos/owner/repository/pulls/7" {
			t.Fatalf("unexpected reconciliation request %s %s", request.Method, request.URL)
		}
		return pullRequestJSONResponse(t, http.StatusOK, advanced), nil
	})

	stored, err := client.EnsureDraftPullRequest(t.Context(), "owner", "repository", spec)
	if err != nil || stored.HeadCommitID != advanced.HeadCommitID {
		t.Fatalf("reconcile pull request: pull_request=%+v err=%v", stored, err)
	}
}

func TestClientRejectsPullRequestWithoutManagedIdentity(t *testing.T) {
	spec := testPullRequestSpec()
	conflicting := managedPullRequest(spec)
	conflicting.Body = "unrelated pull request"
	client := newBranchTestClient(t, func(*http.Request) (*http.Response, error) {
		body := pullRequestJSON(t, conflicting)
		return jsonResponse(http.StatusOK, "["+body+"]"), nil
	})

	_, err := client.EnsureDraftPullRequest(t.Context(), "owner", "repository", spec)
	if !errors.Is(err, workspace.ErrPullRequestConflict) {
		t.Fatalf("expected %v, got %v", workspace.ErrPullRequestConflict, err)
	}
}

func TestClientPublishesPlanWithoutReplacingPullRequestBody(t *testing.T) {
	spec := testPlanPublicationSpec()
	stored := managedPullRequest(testPullRequestSpec())
	stored.Body += "\n\nUser-added context"
	calls := 0
	client := newBranchTestClient(t, func(request *http.Request) (*http.Response, error) {
		calls++
		switch calls {
		case 1:
			return pullRequestJSONResponse(t, http.StatusOK, stored), nil
		case 2:
			if request.Method != http.MethodPatch ||
				request.URL.Path != "/api/v1/repos/owner/repository/pulls/7" {
				t.Fatalf("unexpected publication request %s %s", request.Method, request.URL)
			}
			var payload struct {
				Body string `json:"body"`
			}
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
				t.Fatalf("decode publication request: %v", err)
			}
			if !strings.Contains(payload.Body, "User-added context") ||
				!strings.Contains(payload.Body, spec.PublicationMarker) ||
				!strings.Contains(payload.Body, "## Agreed implementation plan\n\n"+spec.Plan) {
				t.Fatalf("publication replaced or omitted body content: %q", payload.Body)
			}
			stored.Body = payload.Body
			// Forgejo 16 returns 201 for a successful pull-request edit.
			return pullRequestJSONResponse(t, http.StatusCreated, stored), nil
		default:
			t.Fatalf("unexpected publication request %d", calls)
			return nil, nil
		}
	})

	updated, published, err := client.EnsurePullRequestPlan(
		t.Context(), "owner", "repository", spec,
	)
	if err != nil || !published || calls != 2 ||
		!strings.Contains(updated.Body, spec.PublicationMarker) {
		t.Fatalf("publish plan: pull_request=%+v published=%t calls=%d err=%v", updated, published, calls, err)
	}
}

func TestClientAdoptsPreviouslyPublishedPlan(t *testing.T) {
	spec := testPlanPublicationSpec()
	stored := managedPullRequest(testPullRequestSpec())
	stored.Body += "\n\n" + spec.PublicationMarker +
		"\n\n## Agreed implementation plan\n\n" + spec.Plan
	calls := 0
	client := newBranchTestClient(t, func(request *http.Request) (*http.Response, error) {
		calls++
		if request.Method != http.MethodGet {
			t.Fatalf("adoption unexpectedly tried %s", request.Method)
		}
		return pullRequestJSONResponse(t, http.StatusOK, stored), nil
	})

	got, published, err := client.EnsurePullRequestPlan(
		t.Context(), "owner", "repository", spec,
	)
	if err != nil || published || calls != 1 || got.Body != stored.Body {
		t.Fatalf("adopt plan: pull_request=%+v published=%t calls=%d err=%v", got, published, calls, err)
	}
}

func TestClientVerifiesPublishedPlanWithoutUpdatingPullRequest(t *testing.T) {
	spec := testPlanPublicationSpec()
	stored := managedPullRequest(testPullRequestSpec())
	stored.Body += "\n\n" + spec.PublicationMarker +
		"\n\n## Agreed implementation plan\n\n" + spec.Plan
	calls := 0
	client := newBranchTestClient(t, func(request *http.Request) (*http.Response, error) {
		calls++
		if request.Method != http.MethodGet {
			t.Fatalf("verification unexpectedly tried %s", request.Method)
		}
		return pullRequestJSONResponse(t, http.StatusOK, stored), nil
	})

	got, err := client.VerifyPullRequestPlan(t.Context(), "owner", "repository", spec)
	if err != nil || calls != 1 || got.Body != stored.Body {
		t.Fatalf("verify plan: pull_request=%+v calls=%d err=%v", got, calls, err)
	}
}

func TestClientVerificationRejectsMissingPlanWithoutUpdatingPullRequest(t *testing.T) {
	spec := testPlanPublicationSpec()
	stored := managedPullRequest(testPullRequestSpec())
	client := newBranchTestClient(t, func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodGet {
			t.Fatalf("verification unexpectedly tried %s", request.Method)
		}
		return pullRequestJSONResponse(t, http.StatusOK, stored), nil
	})

	_, err := client.VerifyPullRequestPlan(t.Context(), "owner", "repository", spec)
	if !errors.Is(err, workspace.ErrPullRequestConflict) {
		t.Fatalf("expected %v, got %v", workspace.ErrPullRequestConflict, err)
	}
}

func TestClientReconcilesPlanAfterUncertainUpdateResponse(t *testing.T) {
	spec := testPlanPublicationSpec()
	remote := managedPullRequest(testPullRequestSpec())
	calls := 0
	patches := 0
	client := newBranchTestClient(t, func(request *http.Request) (*http.Response, error) {
		calls++
		if request.Method == http.MethodGet {
			return pullRequestJSONResponse(t, http.StatusOK, remote), nil
		}
		patches++
		var payload struct {
			Body string `json:"body"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatalf("decode uncertain publication: %v", err)
		}
		remote.Body = payload.Body
		return nil, errors.New("connection closed after Forgejo applied update")
	})

	if _, _, err := client.EnsurePullRequestPlan(
		t.Context(), "owner", "repository", spec,
	); !errors.Is(err, project.ErrForgejoUnavailable) {
		t.Fatalf("expected uncertain Forgejo error, got %v", err)
	}
	got, published, err := client.EnsurePullRequestPlan(
		t.Context(), "owner", "repository", spec,
	)
	if err != nil || published || patches != 1 || calls != 3 ||
		!strings.Contains(got.Body, spec.PublicationMarker) {
		t.Fatalf("reconcile uncertain update: pull_request=%+v published=%t calls=%d patches=%d err=%v", got, published, calls, patches, err)
	}
}

func TestClientRejectsChangedPlanBehindPublicationMarker(t *testing.T) {
	spec := testPlanPublicationSpec()
	stored := managedPullRequest(testPullRequestSpec())
	stored.Body += "\n\n" + spec.PublicationMarker +
		"\n\n## Agreed implementation plan\n\nDifferent plan"
	client := newBranchTestClient(t, func(*http.Request) (*http.Response, error) {
		return pullRequestJSONResponse(t, http.StatusOK, stored), nil
	})

	_, _, err := client.EnsurePullRequestPlan(t.Context(), "owner", "repository", spec)
	if !errors.Is(err, workspace.ErrPullRequestConflict) {
		t.Fatalf("expected %v, got %v", workspace.ErrPullRequestConflict, err)
	}
}

func TestClientRejectsDuplicatePlanPublicationMarker(t *testing.T) {
	spec := testPlanPublicationSpec()
	stored := managedPullRequest(testPullRequestSpec())
	section := spec.PublicationMarker +
		"\n\n## Agreed implementation plan\n\n" + spec.Plan
	stored.Body += "\n\n" + section + "\n\n" + section
	client := newBranchTestClient(t, func(*http.Request) (*http.Response, error) {
		return pullRequestJSONResponse(t, http.StatusOK, stored), nil
	})

	_, _, err := client.EnsurePullRequestPlan(t.Context(), "owner", "repository", spec)
	if !errors.Is(err, workspace.ErrPullRequestConflict) {
		t.Fatalf("expected %v, got %v", workspace.ErrPullRequestConflict, err)
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

func testPullRequestSpec() workspace.PullRequestSpec {
	marker := "<!-- commitarium-feature: fea_test -->"
	return workspace.PullRequestSpec{
		Title: "WIP: Test feature", Body: marker + "\n\n## Accepted goal\n\nShip it",
		FeatureMarker: marker, BaseBranch: "main", HeadBranch: "commitarium/fea_test",
		InitialHeadCommitID: forgejoTestCommitID,
	}
}

func testPlanPublicationSpec() workspace.PlanPublicationSpec {
	return workspace.PlanPublicationSpec{
		Number: 7, FeatureMarker: "<!-- commitarium-feature: fea_test -->",
		PublicationMarker: "<!-- commitarium-plan: stable-submission -->",
		Plan:              "Implement the agreed behavior and verify it.",
		BaseBranch:        "main", HeadBranch: "commitarium/fea_test",
		HeadCommitID: forgejoTestCommitID,
	}
}

func managedPullRequest(spec workspace.PullRequestSpec) workspace.PullRequest {
	return workspace.PullRequest{
		Number: 7, URL: "http://forgejo:3000/owner/repository/pulls/7",
		Title: spec.Title, Body: spec.Body, State: "open", Draft: true,
		BaseBranch: spec.BaseBranch, HeadBranch: spec.HeadBranch,
		HeadCommitID: spec.InitialHeadCommitID,
		CreatedAt:    time.Date(2026, time.September, 9, 20, 0, 0, 0, time.UTC),
	}
}

func pullRequestJSONResponse(
	t *testing.T,
	status int,
	pullRequest workspace.PullRequest,
) *http.Response {
	t.Helper()
	return jsonResponse(status, pullRequestJSON(t, pullRequest))
}

func pullRequestJSON(t *testing.T, pullRequest workspace.PullRequest) string {
	t.Helper()
	body, err := json.Marshal(struct {
		Number    int64  `json:"number"`
		HTMLURL   string `json:"html_url"`
		Title     string `json:"title"`
		Body      string `json:"body"`
		State     string `json:"state"`
		Draft     bool   `json:"draft"`
		CreatedAt string `json:"created_at"`
		Base      struct {
			Ref string `json:"ref"`
		} `json:"base"`
		Head struct {
			Ref string `json:"ref"`
			SHA string `json:"sha"`
		} `json:"head"`
	}{
		Number: pullRequest.Number, HTMLURL: pullRequest.URL,
		Title: pullRequest.Title, Body: pullRequest.Body,
		State: pullRequest.State, Draft: pullRequest.Draft,
		CreatedAt: pullRequest.CreatedAt.Format(time.RFC3339Nano),
		Base: struct {
			Ref string `json:"ref"`
		}{Ref: pullRequest.BaseBranch},
		Head: struct {
			Ref string `json:"ref"`
			SHA string `json:"sha"`
		}{Ref: pullRequest.HeadBranch, SHA: pullRequest.HeadCommitID},
	})
	if err != nil {
		t.Fatalf("encode pull request: %v", err)
	}
	return string(body)
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
