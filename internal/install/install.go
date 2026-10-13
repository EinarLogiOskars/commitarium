// Package install brings a released app from the marketplace into this
// Commitarium as the user's own copy (MARKETPLACE_PLAN.md). The release is
// fetched with its history and handed to the ordinary project import, so an
// installed app is a project like any other; .commitarium (ADR-026) gives it
// its stack and vision.
package install

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
	"strings"
	"sync"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/marketplace"
)

var ErrUnavailable = errors.New("installing is unavailable")

type Marketplace interface {
	App(ctx context.Context, owner, name string) (marketplace.App, error)
	Credentials(ctx context.Context) (marketplace.Credentials, error)
}

// Origin is where an installed app came from.
type Origin struct {
	ProjectID   string    `json:"project_id"`
	Owner       string    `json:"owner"`
	Name        string    `json:"name"`
	Version     string    `json:"version"`
	InstalledAt time.Time `json:"installed_at"`
}

// Download is a fetched release, ready for the project import, with the
// .commitarium files it carries (ADR-026) keyed by path.
type Download struct {
	App    marketplace.App
	Bundle string
	Files  map[string][]byte
}

// PortableFiles are the .commitarium files an install applies directly, so
// it does not wait for the forge to index the imported repository.
var PortableFiles = []string{".commitarium/stack.json", ".commitarium/vision.json", marketplace.AppManifestPath}

type Service struct {
	market        Marketplace
	gitExecutable string
	root          string
	now           func() time.Time
	mu            sync.Mutex
}

func NewService(market Marketplace, gitExecutable, stateRoot string) (*Service, error) {
	if gitExecutable == "" {
		gitExecutable = "git"
	}
	root := filepath.Join(stateRoot, "installs")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create install state: %w", err)
	}
	return &Service{market: market, gitExecutable: gitExecutable, root: root,
		now: func() time.Time { return time.Now().UTC() }}, nil
}

// Download fetches an app's released history as a Git bundle. The caller
// removes it with the returned function.
func (service *Service) Download(ctx context.Context, owner, name string) (Download, func(), error) {
	app, err := service.market.App(ctx, owner, name)
	if err != nil {
		return Download{}, nil, err
	}
	credentials, err := service.market.Credentials(ctx)
	if err != nil {
		return Download{}, nil, err
	}
	work, err := os.MkdirTemp("", "commitarium-install-*")
	if err != nil {
		return Download{}, nil, fmt.Errorf("%w: create a work directory", ErrUnavailable)
	}
	cleanup := func() { os.RemoveAll(work) }
	repository := filepath.Join(work, "repository.git")
	environment := []string{"GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=http." + origin(app.CloneURL) + ".extraHeader",
		"GIT_CONFIG_VALUE_0=Authorization: Basic " +
			base64.StdEncoding.EncodeToString([]byte(credentials.Username+":"+credentials.Token))}
	run := func(directory string, arguments ...string) error {
		command := exec.CommandContext(ctx, service.gitExecutable, arguments...)
		command.Dir = directory
		command.Env = append(os.Environ(), environment...)
		if output, err := command.CombinedOutput(); err != nil {
			return fmt.Errorf("git %s: %w: %s", arguments[0], err, strings.TrimSpace(string(output)))
		}
		return nil
	}
	if err := run(work, "clone", "--bare", "--branch", "main", "--", app.CloneURL, repository); err != nil {
		cleanup()
		return Download{}, nil, fmt.Errorf("%w: fetch the app: %v", ErrUnavailable, err)
	}
	bundle := filepath.Join(work, "app.bundle")
	if err := run(repository, "bundle", "create", bundle, "--all"); err != nil {
		cleanup()
		return Download{}, nil, fmt.Errorf("%w: package the app: %v", ErrUnavailable, err)
	}
	files := map[string][]byte{}
	for _, path := range PortableFiles {
		command := exec.CommandContext(ctx, service.gitExecutable, "show", "main:"+path)
		command.Dir = repository
		if contents, err := command.Output(); err == nil {
			files[path] = contents
		}
	}
	return Download{App: app, Bundle: bundle, Files: files}, cleanup, nil
}

// Record remembers that projectID is an install of app.
func (service *Service) Record(projectID string, app marketplace.App) error {
	service.mu.Lock()
	defer service.mu.Unlock()
	encoded, err := json.MarshalIndent(Origin{ProjectID: projectID, Owner: app.Owner, Name: app.Name,
		Version: app.Version, InstalledAt: service.now()}, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(service.root, filepath.Base(projectID)+".json")
	if err := os.WriteFile(path+".tmp", encoded, 0o600); err != nil {
		return fmt.Errorf("%w: record the install", ErrUnavailable)
	}
	if err := os.Rename(path+".tmp", path); err != nil {
		return fmt.Errorf("%w: record the install", ErrUnavailable)
	}
	return nil
}

// Installed answers every recorded install.
func (service *Service) Installed() ([]Origin, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	entries, err := os.ReadDir(service.root)
	if err != nil {
		return nil, fmt.Errorf("%w: read installs", ErrUnavailable)
	}
	origins := []Origin{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		contents, err := os.ReadFile(filepath.Join(service.root, entry.Name()))
		if err != nil {
			continue
		}
		var stored Origin
		if json.Unmarshal(contents, &stored) == nil && stored.ProjectID != "" {
			origins = append(origins, stored)
		}
	}
	return origins, nil
}

func origin(cloneURL string) string {
	parsed, err := url.Parse(cloneURL)
	if err != nil {
		return cloneURL
	}
	return parsed.Scheme + "://" + parsed.Host + "/"
}
