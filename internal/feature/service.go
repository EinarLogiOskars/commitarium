package feature

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/project"
)

var ErrTitleRequired = errors.New("feature title is required")

// ErrInvalidKind rejects a kind of work other than a work order or a session.
var ErrInvalidKind = errors.New("kind must be work_order or session")

// NewSessionTitle names a session until its first turn names it.
const NewSessionTitle = "New session"

type ProjectFinder interface {
	GetByID(ctx context.Context, id string) (project.Project, error)
}

type Service struct {
	store      Store
	projects   ProjectFinder
	generateID func() string
	now        func() time.Time
}

func NewService(store Store, projects ProjectFinder) *Service {
	return &Service{
		store:    store,
		projects: projects,
		generateID: func() string {
			return "fea_" + rand.Text()
		},
		now: func() time.Time {
			return time.Now().UTC()
		},
	}
}

func (s *Service) Create(
	ctx context.Context,
	projectID string,
	title string,
	description string,
	overrides SettingsOverrides,
) (Feature, error) {
	kind := overrides.Kind
	if kind == "" {
		kind = KindWorkOrder
	}
	if !kind.IsValid() {
		return Feature{}, ErrInvalidKind
	}
	sanitizedTitle := strings.TrimSpace(title)
	if sanitizedTitle == "" && kind == KindSession {
		// The first turn names a session after what the user asked for.
		sanitizedTitle = NewSessionTitle
	}
	if sanitizedTitle == "" {
		return Feature{}, ErrTitleRequired
	}
	if kind == KindSession {
		overrides.Steps = &project.WorkSteps{SkipPlan: true, SkipReviewer: true}
	}

	storedProject, err := s.projects.GetByID(ctx, projectID)
	if err != nil {
		return Feature{}, fmt.Errorf(
			"get project %q: %w",
			projectID,
			err,
		)
	}

	// Round limits are system-wide; stored project values and overrides from
	// older clients are ignored.
	dialogueLimits := project.DefaultDialogueLimits()

	settings, err := resolveSettings(Feature{
		AgentProviders: storedProject.AgentProviders, AgentModels: storedProject.AgentModels,
		MergePolicy: storedProject.MergePolicy, AutonomyPolicy: storedProject.AutonomyPolicy,
		IndependentAcceptanceTests: storedProject.IndependentAcceptanceTests,
	}, overrides)
	if err != nil {
		return Feature{}, err
	}

	now := s.now()
	createdFeature := Feature{
		ID:                         s.generateID(),
		ProjectID:                  projectID,
		Title:                      sanitizedTitle,
		Description:                strings.TrimSpace(description),
		State:                      StateDraft,
		DialogueLimits:             dialogueLimits,
		AgentProviders:             settings.AgentProviders,
		AgentModels:                settings.AgentModels,
		MergePolicy:                settings.MergePolicy,
		AutonomyPolicy:             settings.AutonomyPolicy,
		IndependentAcceptanceTests: settings.IndependentAcceptanceTests,
		Steps:                      settings.Steps,
		Kind:                       kind,
		CreatedAt:                  now,
		UpdatedAt:                  now,
	}

	if err := s.store.Create(ctx, createdFeature); err != nil {
		return Feature{}, fmt.Errorf(
			"store feature: %w",
			err,
		)
	}

	return createdFeature, nil
}

func (s *Service) GetByID(
	ctx context.Context,
	projectID string,
	id string,
) (Feature, error) {
	storedFeature, err := s.store.GetByID(ctx, id)
	if err != nil {
		return Feature{}, fmt.Errorf(
			"get feature %q: %w",
			id,
			err,
		)
	}

	if storedFeature.ProjectID != projectID {
		return Feature{}, fmt.Errorf(
			"get feature %q for project %q: %w",
			id,
			projectID,
			ErrNotFound,
		)
	}

	return storedFeature, nil
}

func (s *Service) List(
	ctx context.Context,
	projectID string,
) ([]Feature, error) {
	if _, err := s.projects.GetByID(ctx, projectID); err != nil {
		return nil, fmt.Errorf("get project %q: %w", projectID, err)
	}

	features, err := s.store.ListByProjectID(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("list features for project %q: %w", projectID, err)
	}
	return features, nil
}

// resolveSettings applies a work order's setting overrides to base and
// normalizes the result.
func resolveSettings(base Feature, overrides SettingsOverrides) (Feature, error) {
	var err error
	if overrides.AgentProviders != nil {
		base.AgentProviders = *overrides.AgentProviders
		if !base.AgentProviders.Lead.IsValid() || !base.AgentProviders.Reviewer.IsValid() {
			return Feature{}, project.ErrInvalidAgentProviders
		}
	}
	if base.AgentProviders, err = base.AgentProviders.Normalize(); err != nil {
		return Feature{}, err
	}
	if overrides.AgentModels != nil {
		base.AgentModels = *overrides.AgentModels
	}
	if base.AgentModels, err = base.AgentModels.Normalize(); err != nil {
		return Feature{}, project.ErrInvalidAgentModels
	}
	if overrides.MergePolicy != nil {
		if *overrides.MergePolicy == "" {
			return Feature{}, project.ErrInvalidMergePolicy
		}
		base.MergePolicy = *overrides.MergePolicy
	}
	if base.MergePolicy, err = project.NormalizeMergePolicy(base.MergePolicy); err != nil {
		return Feature{}, err
	}
	if overrides.AutonomyPolicy != nil {
		if *overrides.AutonomyPolicy == "" {
			return Feature{}, project.ErrInvalidAutonomyPolicy
		}
		base.AutonomyPolicy = *overrides.AutonomyPolicy
	}
	if base.AutonomyPolicy, err = project.NormalizeAutonomyPolicy(base.AutonomyPolicy); err != nil {
		return Feature{}, err
	}
	if overrides.IndependentAcceptanceTests != nil {
		base.IndependentAcceptanceTests = *overrides.IndependentAcceptanceTests
	}
	if overrides.Steps != nil {
		base.Steps = *overrides.Steps
	}
	if err := base.Steps.Validate(); err != nil {
		return Feature{}, err
	}
	if base.Steps.SkipReviewer {
		// Acceptance tests are written by the reviewer.
		base.IndependentAcceptanceTests = false
	}
	return base, nil
}

// ErrNotReady means a work order's start settings can only be chosen while
// it is Ready.
var ErrNotReady = errors.New("work order is not ready to start")

// SettingsStore saves a Ready work order's settings.
type SettingsStore interface {
	UpdateReadySettings(ctx context.Context, feature Feature) error
}

// ChooseStartSettings records the agents, models, and policies a Ready work
// order starts with. Settings are chosen at Start, not while drafting; until
// then the work order carries its project's defaults.
func (s *Service) ChooseStartSettings(
	ctx context.Context,
	projectID string,
	featureID string,
	overrides SettingsOverrides,
) (Feature, error) {
	stored, err := s.GetByID(ctx, projectID, featureID)
	if err != nil {
		return Feature{}, err
	}
	if stored.State != StateReady {
		return Feature{}, ErrNotReady
	}
	chosen, err := resolveSettings(stored, overrides)
	if err != nil {
		return Feature{}, err
	}
	settings, ok := s.store.(SettingsStore)
	if !ok {
		return Feature{}, errors.New("work-order settings cannot be saved")
	}
	if err := settings.UpdateReadySettings(ctx, chosen); err != nil {
		return Feature{}, err
	}
	return chosen, nil
}
