// Package release puts an app on the marketplace (MARKETPLACE_PLAN.md,
// slice 4). A release is a commit made beside the project's default branch:
// its tree is the branch plus the .commitarium folder (ADR-026), its parents
// are the marketplace's previous main and the branch, and it is tagged vN.
// The local branch stays as work orders left it; only the marketplace gets
// the release commit, always as a fast-forward of its main.
package release

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/marketplace"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/toolchain"
	"github.com/EinarLogiOskars/commitarium/internal/vision"
)

var (
	ErrNotAnApp          = errors.New("only apps can be released")
	ErrStackRequired     = errors.New("the app needs a stack before it can be released")
	ErrNothingToRelease  = errors.New("nothing changed since the last release")
	ErrDiverged          = errors.New("the marketplace copy has changes this app does not")
	ErrRepositoryMissing = errors.New("the app has no repository yet")
	ErrUnavailable       = errors.New("releasing is unavailable")
)

// SecretError names where a release found something that looks like a
// secret. Nothing is pushed.
type SecretError struct{ Findings []string }

func (err *SecretError) Error() string {
	return "the app's history contains what looks like a secret: " + strings.Join(err.Findings, ", ")
}

type Projects interface {
	GetByID(ctx context.Context, projectID string) (project.Project, error)
}

type Toolchains interface {
	Get(ctx context.Context, projectID string) (toolchain.Manifest, error)
}

type Visions interface {
	Get(ctx context.Context, projectID string) (vision.Vision, error)
}

type Features interface {
	List(ctx context.Context, projectID string) ([]feature.Feature, error)
}

type Marketplace interface {
	Credentials(ctx context.Context) (marketplace.Credentials, error)
	EnsureAppRepository(ctx context.Context, name, description string) (marketplace.AppRepository, error)
}

type Config struct {
	// LocalBaseURL, LocalOwner and LocalTokenFile reach the local Forgejo.
	LocalBaseURL   string
	LocalOwner     string
	LocalTokenFile string
	GitExecutable  string
	// StateRoot keeps each project's release record.
	StateRoot string
}

// Version is one release of an app.
type Version struct {
	Version       string    `json:"version"`
	Commit        string    `json:"commit"`
	SourceCommit  string    `json:"source_commit"`
	ReleasedAt    time.Time `json:"released_at"`
	ReleasedBy    string    `json:"released_by"`
	RepositoryURL string    `json:"repository_url"`
}

// Status is an app's release state: its latest release, if any, and whether
// the default branch moved since.
type Status struct {
	Latest     *Version `json:"latest"`
	Unreleased bool     `json:"unreleased"`
}

type record struct {
	Owner    string    `json:"owner"`
	Name     string    `json:"name"`
	Versions []Version `json:"versions"`
}

type Service struct {
	config      Config
	projects    Projects
	toolchains  Toolchains
	visions     Visions
	features    Features
	marketplace Marketplace
	heads       func(ctx context.Context, owner, name, branch string) (string, error)
	now         func() time.Time
	mu          sync.Mutex
}

func NewService(config Config, projects Projects, toolchains Toolchains, visions Visions, features Features,
	market Marketplace, heads func(ctx context.Context, owner, name, branch string) (string, error),
) (*Service, error) {
	if config.GitExecutable == "" {
		config.GitExecutable = "git"
	}
	config.LocalBaseURL = strings.TrimRight(config.LocalBaseURL, "/")
	if config.LocalBaseURL == "" || config.LocalTokenFile == "" || config.LocalOwner == "" || config.StateRoot == "" {
		return nil, errors.New("release needs the local Forgejo and a state root")
	}
	if err := os.MkdirAll(filepath.Join(config.StateRoot, "releases"), 0o700); err != nil {
		return nil, fmt.Errorf("create release state: %w", err)
	}
	return &Service{config: config, projects: projects, toolchains: toolchains, visions: visions, features: features,
		marketplace: market, heads: heads, now: func() time.Time { return time.Now().UTC() }}, nil
}

// Status answers the latest release and whether there is anything new.
func (service *Service) Status(ctx context.Context, projectID string) (Status, error) {
	stored, err := service.projects.GetByID(ctx, projectID)
	if err != nil {
		return Status{}, err
	}
	service.mu.Lock()
	saved, err := service.read(projectID)
	service.mu.Unlock()
	if err != nil {
		return Status{}, err
	}
	status := Status{Unreleased: true}
	if len(saved.Versions) > 0 {
		latest := saved.Versions[len(saved.Versions)-1]
		status.Latest = &latest
		if repository := stored.ForgejoRepository; repository != nil {
			head, headErr := service.heads(ctx, repository.Owner, repository.Name, repository.DefaultBranch)
			status.Unreleased = headErr != nil || head != latest.SourceCommit
		}
	}
	return status, nil
}

// Release puts the app's default branch on the marketplace as the next
// version.
func (service *Service) Release(ctx context.Context, projectID string) (Version, error) {
	stored, err := service.projects.GetByID(ctx, projectID)
	if err != nil {
		return Version{}, err
	}
	if !stored.App {
		return Version{}, ErrNotAnApp
	}
	repository := stored.ForgejoRepository
	if repository == nil || repository.DefaultBranch == "" {
		return Version{}, ErrRepositoryMissing
	}
	files, err := service.portableFiles(ctx, projectID, stored.Name)
	if err != nil {
		return Version{}, err
	}
	credentials, err := service.marketplace.Credentials(ctx)
	if err != nil {
		return Version{}, err
	}

	service.mu.Lock()
	defer service.mu.Unlock()
	saved, err := service.read(projectID)
	if err != nil {
		return Version{}, err
	}
	name := saved.Name
	if name == "" {
		name = repositoryName(stored.Name)
	}
	remote, err := service.marketplace.EnsureAppRepository(ctx, name, stored.Name+marketplace.AppDescriptionSuffix)
	if err != nil {
		return Version{}, err
	}

	work, err := os.MkdirTemp("", "commitarium-release-*")
	if err != nil {
		return Version{}, fmt.Errorf("%w: create a work directory", ErrUnavailable)
	}
	defer os.RemoveAll(work)
	localToken, err := os.ReadFile(service.config.LocalTokenFile)
	if err != nil {
		return Version{}, fmt.Errorf("%w: read the local Forgejo token", ErrUnavailable)
	}
	git := gitRunner{ctx: ctx, executable: service.config.GitExecutable, directory: filepath.Join(work, "repository.git"),
		environment: gitAuthEnvironment(map[string]string{
			service.config.LocalBaseURL + "/": basicAuth(service.config.LocalOwner, strings.TrimSpace(string(localToken))),
			origin(remote.CloneURL):           basicAuth(credentials.Username, credentials.Token),
		})}
	localURL := service.config.LocalBaseURL + "/" + url.PathEscape(repository.Owner) + "/" + url.PathEscape(repository.Name) + ".git"
	if _, err := git.runIn(work, "clone", "--bare", "--single-branch", "--branch", repository.DefaultBranch, "--", localURL, git.directory); err != nil {
		return Version{}, fmt.Errorf("%w: copy the app: %v", ErrUnavailable, err)
	}
	source, err := git.run("rev-parse", "refs/heads/"+repository.DefaultBranch)
	if err != nil {
		return Version{}, fmt.Errorf("%w: read the app's head: %v", ErrUnavailable, err)
	}
	if findings := scanForSecrets(git, source); len(findings) > 0 {
		return Version{}, &SecretError{Findings: findings}
	}

	remoteRefs, err := git.run("ls-remote", "--", remote.CloneURL)
	if err != nil {
		return Version{}, fmt.Errorf("%w: read the marketplace copy: %v", ErrUnavailable, err)
	}
	remoteMain, remoteTags := parseRemoteRefs(remoteRefs)
	parents := []string{}
	if remoteMain != "" {
		if _, err := git.run("fetch", "--no-tags", "--", remote.CloneURL, "refs/heads/main:refs/marketplace/main"); err != nil {
			return Version{}, fmt.Errorf("%w: fetch the marketplace copy: %v", ErrUnavailable, err)
		}
		if _, err := git.run("merge-base", "--is-ancestor", remoteMain, source); err != nil {
			// The marketplace main is not in this app's history: it is either
			// an earlier release, which the new one builds on, or someone
			// else's work, which is never overwritten.
			if !releasedBefore(saved, remoteMain) && !releaseOf(git, remoteMain, source) {
				return Version{}, ErrDiverged
			}
			parents = append(parents, remoteMain)
		}
	}
	parents = append(parents, source)

	tree, err := treeWithFiles(git, source, files)
	if err != nil {
		return Version{}, err
	}
	// Nothing new when the marketplace already has exactly these files: the
	// same code and the same name, stack, and vision.
	if remoteMain != "" {
		if released, err := git.run("rev-parse", remoteMain+"^{tree}"); err == nil && released == tree {
			return Version{}, ErrNothingToRelease
		}
	}
	version := nextVersion(saved, remoteTags)
	identity := []string{"GIT_AUTHOR_NAME=Commitarium", "GIT_AUTHOR_EMAIL=commitarium@local.invalid",
		"GIT_COMMITTER_NAME=Commitarium", "GIT_COMMITTER_EMAIL=commitarium@local.invalid"}
	arguments := []string{"commit-tree", tree}
	for _, parent := range parents {
		arguments = append(arguments, "-p", parent)
	}
	arguments = append(arguments, "-m", "Release "+version)
	commit, err := git.with(identity...).run(arguments...)
	if err != nil {
		return Version{}, fmt.Errorf("%w: make the release commit: %v", ErrUnavailable, err)
	}
	if _, err := git.with(identity...).run("tag", "-a", version, "-m", "Release "+version, commit); err != nil {
		return Version{}, fmt.Errorf("%w: tag the release: %v", ErrUnavailable, err)
	}
	if output, err := git.run("push", "--", remote.CloneURL, commit+":refs/heads/main", "refs/tags/"+version); err != nil {
		if strings.Contains(output, "non-fast-forward") || strings.Contains(output, "rejected") {
			return Version{}, ErrDiverged
		}
		return Version{}, fmt.Errorf("%w: push the release: %v", ErrUnavailable, err)
	}

	released := Version{Version: version, Commit: commit, SourceCommit: source, ReleasedAt: service.now(),
		ReleasedBy: credentials.Username, RepositoryURL: remote.WebURL}
	saved.Owner, saved.Name = remote.Owner, remote.Name
	saved.Versions = append(saved.Versions, released)
	if err := service.write(projectID, saved); err != nil {
		return Version{}, err
	}
	return released, nil
}

// portableFiles answers the .commitarium files (ADR-026). The stack is
// required; an app without a vision releases without one.
func (service *Service) portableFiles(ctx context.Context, projectID, appName string) (map[string][]byte, error) {
	manifest, err := service.toolchains.Get(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if manifest.Status != toolchain.StatusConfigured {
		return nil, ErrStackRequired
	}
	stack, err := toolchain.PortableStack(manifest)
	if err != nil {
		return nil, ErrStackRequired
	}
	app, err := marketplace.EncodeAppManifest(marketplace.AppManifest{Name: appName})
	if err != nil {
		return nil, err
	}
	files := map[string][]byte{toolchain.PortableStackPath: stack, marketplace.AppManifestPath: app}
	if service.visions == nil {
		return files, nil
	}
	stored, err := service.visions.Get(ctx, projectID)
	if errors.Is(err, vision.ErrNotFound) {
		return files, nil
	}
	if err != nil {
		return nil, err
	}
	delivered := map[string]bool{}
	features, err := service.features.List(ctx, projectID)
	if err != nil {
		return nil, err
	}
	for _, item := range features {
		if item.State == feature.StateCompleted {
			delivered[item.ID] = true
		}
	}
	plan, err := vision.PortablePlan(stored.Plan, func(featureID string) bool { return delivered[featureID] })
	if err != nil {
		return nil, err
	}
	files[vision.PortableVisionPath] = plan
	return files, nil
}

// treeWithFiles writes the source tree plus files and answers its ID.
func treeWithFiles(git gitRunner, source string, files map[string][]byte) (string, error) {
	index := git.with("GIT_INDEX_FILE=" + filepath.Join(filepath.Dir(git.directory), "release.index"))
	if _, err := index.run("read-tree", source); err != nil {
		return "", fmt.Errorf("%w: read the app's files: %v", ErrUnavailable, err)
	}
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		file := filepath.Join(filepath.Dir(git.directory), strings.ReplaceAll(path, "/", "_"))
		if err := os.WriteFile(file, files[path], 0o600); err != nil {
			return "", fmt.Errorf("%w: stage %s", ErrUnavailable, path)
		}
		blob, err := git.run("hash-object", "-w", "--", file)
		if err != nil {
			return "", fmt.Errorf("%w: store %s: %v", ErrUnavailable, path, err)
		}
		if _, err := index.run("update-index", "--add", "--cacheinfo", "100644,"+blob+","+path); err != nil {
			return "", fmt.Errorf("%w: add %s: %v", ErrUnavailable, path, err)
		}
	}
	tree, err := index.run("write-tree")
	if err != nil {
		return "", fmt.Errorf("%w: write the release tree: %v", ErrUnavailable, err)
	}
	return tree, nil
}

// secretPattern matches common credential shapes: private keys and the
// token formats of the big providers.
const secretPattern = `-----BEGIN [A-Z ]*PRIVATE KEY-----|AKIA[0-9A-Z]{16}|(^|[^A-Za-z0-9_-])sk-(ant-|proj-)?[A-Za-z0-9_]{32,}|` +
	`gh[pousr]_[A-Za-z0-9]{30,}|xox[abpr]-[A-Za-z0-9-]{10,}|AIza[0-9A-Za-z_-]{35}`

var secretFile = regexp.MustCompile(`(^|/)(\.env(\..+)?|id_rsa|id_ed25519|.+\.pem|.+\.p12|.+\.key)$`)

// scanForSecrets checks every commit that would be pushed, since the whole
// history goes to the marketplace. It answers "file at commit" findings.
func scanForSecrets(git gitRunner, source string) []string {
	findings := []string{}
	seen := map[string]bool{}
	names, _ := git.run("log", "--format=", "--name-only", "--diff-filter=AM", source)
	for _, name := range strings.Split(names, "\n") {
		name = strings.TrimSpace(name)
		if name != "" && !seen[name] && secretFile.MatchString(name) && !strings.HasSuffix(name, ".example") {
			seen[name] = true
			findings = append(findings, name)
		}
	}
	commits, err := git.run("rev-list", source)
	if err != nil {
		return findings
	}
	arguments := append([]string{"grep", "-I", "-l", "-E", "-e", secretPattern}, strings.Fields(commits)...)
	matches, err := git.run(arguments...)
	if err != nil {
		// Exit status 1 means no match; any other failure finds nothing to report.
		matches = ""
	}
	for _, match := range strings.Split(matches, "\n") {
		_, path, ok := strings.Cut(strings.TrimSpace(match), ":")
		if ok && !seen[path] {
			seen[path] = true
			findings = append(findings, path)
		}
	}
	sort.Strings(findings)
	if len(findings) > 5 {
		findings = append(findings[:5], "and more")
	}
	return findings
}

var versionTag = regexp.MustCompile(`^v([1-9][0-9]*)$`)

// nextVersion is one past the highest vN released here or on the
// marketplace.
func nextVersion(saved record, remoteTags []string) string {
	highest := 0
	names := append([]string{}, remoteTags...)
	for _, released := range saved.Versions {
		names = append(names, released.Version)
	}
	for _, name := range names {
		if match := versionTag.FindStringSubmatch(name); match != nil {
			if number, err := strconv.Atoi(match[1]); err == nil && number > highest {
				highest = number
			}
		}
	}
	return "v" + strconv.Itoa(highest+1)
}

func releasedBefore(saved record, commit string) bool {
	for _, released := range saved.Versions {
		if released.Commit == commit {
			return true
		}
	}
	return false
}

// releaseOf reports whether commit is a release of source's history even
// without a record of it: its last parent, the branch it was made from, is
// part of source.
func releaseOf(git gitRunner, commit, source string) bool {
	parents, err := git.run("rev-parse", commit+"^@")
	if err != nil || parents == "" {
		return false
	}
	lines := strings.Split(parents, "\n")
	_, err = git.run("merge-base", "--is-ancestor", strings.TrimSpace(lines[len(lines)-1]), source)
	return err == nil
}

func parseRemoteRefs(output string) (string, []string) {
	main, tags := "", []string{}
	for _, line := range strings.Split(output, "\n") {
		commit, ref, ok := strings.Cut(strings.TrimSpace(line), "\t")
		if !ok {
			continue
		}
		switch {
		case ref == "refs/heads/main":
			main = commit
		case strings.HasPrefix(ref, "refs/tags/") && !strings.HasSuffix(ref, "^{}"):
			tags = append(tags, strings.TrimPrefix(ref, "refs/tags/"))
		}
	}
	return main, tags
}

var unsafeName = regexp.MustCompile(`[^a-z0-9]+`)

// repositoryName is the marketplace repository name for an app name.
func repositoryName(appName string) string {
	name := strings.Trim(unsafeName.ReplaceAllString(strings.ToLower(appName), "-"), "-")
	if len(name) > 60 {
		name = strings.TrimRight(name[:60], "-")
	}
	if name == "" {
		name = "app"
	}
	return name
}

func origin(cloneURL string) string {
	parsed, err := url.Parse(cloneURL)
	if err != nil {
		return cloneURL
	}
	return parsed.Scheme + "://" + parsed.Host + "/"
}

func basicAuth(username, token string) string {
	return "Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte(username+":"+token))
}

// gitAuthEnvironment scopes each credential to its own server, so the local
// token never reaches the marketplace and the other way round.
func gitAuthEnvironment(headers map[string]string) []string {
	environment := []string{"GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1"}
	prefixes := make([]string, 0, len(headers))
	for prefix := range headers {
		prefixes = append(prefixes, prefix)
	}
	sort.Strings(prefixes)
	environment = append(environment, "GIT_CONFIG_COUNT="+strconv.Itoa(len(prefixes)))
	for index, prefix := range prefixes {
		environment = append(environment,
			"GIT_CONFIG_KEY_"+strconv.Itoa(index)+"=http."+prefix+".extraHeader",
			"GIT_CONFIG_VALUE_"+strconv.Itoa(index)+"="+headers[prefix])
	}
	return environment
}

type gitRunner struct {
	ctx         context.Context
	executable  string
	directory   string
	environment []string
}

func (runner gitRunner) with(environment ...string) gitRunner {
	runner.environment = append(append([]string{}, runner.environment...), environment...)
	return runner
}

func (runner gitRunner) run(arguments ...string) (string, error) {
	return runner.runIn(runner.directory, arguments...)
}

func (runner gitRunner) runIn(directory string, arguments ...string) (string, error) {
	command := exec.CommandContext(runner.ctx, runner.executable, arguments...)
	command.Dir = directory
	command.Env = append(os.Environ(), runner.environment...)
	output, err := command.CombinedOutput()
	trimmed := strings.TrimSpace(string(output))
	if err != nil {
		return trimmed, fmt.Errorf("git %s: %w: %s", arguments[0], err, lastLine(trimmed))
	}
	return trimmed, nil
}

func lastLine(output string) string {
	lines := strings.Split(output, "\n")
	return lines[len(lines)-1]
}

func (service *Service) path(projectID string) string {
	return filepath.Join(service.config.StateRoot, "releases", filepath.Base(projectID)+".json")
}

// read answers the project's release record. Callers hold service.mu.
func (service *Service) read(projectID string) (record, error) {
	contents, err := os.ReadFile(service.path(projectID))
	if errors.Is(err, os.ErrNotExist) {
		return record{}, nil
	}
	if err != nil {
		return record{}, fmt.Errorf("%w: read the release record", ErrUnavailable)
	}
	var saved record
	if err := json.Unmarshal(contents, &saved); err != nil {
		return record{}, fmt.Errorf("%w: the release record is unreadable", ErrUnavailable)
	}
	return saved, nil
}

// write replaces the project's release record. Callers hold service.mu.
func (service *Service) write(projectID string, saved record) error {
	encoded, err := json.MarshalIndent(saved, "", "  ")
	if err != nil {
		return err
	}
	temporary := service.path(projectID) + ".tmp"
	if err := os.WriteFile(temporary, encoded, 0o600); err != nil {
		return fmt.Errorf("%w: write the release record", ErrUnavailable)
	}
	if err := os.Rename(temporary, service.path(projectID)); err != nil {
		return fmt.Errorf("%w: write the release record", ErrUnavailable)
	}
	return nil
}
