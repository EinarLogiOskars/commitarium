package toolchain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/project"
)

type ProjectReader interface {
	GetByID(context.Context, string) (project.Project, error)
	GetRepositoryOverview(context.Context, string) (project.RepositoryOverview, error)
	ReadRepositoryBlob(context.Context, string, string, int64) ([]byte, error)
	GetRepositoryToolchainEvidence(context.Context, string) (project.RepositoryToolchainEvidence, error)
}

type projectDeletionReader interface {
	ProjectDeletionPending(context.Context, string) (bool, error)
}

func ensureProjectNotDeleting(ctx context.Context, projects ProjectReader, projectID string) error {
	reader, ok := projects.(projectDeletionReader)
	if !ok {
		return nil
	}
	pending, err := reader.ProjectDeletionPending(ctx, projectID)
	if err != nil {
		return err
	}
	if pending {
		return project.ErrNotFound
	}
	return nil
}

type Manager struct {
	root     string
	projects ProjectReader
	now      func() time.Time
	mu       sync.Mutex
}

func NewManager(root string, projects ProjectReader) (*Manager, error) {
	root = filepath.Clean(strings.TrimSpace(root))
	if !filepath.IsAbs(root) || projects == nil {
		return nil, fmt.Errorf("%w: absolute root and project reader are required", ErrUnavailable)
	}
	if err := os.MkdirAll(filepath.Join(root, "projects"), 0o700); err != nil {
		return nil, fmt.Errorf("%w: create storage root: %v", ErrUnavailable, err)
	}
	return &Manager{root: root, projects: projects, now: func() time.Time { return time.Now().UTC() }}, nil
}

func (manager *Manager) Get(ctx context.Context, projectID string) (Manifest, error) {
	stored, err := manager.projects.GetByID(ctx, projectID)
	if err != nil {
		return Manifest{}, err
	}
	// All toolchain files are atomically replaced. Reads intentionally avoid the
	// install lock so the API can expose installing/failed state while a worker
	// holds that lock for a long-running runtime download.
	return manager.get(stored.ID)
}

func (manager *Manager) get(projectID string) (Manifest, error) {
	configPath := filepath.Join(manager.projectDirectory(projectID), "mise.toml")
	configuredTools, configErr := ReadGeneratedConfig(configPath)
	if configErr != nil {
		return Manifest{}, fmt.Errorf("%w: read generated config", ErrUnavailable)
	}
	provisioning, provisioningErr := ReadProvisioningState(configPath)
	if provisioningErr != nil {
		return Manifest{}, fmt.Errorf("%w: read provisioning state", ErrUnavailable)
	}
	path := manager.manifestPath(projectID)
	contents, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		if len(configuredTools) > 0 {
			return Manifest{ProjectID: projectID, Status: StatusConfigured, Source: SourceRuntime,
				Tools: configuredTools, Services: []string{}, ServicesRunnable: false,
				ProvisioningStatus: provisioning.Status, ProvisioningMessage: provisioning.Message}, nil
		}
		return Manifest{ProjectID: projectID, Status: StatusNeedsSetup, Tools: map[string]string{},
			Services: []string{}, ServicesRunnable: false}, nil
	}
	if err != nil {
		return Manifest{}, fmt.Errorf("%w: read manifest", ErrUnavailable)
	}
	var manifest Manifest
	if err := json.Unmarshal(contents, &manifest); err != nil {
		return Manifest{}, fmt.Errorf("%w: decode manifest", ErrUnavailable)
	}
	normalized, err := NormalizeManifest(manifest)
	if err != nil || normalized.ProjectID != projectID {
		return Manifest{}, fmt.Errorf("%w: stored manifest is invalid", ErrUnavailable)
	}
	if len(configuredTools) == 0 {
		return Manifest{}, fmt.Errorf("%w: generated config is missing", ErrUnavailable)
	}
	normalized.Tools = configuredTools
	normalized.ProvisioningStatus = provisioning.Status
	normalized.ProvisioningMessage = provisioning.Message
	normalized.UpdatedAt = manifest.UpdatedAt
	return normalized, nil
}

func (manager *Manager) Configure(ctx context.Context, projectID string, manifest Manifest) (Manifest, error) {
	if err := ensureProjectNotDeleting(ctx, manager.projects, projectID); err != nil {
		return Manifest{}, err
	}
	stored, err := manager.projects.GetByID(ctx, projectID)
	if err != nil {
		return Manifest{}, err
	}
	manifest.ProjectID = stored.ID
	manifest.UpdatedAt = manager.now()
	normalized, err := NormalizeManifest(manifest)
	if err != nil {
		return Manifest{}, err
	}
	normalized.UpdatedAt = manifest.UpdatedAt.UTC()
	manager.mu.Lock()
	defer manager.mu.Unlock()
	err = manager.withProjectLock(stored.ID, func() error {
		directory := manager.projectDirectory(stored.ID)
		if writeErr := WriteGeneratedConfig(filepath.Join(directory, "mise.toml"), normalized.Tools); writeErr != nil {
			return fmt.Errorf("%w: write generated config: %v", ErrUnavailable, writeErr)
		}
		if writeErr := WriteProvisioningState(filepath.Join(directory, "mise.toml"), ProvisioningState{
			Status: ProvisioningPending, Message: "Runtime installation has not started.", UpdatedAt: normalized.UpdatedAt,
		}); writeErr != nil {
			return fmt.Errorf("%w: reset provisioning state: %v", ErrUnavailable, writeErr)
		}
		encoded, encodeErr := json.Marshal(normalized)
		if encodeErr != nil {
			return fmt.Errorf("%w: encode manifest", ErrUnavailable)
		}
		if writeErr := atomicWrite(filepath.Join(directory, "manifest.json"), append(encoded, '\n'), 0o600); writeErr != nil {
			return fmt.Errorf("%w: write manifest: %v", ErrUnavailable, writeErr)
		}
		return nil
	})
	if err != nil {
		return Manifest{}, err
	}
	normalized.ProvisioningStatus = ProvisioningPending
	normalized.ProvisioningMessage = "Runtime installation has not started."
	return normalized, nil
}

// DeleteProject removes only one project's generated configuration. Shared
// mise data, cache, and state directories are siblings of projects/ and are
// never named by this operation.
func (manager *Manager) DeleteProject(ctx context.Context, projectID string) error {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	directory := manager.projectDirectory(projectID)
	projectsRoot := filepath.Join(manager.root, "projects")
	if strings.TrimSpace(projectID) == "" || filepath.Dir(directory) != projectsRoot || directory == projectsRoot {
		return fmt.Errorf("%w: project identity is unsafe", ErrUnavailable)
	}
	info, err := os.Lstat(directory)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil
	case err != nil:
		return fmt.Errorf("%w: inspect project toolchain directory", ErrUnavailable)
	case info.Mode()&os.ModeSymlink != 0 || !info.IsDir():
		return fmt.Errorf("%w: project toolchain path is unsafe", ErrUnavailable)
	}
	// Provisioning in worker containers takes this same file lock.
	return manager.withProjectLock(projectID, func() error {
		if err := os.RemoveAll(directory); err != nil {
			return fmt.Errorf("%w: remove project toolchain directory", ErrUnavailable)
		}
		return nil
	})
}

func (manager *Manager) Detect(ctx context.Context, projectID string) (Suggestion, error) {
	if _, err := manager.projects.GetByID(ctx, projectID); err != nil {
		return Suggestion{}, err
	}
	overview, err := manager.projects.GetRepositoryOverview(ctx, projectID)
	if err != nil {
		return Suggestion{}, err
	}
	files := make(map[string]project.RepositoryTreeEntry, len(overview.Tree))
	for _, entry := range overview.Tree {
		files[strings.ToLower(entry.Path)] = entry
	}
	suggestion := Suggestion{Tools: map[string]string{}, Services: []string{}, Evidence: []string{}, Confidence: "low"}
	detect := func(tool, version string, names ...string) {
		for _, name := range names {
			if _, exists := files[name]; exists {
				suggestion.Tools[tool] = version
				suggestion.Evidence = append(suggestion.Evidence, name)
				return
			}
		}
	}
	detect("python", "3.14.7", "pyproject.toml", "requirements.txt", "setup.py", "poetry.lock", "uv.lock")
	detect("node", "24.21.0", "package.json", "package-lock.json", "pnpm-lock.yaml", "yarn.lock")
	detect("java", "temurin-21.0.12+8.0.LTS", "pom.xml", "mvnw", "build.gradle", "build.gradle.kts", "settings.gradle", "settings.gradle.kts", "gradlew")
	detect("maven", "3.9.16", "pom.xml", "mvnw")
	detect("gradle", "9.7.1", "build.gradle", "build.gradle.kts", "settings.gradle", "settings.gradle.kts", "gradlew")
	detect("go", "1.27.1", "go.mod", "go.work")
	detect("rust", "1.98.1", "cargo.toml", "cargo.lock")
	if entry, exists := files["mise.toml"]; exists {
		suggestion.Evidence = append(suggestion.Evidence, "mise.toml (read as detection input only)")
		if entry.Type == "file" && entry.Size <= project.RepositoryDetectionFileMaxBytes {
			contents, readErr := manager.projects.ReadRepositoryBlob(ctx, projectID, entry.BlobID, project.RepositoryDetectionFileMaxBytes)
			if readErr == nil {
				for tool, version := range DetectToolsFromMise(contents) {
					suggestion.Tools[tool] = version
				}
			}
		}
	}
	if entry, exists := files[".tool-versions"]; exists && entry.Type == "file" &&
		entry.Size <= project.RepositoryDetectionFileMaxBytes {
		contents, readErr := manager.projects.ReadRepositoryBlob(ctx, projectID, entry.BlobID, project.RepositoryDetectionFileMaxBytes)
		if readErr == nil {
			suggestion.Evidence = append(suggestion.Evidence, ".tool-versions")
			for tool, version := range DetectToolsFromToolVersions(contents) {
				suggestion.Tools[tool] = version
			}
		}
	}
	if len(suggestion.Tools) > 0 {
		suggestion.Confidence = "high"
	}
	sort.Strings(suggestion.Evidence)
	return suggestion, nil
}

func (manager *Manager) projectDirectory(projectID string) string {
	return filepath.Join(manager.root, "projects", projectID)
}

func (manager *Manager) manifestPath(projectID string) string {
	return filepath.Join(manager.projectDirectory(projectID), "manifest.json")
}

func (manager *Manager) withProjectLock(projectID string, action func() error) error {
	directory := manager.projectDirectory(projectID)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("%w: create project toolchain directory", ErrUnavailable)
	}
	lock, err := os.OpenFile(filepath.Join(directory, "mise.toml.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("%w: open project toolchain lock", ErrUnavailable)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("%w: lock project toolchain", ErrUnavailable)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	return action()
}
