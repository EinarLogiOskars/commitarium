package gitimport

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/EinarLogiOskars/commitarium/internal/project"
)

const maxGitOutputBytes = 64 * 1024

type RepositoryProvisioner interface {
	EnsureImportRepository(context.Context, project.RepositoryImportSpec) (project.ForgejoRepository, error)
	FinalizeImportRepository(context.Context, project.RepositoryImportSpec) (project.ForgejoRepository, error)
	VerifyImportRepository(context.Context, project.RepositoryImportSpec) (project.ForgejoRepository, error)
}

type CommandRunner interface {
	Run(ctx context.Context, directory string, environment []string, arguments ...string) (string, error)
}

type Config struct {
	InternalBaseURL string
	TokenFile       string
	GitExecutable   string
	TempRoot        string
	Provisioner     RepositoryProvisioner
	Runner          CommandRunner
}

type Manager struct {
	internalBaseURL string
	tokenFile       string
	tempRoot        string
	provisioner     RepositoryProvisioner
	runner          CommandRunner
}

var _ project.RepositoryImporter = (*Manager)(nil)

func NewManager(config Config) (*Manager, error) {
	baseURL, err := normalizeBaseURL(config.InternalBaseURL)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(config.TokenFile) == "" {
		return nil, errors.New("Forgejo token file is required")
	}
	if config.Provisioner == nil {
		return nil, errors.New("Forgejo repository provisioner is required")
	}
	tempRoot := strings.TrimSpace(config.TempRoot)
	if tempRoot == "" {
		tempRoot = os.TempDir()
	}
	runner := config.Runner
	if runner == nil {
		executable := strings.TrimSpace(config.GitExecutable)
		if executable == "" {
			executable = "git"
		}
		runner = execRunner{executable: executable}
	}
	return &Manager{
		internalBaseURL: baseURL, tokenFile: strings.TrimSpace(config.TokenFile),
		tempRoot: tempRoot, provisioner: config.Provisioner, runner: runner,
	}, nil
}

func (manager *Manager) Import(
	ctx context.Context,
	spec project.RepositoryImportSpec,
	bundlePath string,
) (project.ForgejoRepository, error) {
	tokenBytes, err := os.ReadFile(manager.tokenFile)
	if err != nil || strings.TrimSpace(string(tokenBytes)) == "" {
		return project.ForgejoRepository{}, fmt.Errorf("%w: Forgejo token file cannot be read", project.ErrImportUnavailable)
	}
	token := strings.TrimSpace(string(tokenBytes))
	temporary, err := os.MkdirTemp(manager.tempRoot, "commitarium-git-import-*")
	if err != nil {
		return project.ForgejoRepository{}, fmt.Errorf("%w: create import checkout: %v", project.ErrImportUnavailable, err)
	}
	defer os.RemoveAll(temporary)
	mirror := filepath.Join(temporary, "repository.git")
	if _, err := manager.runner.Run(ctx, temporary, gitEnvironment("", ""), "clone", "--bare", "--", bundlePath, mirror); err != nil {
		return project.ForgejoRepository{}, manager.gitError(ctx, "uploaded file is not a usable Git bundle")
	}
	if _, err := manager.runner.Run(
		ctx, mirror, gitEnvironment("", ""), "show-ref", "--verify", "--", "refs/heads/"+spec.DefaultBranch,
	); err != nil {
		return project.ForgejoRepository{}, manager.gitError(ctx, "default branch is absent from the Git bundle")
	}
	refs, err := manager.runner.Run(ctx, mirror, gitEnvironment("", ""), "for-each-ref", "--format=%(refname)")
	if err != nil {
		return project.ForgejoRepository{}, manager.gitError(ctx, "Git bundle references cannot be inspected")
	}
	hasTags := false
	for _, ref := range strings.Fields(refs) {
		switch {
		case strings.HasPrefix(ref, "refs/heads/"):
		case strings.HasPrefix(ref, "refs/tags/"):
			hasTags = true
		default:
			return project.ForgejoRepository{}, fmt.Errorf("%w: bundle contains unsupported reference %q", project.ErrInvalidGitBundle, ref)
		}
	}
	// Validate the complete bundle before creating anything in Forgejo. This
	// keeps a malformed upload from reserving the deterministic repository name.
	repository, err := manager.provisioner.EnsureImportRepository(ctx, spec)
	if err != nil {
		return project.ForgejoRepository{}, err
	}
	remoteURL := repositoryURL(manager.internalBaseURL, repository.Owner, repository.Name)
	if _, err := manager.runner.Run(ctx, mirror, gitEnvironment("", ""), "remote", "set-url", "origin", remoteURL); err != nil {
		return project.ForgejoRepository{}, manager.unavailableError(ctx, "Git import remote cannot be configured")
	}
	arguments := []string{"push", "--force", "--prune", "origin", "refs/heads/*:refs/heads/*"}
	if hasTags {
		arguments = append(arguments, "refs/tags/*:refs/tags/*")
	}
	if _, err := manager.runner.Run(ctx, mirror, gitEnvironment(repository.Owner, token), arguments...); err != nil {
		return project.ForgejoRepository{}, manager.unavailableError(ctx, "Git bundle cannot be pushed to Forgejo")
	}
	return manager.provisioner.FinalizeImportRepository(ctx, spec)
}

func (manager *Manager) Verify(
	ctx context.Context,
	spec project.RepositoryImportSpec,
) (project.ForgejoRepository, error) {
	return manager.provisioner.VerifyImportRepository(ctx, spec)
}

func (manager *Manager) gitError(ctx context.Context, message string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return fmt.Errorf("%w: %s", project.ErrInvalidGitBundle, message)
}

func (manager *Manager) unavailableError(ctx context.Context, message string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return fmt.Errorf("%w: %s", project.ErrImportUnavailable, message)
}

func normalizeBaseURL(value string) (string, error) {
	value = strings.TrimRight(strings.TrimSpace(value), "/")
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("internal Forgejo URL must be an absolute HTTP URL without credentials, query, or fragment")
	}
	return value, nil
}

func repositoryURL(baseURL, owner, repository string) string {
	return baseURL + "/" + url.PathEscape(owner) + "/" + url.PathEscape(repository) + ".git"
}

func gitEnvironment(owner, token string) []string {
	environment := []string{"GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1"}
	if token != "" {
		basicCredential := base64.StdEncoding.EncodeToString([]byte(owner + ":" + token))
		environment = append(environment,
			"GIT_CONFIG_COUNT=1",
			"GIT_CONFIG_KEY_0=http.extraHeader",
			"GIT_CONFIG_VALUE_0=Authorization: Basic "+basicCredential,
		)
	}
	return environment
}

type execRunner struct {
	executable string
}

func (runner execRunner) Run(
	ctx context.Context,
	directory string,
	environment []string,
	arguments ...string,
) (string, error) {
	command := exec.CommandContext(ctx, runner.executable, arguments...)
	command.Dir = directory
	command.Env = append(os.Environ(), environment...)
	var output bytes.Buffer
	command.Stdout = &limitedWriter{buffer: &output, remaining: maxGitOutputBytes}
	command.Stderr = &limitedWriter{buffer: &output, remaining: maxGitOutputBytes}
	err := command.Run()
	return strings.TrimSpace(output.String()), err
}

type limitedWriter struct {
	buffer    *bytes.Buffer
	remaining int
}

func (writer *limitedWriter) Write(data []byte) (int, error) {
	written := len(data)
	if writer.remaining > 0 {
		keep := len(data)
		if keep > writer.remaining {
			keep = writer.remaining
		}
		_, _ = writer.buffer.Write(data[:keep])
		writer.remaining -= keep
	}
	return written, nil
}
