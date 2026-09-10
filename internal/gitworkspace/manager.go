package gitworkspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/workspace"
)

const maxGitOutputBytes = 64 * 1024

var safeWorkspaceID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
var workspaceCommitID = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

type Config struct {
	Root            string
	InternalBaseURL string
	HostBaseURL     string
	TokenFile       string
	GitExecutable   string
	Runner          CommandRunner
}

type CommandRunner interface {
	Run(
		ctx context.Context,
		directory string,
		environment []string,
		arguments ...string,
	) (string, error)
}

type Manager struct {
	root            string
	internalBaseURL string
	hostBaseURL     string
	tokenFile       string
	runner          CommandRunner
}

func NewManager(config Config) (*Manager, error) {
	root, err := canonicalDirectory(config.Root)
	if err != nil {
		return nil, fmt.Errorf("workspace root: %w", err)
	}
	internalBaseURL, err := normalizeBaseURL(config.InternalBaseURL)
	if err != nil {
		return nil, fmt.Errorf("internal Forgejo URL: %w", err)
	}
	hostBaseURL, err := normalizeBaseURL(config.HostBaseURL)
	if err != nil {
		return nil, fmt.Errorf("host Forgejo URL: %w", err)
	}
	tokenFile := strings.TrimSpace(config.TokenFile)
	if tokenFile == "" {
		return nil, errors.New("Forgejo token file is required")
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
		root: root, internalBaseURL: internalBaseURL, hostBaseURL: hostBaseURL,
		tokenFile: tokenFile, runner: runner,
	}, nil
}

func (manager *Manager) Ensure(ctx context.Context, spec workspace.CheckoutSpec) error {
	if err := spec.Validate(); err != nil {
		return fmt.Errorf("%w: %v", workspace.ErrCheckoutConflict, err)
	}
	if !safeWorkspaceID.MatchString(spec.WorkspaceID) ||
		strings.Contains(spec.RepositoryOwner, "/") || strings.Contains(spec.RepositoryName, "/") {
		return fmt.Errorf("%w: workspace or repository identity is unsafe", workspace.ErrCheckoutConflict)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	target := filepath.Join(manager.root, spec.WorkspaceID)
	info, err := os.Lstat(target)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if spec.AlreadyReady {
			return fmt.Errorf("%w: recorded checkout directory is missing", workspace.ErrCheckoutConflict)
		}
		if err := manager.clone(ctx, spec, target); err != nil {
			return err
		}
	case err != nil:
		return fmt.Errorf("%w: checkout directory cannot be inspected", workspace.ErrCheckoutUnavailable)
	case info.Mode()&os.ModeSymlink != 0 || !info.IsDir():
		return fmt.Errorf("%w: checkout path is not an ordinary directory", workspace.ErrCheckoutConflict)
	}
	return manager.reconcile(ctx, spec, target)
}

// PreparePublication builds the exact commit that a later reconciliation will
// publish. A temporary index captures tracked and untracked files without
// changing the user's real Git staging area.
func (manager *Manager) PreparePublication(
	ctx context.Context,
	spec workspace.CheckoutSpec,
	message string,
	committedAt time.Time,
) (workspace.CommitSnapshot, error) {
	message = strings.TrimSpace(message)
	if message == "" || strings.ContainsAny(message, "\r\n") ||
		len(message) > workspace.MaxCommitMessageBytes {
		return workspace.CommitSnapshot{}, errors.New("commit message must be one non-empty line of at most 200 bytes")
	}
	if err := manager.Ensure(ctx, spec); err != nil {
		return workspace.CommitSnapshot{}, err
	}
	target := filepath.Join(manager.root, spec.WorkspaceID)
	run := func(environment []string, arguments ...string) (string, error) {
		return manager.runner.Run(ctx, target, environment, arguments...)
	}
	head, err := run(gitEnvironment(""), "rev-parse", "HEAD")
	if err != nil {
		return workspace.CommitSnapshot{}, manager.localConflict(ctx, "checkout HEAD is unavailable")
	}
	index, err := os.CreateTemp(manager.root, ".commitarium-index-*")
	if err != nil {
		return workspace.CommitSnapshot{}, manager.localUnavailable(ctx, "temporary Git index cannot be created")
	}
	indexPath := index.Name()
	if err := index.Close(); err != nil {
		_ = os.Remove(indexPath)
		return workspace.CommitSnapshot{}, manager.localUnavailable(ctx, "temporary Git index cannot be closed")
	}
	if err := os.Remove(indexPath); err != nil {
		return workspace.CommitSnapshot{}, manager.localUnavailable(ctx, "temporary Git index cannot be initialized")
	}
	defer func() { _ = os.Remove(indexPath) }()
	environment := append(gitEnvironment(""), "GIT_INDEX_FILE="+indexPath)
	if _, err := run(environment, "read-tree", "HEAD"); err != nil {
		return workspace.CommitSnapshot{}, manager.localUnavailable(ctx, "Git could not initialize the publication snapshot")
	}
	if _, err := run(environment, "add", "-A", "--", "."); err != nil {
		return workspace.CommitSnapshot{}, manager.localUnavailable(ctx, "Git could not snapshot workspace changes")
	}
	tree, err := run(environment, "write-tree")
	if err != nil {
		return workspace.CommitSnapshot{}, manager.localUnavailable(ctx, "Git could not write the publication tree")
	}
	headTree, err := run(gitEnvironment(""), "rev-parse", "HEAD^{tree}")
	if err != nil {
		return workspace.CommitSnapshot{}, manager.localConflict(ctx, "checkout HEAD tree is unavailable")
	}
	if tree == headTree {
		if head == spec.BaseCommitID {
			return workspace.CommitSnapshot{}, workspace.ErrNoChanges
		}
		return workspace.CommitSnapshot{LocalCommitIDBefore: head, CommitID: head}, nil
	}
	date := committedAt.UTC().Format(time.RFC3339)
	environment = append(environment,
		"GIT_AUTHOR_NAME=Commitarium", "GIT_AUTHOR_EMAIL=commitarium@local",
		"GIT_AUTHOR_DATE="+date, "GIT_COMMITTER_NAME=Commitarium",
		"GIT_COMMITTER_EMAIL=commitarium@local", "GIT_COMMITTER_DATE="+date,
	)
	commitID, err := run(environment, "commit-tree", tree, "-p", head, "-m", message)
	if err != nil {
		return workspace.CommitSnapshot{}, manager.localUnavailable(ctx, "Git could not create the publication commit")
	}
	return workspace.CommitSnapshot{LocalCommitIDBefore: head, CommitID: commitID}, nil
}

func (manager *Manager) ApplyPublication(
	ctx context.Context,
	spec workspace.CheckoutSpec,
	snapshot workspace.CommitSnapshot,
) error {
	if err := manager.Ensure(ctx, spec); err != nil {
		return err
	}
	target := filepath.Join(manager.root, spec.WorkspaceID)
	run := func(arguments ...string) (string, error) {
		return manager.runner.Run(ctx, target, gitEnvironment(""), arguments...)
	}
	head, err := run("rev-parse", "HEAD")
	if err != nil {
		return manager.localConflict(ctx, "checkout HEAD is unavailable")
	}
	if head == snapshot.CommitID {
		if err := manager.reconcilePublicationIndex(ctx, target, snapshot.CommitID); err != nil {
			return err
		}
		return manager.requireCleanPublication(ctx, target)
	}
	if head != snapshot.LocalCommitIDBefore {
		return manager.localConflict(ctx, "checkout changed after publication was prepared")
	}
	if _, err := run("update-ref", "refs/heads/"+spec.Branch, snapshot.CommitID, snapshot.LocalCommitIDBefore); err != nil {
		return manager.localConflict(ctx, "Git could not apply the prepared publication commit")
	}
	// commit-tree intentionally uses a private index. Once its exact commit is
	// installed, advance the real index to the same tree just as `git commit`
	// would, without rewriting any working-tree file.
	if _, err := run("read-tree", snapshot.CommitID); err != nil {
		return manager.localConflict(ctx, "Git could not finalize the publication index")
	}
	return manager.requireCleanPublication(ctx, target)
}

func (manager *Manager) reconcilePublicationIndex(
	ctx context.Context,
	target string,
	commitID string,
) error {
	// If a process died after moving the branch but before advancing the real
	// index, first prove that the working tree still exactly matches our commit.
	// Only then is it safe to finish the index update.
	index, err := os.CreateTemp(manager.root, ".commitarium-recovery-index-*")
	if err != nil {
		return manager.localUnavailable(ctx, "temporary recovery index cannot be created")
	}
	indexPath := index.Name()
	_ = index.Close()
	if err := os.Remove(indexPath); err != nil {
		return manager.localUnavailable(ctx, "temporary recovery index cannot be initialized")
	}
	defer func() { _ = os.Remove(indexPath) }()
	environment := append(gitEnvironment(""), "GIT_INDEX_FILE="+indexPath)
	run := func(arguments ...string) (string, error) {
		return manager.runner.Run(ctx, target, environment, arguments...)
	}
	if _, err := run("read-tree", commitID); err != nil {
		return manager.localConflict(ctx, "publication commit tree is unavailable")
	}
	if _, err := run("add", "-A", "--", "."); err != nil {
		return manager.localUnavailable(ctx, "Git could not inspect publication recovery state")
	}
	worktreeTree, err := run("write-tree")
	if err != nil {
		return manager.localUnavailable(ctx, "Git could not inspect publication recovery tree")
	}
	commitTree, err := manager.runner.Run(
		ctx, target, gitEnvironment(""), "rev-parse", commitID+"^{tree}",
	)
	if err != nil || worktreeTree != commitTree {
		return manager.localConflict(ctx, "working tree changed after publication was prepared")
	}
	if _, err := manager.runner.Run(
		ctx, target, gitEnvironment(""), "read-tree", commitID,
	); err != nil {
		return manager.localConflict(ctx, "Git could not finalize the publication index")
	}
	return nil
}

func (manager *Manager) PushPublication(
	ctx context.Context,
	spec workspace.CheckoutSpec,
	commitID string,
) error {
	if !workspaceCommitID.MatchString(commitID) {
		return workspace.ErrPublicationConflict
	}
	tokenBytes, err := os.ReadFile(manager.tokenFile)
	if err != nil || strings.TrimSpace(string(tokenBytes)) == "" {
		return manager.localUnavailable(ctx, "Forgejo token file cannot be read")
	}
	target := filepath.Join(manager.root, spec.WorkspaceID)
	_, err = manager.runner.Run(
		ctx, target, gitEnvironment(strings.TrimSpace(string(tokenBytes))),
		"push", "commitarium", commitID+":refs/heads/"+spec.Branch,
	)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return manager.localUnavailable(ctx, "Git could not push the prepared commit to Forgejo")
	}
	return nil
}

func (manager *Manager) requireCleanPublication(ctx context.Context, target string) error {
	status, err := manager.runner.Run(
		ctx, target, gitEnvironment(""), "status", "--porcelain=v1", "--untracked-files=all",
	)
	if err != nil || status != "" {
		return manager.localConflict(ctx, "publication did not leave the checkout at its exact clean commit")
	}
	return nil
}

func (manager *Manager) clone(
	ctx context.Context,
	spec workspace.CheckoutSpec,
	target string,
) error {
	tokenBytes, err := os.ReadFile(manager.tokenFile)
	if err != nil {
		return fmt.Errorf("%w: Forgejo token file cannot be read", workspace.ErrCheckoutUnavailable)
	}
	token := strings.TrimSpace(string(tokenBytes))
	if token == "" {
		return fmt.Errorf("%w: Forgejo token file is empty", workspace.ErrCheckoutUnavailable)
	}
	internalURL := repositoryURL(
		manager.internalBaseURL, spec.RepositoryOwner, spec.RepositoryName,
	)
	environment := gitEnvironment(token)
	_, err = manager.runner.Run(
		ctx,
		manager.root,
		environment,
		"clone", "--branch", spec.Branch, "--single-branch", "--no-tags",
		"--", internalURL, target,
	)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return fmt.Errorf("%w: Git could not clone the feature branch", workspace.ErrCheckoutUnavailable)
	}
	return nil
}

func (manager *Manager) reconcile(
	ctx context.Context,
	spec workspace.CheckoutSpec,
	target string,
) error {
	canonicalTarget, err := canonicalDirectory(target)
	if err != nil || canonicalTarget != target {
		return fmt.Errorf("%w: checkout directory resolves outside its assigned path", workspace.ErrCheckoutConflict)
	}
	run := func(arguments ...string) (string, error) {
		return manager.runner.Run(ctx, target, gitEnvironment(""), arguments...)
	}
	topLevel, err := run("rev-parse", "--show-toplevel")
	if err != nil {
		return manager.localConflict(ctx, "checkout is not a complete Git working tree")
	}
	canonicalTopLevel, err := canonicalDirectory(topLevel)
	if err != nil || canonicalTopLevel != target {
		return fmt.Errorf("%w: Git working tree has an unexpected root", workspace.ErrCheckoutConflict)
	}
	branch, err := run("symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil || branch != spec.Branch {
		return manager.localConflict(ctx, "checkout is not on its assigned feature branch")
	}
	head, err := run("rev-parse", "HEAD")
	if err != nil {
		return manager.localConflict(ctx, "checkout HEAD is unavailable")
	}

	internalURL := repositoryURL(
		manager.internalBaseURL, spec.RepositoryOwner, spec.RepositoryName,
	)
	hostURL := repositoryURL(manager.hostBaseURL, spec.RepositoryOwner, spec.RepositoryName)
	origin, originExists, err := readRemote(run, "origin")
	if err != nil || !originExists || (origin != internalURL && origin != hostURL) {
		return manager.localConflict(ctx, "checkout origin points to an unexpected repository")
	}
	internalRemote, internalRemoteExists, err := readRemote(run, "commitarium")
	if err != nil || (internalRemoteExists && internalRemote != internalURL) {
		return manager.localConflict(ctx, "checkout internal remote is inconsistent")
	}

	if spec.AlreadyReady {
		if origin != hostURL || !internalRemoteExists {
			return manager.localConflict(ctx, "ready checkout remotes were changed")
		}
		if spec.RequireCleanBaseline && head != spec.BaseCommitID {
			return manager.localConflict(ctx, "checkout HEAD moved after the planning baseline was recorded")
		}
		if head != spec.BaseCommitID {
			if _, err := run("merge-base", "--is-ancestor", spec.BaseCommitID, head); err != nil {
				return manager.localConflict(ctx, "checkout HEAD no longer contains its recorded base commit")
			}
		}
		if spec.RequireCleanBaseline {
			status, err := run("status", "--porcelain=v1", "--untracked-files=all")
			if err != nil || status != "" {
				return manager.localConflict(ctx, "checkout has changes outside the agreed planning baseline")
			}
		}
		// Deliberately do not reject a dirty ready checkout: it may contain
		// first-class user edits that the next agent turn must reconcile.
		return nil
	}

	if head != spec.BaseCommitID {
		return manager.localConflict(ctx, "new checkout does not match its recorded base commit")
	}
	status, err := run("status", "--porcelain=v1", "--untracked-files=all")
	if err != nil || status != "" {
		return manager.localConflict(ctx, "new checkout is incomplete or already modified")
	}
	if origin != hostURL {
		if _, err := run("remote", "set-url", "origin", hostURL); err != nil {
			return manager.localUnavailable(ctx, "Git could not configure the host remote")
		}
	}
	if !internalRemoteExists {
		if _, err := run("remote", "add", "commitarium", internalURL); err != nil {
			return manager.localUnavailable(ctx, "Git could not configure the agent remote")
		}
	}
	return nil
}

func (manager *Manager) localConflict(ctx context.Context, message string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return fmt.Errorf("%w: %s", workspace.ErrCheckoutConflict, message)
}

func (manager *Manager) localUnavailable(ctx context.Context, message string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return fmt.Errorf("%w: %s", workspace.ErrCheckoutUnavailable, message)
}

func readRemote(
	run func(...string) (string, error),
	name string,
) (string, bool, error) {
	value, err := run("config", "--get", "remote."+name+".url")
	if err == nil {
		return value, true, nil
	}
	var exitError interface{ ExitCode() int }
	if errors.As(err, &exitError) && exitError.ExitCode() == 1 {
		return "", false, nil
	}
	return "", false, err
}

func normalizeBaseURL(value string) (string, error) {
	value = strings.TrimRight(strings.TrimSpace(value), "/")
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("must be an absolute HTTP URL without credentials, query, or fragment")
	}
	return value, nil
}

func repositoryURL(baseURL, owner, repository string) string {
	return baseURL + "/" + url.PathEscape(owner) + "/" + url.PathEscape(repository) + ".git"
}

func canonicalDirectory(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", errors.New("path must be absolute")
	}
	canonical, err := filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("path is not a directory")
	}
	return canonical, nil
}

func gitEnvironment(token string) []string {
	environment := make([]string, 0, len(os.Environ())+4)
	for _, variable := range os.Environ() {
		name, _, _ := strings.Cut(variable, "=")
		if name == "GIT_TERMINAL_PROMPT" || strings.HasPrefix(name, "GIT_CONFIG_") {
			continue
		}
		environment = append(environment, variable)
	}
	environment = append(environment, "GIT_TERMINAL_PROMPT=0")
	if token != "" {
		environment = append(
			environment,
			"GIT_CONFIG_COUNT=1",
			"GIT_CONFIG_KEY_0=http.extraHeader",
			"GIT_CONFIG_VALUE_0=Authorization: token "+token,
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
	if environment != nil {
		command.Env = environment
	}
	output := &boundedBuffer{remaining: maxGitOutputBytes}
	command.Stdout = output
	command.Stderr = output
	err := command.Run()
	return strings.TrimSpace(output.String()), err
}

type boundedBuffer struct {
	buffer    bytes.Buffer
	remaining int
}

func (buffer *boundedBuffer) Write(value []byte) (int, error) {
	written := len(value)
	if buffer.remaining > 0 {
		kept := value
		if len(kept) > buffer.remaining {
			kept = kept[:buffer.remaining]
		}
		_, _ = buffer.buffer.Write(kept)
		buffer.remaining -= len(kept)
	}
	return written, nil
}

func (buffer *boundedBuffer) String() string {
	return buffer.buffer.String()
}
