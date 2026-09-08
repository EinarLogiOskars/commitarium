package feature

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/project"
)

type recordingStore struct {
	createdFeature Feature
	createCalls    int
	createErr      error

	receivedID string
	getResult  Feature
	getByIDErr error
}

func (s *recordingStore) Create(
	_ context.Context,
	createdFeature Feature,
) error {
	s.createCalls++
	s.createdFeature = createdFeature
	return s.createErr
}

func (s *recordingStore) GetByID(
	_ context.Context,
	id string,
) (Feature, error) {
	s.receivedID = id
	return s.getResult, s.getByIDErr
}

type recordingProjectFinder struct {
	receivedID string
	calls      int
	err        error
}

func (f *recordingProjectFinder) GetByID(
	_ context.Context,
	id string,
) (project.Project, error) {
	f.calls++
	f.receivedID = id
	return project.Project{ID: id}, f.err
}

func TestServiceCreate(t *testing.T) {
	fixedTime := time.Date(
		2026,
		time.September,
		8,
		12,
		0,
		0,
		123456789,
		time.UTC,
	)
	store := &recordingStore{}
	projects := &recordingProjectFinder{}
	service := &Service{
		store:    store,
		projects: projects,
		generateID: func() string {
			return "fea_test"
		},
		now: func() time.Time {
			return fixedTime
		},
	}

	createdFeature, err := service.Create(
		t.Context(),
		"prj_test",
		"  Durable features  ",
		"  Persist feature data  ",
	)
	if err != nil {
		t.Fatalf("create feature: %v", err)
	}

	expected := Feature{
		ID:          "fea_test",
		ProjectID:   "prj_test",
		Title:       "Durable features",
		Description: "Persist feature data",
		State:       StateDraft,
		CreatedAt:   fixedTime,
		UpdatedAt:   fixedTime,
	}

	if projects.receivedID != expected.ProjectID {
		t.Errorf(
			"expected project lookup %q, got %q",
			expected.ProjectID,
			projects.receivedID,
		)
	}

	if createdFeature != expected {
		t.Errorf("expected feature %+v, got %+v", expected, createdFeature)
	}

	if store.createdFeature != expected {
		t.Errorf(
			"expected stored feature %+v, got %+v",
			expected,
			store.createdFeature,
		)
	}
}

func TestServiceCreateRejectsBlankTitle(t *testing.T) {
	store := &recordingStore{}
	projects := &recordingProjectFinder{}
	service := NewService(store, projects)

	_, err := service.Create(t.Context(), "prj_test", "  ", "description")

	if !errors.Is(err, ErrTitleRequired) {
		t.Fatalf("expected error %v, got %v", ErrTitleRequired, err)
	}

	if projects.calls != 0 {
		t.Errorf("expected no project lookups, got %d", projects.calls)
	}

	if store.createCalls != 0 {
		t.Errorf("expected no store calls, got %d", store.createCalls)
	}
}

func TestServiceCreateReturnsProjectError(t *testing.T) {
	store := &recordingStore{}
	projects := &recordingProjectFinder{err: project.ErrNotFound}
	service := NewService(store, projects)

	_, err := service.Create(
		t.Context(),
		"prj_missing",
		"Feature",
		"Description",
	)

	if !errors.Is(err, project.ErrNotFound) {
		t.Fatalf("expected error %v, got %v", project.ErrNotFound, err)
	}

	if store.createCalls != 0 {
		t.Errorf("expected no store calls, got %d", store.createCalls)
	}
}

func TestServiceCreateReturnsStoreError(t *testing.T) {
	storeErr := errors.New("storage failed")
	store := &recordingStore{createErr: storeErr}
	service := NewService(store, &recordingProjectFinder{})

	createdFeature, err := service.Create(
		t.Context(),
		"prj_test",
		"Feature",
		"Description",
	)

	if !errors.Is(err, storeErr) {
		t.Fatalf("expected error %v, got %v", storeErr, err)
	}

	if createdFeature != (Feature{}) {
		t.Errorf("expected empty feature, got %+v", createdFeature)
	}
}

func TestServiceGetByID(t *testing.T) {
	expected := Feature{
		ID:        "fea_test",
		ProjectID: "prj_test",
		Title:     "Feature",
	}
	store := &recordingStore{getResult: expected}
	service := NewService(store, &recordingProjectFinder{})

	actual, err := service.GetByID(
		t.Context(),
		expected.ProjectID,
		expected.ID,
	)
	if err != nil {
		t.Fatalf("get feature: %v", err)
	}

	if store.receivedID != expected.ID {
		t.Errorf("expected store ID %q, got %q", expected.ID, store.receivedID)
	}

	if actual != expected {
		t.Errorf("expected feature %+v, got %+v", expected, actual)
	}
}

func TestServiceGetByIDRejectsWrongProject(t *testing.T) {
	store := &recordingStore{
		getResult: Feature{
			ID:        "fea_test",
			ProjectID: "prj_actual",
		},
	}
	service := NewService(store, &recordingProjectFinder{})

	foundFeature, err := service.GetByID(
		t.Context(),
		"prj_other",
		"fea_test",
	)

	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected error %v, got %v", ErrNotFound, err)
	}

	if foundFeature != (Feature{}) {
		t.Errorf("expected empty feature, got %+v", foundFeature)
	}
}

func TestServiceGetByIDReturnsStoreError(t *testing.T) {
	storeErr := errors.New("storage failed")
	store := &recordingStore{getByIDErr: storeErr}
	service := NewService(store, &recordingProjectFinder{})

	foundFeature, err := service.GetByID(
		t.Context(),
		"prj_test",
		"fea_test",
	)

	if !errors.Is(err, storeErr) {
		t.Fatalf("expected error %v, got %v", storeErr, err)
	}

	if foundFeature != (Feature{}) {
		t.Errorf("expected empty feature, got %+v", foundFeature)
	}
}
