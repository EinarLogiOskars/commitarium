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
) (Feature, error) {
	sanitizedTitle := strings.TrimSpace(title)
	if sanitizedTitle == "" {
		return Feature{}, ErrTitleRequired
	}

	if _, err := s.projects.GetByID(ctx, projectID); err != nil {
		return Feature{}, fmt.Errorf(
			"get project %q: %w",
			projectID,
			err,
		)
	}

	now := s.now()
	createdFeature := Feature{
		ID:          s.generateID(),
		ProjectID:   projectID,
		Title:       sanitizedTitle,
		Description: strings.TrimSpace(description),
		State:       StateDraft,
		CreatedAt:   now,
		UpdatedAt:   now,
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
