package project

import (
	"context"
	"errors"
	"testing"
	"time"
)

type recordingStore struct {
	createdProject Project
	err            error

	receivedID    string
	projectResult Project
	getByIDErr    error

	listResult []Project
	listErr    error

	updatedProjectID string
	updatedLimits    DialogueLimits
	updateResult     Project
	updateErr        error
	updatedProviders AgentProviders

	boundProjectID  string
	boundRepository ForgejoRepository
	bindResult      Project
	bindErr         error
}

func (s *recordingStore) UpdateAgentProviders(
	_ context.Context,
	projectID string,
	providers AgentProviders,
) (Project, error) {
	s.updatedProjectID = projectID
	s.updatedProviders = providers
	return s.updateResult, s.updateErr
}

func (s *recordingStore) Create(
	_ context.Context,
	project Project,
) error {
	s.createdProject = project
	return s.err
}

func (s *recordingStore) GetByID(
	_ context.Context,
	id string,
) (Project, error) {
	s.receivedID = id
	return s.projectResult, s.getByIDErr
}

func (s *recordingStore) List(context.Context) ([]Project, error) {
	return s.listResult, s.listErr
}

func (s *recordingStore) UpdateDialogueLimits(
	_ context.Context,
	projectID string,
	limits DialogueLimits,
) (Project, error) {
	s.updatedProjectID = projectID
	s.updatedLimits = limits
	return s.updateResult, s.updateErr
}

func (s *recordingStore) BindForgejoRepository(
	_ context.Context,
	projectID string,
	repository ForgejoRepository,
) (Project, error) {
	s.boundProjectID = projectID
	s.boundRepository = repository
	return s.bindResult, s.bindErr
}

type recordingRepositoryVerifier struct {
	owner  string
	name   string
	result ForgejoRepository
	err    error
	calls  int
}

func (v *recordingRepositoryVerifier) VerifyRepository(
	_ context.Context,
	owner string,
	name string,
) (ForgejoRepository, error) {
	v.calls++
	v.owner = owner
	v.name = name
	return v.result, v.err
}

func TestServiceCreate(t *testing.T) {
	fixedTime := time.Date(2026, time.September, 6, 12, 0, 0, 0, time.UTC)
	store := &recordingStore{}

	service := &Service{
		store: store,
		generateID: func() string {
			return "prj_test"
		},
		now: func() time.Time {
			return fixedTime
		},
	}

	limits := DefaultDialogueLimits()
	project, err := service.Create(t.Context(), "   Commitarium   ", "", limits, DefaultAgentProviders())

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if project.ID != "prj_test" {
		t.Errorf("expected ID %q, got %q", "prj_test", project.ID)
	}

	if project.Name != "Commitarium" {
		t.Errorf("expected trimmed name %q, got %q", "Commitarium", project.Name)
	}
	if project.RecoveryPolicy != RecoveryPolicyApprovalRequired {
		t.Errorf("expected default recovery policy %q, got %q", RecoveryPolicyApprovalRequired, project.RecoveryPolicy)
	}
	if project.DialogueLimits != limits {
		t.Errorf("expected dialogue limits %+v, got %+v", limits, project.DialogueLimits)
	}
	if project.AgentProviders != DefaultAgentProviders() {
		t.Errorf("expected default agent providers, got %+v", project.AgentProviders)
	}

	if !project.CreatedAt.Equal(fixedTime) {
		t.Errorf("expected time %v, got %v", fixedTime, project.CreatedAt)
	}

	if store.createdProject != project {
		t.Errorf(
			"expected stored project %+v, got %+v",
			project,
			store.createdProject,
		)
	}
}

func TestServiceCreateAcceptsAutomaticRecovery(t *testing.T) {
	store := &recordingStore{}
	service := NewService(store)

	created, err := service.Create(
		t.Context(), "Commitarium", RecoveryPolicyAutomatic, DefaultDialogueLimits(),
		DefaultAgentProviders(),
	)
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	if created.RecoveryPolicy != RecoveryPolicyAutomatic {
		t.Fatalf("expected automatic recovery, got %+v", created)
	}
}

func TestServiceCreateRejectsInvalidRecoveryPolicy(t *testing.T) {
	store := &recordingStore{}
	service := NewService(store)

	_, err := service.Create(
		t.Context(), "Commitarium", RecoveryPolicy("reckless"), DefaultDialogueLimits(),
		DefaultAgentProviders(),
	)
	if !errors.Is(err, ErrInvalidRecoveryPolicy) {
		t.Fatalf("expected error %v, got %v", ErrInvalidRecoveryPolicy, err)
	}
	if store.createdProject != (Project{}) {
		t.Fatalf("invalid project reached storage: %+v", store.createdProject)
	}
}

func TestServiceCreateRejectsBlankName(t *testing.T) {
	store := &recordingStore{}
	service := NewService(store)

	_, err := service.Create(t.Context(), " ", "", DefaultDialogueLimits(), DefaultAgentProviders())

	if !errors.Is(err, ErrNameRequired) {
		t.Fatalf("expected error %v, got %v", ErrNameRequired, err)
	}
}

func TestServiceCreateReturnsStoreError(t *testing.T) {
	storeError := errors.New("storage failed")

	store := &recordingStore{
		err: storeError,
	}

	service := NewService(store)

	project, err := service.Create(t.Context(), "Commitarium", "", DefaultDialogueLimits(), DefaultAgentProviders())

	if !errors.Is(err, storeError) {
		t.Fatalf("expected error %v, got %v", storeError, err)
	}

	if project != (Project{}) {
		t.Errorf("expected empty project, got %+v", project)
	}
}

func TestServiceUpdatesDialogueLimits(t *testing.T) {
	limits := DialogueLimits{PlanningRounds: 0, ImplementationReviewRounds: 3}
	store := &recordingStore{updateResult: Project{ID: "prj_test", DialogueLimits: limits}}

	updated, err := NewService(store).UpdateDialogueLimits(t.Context(), "prj_test", limits)
	if err != nil {
		t.Fatalf("update dialogue limits: %v", err)
	}
	if store.updatedProjectID != "prj_test" || store.updatedLimits != limits || updated != store.updateResult {
		t.Fatalf("unexpected update project=%q limits=%+v result=%+v", store.updatedProjectID, store.updatedLimits, updated)
	}
}

func TestServiceRejectsNegativeDialogueLimits(t *testing.T) {
	store := &recordingStore{}
	_, err := NewService(store).UpdateDialogueLimits(
		t.Context(),
		"prj_test",
		DialogueLimits{PlanningRounds: -1, ImplementationReviewRounds: 6},
	)
	if !errors.Is(err, ErrInvalidDialogueLimits) {
		t.Fatalf("expected %v, got %v", ErrInvalidDialogueLimits, err)
	}
	if store.updatedProjectID != "" {
		t.Fatal("invalid limits reached storage")
	}
}

func TestServiceGetByID(t *testing.T) {
	expected := Project{
		ID:        "prj_test",
		Name:      "Commitarium",
		CreatedAt: time.Date(2026, time.September, 7, 12, 0, 0, 0, time.UTC),
	}

	store := &recordingStore{
		projectResult: expected,
	}
	service := NewService(store)

	actual, err := service.GetByID(t.Context(), expected.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if store.receivedID != expected.ID {
		t.Errorf("expected store ID %q, got %q", expected.ID, store.receivedID)
	}

	if actual != expected {
		t.Errorf("expected project %+v, got %+v", expected, actual)
	}
}

func TestServiceGetByIDReturnsStoreError(t *testing.T) {
	store := &recordingStore{
		getByIDErr: ErrNotFound,
	}
	service := NewService(store)

	project, err := service.GetByID(t.Context(), "prj_missing")

	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected error %v, got %v", ErrNotFound, err)
	}

	if project != (Project{}) {
		t.Errorf("expected empty project, got %+v", project)
	}

	if store.receivedID != "prj_missing" {
		t.Errorf(
			"expected store to receive ID %q, got %q",
			"prj_missing",
			store.receivedID,
		)
	}
}

func TestServiceList(t *testing.T) {
	want := []Project{{ID: "prj_one"}, {ID: "prj_two"}}
	store := &recordingStore{listResult: want}
	got, err := NewService(store).List(t.Context())
	if err != nil {
		t.Fatalf("list projects: %v", err)
	}
	if len(got) != len(want) || got[0].ID != want[0].ID || got[1].ID != want[1].ID {
		t.Fatalf("unexpected projects %+v", got)
	}
}

func TestServiceBindForgejoRepository(t *testing.T) {
	fixedTime := time.Date(2026, time.September, 9, 18, 0, 0, 0, time.UTC)
	verifier := &recordingRepositoryVerifier{result: ForgejoRepository{
		Owner: "canonical-owner", Name: "canonical-name", DefaultBranch: "main",
	}}
	want := Project{ID: "prj_test", ForgejoRepository: &ForgejoRepository{
		Owner: "canonical-owner", Name: "canonical-name", DefaultBranch: "main", BoundAt: fixedTime,
	}}
	store := &recordingStore{projectResult: Project{ID: "prj_test"}, bindResult: want}
	service := NewServiceWithRepositoryVerifier(store, verifier)
	service.now = func() time.Time { return fixedTime }

	got, err := service.BindForgejoRepository(t.Context(), "prj_test", " owner ", " repository ")
	if err != nil {
		t.Fatalf("bind repository: %v", err)
	}
	if verifier.calls != 1 || verifier.owner != "owner" || verifier.name != "repository" {
		t.Fatalf("unexpected verifier call owner=%q name=%q calls=%d", verifier.owner, verifier.name, verifier.calls)
	}
	if store.boundProjectID != "prj_test" || store.boundRepository != *want.ForgejoRepository {
		t.Fatalf("unexpected stored binding %q %+v", store.boundProjectID, store.boundRepository)
	}
	if got.ID != want.ID || got.ForgejoRepository == nil || *got.ForgejoRepository != *want.ForgejoRepository {
		t.Fatalf("unexpected project %+v", got)
	}
}

func TestServiceBindForgejoRepositoryIsIdempotentForSameCoordinate(t *testing.T) {
	bound := ForgejoRepository{
		Owner: "owner", Name: "repository", DefaultBranch: "main",
		BoundAt: time.Date(2026, time.September, 9, 18, 0, 0, 0, time.UTC),
	}
	stored := Project{ID: "prj_test", ForgejoRepository: &bound}
	store := &recordingStore{projectResult: stored}
	verifier := &recordingRepositoryVerifier{err: errors.New("must not be called")}

	got, err := NewServiceWithRepositoryVerifier(store, verifier).BindForgejoRepository(
		t.Context(), "prj_test", "OWNER", "Repository",
	)
	if err != nil {
		t.Fatalf("repeat binding: %v", err)
	}
	if verifier.calls != 0 || store.boundProjectID != "" {
		t.Fatalf("repeat binding performed remote or storage work")
	}
	if got.ForgejoRepository == nil || *got.ForgejoRepository != bound {
		t.Fatalf("repeat binding changed repository %+v", got)
	}
}

func TestServiceBindForgejoRepositoryRejectsDifferentCoordinate(t *testing.T) {
	store := &recordingStore{projectResult: Project{
		ID: "prj_test", ForgejoRepository: &ForgejoRepository{Owner: "owner", Name: "one"},
	}}
	_, err := NewServiceWithRepositoryVerifier(store, &recordingRepositoryVerifier{}).
		BindForgejoRepository(t.Context(), "prj_test", "owner", "two")
	if !errors.Is(err, ErrForgejoRepositoryAlreadyBound) {
		t.Fatalf("expected %v, got %v", ErrForgejoRepositoryAlreadyBound, err)
	}
}

func TestServiceBindForgejoRepositoryReturnsVerificationError(t *testing.T) {
	verifier := &recordingRepositoryVerifier{err: ErrForgejoRepositoryNotFound}
	store := &recordingStore{projectResult: Project{ID: "prj_test"}}
	_, err := NewServiceWithRepositoryVerifier(store, verifier).BindForgejoRepository(
		t.Context(), "prj_test", "owner", "missing",
	)
	if !errors.Is(err, ErrForgejoRepositoryNotFound) {
		t.Fatalf("expected %v, got %v", ErrForgejoRepositoryNotFound, err)
	}
	if store.boundProjectID != "" {
		t.Fatal("failed verification reached storage")
	}
}

func TestNormalizeRepositoryCoordinate(t *testing.T) {
	tests := []struct {
		owner string
		name  string
		want  error
	}{
		{name: "repository", want: ErrForgejoOwnerRequired},
		{owner: "owner", want: ErrForgejoRepositoryNameRequired},
		{owner: "bad/owner", name: "repository", want: ErrInvalidForgejoRepositoryCoordinate},
		{owner: "owner", name: "bad\\repository", want: ErrInvalidForgejoRepositoryCoordinate},
	}
	for _, test := range tests {
		_, _, err := NormalizeRepositoryCoordinate(test.owner, test.name)
		if !errors.Is(err, test.want) {
			t.Errorf("owner=%q name=%q: expected %v, got %v", test.owner, test.name, test.want, err)
		}
	}
}
