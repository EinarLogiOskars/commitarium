package project

import (
	"errors"
	"testing"
	"time"
)

func TestServiceCreate(t *testing.T) {
	fixedTime := time.Date(2026, time.September, 6, 12, 0, 0, 0, time.UTC)

	service := &Service{
		generateID: func() string {
			return "prj_test"
		},
		now: func() time.Time {
			return fixedTime
		},
	}

	project, err := service.Create("   Commitarium   ")

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if project.ID != "prj_test" {
		t.Errorf("expected ID %q, got %q", "prj_test", project.ID)
	}

	if project.Name != "Commitarium" {
		t.Errorf("expected trimmed name %q, got %q", "Commitarium", project.Name)
	}

	if !project.CreatedAt.Equal(fixedTime) {
		t.Errorf("expected time %v, got %v", fixedTime, project.CreatedAt)
	}
}

func TestServiceCreateRejectsBlankName(t *testing.T) {
	service := NewService()

	_, err := service.Create(" ")

	if !errors.Is(err, ErrNameRequired) {
		t.Fatalf("expected error %v, got %v", ErrNameRequired, err)
	}
}
