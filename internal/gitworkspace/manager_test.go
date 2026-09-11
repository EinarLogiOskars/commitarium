package gitworkspace

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/workspace"
)

const (
	testInternalURL = "http://forgejo:3000"
	testHostURL     = "http://127.0.0.1:3001"
)

func TestManagerAdoptsCleanCheckoutAndPreservesLaterUserChanges(t *testing.T) {
	manager, root := newTestManager(t, nil)
	spec := initializeCheckout(t, root)

	if err := manager.Ensure(t.Context(), spec); err != nil {
		t.Fatalf("ensure new checkout: %v", err)
	}
	target := filepath.Join(root, spec.WorkspaceID)
	if origin := testGit(t, target, "config", "--get", "remote.origin.url"); origin != repositoryURL(testHostURL, spec.RepositoryOwner, spec.RepositoryName) {
		t.Fatalf("unexpected host origin %q", origin)
	}
	if internal := testGit(t, target, "config", "--get", "remote.commitarium.url"); internal != repositoryURL(testInternalURL, spec.RepositoryOwner, spec.RepositoryName) {
		t.Fatalf("unexpected internal remote %q", internal)
	}

	if err := os.WriteFile(filepath.Join(target, "user-edit.txt"), []byte("preserve me\n"), 0o644); err != nil {
		t.Fatalf("write user edit: %v", err)
	}
	spec.AlreadyReady = true
	if err := manager.Ensure(t.Context(), spec); err != nil {
		t.Fatalf("reconcile dirty ready checkout: %v", err)
	}
	if _, err := os.Stat(filepath.Join(target, "user-edit.txt")); err != nil {
		t.Fatalf("user edit was not preserved: %v", err)
	}
}

func TestManagerAllowsReadyCheckoutToAdvanceFromRecordedBase(t *testing.T) {
	manager, root := newTestManager(t, nil)
	spec := initializeCheckout(t, root)
	if err := manager.Ensure(t.Context(), spec); err != nil {
		t.Fatalf("ensure new checkout: %v", err)
	}
	target := filepath.Join(root, spec.WorkspaceID)
	if err := os.WriteFile(filepath.Join(target, "later.txt"), []byte("later\n"), 0o644); err != nil {
		t.Fatalf("write later commit: %v", err)
	}
	testGit(t, target, "add", "later.txt")
	testGit(t, target, "commit", "-m", "later")

	spec.AlreadyReady = true
	if err := manager.Ensure(t.Context(), spec); err != nil {
		t.Fatalf("reconcile advanced checkout: %v", err)
	}
}

func TestManagerVerifiesExactCleanPublishedHead(t *testing.T) {
	manager, root := newTestManager(t, nil)
	spec := initializeCheckout(t, root)
	if err := manager.Ensure(t.Context(), spec); err != nil {
		t.Fatalf("ensure new checkout: %v", err)
	}
	target := filepath.Join(root, spec.WorkspaceID)
	if err := os.WriteFile(filepath.Join(target, "implemented.txt"), []byte("done\n"), 0o644); err != nil {
		t.Fatalf("write implementation: %v", err)
	}
	testGit(t, target, "add", "implemented.txt")
	testGit(t, target, "commit", "-m", "implement feature")
	publishedHead := testGit(t, target, "rev-parse", "HEAD")

	spec.AlreadyReady = true
	spec.ExpectedHeadCommitID = publishedHead
	spec.RequireClean = true
	if err := manager.Ensure(t.Context(), spec); err != nil {
		t.Fatalf("verify exact published head: %v", err)
	}
	if err := os.WriteFile(filepath.Join(target, "uncommitted.txt"), []byte("partial\n"), 0o644); err != nil {
		t.Fatalf("write partial change: %v", err)
	}
	if err := manager.Ensure(t.Context(), spec); !errors.Is(err, workspace.ErrCheckoutConflict) {
		t.Fatalf("expected dirty published checkout conflict, got %v", err)
	}
}

func TestManagerRequiresCleanPlanningBaselineWhenRequested(t *testing.T) {
	manager, root := newTestManager(t, nil)
	spec := initializeCheckout(t, root)
	if err := manager.Ensure(t.Context(), spec); err != nil {
		t.Fatalf("ensure new checkout: %v", err)
	}
	target := filepath.Join(root, spec.WorkspaceID)
	if err := os.WriteFile(filepath.Join(target, "user-edit.txt"), []byte("preserve me\n"), 0o644); err != nil {
		t.Fatalf("write user edit: %v", err)
	}
	spec.AlreadyReady = true
	spec.RequireCleanBaseline = true
	if err := manager.Ensure(t.Context(), spec); !errors.Is(err, workspace.ErrCheckoutConflict) {
		t.Fatalf("expected %v for dirty planning baseline, got %v", workspace.ErrCheckoutConflict, err)
	}
	contents, err := os.ReadFile(filepath.Join(target, "user-edit.txt"))
	if err != nil || string(contents) != "preserve me\n" {
		t.Fatalf("strict reconciliation changed the user edit: %q err=%v", contents, err)
	}
}

func TestManagerPromotesCleanClarificationCheckoutIdempotently(t *testing.T) {
	manager, root := newTestManager(t, nil)
	spec := initializeCheckout(t, root)
	target := filepath.Join(root, spec.WorkspaceID)
	testGit(t, target, "branch", "-m", "main")
	spec.Branch = "main"
	if err := manager.Ensure(t.Context(), spec); err != nil {
		t.Fatalf("ensure clarification checkout: %v", err)
	}
	promotion := workspace.CheckoutPromotionSpec{
		WorkspaceID: spec.WorkspaceID, RepositoryOwner: spec.RepositoryOwner,
		RepositoryName: spec.RepositoryName, BaseBranch: "main",
		FeatureBranch: "commitarium/fea_test", BaseCommitID: spec.BaseCommitID,
	}
	if err := manager.Promote(t.Context(), promotion); err != nil {
		t.Fatalf("promote clarification checkout: %v", err)
	}
	if branch := testGit(t, target, "branch", "--show-current"); branch != promotion.FeatureBranch {
		t.Fatalf("promoted branch = %q, want %q", branch, promotion.FeatureBranch)
	}
	if head := testGit(t, target, "rev-parse", "HEAD"); head != promotion.BaseCommitID {
		t.Fatalf("promoted head = %q, want %q", head, promotion.BaseCommitID)
	}
	if err := manager.Promote(t.Context(), promotion); err != nil {
		t.Fatalf("retry promoted checkout: %v", err)
	}
}

func TestManagerRefusesToPromoteDirtyClarificationCheckout(t *testing.T) {
	manager, root := newTestManager(t, nil)
	spec := initializeCheckout(t, root)
	target := filepath.Join(root, spec.WorkspaceID)
	testGit(t, target, "branch", "-m", "main")
	spec.Branch = "main"
	if err := manager.Ensure(t.Context(), spec); err != nil {
		t.Fatalf("ensure clarification checkout: %v", err)
	}
	partialPath := filepath.Join(target, "partial.txt")
	if err := os.WriteFile(partialPath, []byte("preserve me\n"), 0o644); err != nil {
		t.Fatalf("write partial clarification work: %v", err)
	}
	err := manager.Promote(t.Context(), workspace.CheckoutPromotionSpec{
		WorkspaceID: spec.WorkspaceID, RepositoryOwner: spec.RepositoryOwner,
		RepositoryName: spec.RepositoryName, BaseBranch: "main",
		FeatureBranch: "commitarium/fea_test", BaseCommitID: spec.BaseCommitID,
	})
	if !errors.Is(err, workspace.ErrCheckoutConflict) {
		t.Fatalf("expected dirty checkout conflict, got %v", err)
	}
	if branch := testGit(t, target, "branch", "--show-current"); branch != "main" {
		t.Fatalf("dirty checkout moved to branch %q", branch)
	}
	contents, readErr := os.ReadFile(partialPath)
	if readErr != nil || string(contents) != "preserve me\n" {
		t.Fatalf("promotion changed partial work: %q err=%v", contents, readErr)
	}
}

func TestManagerRejectsAmbiguousCheckoutWithoutChangingIt(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, string, *workspace.CheckoutSpec)
	}{
		{
			name: "dirty before readiness",
			mutate: func(t *testing.T, target string, _ *workspace.CheckoutSpec) {
				if err := os.WriteFile(filepath.Join(target, "partial.txt"), []byte("partial\n"), 0o644); err != nil {
					t.Fatalf("write partial file: %v", err)
				}
			},
		},
		{
			name: "wrong origin",
			mutate: func(t *testing.T, target string, _ *workspace.CheckoutSpec) {
				testGit(t, target, "remote", "set-url", "origin", "http://example.invalid/other.git")
			},
		},
		{
			name: "missing ready directory",
			mutate: func(t *testing.T, target string, spec *workspace.CheckoutSpec) {
				spec.AlreadyReady = true
				if err := os.Rename(target, target+"-moved"); err != nil {
					t.Fatalf("move checkout: %v", err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager, root := newTestManager(t, nil)
			spec := initializeCheckout(t, root)
			target := filepath.Join(root, spec.WorkspaceID)
			test.mutate(t, target, &spec)
			if err := manager.Ensure(t.Context(), spec); !errors.Is(err, workspace.ErrCheckoutConflict) {
				t.Fatalf("expected %v, got %v", workspace.ErrCheckoutConflict, err)
			}
		})
	}
}

func TestManagerPassesCredentialOutsideGitArguments(t *testing.T) {
	runner := &recordingRunner{err: errors.New("expected clone failure")}
	manager, _ := newTestManager(t, runner)
	spec := workspace.CheckoutSpec{
		WorkspaceID: "wsp_fea_test", RepositoryOwner: "owner",
		RepositoryName: "repository", Branch: "commitarium/fea_test",
		BaseCommitID: "0123456789abcdef0123456789abcdef01234567",
	}

	err := manager.Ensure(t.Context(), spec)
	if !errors.Is(err, workspace.ErrCheckoutUnavailable) {
		t.Fatalf("expected %v, got %v", workspace.ErrCheckoutUnavailable, err)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("expected one clone command, got %+v", runner.calls)
	}
	call := runner.calls[0]
	if strings.Contains(strings.Join(call.arguments, " "), "secret-token") {
		t.Fatal("Git arguments exposed the token")
	}
	if !containsEnvironment(call.environment, "GIT_CONFIG_VALUE_0=Authorization: token secret-token") {
		t.Fatalf("clone did not receive the ephemeral credential header")
	}
}

func TestNewManagerRejectsUnsafeConfiguration(t *testing.T) {
	root := t.TempDir()
	tokenFile := filepath.Join(root, "token")
	if err := os.WriteFile(tokenFile, []byte("token"), 0o600); err != nil {
		t.Fatalf("write token: %v", err)
	}
	for _, config := range []Config{
		{},
		{Root: "relative", InternalBaseURL: testInternalURL, HostBaseURL: testHostURL, TokenFile: tokenFile},
		{Root: root, InternalBaseURL: "file:///tmp/forgejo", HostBaseURL: testHostURL, TokenFile: tokenFile},
		{Root: root, InternalBaseURL: testInternalURL, HostBaseURL: "http://user:password@localhost", TokenFile: tokenFile},
	} {
		if _, err := NewManager(config); err == nil {
			t.Fatalf("accepted unsafe configuration %+v", config)
		}
	}
}

type commandCall struct {
	directory   string
	environment []string
	arguments   []string
}

type recordingRunner struct {
	calls []commandCall
	err   error
}

func (runner *recordingRunner) Run(
	_ context.Context,
	directory string,
	environment []string,
	arguments ...string,
) (string, error) {
	runner.calls = append(runner.calls, commandCall{
		directory: directory, environment: append([]string(nil), environment...),
		arguments: append([]string(nil), arguments...),
	})
	return "", runner.err
}

func newTestManager(t *testing.T, runner CommandRunner) (*Manager, string) {
	t.Helper()
	root, err := canonicalDirectory(t.TempDir())
	if err != nil {
		t.Fatalf("canonicalize test root: %v", err)
	}
	tokenFile := filepath.Join(root, "token")
	if err := os.WriteFile(tokenFile, []byte("secret-token\n"), 0o600); err != nil {
		t.Fatalf("write token: %v", err)
	}
	manager, err := NewManager(Config{
		Root: root, InternalBaseURL: testInternalURL, HostBaseURL: testHostURL,
		TokenFile: tokenFile, Runner: runner,
	})
	if err != nil {
		t.Fatalf("create manager: %v", err)
	}
	return manager, root
}

func initializeCheckout(t *testing.T, root string) workspace.CheckoutSpec {
	t.Helper()
	spec := workspace.CheckoutSpec{
		WorkspaceID: "wsp_fea_test", RepositoryOwner: "owner",
		RepositoryName: "repository", Branch: "commitarium/fea_test",
	}
	target := filepath.Join(root, spec.WorkspaceID)
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatalf("create checkout: %v", err)
	}
	testGit(t, target, "init", "-b", spec.Branch)
	testGit(t, target, "config", "user.name", "Commitarium Test")
	testGit(t, target, "config", "user.email", "commitarium@example.invalid")
	if err := os.WriteFile(filepath.Join(target, "README.md"), []byte("test\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	testGit(t, target, "add", "README.md")
	testGit(t, target, "commit", "-m", "initial")
	spec.BaseCommitID = testGit(t, target, "rev-parse", "HEAD")
	testGit(
		t, target, "remote", "add", "origin",
		repositoryURL(testInternalURL, spec.RepositoryOwner, spec.RepositoryName),
	)
	return spec
}

func testGit(t *testing.T, directory string, arguments ...string) string {
	t.Helper()
	command := exec.CommandContext(t.Context(), "git", arguments...)
	command.Dir = directory
	command.Env = gitEnvironment("")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(arguments, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

func containsEnvironment(environment []string, wanted string) bool {
	for _, variable := range environment {
		if variable == wanted {
			return true
		}
	}
	return false
}
