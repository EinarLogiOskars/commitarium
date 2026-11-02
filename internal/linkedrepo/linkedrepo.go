// Package linkedrepo links a project to a repository on GitHub or another
// git host (SERVER_COMPLETE_PLAN.md, S2). Both ways are the user's to start:
// Pull brings the repository's commits into main, as a fast-forward, or onto
// a branch Mayka merges when main has commits of its own. Push sends main to
// a branch the user names there, for a pull request the user opens. Nothing is ever
// forced, and nothing syncs on its own.
package linkedrepo

import (
	"bytes"
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/project"
)

var (
	ErrNotLinked = errors.New("the project is not linked to a repository")
	// ErrInvalidLink is an address that is not a plain https repository, or
	// one that could not be reached with the token given.
	ErrInvalidLink = errors.New("invalid linked repository")
	ErrNoToken     = errors.New("pushing needs an access token")
	// ErrInvalidBranch is a push branch git would not take, main itself, or
	// one there with commits main lacks.
	ErrInvalidBranch = errors.New("invalid branch")
	ErrNoRepository  = errors.New("the project has no repository yet")
)

// What a pull did.
const (
	UpToDate = "up_to_date"
	PulledIn = "pulled"
	// NeedsMerge is a pull that left the commits on the linked branch,
	// since main has commits of its own: Mayka merges them.
	NeedsMerge = "needs_merge"
)

// BranchPrefix names the project branch the linked repository's commits
// land on before they reach main, and stay on when main has its own.
const BranchPrefix = "linked/"

// DefaultPushBranch is the branch Push offers before the user has named one.
const DefaultPushBranch = "commitarium"

// Link is a project's linked repository. Its token stays in the
// coordinator's credentials, out of agents' reach and out of backups.
type Link struct {
	URL      string    `json:"url"`
	Branch   string    `json:"branch"`
	Username string    `json:"username,omitempty"`
	Token    string    `json:"token,omitempty"`
	PulledAt time.Time `json:"pulled_at,omitzero"`
	PushedAt time.Time `json:"pushed_at,omitzero"`
	// PushBranch is the branch the user last pushed to.
	PushBranch string `json:"push_branch,omitempty"`
}

// View is a link as the app shows it, without its token.
type View struct {
	URL         string    `json:"url"`
	Branch      string    `json:"branch"`
	LocalBranch string    `json:"local_branch"`
	HasToken    bool      `json:"has_token"`
	PulledAt    time.Time `json:"pulled_at,omitzero"`
	PushedAt    time.Time `json:"pushed_at,omitzero"`
	// PushBranch is the branch Push offers: the last one, or a default.
	PushBranch string `json:"push_branch"`
}

// Pulled is what a pull did, and how many commits it brought.
type Pulled struct {
	Outcome     string `json:"outcome"`
	Commits     int    `json:"commits"`
	LocalBranch string `json:"local_branch"`
}

// Pushed is the branch a push went to, and where to open its pull request.
// No commits means main had nothing the repository lacks.
type Pushed struct {
	Branch      string `json:"branch"`
	Commits     int    `json:"commits"`
	PullRequest string `json:"pull_request_url,omitempty"`
}

type Projects interface {
	GetByID(ctx context.Context, id string) (project.Project, error)
}

// Landing moves a protected branch forward to a commit already pushed to
// head.
type Landing interface {
	FastForwardBranch(ctx context.Context, owner, repository, base, head, commit, title string) error
}

type Config struct {
	// CredentialsRoot holds the links file.
	CredentialsRoot  string
	ForgejoURL       string
	ForgejoTokenFile string
	// CacheRoot holds a bare repository per project the two sides are
	// fetched into; it can be lost at any time.
	CacheRoot string
	Projects  Projects
	Landing   Landing
}

type Service struct {
	path       string
	forgejoURL string
	tokenFile  string
	cacheRoot  string
	projects   Projects
	landing    Landing
	git        func(ctx context.Context, dir string, env []string, args ...string) (string, error)

	// mu guards the links file; work holds git work to one project at a
	// time across all.
	mu   sync.Mutex
	work sync.Mutex
}

func NewService(config Config) (*Service, error) {
	if config.CredentialsRoot == "" || config.ForgejoURL == "" || config.ForgejoTokenFile == "" ||
		config.Projects == nil || config.Landing == nil {
		return nil, errors.New("linked repositories need credentials, Forgejo, projects and a landing")
	}
	cacheRoot := config.CacheRoot
	if cacheRoot == "" {
		cacheRoot = filepath.Join(os.TempDir(), "commitarium-linked")
	}
	return &Service{
		path:       filepath.Join(config.CredentialsRoot, "linked-repositories.json"),
		forgejoURL: strings.TrimRight(config.ForgejoURL, "/"),
		tokenFile:  config.ForgejoTokenFile,
		cacheRoot:  cacheRoot,
		projects:   config.Projects,
		landing:    config.Landing,
		git:        runGit,
	}, nil
}

// Get returns the project's link.
func (service *Service) Get(projectID string) (View, error) {
	link, err := service.link(projectID)
	if err != nil {
		return View{}, err
	}
	return View{
		URL: link.URL, Branch: link.Branch, LocalBranch: BranchPrefix + link.Branch,
		HasToken: link.Token != "", PulledAt: link.PulledAt, PushedAt: link.PushedAt,
		PushBranch: cmp.Or(link.PushBranch, DefaultPushBranch),
	}, nil
}

// Link links the project to the repository at rawURL, on its default
// branch. It is reached first, so a wrong address or token fails here. A
// public repository needs no token to bring commits in, but one to push.
func (service *Service) Link(ctx context.Context, projectID, rawURL, username, token string) error {
	remoteURL, err := validURL(rawURL)
	if err != nil {
		return err
	}
	if _, err := service.repository(ctx, projectID); err != nil {
		return err
	}
	link := Link{URL: remoteURL, Username: strings.TrimSpace(username), Token: strings.TrimSpace(token)}
	head, err := service.git(ctx, "", remoteEnvironment(link), "ls-remote", "--symref", "--", remoteURL, "HEAD")
	if err != nil {
		return fmt.Errorf("%w: the repository could not be reached; check its address, and give a token for a private one",
			ErrInvalidLink)
	}
	link.Branch = defaultBranch(head)
	if link.Branch == "" {
		return fmt.Errorf("%w: the repository has no default branch yet", ErrInvalidLink)
	}
	return service.update(func(links map[string]Link) {
		if old, found := links[projectID]; found && old.URL == link.URL && link.Token == "" {
			link.Token, link.Username = old.Token, old.Username
		}
		links[projectID] = link
	})
}

// Unlink forgets the link and its token. Both repositories keep their code.
func (service *Service) Unlink(projectID string) error {
	return service.update(func(links map[string]Link) { delete(links, projectID) })
}

// Pull brings the linked repository's new commits into main when main has
// none of its own. Either way they land on the project's linked/ branch,
// so when main has its own Mayka can merge them from there.
func (service *Service) Pull(ctx context.Context, projectID string) (Pulled, error) {
	link, err := service.link(projectID)
	if err != nil {
		return Pulled{}, err
	}
	local := BranchPrefix + link.Branch
	pulled := Pulled{Outcome: UpToDate, LocalBranch: local}
	err = service.withSides(ctx, projectID, link, func(s sides) error {
		pulled.Commits = s.theirsOnly
		if s.theirsOnly == 0 {
			return nil
		}
		if _, err := service.git(ctx, s.dir, s.forgejoEnv, "push", "--force", s.forgejoURL,
			s.theirs+":refs/heads/"+local); err != nil {
			return fmt.Errorf("the commits could not be copied to the project: %s", gitReason(err))
		}
		if s.oursOnly > 0 {
			pulled.Outcome = NeedsMerge
		} else {
			err := service.landing.FastForwardBranch(ctx, s.repository.Owner, s.repository.Name,
				s.repository.DefaultBranch, local, s.theirs,
				fmt.Sprintf("Pull %d commit%s from %s", s.theirsOnly, plural(s.theirsOnly), link.URL))
			if err != nil {
				return err
			}
			pulled.Outcome = PulledIn
		}
		return service.update(func(links map[string]Link) {
			stored := links[projectID]
			stored.PulledAt = time.Now().UTC()
			links[projectID] = stored
		})
	})
	return pulled, err
}

// Push sends main to the branch the user names on the linked repository,
// for a pull request into its main there. The branch is made, or moved
// forward; never forced, and never the main branch itself, which changes
// only through that pull request.
func (service *Service) Push(ctx context.Context, projectID, branch string) (Pushed, error) {
	link, err := service.link(projectID)
	if err != nil {
		return Pushed{}, err
	}
	if link.Token == "" {
		return Pushed{}, ErrNoToken
	}
	branch = strings.TrimSpace(branch)
	if branch == link.Branch {
		return Pushed{}, fmt.Errorf("%w: push to a branch of its own; %s changes through a pull request",
			ErrInvalidBranch, link.Branch)
	}
	if _, err := service.git(ctx, "", nil, "check-ref-format", "--branch", branch); err != nil ||
		branch == "" || strings.HasPrefix(branch, "-") {
		return Pushed{}, fmt.Errorf("%w: %q is not a branch name git takes", ErrInvalidBranch, branch)
	}
	pushed := Pushed{Branch: branch}
	err = service.withSides(ctx, projectID, link, func(s sides) error {
		pushed.Commits = s.oursOnly
		if s.oursOnly == 0 {
			return nil
		}
		// No --force: a branch there with commits main lacks refuses it.
		if _, err := service.git(ctx, s.dir, remoteEnvironment(link), "push", link.URL,
			s.ours+":refs/heads/"+branch); err != nil {
			if strings.Contains(err.Error(), "fetch first") || strings.Contains(err.Error(), "non-fast-forward") {
				return fmt.Errorf("%w: the branch %s there has commits main lacks; choose another name",
					ErrInvalidBranch, branch)
			}
			return fmt.Errorf("the linked repository refused the push: %s", gitReason(err))
		}
		pushed.PullRequest = pullRequestURL(link.URL, link.Branch, branch)
		return service.update(func(links map[string]Link) {
			stored := links[projectID]
			stored.PushedAt = time.Now().UTC()
			stored.PushBranch = branch
			links[projectID] = stored
		})
	})
	return pushed, err
}

// pullRequestURL is the page where the host opens a pull request from
// branch into base: GitHub's and GitLab's own, and Forgejo's and Gitea's
// compare page for any other.
func pullRequestURL(remoteURL, base, branch string) string {
	parsed, err := url.Parse(strings.TrimSuffix(remoteURL, ".git"))
	if err != nil {
		return ""
	}
	web := parsed.Scheme + "://" + parsed.Host + strings.TrimSuffix(parsed.Path, "/")
	switch parsed.Host {
	case "github.com":
		return web + "/compare/" + base + "..." + branch + "?expand=1"
	case "gitlab.com":
		return web + "/-/merge_requests/new?merge_request%5Bsource_branch%5D=" + url.QueryEscape(branch) +
			"&merge_request%5Btarget_branch%5D=" + url.QueryEscape(base)
	default:
		return web + "/compare/" + base + "..." + branch
	}
}

// sides is the cache repository with both sides fetched into it.
type sides struct {
	dir        string
	repository project.ForgejoRepository
	forgejoURL string
	forgejoEnv []string
	ours       string
	theirs     string
	// oursOnly and theirsOnly count the commits each side has that the
	// other lacks.
	oursOnly   int
	theirsOnly int
}

// withSides fetches main and the linked branch, compares them, then runs
// work, one project at a time.
func (service *Service) withSides(ctx context.Context, projectID string, link Link, work func(sides) error) error {
	repository, err := service.repository(ctx, projectID)
	if err != nil {
		return err
	}
	token, err := os.ReadFile(service.tokenFile)
	if err != nil {
		return fmt.Errorf("%w: the Forgejo token cannot be read", project.ErrForgejoUnavailable)
	}
	service.work.Lock()
	defer service.work.Unlock()
	s := sides{
		dir:        filepath.Join(service.cacheRoot, projectID+".git"),
		repository: repository,
		forgejoURL: service.forgejoURL + "/" + url.PathEscape(repository.Owner) + "/" +
			url.PathEscape(repository.Name) + ".git",
		forgejoEnv: authEnvironment(repository.Owner, strings.TrimSpace(string(token))),
	}
	if _, err := os.Stat(filepath.Join(s.dir, "HEAD")); err != nil {
		if err := os.MkdirAll(s.dir, 0o700); err != nil {
			return fmt.Errorf("prepare the comparison: %w", err)
		}
		if _, err := service.git(ctx, s.dir, nil, "init", "--bare", "--quiet"); err != nil {
			return fmt.Errorf("prepare the comparison: %s", gitReason(err))
		}
	}
	if _, err := service.git(ctx, s.dir, s.forgejoEnv, "fetch", "--quiet", "--no-tags", s.forgejoURL,
		"+refs/heads/"+repository.DefaultBranch+":refs/ours"); err != nil {
		return fmt.Errorf("the project's main could not be read: %s", gitReason(err))
	}
	if _, err := service.git(ctx, s.dir, remoteEnvironment(link), "fetch", "--quiet", "--no-tags", link.URL,
		"+refs/heads/"+link.Branch+":refs/theirs"); err != nil {
		return fmt.Errorf("the linked repository could not be read: %s", gitReason(err))
	}
	if s.ours, err = service.git(ctx, s.dir, nil, "rev-parse", "refs/ours"); err != nil {
		return fmt.Errorf("the project's main could not be read: %s", gitReason(err))
	}
	if s.theirs, err = service.git(ctx, s.dir, nil, "rev-parse", "refs/theirs"); err != nil {
		return fmt.Errorf("the linked repository could not be read: %s", gitReason(err))
	}
	counts, err := service.git(ctx, s.dir, nil, "rev-list", "--left-right", "--count", "refs/ours...refs/theirs")
	fields := strings.Fields(counts)
	if err != nil || len(fields) != 2 {
		return fmt.Errorf("the two sides could not be compared: %s", gitReason(err))
	}
	s.oursOnly, _ = strconv.Atoi(fields[0])
	s.theirsOnly, _ = strconv.Atoi(fields[1])
	return work(s)
}

func (service *Service) repository(ctx context.Context, projectID string) (project.ForgejoRepository, error) {
	found, err := service.projects.GetByID(ctx, projectID)
	if err != nil {
		return project.ForgejoRepository{}, err
	}
	if found.ForgejoRepository == nil {
		return project.ForgejoRepository{}, ErrNoRepository
	}
	return *found.ForgejoRepository, nil
}

func (service *Service) link(projectID string) (Link, error) {
	links, err := service.load()
	if err != nil {
		return Link{}, err
	}
	link, found := links[projectID]
	if !found {
		return Link{}, ErrNotLinked
	}
	return link, nil
}

func (service *Service) load() (map[string]Link, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	return service.read()
}

// read reads the links file. Callers hold mu.
func (service *Service) read() (map[string]Link, error) {
	links := map[string]Link{}
	data, err := os.ReadFile(service.path)
	if errors.Is(err, os.ErrNotExist) {
		return links, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the linked repositories: %w", err)
	}
	if err := json.Unmarshal(data, &links); err != nil {
		return nil, fmt.Errorf("read the linked repositories: %w", err)
	}
	return links, nil
}

// update changes the links file atomically, kept to the coordinator alone.
func (service *Service) update(change func(map[string]Link)) error {
	service.mu.Lock()
	defer service.mu.Unlock()
	links, err := service.read()
	if err != nil {
		return err
	}
	change(links)
	encoded, err := json.MarshalIndent(links, "", "  ")
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(service.path), ".linked-*.tmp")
	if err != nil {
		return fmt.Errorf("store the linked repository: %w", err)
	}
	defer os.Remove(temporary.Name())
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return fmt.Errorf("store the linked repository: %w", err)
	}
	if _, err := temporary.Write(encoded); err != nil {
		temporary.Close()
		return fmt.Errorf("store the linked repository: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("store the linked repository: %w", err)
	}
	return os.Rename(temporary.Name(), service.path)
}

// validURL accepts an https URL of a repository, without credentials:
// those go in the token.
func validURL(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil ||
		len(strings.Trim(parsed.Path, "/")) == 0 {
		return "", fmt.Errorf("%w: give the repository's https address, such as https://github.com/you/app.git",
			ErrInvalidLink)
	}
	return trimmed, nil
}

// defaultBranch reads the branch HEAD points at from ls-remote --symref.
func defaultBranch(output string) string {
	for _, line := range strings.Split(output, "\n") {
		if target, found := strings.CutPrefix(line, "ref: refs/heads/"); found {
			branch, _, _ := strings.Cut(target, "\t")
			return strings.TrimSpace(branch)
		}
	}
	return ""
}

func remoteEnvironment(link Link) []string {
	username := link.Username
	if link.Token != "" && username == "" {
		// GitHub and GitLab take any name beside a token.
		username = "x-access-token"
	}
	return authEnvironment(username, link.Token)
}

// authEnvironment signs git in with a header, so the token is in no URL,
// command line or config file.
func authEnvironment(username, token string) []string {
	environment := []string{"GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1"}
	if token != "" {
		credential := base64.StdEncoding.EncodeToString([]byte(username + ":" + token))
		environment = append(environment,
			"GIT_CONFIG_COUNT=1",
			"GIT_CONFIG_KEY_0=http.extraHeader",
			"GIT_CONFIG_VALUE_0=Authorization: Basic "+credential,
		)
	}
	return environment
}

// gitError carries git's own words.
type gitError struct {
	output string
	err    error
}

func (e gitError) Error() string { return e.output }

func runGit(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, "git", args...)
	command.Dir = dir
	command.Env = append(os.Environ(), env...)
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Run(); err != nil {
		return "", gitError{output: strings.TrimSpace(output.String()), err: err}
	}
	return strings.TrimSpace(output.String()), nil
}

// gitReason is the last line git said, which names the trouble.
func gitReason(err error) string {
	if err == nil {
		return "no answer"
	}
	lines := strings.Split(strings.TrimSpace(err.Error()), "\n")
	return lines[len(lines)-1]
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
