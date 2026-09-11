package project

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

var ErrNameRequired = errors.New("project name is required")

type Service struct {
	store              Store
	repositoryVerifier RepositoryVerifier
	repositoryImporter RepositoryImporter
	importMu           sync.Mutex
	generateID         func() string
	now                func() time.Time
}

func NewService(store Store) *Service {
	return NewServiceWithRepositoryVerifier(store, unavailableRepositoryVerifier{})
}

func NewServiceWithRepositoryVerifier(
	store Store,
	verifier RepositoryVerifier,
) *Service {
	if verifier == nil {
		verifier = unavailableRepositoryVerifier{}
	}
	return newService(store, verifier, unavailableRepositoryImporter{})
}

func NewServiceWithRepositoryVerifierAndImporter(
	store Store,
	verifier RepositoryVerifier,
	importer RepositoryImporter,
) *Service {
	if importer == nil {
		importer = unavailableRepositoryImporter{}
	}
	return newService(store, verifier, importer)
}

func newService(store Store, verifier RepositoryVerifier, importer RepositoryImporter) *Service {
	if verifier == nil {
		verifier = unavailableRepositoryVerifier{}
	}
	return &Service{
		store:              store,
		repositoryVerifier: verifier,
		repositoryImporter: importer,
		generateID: func() string {
			return "prj_" + rand.Text()
		},
		now: func() time.Time {
			return time.Now().UTC()
		},
	}
}

func (s *Service) Import(ctx context.Context, spec ImportSpec, bundle io.Reader) (Project, bool, error) {
	normalized, err := normalizeImportSpec(spec)
	if err != nil {
		return Project{}, false, err
	}
	if bundle == nil {
		return Project{}, false, ErrInvalidGitBundle
	}
	bundlePath, digest, err := writeImportBundle(bundle)
	if err != nil {
		return Project{}, false, err
	}
	defer os.Remove(bundlePath)

	s.importMu.Lock()
	defer s.importMu.Unlock()
	repositorySpec := RepositoryImportSpec{
		ImportID:      normalized.ImportID,
		Repository:    importRepositoryName(normalized.Name, normalized.ImportID),
		DefaultBranch: normalized.DefaultBranch,
		BundleDigest:  digest,
	}
	projectID := projectImportID(normalized.ImportID)
	stored, err := s.store.GetByID(ctx, projectID)
	if err == nil {
		repository, verifyErr := s.repositoryImporter.Verify(ctx, repositorySpec)
		if verifyErr != nil {
			return Project{}, false, fmt.Errorf("verify completed project import: %w", verifyErr)
		}
		if !sameImportedProject(stored, normalized, repository) {
			return Project{}, false, ErrImportConflict
		}
		return stored, false, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return Project{}, false, fmt.Errorf("find project import: %w", err)
	}
	repository, err := s.repositoryImporter.Import(ctx, repositorySpec, bundlePath)
	if err != nil {
		return Project{}, false, fmt.Errorf("import Forgejo repository: %w", err)
	}
	if !strings.EqualFold(repository.Name, repositorySpec.Repository) ||
		repository.DefaultBranch != repositorySpec.DefaultBranch {
		return Project{}, false, ErrImportConflict
	}
	repository.BoundAt = s.now().UTC()
	if err := repository.Validate(); err != nil {
		return Project{}, false, fmt.Errorf("validate imported repository: %w", ErrImportConflict)
	}
	created := Project{
		ID: projectID, Name: normalized.Name, RecoveryPolicy: normalized.RecoveryPolicy,
		MergePolicy:    normalized.MergePolicy,
		DialogueLimits: normalized.DialogueLimits, AgentProviders: normalized.AgentProviders,
		ForgejoRepository: &repository, CreatedAt: s.now().UTC(),
	}
	if err := s.store.Create(ctx, created); err != nil {
		if errors.Is(err, ErrAlreadyExists) {
			stored, getErr := s.store.GetByID(ctx, projectID)
			if getErr == nil && sameImportedProject(stored, normalized, repository) {
				return stored, false, nil
			}
			return Project{}, false, ErrImportConflict
		}
		return Project{}, false, fmt.Errorf("store imported project: %w", err)
	}
	return created, true, nil
}

type unavailableRepositoryVerifier struct{}

func (unavailableRepositoryVerifier) VerifyRepository(
	context.Context,
	string,
	string,
) (ForgejoRepository, error) {
	return ForgejoRepository{}, ErrForgejoUnavailable
}

func (s *Service) Create(
	ctx context.Context,
	name string,
	recoveryPolicy RecoveryPolicy,
	dialogueLimits DialogueLimits,
	agentProviders AgentProviders,
	mergePolicy MergePolicy,
) (Project, error) {
	sanitizedName := strings.TrimSpace(name)

	if sanitizedName == "" {
		return Project{}, ErrNameRequired
	}

	policy, err := NormalizeRecoveryPolicy(recoveryPolicy)
	if err != nil {
		return Project{}, err
	}
	mergePolicy, err = NormalizeMergePolicy(mergePolicy)
	if err != nil {
		return Project{}, err
	}
	if err := dialogueLimits.Validate(); err != nil {
		return Project{}, err
	}
	agentProviders, err = agentProviders.Normalize()
	if err != nil {
		return Project{}, err
	}

	project := Project{
		ID:             s.generateID(),
		Name:           sanitizedName,
		RecoveryPolicy: policy,
		MergePolicy:    mergePolicy,
		DialogueLimits: dialogueLimits,
		AgentProviders: agentProviders,
		CreatedAt:      s.now(),
	}

	if err := s.store.Create(ctx, project); err != nil {
		return Project{}, fmt.Errorf("store project: %w", err)
	}

	return project, nil
}

func (s *Service) UpdateMergePolicy(
	ctx context.Context,
	projectID string,
	policy MergePolicy,
) (Project, error) {
	normalized, err := NormalizeMergePolicy(policy)
	if err != nil {
		return Project{}, err
	}
	updated, err := s.store.UpdateMergePolicy(ctx, projectID, normalized)
	if err != nil {
		return Project{}, fmt.Errorf("update merge policy for project %q: %w", projectID, err)
	}
	return updated, nil
}

func (s *Service) UpdateAgentProviders(
	ctx context.Context,
	projectID string,
	providers AgentProviders,
) (Project, error) {
	normalized, err := providers.Normalize()
	if err != nil {
		return Project{}, err
	}
	updated, err := s.store.UpdateAgentProviders(ctx, projectID, normalized)
	if err != nil {
		return Project{}, fmt.Errorf("update agent providers for project %q: %w", projectID, err)
	}
	return updated, nil
}

func (s *Service) UpdateDialogueLimits(
	ctx context.Context,
	projectID string,
	limits DialogueLimits,
) (Project, error) {
	if err := limits.Validate(); err != nil {
		return Project{}, err
	}
	updated, err := s.store.UpdateDialogueLimits(ctx, projectID, limits)
	if err != nil {
		return Project{}, fmt.Errorf("update dialogue limits for project %q: %w", projectID, err)
	}
	return updated, nil
}

func (s *Service) GetByID(
	ctx context.Context,
	id string,
) (Project, error) {
	project, err := s.store.GetByID(ctx, id)
	if err != nil {
		return Project{}, fmt.Errorf(
			"get project %q: %w",
			id,
			err,
		)
	}

	return project, nil
}

func (s *Service) List(ctx context.Context) ([]Project, error) {
	projects, err := s.store.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	return projects, nil
}

func (s *Service) BindForgejoRepository(
	ctx context.Context,
	projectID string,
	owner string,
	name string,
) (Project, error) {
	owner, name, err := NormalizeRepositoryCoordinate(owner, name)
	if err != nil {
		return Project{}, err
	}
	storedProject, err := s.store.GetByID(ctx, projectID)
	if err != nil {
		return Project{}, fmt.Errorf("get project %q before Forgejo binding: %w", projectID, err)
	}
	if storedProject.ForgejoRepository != nil {
		if sameRepositoryCoordinate(*storedProject.ForgejoRepository, owner, name) {
			return storedProject, nil
		}
		return Project{}, ErrForgejoRepositoryAlreadyBound
	}

	repository, err := s.repositoryVerifier.VerifyRepository(ctx, owner, name)
	if err != nil {
		return Project{}, fmt.Errorf("verify Forgejo repository %q/%q: %w", owner, name, err)
	}
	repository.BoundAt = s.now().UTC()
	if err := repository.Validate(); err != nil {
		return Project{}, fmt.Errorf("verify Forgejo repository %q/%q: %w", owner, name, err)
	}
	boundProject, err := s.store.BindForgejoRepository(ctx, projectID, repository)
	if err != nil {
		return Project{}, fmt.Errorf("bind Forgejo repository to project %q: %w", projectID, err)
	}
	return boundProject, nil
}
