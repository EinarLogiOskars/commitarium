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

type recordingProvisioner struct {
	ensureCalls   int
	finalizeCalls int
	verifyCalls   int
	repository    project.ForgejoRepository
}

func (p *recordingProvisioner) EnsureImportRepository(context.Context, project.RepositoryImportSpec) (project.ForgejoRepository, error) {
	p.ensureCalls++
	return p.repository, nil
}

func (p *recordingProvisioner) FinalizeImportRepository(context.Context, project.RepositoryImportSpec) (project.ForgejoRepository, error) {
	p.finalizeCalls++
	return p.repository, nil
}

func (p *recordingProvisioner) VerifyImportRepository(context.Context, project.RepositoryImportSpec) (project.ForgejoRepository, error) {
	p.verifyCalls++
	return p.repository, nil
}

type localGitRunner struct {
	pushArguments   []string
	pushEnvironment []string
	pushErr         error
}

func (runner *localGitRunner) Run(
	ctx context.Context,
	directory string,
	environment []string,
	arguments ...string,
) (string, error) {
	if len(arguments) > 0 && arguments[0] == "push" {
		runner.pushArguments = append([]string(nil), arguments...)
		runner.pushEnvironment = append([]string(nil), environment...)
		mirrorConfig := exec.CommandContext(ctx, "git", "config", "--get", "remote.origin.mirror")
		mirrorConfig.Dir = directory
		if output, err := mirrorConfig.Output(); err == nil && strings.TrimSpace(string(output)) == "true" {
			return "", errors.New("push repository still has mirror-only remote configuration")
		}
		return "", runner.pushErr
	}
	command := exec.CommandContext(ctx, "git", arguments...)
	command.Dir = directory
	command.Env = append(os.Environ(), environment...)
	output, err := command.CombinedOutput()
	return strings.TrimSpace(string(output)), err
}

func TestManagerImportsValidatedBranchesAndTags(t *testing.T) {
	bundle := makeTestBundle(t, "main")
	tokenFile := filepath.Join(t.TempDir(), "forgejo-token")
	if err := os.WriteFile(tokenFile, []byte("secret-token\n"), 0o600); err != nil {
		t.Fatalf("write token: %v", err)
	}
	provisioner := &recordingProvisioner{repository: project.ForgejoRepository{
		Owner: "coordinator", Name: "example-aabbcc", DefaultBranch: "main",
	}}
	runner := &localGitRunner{}
	manager, err := NewManager(Config{
		InternalBaseURL: "http://forgejo:3000", TokenFile: tokenFile,
		Provisioner: provisioner, Runner: runner, TempRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("create manager: %v", err)
	}
	spec := project.RepositoryImportSpec{
		ImportID: "desktop-1", Repository: "example-aabbcc", DefaultBranch: "main",
		BundleDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	repository, err := manager.Import(t.Context(), spec, bundle)
	if err != nil {
		t.Fatalf("import bundle: %v", err)
	}
	if repository != provisioner.repository || provisioner.ensureCalls != 1 || provisioner.finalizeCalls != 1 {
		t.Fatalf("unexpected provisioner result=%+v ensure=%d finalize=%d", repository, provisioner.ensureCalls, provisioner.finalizeCalls)
	}
	joinedArguments := strings.Join(runner.pushArguments, " ")
	if strings.Contains(joinedArguments, "secret-token") ||
		!strings.Contains(strings.Join(runner.pushEnvironment, "\n"), "Authorization: Basic Y29vcmRpbmF0b3I6c2VjcmV0LXRva2Vu") {
		t.Fatalf("credential was not isolated from Git arguments: args=%q env=%q", joinedArguments, runner.pushEnvironment)
	}
	if !strings.Contains(joinedArguments, "refs/heads/*:refs/heads/*") ||
		!strings.Contains(joinedArguments, "refs/tags/*:refs/tags/*") {
		t.Fatalf("expected branch and tag refspecs, got %q", joinedArguments)
	}
}

func TestManagerRejectsBundleWithoutRequestedDefaultBranch(t *testing.T) {
	bundle := makeTestBundle(t, "trunk")
	tokenFile := filepath.Join(t.TempDir(), "forgejo-token")
	if err := os.WriteFile(tokenFile, []byte("token"), 0o600); err != nil {
		t.Fatalf("write token: %v", err)
	}
	provisioner := &recordingProvisioner{repository: project.ForgejoRepository{
		Owner: "coordinator", Name: "example", DefaultBranch: "main",
	}}
	manager, err := NewManager(Config{
		InternalBaseURL: "http://forgejo:3000", TokenFile: tokenFile,
		Provisioner: provisioner, Runner: &localGitRunner{}, TempRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("create manager: %v", err)
	}
	_, err = manager.Import(t.Context(), project.RepositoryImportSpec{
		ImportID: "desktop-1", Repository: "example", DefaultBranch: "main",
	}, bundle)
	if !errors.Is(err, project.ErrInvalidGitBundle) || provisioner.finalizeCalls != 0 {
		t.Fatalf("expected invalid bundle before finalization, got %v finalize=%d", err, provisioner.finalizeCalls)
	}
}

func TestManagerTreatsForgejoPushFailureAsRetryableUnavailability(t *testing.T) {
	bundle := makeTestBundle(t, "main")
	tokenFile := filepath.Join(t.TempDir(), "forgejo-token")
	if err := os.WriteFile(tokenFile, []byte("token"), 0o600); err != nil {
		t.Fatalf("write token: %v", err)
	}
	provisioner := &recordingProvisioner{repository: project.ForgejoRepository{
		Owner: "coordinator", Name: "example", DefaultBranch: "main",
	}}
	manager, err := NewManager(Config{
		InternalBaseURL: "http://forgejo:3000", TokenFile: tokenFile,
		Provisioner: provisioner,
		Runner:      &localGitRunner{pushErr: errors.New("network unavailable")},
		TempRoot:    t.TempDir(),
	})
	if err != nil {
		t.Fatalf("create manager: %v", err)
	}
	_, err = manager.Import(t.Context(), project.RepositoryImportSpec{
		ImportID: "desktop-1", Repository: "example", DefaultBranch: "main",
	}, bundle)
	if !errors.Is(err, project.ErrImportUnavailable) || provisioner.finalizeCalls != 0 {
		t.Fatalf("expected retryable unavailability, got %v finalize=%d", err, provisioner.finalizeCalls)
	}
}

func makeTestBundle(t *testing.T, branch string) string {
	t.Helper()
	repository := filepath.Join(t.TempDir(), "source")
	if err := os.Mkdir(repository, 0o755); err != nil {
		t.Fatalf("create source repository: %v", err)
	}
	runTestGit(t, repository, "init", "-b", branch)
	runTestGit(t, repository, "config", "user.name", "Commitarium Test")
	runTestGit(t, repository, "config", "user.email", "test@commitarium.local")
	if err := os.WriteFile(filepath.Join(repository, "README.md"), []byte("test\n"), 0o644); err != nil {
		t.Fatalf("write source file: %v", err)
	}
	runTestGit(t, repository, "add", "README.md")
	runTestGit(t, repository, "commit", "-m", "initial")
	runTestGit(t, repository, "tag", "v0.1.0")
	bundle := filepath.Join(t.TempDir(), "source.bundle")
	runTestGit(t, repository, "bundle", "create", bundle, "--branches", "--tags")
	return bundle
}

func runTestGit(t *testing.T, directory string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = directory
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", arguments, err, output)
	}
}
