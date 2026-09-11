package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/url"
	"regexp"
	"strings"
	"time"
)

type ImplementationPublicationKind string

const (
	ImplementationPublicationInitial        ImplementationPublicationKind = "implementation"
	ImplementationPublicationReviewResponse ImplementationPublicationKind = "review-response"
	ImplementationPublicationMergeReadiness ImplementationPublicationKind = "merge-readiness"
)

func (kind ImplementationPublicationKind) Validate() error {
	switch kind {
	case ImplementationPublicationInitial,
		ImplementationPublicationReviewResponse,
		ImplementationPublicationMergeReadiness:
		return nil
	default:
		return errors.New("implementation publication kind is not recognized")
	}
}

func (kind ImplementationPublicationKind) CommentHeading() string {
	switch kind {
	case ImplementationPublicationInitial:
		return "Implementation summary"
	case ImplementationPublicationReviewResponse:
		return "Review response"
	case ImplementationPublicationMergeReadiness:
		return "Merge readiness"
	default:
		return ""
	}
}

func (kind ImplementationPublicationKind) Marker(attemptID string) string {
	if kind.Validate() != nil || strings.TrimSpace(attemptID) == "" ||
		attemptID != strings.TrimSpace(attemptID) {
		return ""
	}
	digest := sha256.Sum256([]byte(attemptID))
	return "<!-- commitarium-" + string(kind) + ": " + hex.EncodeToString(digest[:]) + " -->"
}

type Status string

const (
	StatusPreparing   Status = "preparing"
	StatusBranchReady Status = "branch_ready"
)

var safeCommitID = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

type Workspace struct {
	ID                    string
	ProjectID             string
	FeatureID             string
	RepositoryOwner       string
	RepositoryName        string
	BaseBranch            string
	Branch                string
	BaseCommitID          string
	Status                Status
	BranchCreatedAt       *time.Time
	CheckoutRelativePath  string
	CheckoutCreatedAt     *time.Time
	PullRequestNumber     int64
	PullRequestURL        string
	PullRequestRecordedAt *time.Time
	CreatedAt             time.Time
	UpdatedAt             time.Time
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
		if workspace.PullRequestNumber != 0 || workspace.PullRequestURL != "" ||
			workspace.PullRequestRecordedAt != nil {
			return errors.New("preparing workspace cannot have a pull request")
		}
	case StatusBranchReady:
		if workspace.BranchCreatedAt == nil || workspace.BranchCreatedAt.IsZero() {
			return errors.New("branch-ready workspace requires a branch creation time")
		}
		if workspace.BranchCreatedAt.Before(workspace.CreatedAt) ||
			workspace.BranchCreatedAt.After(workspace.UpdatedAt) {
			return errors.New("branch creation time must be within the workspace lifetime")
		}
		pullRequestFieldsSet := 0
		if workspace.PullRequestNumber != 0 {
			pullRequestFieldsSet++
		}
		if workspace.PullRequestURL != "" {
			pullRequestFieldsSet++
		}
		if workspace.PullRequestRecordedAt != nil {
			pullRequestFieldsSet++
		}
		if pullRequestFieldsSet != 0 && pullRequestFieldsSet != 3 {
			return errors.New("pull request number, URL, and recording time must be set together")
		}
		if pullRequestFieldsSet == 3 {
			if !workspace.CheckoutReady() {
				return errors.New("pull request requires a ready checkout")
			}
			if workspace.PullRequestNumber < 1 || !safePullRequestURL(workspace.PullRequestURL) {
				return errors.New("pull request identity is invalid")
			}
			if workspace.PullRequestRecordedAt.IsZero() ||
				workspace.PullRequestRecordedAt.Before(*workspace.CheckoutCreatedAt) ||
				workspace.PullRequestRecordedAt.After(workspace.UpdatedAt) {
				return errors.New("pull request recording time must follow checkout creation")
			}
		}
	default:
		return errors.New("workspace status is not recognized")
	}
	if (workspace.CheckoutRelativePath == "") != (workspace.CheckoutCreatedAt == nil) {
		return errors.New("checkout path and creation time must be set together")
	}
	if workspace.CheckoutRelativePath != "" {
		if strings.TrimSpace(workspace.CheckoutRelativePath) != workspace.CheckoutRelativePath {
			return errors.New("checkout path must be trimmed")
		}
		if workspace.CheckoutCreatedAt.IsZero() ||
			workspace.CheckoutCreatedAt.Before(workspace.CreatedAt) ||
			workspace.CheckoutCreatedAt.After(workspace.UpdatedAt) {
			return errors.New("checkout creation time must be within the workspace lifetime")
		}
	}
	return nil
}

func (workspace Workspace) CheckoutReady() bool {
	return workspace.CheckoutRelativePath != "" && workspace.CheckoutCreatedAt != nil
}

func (workspace Workspace) PullRequestReady() bool {
	return workspace.PullRequestNumber > 0 && workspace.PullRequestURL != "" &&
		workspace.PullRequestRecordedAt != nil
}

type PullRequestSpec struct {
	Title               string
	Body                string
	FeatureMarker       string
	BaseBranch          string
	HeadBranch          string
	InitialHeadCommitID string
	ExistingNumber      int64
}

func (spec PullRequestSpec) Validate() error {
	required := []string{
		spec.Title, spec.Body, spec.FeatureMarker, spec.BaseBranch,
		spec.HeadBranch,
	}
	for _, value := range required {
		if strings.TrimSpace(value) == "" || value != strings.TrimSpace(value) {
			return errors.New("pull request fields are required and must be trimmed")
		}
	}
	if !strings.Contains(spec.Body, spec.FeatureMarker) {
		return errors.New("pull request body must contain its feature marker")
	}
	if !safeCommitID.MatchString(spec.InitialHeadCommitID) {
		return errors.New("pull request initial head must be a lowercase commit ID")
	}
	if spec.ExistingNumber < 0 {
		return errors.New("pull request number cannot be negative")
	}
	return nil
}

type PullRequest struct {
	Number       int64
	URL          string
	Title        string
	Body         string
	State        string
	Draft        bool
	BaseBranch   string
	HeadBranch   string
	HeadCommitID string
	CreatedAt    time.Time
}

// PlanPublicationSpec describes the one curated planning artifact that belongs
// in the managed pull request. PublicationMarker is stable across retries, so
// the remote pull request can prove that this exact submission was applied.
type PlanPublicationSpec struct {
	Number            int64
	FeatureMarker     string
	PublicationMarker string
	Plan              string
	BaseBranch        string
	HeadBranch        string
	HeadCommitID      string
}

// ImplementationPublicationSpec is the read-only proof the coordinator
// expects after a lead agent publishes one implementation turn. The lead owns
// the Git and Forgejo writes; this value only describes what must already be
// visible before review can be routed to another agent.
type ImplementationPublicationSpec struct {
	Number                int64
	FeatureMarker         string
	PlanPublicationMarker string
	Plan                  string
	PublicationKind       ImplementationPublicationKind
	AttemptID             string
	Summary               string
	ExpectedAuthor        string
	BaseBranch            string
	HeadBranch            string
	HeadCommitID          string
}

// ReviewPublicationSpec describes the exact Forgejo review the coordinator
// expects after an independent reviewer has inspected one immutable commit.
type ReviewPublicationSpec struct {
	Number                int64
	ReviewID              int64
	FeatureMarker         string
	PlanPublicationMarker string
	Plan                  string
	PublicationMarker     string
	Summary               string
	ExpectedAuthor        string
	ExpectedState         string
	BaseBranch            string
	HeadBranch            string
	HeadCommitID          string
}

func (spec ReviewPublicationSpec) Validate() error {
	if spec.Number < 1 || spec.ReviewID < 1 {
		return errors.New("review pull request and review IDs are required")
	}
	for _, value := range []string{
		spec.FeatureMarker, spec.PlanPublicationMarker, spec.Plan,
		spec.PublicationMarker, spec.Summary, spec.ExpectedAuthor,
		spec.ExpectedState, spec.BaseBranch, spec.HeadBranch,
	} {
		if strings.TrimSpace(value) == "" || value != strings.TrimSpace(value) {
			return errors.New("review publication fields are required and must be trimmed")
		}
	}
	if spec.ExpectedState != "APPROVED" && spec.ExpectedState != "REQUEST_CHANGES" {
		return errors.New("review publication state is not recognized")
	}
	if !safeCommitID.MatchString(spec.HeadCommitID) {
		return errors.New("reviewed head must be a lowercase commit ID")
	}
	return nil
}

func (spec ImplementationPublicationSpec) Validate() error {
	if spec.Number < 1 {
		return errors.New("implementation pull request number is required")
	}
	for _, value := range []string{
		spec.FeatureMarker, spec.PlanPublicationMarker, spec.Plan,
		spec.AttemptID, spec.Summary, spec.ExpectedAuthor,
		spec.BaseBranch, spec.HeadBranch,
	} {
		if strings.TrimSpace(value) == "" || value != strings.TrimSpace(value) {
			return errors.New("implementation publication fields are required and must be trimmed")
		}
	}
	if err := spec.PublicationKind.Validate(); err != nil {
		return err
	}
	if !safeCommitID.MatchString(spec.HeadCommitID) {
		return errors.New("implementation publication head must be a lowercase commit ID")
	}
	return nil
}

func (spec PlanPublicationSpec) Validate() error {
	if spec.Number < 1 {
		return errors.New("pull request number is required")
	}
	required := []string{
		spec.FeatureMarker, spec.PublicationMarker, spec.Plan,
		spec.BaseBranch, spec.HeadBranch,
	}
	for _, value := range required {
		if strings.TrimSpace(value) == "" || value != strings.TrimSpace(value) {
			return errors.New("plan publication fields are required and must be trimmed")
		}
	}
	if !safeCommitID.MatchString(spec.HeadCommitID) {
		return errors.New("plan publication head must be a lowercase commit ID")
	}
	return nil
}

func (pullRequest PullRequest) Validate() error {
	if pullRequest.Number < 1 || !safePullRequestURL(pullRequest.URL) {
		return errors.New("pull request identity is invalid")
	}
	if strings.TrimSpace(pullRequest.Title) == "" ||
		pullRequest.Title != strings.TrimSpace(pullRequest.Title) {
		return errors.New("pull request title is required and must be trimmed")
	}
	if pullRequest.State != "open" && pullRequest.State != "closed" {
		return errors.New("pull request state is invalid")
	}
	if strings.TrimSpace(pullRequest.BaseBranch) == "" ||
		strings.TrimSpace(pullRequest.HeadBranch) == "" {
		return errors.New("pull request branches are required")
	}
	if !safeCommitID.MatchString(pullRequest.HeadCommitID) {
		return errors.New("pull request head must be a lowercase commit ID")
	}
	if pullRequest.CreatedAt.IsZero() {
		return errors.New("pull request creation time is required")
	}
	return nil
}

func safePullRequestURL(value string) bool {
	if strings.TrimSpace(value) == "" || value != strings.TrimSpace(value) {
		return false
	}
	parsed, err := url.Parse(value)
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") &&
		parsed.Host != "" && parsed.User == nil
}

type CheckoutSpec struct {
	WorkspaceID          string
	RepositoryOwner      string
	RepositoryName       string
	Branch               string
	BaseCommitID         string
	ExpectedHeadCommitID string
	AlreadyReady         bool
	RequireCleanBaseline bool
	RequireClean         bool
}

// CheckoutPromotionSpec identifies one pre-branch checkout and the feature
// branch it must enter without changing its pinned repository or base commit.
type CheckoutPromotionSpec struct {
	WorkspaceID     string
	RepositoryOwner string
	RepositoryName  string
	BaseBranch      string
	FeatureBranch   string
	BaseCommitID    string
}

func (spec CheckoutPromotionSpec) Validate() error {
	baseline := CheckoutSpec{
		WorkspaceID: spec.WorkspaceID, RepositoryOwner: spec.RepositoryOwner,
		RepositoryName: spec.RepositoryName, Branch: spec.BaseBranch,
		BaseCommitID: spec.BaseCommitID,
	}
	if err := baseline.Validate(); err != nil {
		return err
	}
	return (Branch{Name: spec.FeatureBranch, CommitID: spec.BaseCommitID}).Validate()
}

func (spec CheckoutSpec) Validate() error {
	if strings.TrimSpace(spec.WorkspaceID) == "" || spec.WorkspaceID != strings.TrimSpace(spec.WorkspaceID) {
		return errors.New("workspace ID is required and must be trimmed")
	}
	if strings.TrimSpace(spec.RepositoryOwner) == "" || strings.TrimSpace(spec.RepositoryName) == "" {
		return errors.New("repository identity is required")
	}
	if spec.RequireCleanBaseline && !spec.AlreadyReady {
		return errors.New("a clean baseline check requires an existing checkout")
	}
	if (spec.ExpectedHeadCommitID != "" || spec.RequireClean) && !spec.AlreadyReady {
		return errors.New("head and clean checks require an existing checkout")
	}
	if spec.ExpectedHeadCommitID != "" && !safeCommitID.MatchString(spec.ExpectedHeadCommitID) {
		return errors.New("expected checkout head must be a lowercase commit ID")
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
