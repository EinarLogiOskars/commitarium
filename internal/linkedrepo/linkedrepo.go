// Package linkedrepo keeps a project in step with a repository on GitHub or
// another git host, both ways (SERVER_COMPLETE_PLAN.md, S2). Commits there
// come in as a fast-forward of the project's main; main goes out the same
// way. Nothing is ever forced: when both sides moved, the user decides, and
// Mayka can merge them in a work order.
package linkedrepo

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
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
	// ErrTheirsAhead is a push refused because the linked repository has
	// commits main lacks: those come in first.
	ErrTheirsAhead  = errors.New("the linked repository has new commits; bring those in first")
	ErrNoRepository = errors.New("the project has no repository yet")
)

// The states of the two sides.
const (
	Same        = "same"
	TheirsAhead = "theirs_ahead"
	OursAhead   = "ours_ahead"
	Diverged    = "diverged"
)

// BranchPrefix names the project branch the linked repository's commits
// land on before they reach main, and stay on when both sides moved.
const BranchPrefix = "linked/"

const (
	checkEvery = 5 * time.Minute
	// viewFresh is how long a comparison is shown before the remote is
	// asked again.
	viewFresh = time.Minute
)

// Link is a project's linked repository. Its token stays in the
// coordinator's credentials, out of agents' reach and out of backups.
type Link struct {
	URL       string    `json:"url"`
	Branch    string    `json:"branch"`
	Username  string    `json:"username,omitempty"`
	Token     string    `json:"token,omitempty"`
	Automatic bool      `json:"automatic"`
	PulledAt  time.Time `json:"pulled_at,omitzero"`
	PushedAt  time.Time `json:"pushed_at,omitzero"`
	// LastError is what the last automatic sync ran into.
	LastError string `json:"last_error,omitempty"`
}

// Comparison is how the two sides stand: commits each has that the other
// lacks.
type Comparison struct {
	State  string `json:"state"`
	Theirs int    `json:"theirs"`
	Ours   int    `json:"ours"`
}

// View is a link as the app shows it, without its token.
type View struct {
	URL          string      `json:"url"`
	Branch       string      `json:"branch"`
	LocalBranch  string      `json:"local_branch"`
	HasToken     bool        `json:"has_token"`
	Automatic    bool        `json:"automatic"`
	PulledAt     time.Time   `json:"pulled_at,omitzero"`
	PushedAt     time.Time   `json:"pushed_at,omitzero"`
	LastError    string      `json:"last_error,omitempty"`
	Comparison   *Comparison `json:"comparison,omitempty"`
	CompareError string      `json:"compare_error,omitempty"`
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

	// mu guards the links file and the caches; work holds git work to one
	// project at a time across all.
	mu    sync.Mutex
	work  sync.Mutex
	views map[string]cachedComparison
}

type cachedComparison struct {
	at         time.Time
	comparison Comparison
	err        string
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
		views:      map[string]cachedComparison{},
	}, nil
}

// Get returns the project's link, with how the two sides stand.
func (service *Service) Get(ctx context.Context, projectID string) (View, error) {
	link, err := service.link(projectID)
	if err != nil {
		return View{}, err
	}
	view := View{
		URL: link.URL, Branch: link.Branch, LocalBranch: BranchPrefix + link.Branch,
		HasToken: link.Token != "", Automatic: link.Automatic,
		PulledAt: link.PulledAt, PushedAt: link.PushedAt, LastError: link.LastError,
	}
	service.mu.Lock()
	cached, fresh := service.views[projectID]
	service.mu.Unlock()
	if !fresh || time.Since(cached.at) > viewFresh {
		cached = cachedComparison{at: time.Now()}
		err := service.withSides(ctx, projectID, link, func(sides) error { return nil }, &cached.comparison)
		if err != nil {
			cached.err = err.Error()
		}
		service.mu.Lock()
		service.views[projectID] = cached
		service.mu.Unlock()
	}
	if cached.err != "" {
		view.CompareError = cached.err
	} else {
		view.Comparison = &cached.comparison
	}
	return view, nil
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
	return service.update(projectID, func(links map[string]Link) {
		if old, found := links[projectID]; found && old.URL == link.URL && link.Token == "" {
			link.Token, link.Username = old.Token, old.Username
		}
		links[projectID] = link
	})
}

// Unlink forgets the link and its token. Both repositories keep their code.
func (service *Service) Unlink(projectID string) error {
	return service.update(projectID, func(links map[string]Link) { delete(links, projectID) })
}

// SetAutomatic turns syncing on its own on or off.
func (service *Service) SetAutomatic(projectID string, automatic bool) error {
	if _, err := service.link(projectID); err != nil {
		return err
	}
	return service.update(projectID, func(links map[string]Link) {
		link := links[projectID]
		link.Automatic = automatic
		links[projectID] = link
	})
}

// BringIn brings the linked repository's new commits into main when main
// has none of its own. Either way they land on the project's linked/
// branch, so when both sides moved Mayka can merge them from there.
func (service *Service) BringIn(ctx context.Context, projectID string) (Comparison, error) {
	link, err := service.link(projectID)
	if err != nil {
		return Comparison{}, err
	}
	var comparison Comparison
	err = service.withSides(ctx, projectID, link, func(s sides) error {
		if comparison.State != TheirsAhead && comparison.State != Diverged {
			return nil
		}
		local := BranchPrefix + link.Branch
		if _, err := service.git(ctx, s.dir, s.forgejoEnv, "push", "--force", s.forgejoURL,
			s.theirs+":refs/heads/"+local); err != nil {
			return fmt.Errorf("the commits could not be copied to the project: %s", gitReason(err))
		}
		if comparison.State == Diverged {
			return nil
		}
		err := service.landing.FastForwardBranch(ctx, s.repository.Owner, s.repository.Name,
			s.repository.DefaultBranch, local, s.theirs,
			fmt.Sprintf("Bring in %d commit%s from %s", comparison.Theirs, plural(comparison.Theirs), link.URL))
		if err != nil {
			return err
		}
		comparison = Comparison{State: Same}
		return service.update(projectID, func(links map[string]Link) {
			stored := links[projectID]
			stored.PulledAt = time.Now().UTC()
			links[projectID] = stored
		})
	}, &comparison)
	return comparison, err
}

// Push sends main's new commits to the linked repository, only ever as a
// fast-forward: a repository with commits main lacks refuses it.
func (service *Service) Push(ctx context.Context, projectID string) (Comparison, error) {
	link, err := service.link(projectID)
	if err != nil {
		return Comparison{}, err
	}
	if link.Token == "" {
		return Comparison{}, ErrNoToken
	}
	var comparison Comparison
	err = service.withSides(ctx, projectID, link, func(s sides) error {
		switch comparison.State {
		case Same:
			return nil
		case TheirsAhead, Diverged:
			return ErrTheirsAhead
		}
		// No --force: git refuses anything but a fast-forward, should the
		// repository move between the fetch and the push.
		if _, err := service.git(ctx, s.dir, remoteEnvironment(link), "push", link.URL,
			s.ours+":refs/heads/"+link.Branch); err != nil {
			if strings.Contains(err.Error(), "fetch first") || strings.Contains(err.Error(), "non-fast-forward") {
				return ErrTheirsAhead
			}
			return fmt.Errorf("the linked repository refused the push: %s", gitReason(err))
		}
		comparison = Comparison{State: Same}
		return service.update(projectID, func(links map[string]Link) {
			stored := links[projectID]
			stored.PushedAt = time.Now().UTC()
			links[projectID] = stored
		})
	}, &comparison)
	return comparison, err
}

// KeepInStep syncs the projects set to sync on their own, every few
// minutes, until ctx ends: their commits come in, main's go out. When both
// moved it does nothing; the user sees that and decides.
func (service *Service) KeepInStep(ctx context.Context) {
	ticker := time.NewTicker(checkEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		links, err := service.load()
		if err != nil {
			continue
		}
		for projectID, link := range links {
			if !link.Automatic {
				continue
			}
			problem := ""
			comparison, err := service.BringIn(ctx, projectID)
			if err == nil && comparison.State == Same && link.Token != "" {
				_, err = service.Push(ctx, projectID)
			}
			if err != nil {
				problem = err.Error()
			} else if comparison.State == Diverged {
				problem = "Both sides have new commits; ask Mayka to merge them."
			}
			if problem != link.LastError {
				if err := service.update(projectID, func(links map[string]Link) {
					stored, found := links[projectID]
					if found {
						stored.LastError = problem
						links[projectID] = stored
					}
				}); err != nil {
					log.Printf("linked repository %s: %v", projectID, err)
				}
			}
		}
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
}

// withSides fetches main and the linked branch, compares them into
// comparison, then runs work, one project at a time.
func (service *Service) withSides(
	ctx context.Context, projectID string, link Link, work func(sides) error, comparison *Comparison,
) error {
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
	ours, _ := strconv.Atoi(fields[0])
	theirs, _ := strconv.Atoi(fields[1])
	*comparison = Comparison{State: state(ours, theirs), Ours: ours, Theirs: theirs}
	err = work(s)
	service.mu.Lock()
	delete(service.views, projectID)
	if err == nil {
		service.views[projectID] = cachedComparison{at: time.Now(), comparison: *comparison}
	}
	service.mu.Unlock()
	return err
}

func state(ours, theirs int) string {
	switch {
	case ours == 0 && theirs == 0:
		return Same
	case ours == 0:
		return TheirsAhead
	case theirs == 0:
		return OursAhead
	default:
		return Diverged
	}
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
func (service *Service) update(projectID string, change func(map[string]Link)) error {
	service.mu.Lock()
	defer service.mu.Unlock()
	links, err := service.read()
	if err != nil {
		return err
	}
	change(links)
	delete(service.views, projectID)
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
