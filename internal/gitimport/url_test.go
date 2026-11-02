package gitimport

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/project"
)

// cloneRunner stands a local repository in for the URL it is asked to clone.
type cloneRunner struct {
	local       string
	environment []string
	localGitRunner
}

func (runner *cloneRunner) Run(ctx context.Context, directory string, environment []string, arguments ...string) (string, error) {
	if len(arguments) > 0 && arguments[0] == "clone" {
		runner.environment = environment
		arguments = append([]string(nil), arguments...)
		arguments[len(arguments)-2] = runner.local
	}
	return runner.localGitRunner.Run(ctx, directory, environment, arguments...)
}

func TestARepositoryByURLBecomesABundleOfItsDefaultBranch(t *testing.T) {
	source := t.TempDir()
	for _, step := range [][]string{
		{"init", "--initial-branch", "trunk"},
		{"-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "--allow-empty", "-m", "first"},
	} {
		command := exec.Command("git", step...)
		command.Dir = source
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", step, err, output)
		}
	}
	runner := &cloneRunner{local: source}
	manager, err := NewManager(Config{
		InternalBaseURL: "http://forgejo:3000", TokenFile: filepath.Join(t.TempDir(), "token"),
		GitExecutable: "git", TempRoot: t.TempDir(), Provisioner: &recordingProvisioner{}, Runner: runner,
	})
	if err != nil {
		t.Fatal(err)
	}
	bundle, branch, cleanup, err := manager.BundleFromURL(t.Context(), "https://github.com/you/app.git", "", "secret")
	if err != nil {
		t.Fatalf("bundle: %v", err)
	}
	if branch != "trunk" {
		t.Fatalf("default branch = %q", branch)
	}
	if _, err := os.Stat(bundle); err != nil {
		t.Fatalf("no bundle: %v", err)
	}
	// The token goes in a header, never in the URL or the arguments.
	if !strings.Contains(strings.Join(runner.environment, " "), "Authorization: Basic") {
		t.Fatalf("token not in the clone's header: %v", runner.environment)
	}
	cleanup()
	if _, err := os.Stat(bundle); !os.IsNotExist(err) {
		t.Fatalf("cleanup left the bundle: %v", err)
	}
}

func TestOnlyAPlainHTTPSRepositoryAddressIsCloned(t *testing.T) {
	manager, err := NewManager(Config{
		InternalBaseURL: "http://forgejo:3000", TokenFile: filepath.Join(t.TempDir(), "token"),
		GitExecutable: "git", TempRoot: t.TempDir(), Provisioner: &recordingProvisioner{},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, address := range []string{
		"file:///etc", "git@github.com:you/app.git", "http://github.com/you/app.git",
		"https://user:pass@github.com/you/app.git", "https://github.com/", "ext::sh -c id",
	} {
		_, _, _, err := manager.BundleFromURL(t.Context(), address, "", "")
		if !errors.Is(err, project.ErrInvalidGitBundle) {
			t.Fatalf("%s: %v", address, err)
		}
	}
}
