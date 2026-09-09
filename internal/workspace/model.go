package workspace

import (
	"errors"
	"regexp"
	"strings"
	"time"
)

type Status string

const (
	StatusPreparing   Status = "preparing"
	StatusBranchReady Status = "branch_ready"
)

var safeCommitID = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

type Workspace struct {
	ID                   string
	ProjectID            string
	FeatureID            string
	RepositoryOwner      string
	RepositoryName       string
	BaseBranch           string
	Branch               string
	BaseCommitID         string
	Status               Status
	BranchCreatedAt      *time.Time
	CheckoutRelativePath string
	CheckoutCreatedAt    *time.Time
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

func (workspace Workspace) Validate() error {
	required := []struct {
		name  string
		value string
	}{
		{"workspace ID", workspace.ID},
		{"project ID", workspace.ProjectID},
		{"feature ID", workspace.FeatureID},
		{"repository owner", workspace.RepositoryOwner},
		{"repository name", workspace.RepositoryName},
		{"base branch", workspace.BaseBranch},
		{"feature branch", workspace.Branch},
	}
	for _, field := range required {
		if strings.TrimSpace(field.value) == "" || field.value != strings.TrimSpace(field.value) {
			return errors.New(field.name + " is required and must be trimmed")
		}
	}
	if !safeCommitID.MatchString(workspace.BaseCommitID) {
		return errors.New("base commit ID must be a lowercase SHA-1 or SHA-256 object ID")
	}
	if workspace.CreatedAt.IsZero() || workspace.UpdatedAt.IsZero() {
		return errors.New("workspace timestamps are required")
	}
	if workspace.UpdatedAt.Before(workspace.CreatedAt) {
		return errors.New("workspace update time cannot precede its creation time")
	}
	switch workspace.Status {
	case StatusPreparing:
		if workspace.BranchCreatedAt != nil {
			return errors.New("preparing workspace cannot have a branch creation time")
		}
		if workspace.CheckoutRelativePath != "" || workspace.CheckoutCreatedAt != nil {
			return errors.New("preparing workspace cannot have a checkout")
		}
	case StatusBranchReady:
		if workspace.BranchCreatedAt == nil || workspace.BranchCreatedAt.IsZero() {
			return errors.New("branch-ready workspace requires a branch creation time")
		}
		if workspace.BranchCreatedAt.Before(workspace.CreatedAt) ||
			workspace.BranchCreatedAt.After(workspace.UpdatedAt) {
			return errors.New("branch creation time must be within the workspace lifetime")
		}
		if (workspace.CheckoutRelativePath == "") != (workspace.CheckoutCreatedAt == nil) {
			return errors.New("checkout path and creation time must be set together")
		}
		if workspace.CheckoutRelativePath != "" {
			if strings.TrimSpace(workspace.CheckoutRelativePath) != workspace.CheckoutRelativePath {
				return errors.New("checkout path must be trimmed")
			}
			if workspace.CheckoutCreatedAt.IsZero() ||
				workspace.CheckoutCreatedAt.Before(*workspace.BranchCreatedAt) ||
				workspace.CheckoutCreatedAt.After(workspace.UpdatedAt) {
				return errors.New("checkout creation time must follow branch creation")
			}
		}
	default:
		return errors.New("workspace status is not recognized")
	}
	return nil
}

func (workspace Workspace) CheckoutReady() bool {
	return workspace.CheckoutRelativePath != "" && workspace.CheckoutCreatedAt != nil
}

type CheckoutSpec struct {
	WorkspaceID     string
	RepositoryOwner string
	RepositoryName  string
	Branch          string
	BaseCommitID    string
	AlreadyReady    bool
}

func (spec CheckoutSpec) Validate() error {
	if strings.TrimSpace(spec.WorkspaceID) == "" || spec.WorkspaceID != strings.TrimSpace(spec.WorkspaceID) {
		return errors.New("workspace ID is required and must be trimmed")
	}
	if strings.TrimSpace(spec.RepositoryOwner) == "" || strings.TrimSpace(spec.RepositoryName) == "" {
		return errors.New("repository identity is required")
	}
	return (Branch{Name: spec.Branch, CommitID: spec.BaseCommitID}).Validate()
}

type Branch struct {
	Name     string
	CommitID string
}

func (branch Branch) Validate() error {
	if strings.TrimSpace(branch.Name) == "" || branch.Name != strings.TrimSpace(branch.Name) {
		return errors.New("branch name is required and must be trimmed")
	}
	if !safeCommitID.MatchString(branch.CommitID) {
		return errors.New("branch commit ID must be a lowercase SHA-1 or SHA-256 object ID")
	}
	return nil
}
