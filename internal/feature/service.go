package feature

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/project"
)

var ErrTitleRequired = errors.New("feature title is required")

// ErrInvalidKind rejects a kind of work other than a work order or a session.
var ErrInvalidKind = errors.New("kind must be work_order or session")

// untitledSessionPrefix names a session until something gives it a title:
// "Untitled session 1", "Untitled session 2", and so on within a project.
const untitledSessionPrefix = "Untitled session "

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
	if strings.TrimSpace(title) == "" && kind == KindSession {
		untitled, err := s.untitledSessionTitle(ctx, projectID)
		if err != nil {
			return Feature{}, err
		}
		title = untitled
	}
	sanitizedTitle := strings.TrimSpace(title)
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

	// Agents and models are never project defaults (ADR-027): a work order
	// has none until it is created or started with a choice.
	settings, err := resolveSettings(Feature{
		MergePolicy: storedProject.MergePolicy, AutonomyPolicy: storedProject.AutonomyPolicy,
		IndependentAcceptanceTests: storedProject.IndependentAcceptanceTests,
	}, overrides)
	if err != nil {
		return Feature{}, err
	}

	createdBy := overrides.CreatedBy
	if createdBy != CreatedByAssistant {
		createdBy = CreatedByUser
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
		Background:                 overrides.Background,
		CreatedBy:                  createdBy,
		PreviewCheck:               overrides.PreviewCheck,
		CreatedAt:                  now,
		UpdatedAt:                  now,
	}

	if overrides.PhaseChoices != nil {
		if _, err := overrides.PhaseChoices.Normalize(); err != nil {
			return Feature{}, err
		}
	}
	if err := s.store.Create(ctx, createdFeature); err != nil {
		return Feature{}, fmt.Errorf(
			"store feature: %w",
			err,
		)
	}
	if err := s.savePhaseChoices(ctx, createdFeature.ID, overrides.PhaseChoices); err != nil {
		return Feature{}, err
	}

	return createdFeature, nil
}

// untitledSessionTitle numbers a new untitled session after the highest
// untitled session in the project.
func (s *Service) untitledSessionTitle(ctx context.Context, projectID string) (string, error) {
	existing, err := s.store.ListByProjectID(ctx, projectID)
	if err != nil {
		return "", fmt.Errorf("list work orders to number a session: %w", err)
	}
	highest := 0
	for _, stored := range existing {
		number, err := strconv.Atoi(strings.TrimPrefix(stored.Title, untitledSessionPrefix))
		if stored.Kind == KindSession && strings.HasPrefix(stored.Title, untitledSessionPrefix) &&
			err == nil && number > highest {
			highest = number
		}
	}
	return untitledSessionPrefix + strconv.Itoa(highest+1), nil
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
	if base.AgentProviders, err = base.AgentProviders.NormalizeChosen(); err != nil {
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
	if !base.Steps.CodeReview() {
		// Acceptance tests are written by the reviewer during code review.
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

// PhaseChoiceStore keeps work orders' choices by phase (ADR-031).
type PhaseChoiceStore interface {
	PhaseChoices(ctx context.Context, featureID string) (project.PhaseChoices, error)
	SavePhaseChoices(ctx context.Context, featureID string, choices project.PhaseChoices) error
}

// savePhaseChoices stores the phase choices overrides name, if any.
func (s *Service) savePhaseChoices(ctx context.Context, featureID string, choices *project.PhaseChoices) error {
	if choices == nil {
		return nil
	}
	normalized, err := choices.Normalize()
	if err != nil {
		return err
	}
	store, ok := s.store.(PhaseChoiceStore)
	if !ok {
		return errors.New("work orders' phase choices cannot be saved")
	}
	return store.SavePhaseChoices(ctx, featureID, normalized)
}

// DropPlanModels clears the planning model and effort of each role whose
// model the user switched, so the new model runs every phase (ADR-031).
func (s *Service) DropPlanModels(ctx context.Context, featureID string, lead, reviewer bool) error {
	store, ok := s.store.(PhaseChoiceStore)
	if !ok || (!lead && !reviewer) {
		return nil
	}
	choices, err := store.PhaseChoices(ctx, featureID)
	if err != nil {
		return err
	}
	updated := choices
	if lead {
		updated = updated.WithoutPlanModel(false)
	}
	if reviewer {
		updated = updated.WithoutPlanModel(true)
	}
	if updated == choices {
		return nil
	}
	return store.SavePhaseChoices(ctx, featureID, updated)
}

// PhaseChoices returns a work order's choices by phase.
func (s *Service) PhaseChoices(ctx context.Context, featureID string) (project.PhaseChoices, error) {
	store, ok := s.store.(PhaseChoiceStore)
	if !ok {
		return project.PhaseChoices{}, nil
	}
	return store.PhaseChoices(ctx, featureID)
}

// RenameStore renames work orders.
type RenameStore interface {
	Rename(ctx context.Context, id string, title string) error
}

// Rename changes a work order's title.
func (s *Service) Rename(ctx context.Context, projectID string, featureID string, title string) (Feature, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return Feature{}, ErrTitleRequired
	}
	if _, err := s.GetByID(ctx, projectID, featureID); err != nil {
		return Feature{}, err
	}
	renamer, ok := s.store.(RenameStore)
	if !ok {
		return Feature{}, errors.New("work orders cannot be renamed")
	}
	if err := renamer.Rename(ctx, featureID, title); err != nil {
		return Feature{}, err
	}
	return s.GetByID(ctx, projectID, featureID)
}

// CreatorStore records who created work orders.
type CreatorStore interface {
	MarkCreatedByAssistant(ctx context.Context, projectID string, featureIDs []string) error
}

// MarkCreatedByAssistant records that the assistant created a project's
// work orders, as its conversation's applied actions show for work orders
// from before their creator was stored. It never makes one the user's.
func (s *Service) MarkCreatedByAssistant(ctx context.Context, projectID string, featureIDs []string) error {
	if len(featureIDs) == 0 {
		return nil
	}
	marker, ok := s.store.(CreatorStore)
	if !ok {
		return errors.New("work orders' creators cannot be recorded")
	}
	return marker.MarkCreatedByAssistant(ctx, projectID, featureIDs)
}

// ChooseStartSettings records the agents, models, and policies a Ready work
// order starts with. Settings are chosen at Start, not while drafting; until
// then the work order keeps what it was created with, which may name no
// agents (ADR-027).
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
	if overrides.PhaseChoices != nil {
		if _, err := overrides.PhaseChoices.Normalize(); err != nil {
			return Feature{}, err
		}
	}
	if err := settings.UpdateReadySettings(ctx, chosen); err != nil {
		return Feature{}, err
	}
	if err := s.savePhaseChoices(ctx, featureID, overrides.PhaseChoices); err != nil {
		return Feature{}, err
	}
	return chosen, nil
}
