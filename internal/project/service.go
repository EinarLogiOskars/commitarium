package project

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
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
	store                    Store
	repositoryVerifier       RepositoryVerifier
	repositoryImporter       RepositoryImporter
	repositoryOverviewReader RepositoryOverviewReader
	repositoryInitializer    RepositoryInitializer
	importMu                 sync.Mutex
	provisionMu              sync.Mutex
	generateID               func() string
	now                      func() time.Time
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
	return newService(
		store,
		verifier,
		unavailableRepositoryImporter{},
		unavailableRepositoryOverviewReader{},
	)
}

func NewServiceWithRepositoryVerifierAndImporter(
	store Store,
	verifier RepositoryVerifier,
	importer RepositoryImporter,
) *Service {
	if importer == nil {
		importer = unavailableRepositoryImporter{}
	}
	return newService(
		store,
		verifier,
		importer,
		unavailableRepositoryOverviewReader{},
	)
}

func NewServiceWithRepositoryServices(
	store Store,
	verifier RepositoryVerifier,
	importer RepositoryImporter,
	overviewReader RepositoryOverviewReader,
	initializers ...RepositoryInitializer,
) *Service {
	if importer == nil {
		importer = unavailableRepositoryImporter{}
	}
	if overviewReader == nil {
		overviewReader = unavailableRepositoryOverviewReader{}
	}
	service := newService(store, verifier, importer, overviewReader)
	if len(initializers) > 0 {
		service.repositoryInitializer = initializers[0]
	}
	return service
}

func projectIDForCreateKey(key string) string {
	digest := sha256.Sum256([]byte("project-create:" + strings.TrimSpace(key)))
	return "prj_" + hex.EncodeToString(digest[:12])
}

func newService(
	store Store,
	verifier RepositoryVerifier,
	importer RepositoryImporter,
	overviewReader RepositoryOverviewReader,
) *Service {
	if verifier == nil {
		verifier = unavailableRepositoryVerifier{}
	}
	return &Service{
		store:                    store,
		repositoryVerifier:       verifier,
		repositoryImporter:       importer,
		repositoryOverviewReader: overviewReader,
		generateID: func() string {
			return "prj_" + rand.Text()
		},
		now: func() time.Time {
			return time.Now().UTC()
		},
	}
}

func (s *Service) GetRepositoryOverview(
	ctx context.Context,
	projectID string,
) (RepositoryOverview, error) {
	storedProject, err := s.store.GetByID(ctx, projectID)
	if err != nil {
		return RepositoryOverview{}, fmt.Errorf(
			"get project %q for repository overview: %w",
			projectID,
			err,
		)
	}
	if storedProject.ForgejoRepository == nil {
		return RepositoryOverview{}, ErrForgejoRepositoryNotReady
	}
	repository := *storedProject.ForgejoRepository
	overview, err := s.repositoryOverviewReader.ReadRepositoryOverview(
		ctx,
		repository.Owner,
		repository.Name,
		repository.DefaultBranch,
	)
	if err != nil {
		return RepositoryOverview{}, fmt.Errorf(
			"read Forgejo repository overview for project %q: %w",
			projectID,
			err,
		)
	}
	return overview, nil
}

func (s *Service) ReadRepositoryBlob(
	ctx context.Context,
	projectID string,
	blobID string,
	maxBytes int64,
) ([]byte, error) {
	storedProject, err := s.store.GetByID(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("get project %q for repository blob: %w", projectID, err)
	}
	if storedProject.ForgejoRepository == nil {
		return nil, ErrForgejoRepositoryNotReady
	}
	reader, ok := s.repositoryOverviewReader.(RepositoryBlobReader)
	if !ok {
		return nil, ErrForgejoUnavailable
	}
	repository := *storedProject.ForgejoRepository
	contents, err := reader.ReadRepositoryBlob(ctx, repository.Owner, repository.Name, blobID, maxBytes)
	if err != nil {
		return nil, fmt.Errorf("read Forgejo repository blob for project %q: %w", projectID, err)
	}
	return contents, nil
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
		MergePolicy: normalized.MergePolicy, AutonomyPolicy: normalized.AutonomyPolicy,
		DialogueLimits: normalized.DialogueLimits, AgentProviders: normalized.AgentProviders,
		AgentModels:       normalized.AgentModels,
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
	autonomyPolicies ...AutonomyPolicy,
) (Project, error) {
	return s.CreateWithAgentModels(
		ctx, name, recoveryPolicy, dialogueLimits, agentProviders, AgentModels{},
		mergePolicy, autonomyPolicies...,
	)
}

func (s *Service) CreateWithAgentModels(
	ctx context.Context,
	name string,
	recoveryPolicy RecoveryPolicy,
	dialogueLimits DialogueLimits,
	agentProviders AgentProviders,
	agentModels AgentModels,
	mergePolicy MergePolicy,
	autonomyPolicies ...AutonomyPolicy,
) (Project, error) {
	project, err := s.newProject(
		s.generateID(), name, recoveryPolicy, dialogueLimits, agentProviders,
		agentModels, mergePolicy, autonomyPolicies...,
	)
	if err != nil {
		return Project{}, err
	}
	if err := s.store.Create(ctx, project); err != nil {
		return Project{}, fmt.Errorf("store project: %w", err)
	}
	return project, nil
}

// CreateProvisionedWithAgentModels creates an ordinary project and ensures it
// has a cloneable private Forgejo repository with a real default branch before
// returning. A non-empty request key makes retries converge on the same project
// and repository identity after any interrupted external or database step.
func (s *Service) CreateProvisionedWithAgentModels(
	ctx context.Context,
	requestKey string,
	name string,
	recoveryPolicy RecoveryPolicy,
	dialogueLimits DialogueLimits,
	agentProviders AgentProviders,
	agentModels AgentModels,
	mergePolicy MergePolicy,
	autonomyPolicies ...AutonomyPolicy,
) (Project, error) {
	projectID := s.generateID()
	if strings.TrimSpace(requestKey) != "" {
		projectID = projectIDForCreateKey(requestKey)
	}
	desired, err := s.newProject(
		projectID, name, recoveryPolicy, dialogueLimits, agentProviders,
		agentModels, mergePolicy, autonomyPolicies...,
	)
	if err != nil {
		return Project{}, err
	}
	stored := desired
	if err := s.store.Create(ctx, desired); err != nil {
		if !errors.Is(err, ErrAlreadyExists) || strings.TrimSpace(requestKey) == "" {
			return Project{}, fmt.Errorf("store project: %w", err)
		}
		stored, err = s.store.GetByID(ctx, projectID)
		if err != nil {
			return Project{}, fmt.Errorf("get idempotent project create %q: %w", projectID, err)
		}
		if !sameProjectCreateRequest(stored, desired) {
			return Project{}, ErrProjectCreateConflict
		}
	}
	return s.provisionRepository(ctx, stored)
}

func (s *Service) newProject(
	projectID string,
	name string,
	recoveryPolicy RecoveryPolicy,
	dialogueLimits DialogueLimits,
	agentProviders AgentProviders,
	agentModels AgentModels,
	mergePolicy MergePolicy,
	autonomyPolicies ...AutonomyPolicy,
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
	if len(autonomyPolicies) > 1 {
		return Project{}, ErrInvalidAutonomyPolicy
	}
	var autonomyPolicy AutonomyPolicy
	if len(autonomyPolicies) == 1 {
		autonomyPolicy = autonomyPolicies[0]
	}
	autonomyPolicy, err = NormalizeAutonomyPolicy(autonomyPolicy)
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
	agentModels, err = agentModels.Normalize()
	if err != nil {
		return Project{}, err
	}

	project := Project{
		ID:             projectID,
		Name:           sanitizedName,
		RecoveryPolicy: policy,
		MergePolicy:    mergePolicy,
		AutonomyPolicy: autonomyPolicy,
		DialogueLimits: dialogueLimits,
		AgentProviders: agentProviders,
		AgentModels:    agentModels,
		CreatedAt:      s.now(),
	}
	return project, nil
}

func sameProjectCreateRequest(stored, desired Project) bool {
	return stored.ID == desired.ID && stored.Name == desired.Name &&
		stored.RecoveryPolicy == desired.RecoveryPolicy &&
		stored.MergePolicy == desired.MergePolicy &&
		stored.AutonomyPolicy == desired.AutonomyPolicy &&
		stored.DialogueLimits == desired.DialogueLimits &&
		stored.AgentProviders == desired.AgentProviders &&
		stored.AgentModels == desired.AgentModels
}

func (s *Service) ProvisionForgejoRepository(
	ctx context.Context,
	projectID string,
) (Project, error) {
	stored, err := s.store.GetByID(ctx, projectID)
	if err != nil {
		return Project{}, fmt.Errorf("get project %q for repository provisioning: %w", projectID, err)
	}
	return s.provisionRepository(ctx, stored)
}

func (s *Service) provisionRepository(ctx context.Context, stored Project) (Project, error) {
	s.provisionMu.Lock()
	defer s.provisionMu.Unlock()

	current, err := s.store.GetByID(ctx, stored.ID)
	if err != nil {
		return Project{}, fmt.Errorf("refresh project %q for repository provisioning: %w", stored.ID, err)
	}
	stored = current
	if stored.ForgejoRepository != nil {
		return stored, nil
	}
	if s.repositoryInitializer == nil {
		return Project{}, ErrRepositoryProvisioningUnavailable
	}
	repository, err := s.repositoryInitializer.InitializeRepository(
		ctx,
		RepositoryInitializationSpec{
			ProjectID: stored.ID, Repository: importRepositoryName(stored.Name, stored.ID),
			DefaultBranch: "main",
		},
	)
	if err != nil {
		return Project{}, fmt.Errorf("%w: %v", ErrRepositoryProvisioningUnavailable, err)
	}
	repository.BoundAt = s.now().UTC()
	if err := repository.Validate(); err != nil {
		return Project{}, fmt.Errorf("%w: invalid initialized repository: %v", ErrRepositoryProvisioningUnavailable, err)
	}
	bound, err := s.store.BindForgejoRepository(ctx, stored.ID, repository)
	if err != nil {
		return Project{}, fmt.Errorf("bind initialized repository to project %q: %w", stored.ID, err)
	}
	return bound, nil
}

func (s *Service) UpdateAutonomyPolicy(
	ctx context.Context,
	projectID string,
	policy AutonomyPolicy,
) (Project, error) {
	normalized, err := NormalizeAutonomyPolicy(policy)
	if err != nil {
		return Project{}, err
	}
	updated, err := s.store.UpdateAutonomyPolicy(ctx, projectID, normalized)
	if err != nil {
		return Project{}, fmt.Errorf("update autonomy policy for project %q: %w", projectID, err)
	}
	return updated, nil
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

func (s *Service) UpdateAgentSettings(
	ctx context.Context,
	projectID string,
	providers AgentProviders,
	models AgentModels,
) (Project, error) {
	normalizedProviders, err := providers.Normalize()
	if err != nil {
		return Project{}, err
	}
	normalizedModels, err := models.Normalize()
	if err != nil || normalizedModels.ValidateRequired() != nil {
		return Project{}, ErrInvalidAgentModels
	}
	updater, ok := s.store.(interface {
		UpdateAgentSettings(context.Context, string, AgentProviders, AgentModels) (Project, error)
	})
	if !ok {
		return Project{}, errors.New("project store does not support agent model settings")
	}
	updated, err := updater.UpdateAgentSettings(ctx, projectID, normalizedProviders, normalizedModels)
	if err != nil {
		return Project{}, fmt.Errorf("update agent settings for project %q: %w", projectID, err)
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
