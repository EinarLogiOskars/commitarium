package workerservice

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
)

func TestImmutableEnvironmentResolverReturnsExactDefensiveSnapshot(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	assignment := environmentTestAssignment()
	variables := []string{
		"PATH=/usr/bin:/bin",
		"CODEX_HOME=/var/lib/commitarium/provider",
	}
	resolver, err := NewImmutableEnvironmentResolver(ImmutableEnvironmentResolverConfig{
		AgentProfileID: assignment.AgentProfileID,
		WorkspaceRoot:  root,
		Materializations: []MaterializedEnvironment{{
			Assignment: assignment, WorkingDirectory: workspace, Variables: variables,
		}},
	})
	if err != nil {
		t.Fatalf("create immutable resolver: %v", err)
	}

	variables[0] = "PATH=/mutated-before-resolve"
	resolved, err := resolver.Resolve(t.Context(), assignment)
	if err != nil {
		t.Fatalf("resolve launch environment: %v", err)
	}
	if !launchEnvironmentMatches(resolved, assignment) ||
		resolved.Variables[0] != "PATH=/usr/bin:/bin" {
		t.Fatalf("resolved environment = %+v", resolved)
	}
	canonicalWorkspace, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		t.Fatalf("resolve expected workspace: %v", err)
	}
	if resolved.WorkingDirectory != canonicalWorkspace {
		t.Fatalf("working directory = %q, want %q", resolved.WorkingDirectory, canonicalWorkspace)
	}

	resolved.Variables[0] = "PATH=/mutated-after-resolve"
	again, err := resolver.Resolve(t.Context(), assignment)
	if err != nil || again.Variables[0] != "PATH=/usr/bin:/bin" {
		t.Fatalf("second resolved environment = %+v, error=%v", again, err)
	}
}

func TestImmutableEnvironmentResolverRejectsUnavailableOrChangedAssignments(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	assignment := environmentTestAssignment()
	resolver, err := NewImmutableEnvironmentResolver(ImmutableEnvironmentResolverConfig{
		AgentProfileID: assignment.AgentProfileID,
		WorkspaceRoot:  root,
		Materializations: []MaterializedEnvironment{{
			Assignment: assignment, WorkingDirectory: workspace,
			Variables: []string{"PATH=/usr/bin:/bin"},
		}},
	})
	if err != nil {
		t.Fatalf("create immutable resolver: %v", err)
	}

	tests := []struct {
		name       string
		assignment workerhttp.Assignment
		want       error
	}{
		{name: "different profile", assignment: withAssignmentProfile(assignment, "profile_other"), want: ErrProfileUnavailable},
		{name: "unknown workspace", assignment: withAssignmentWorkspace(assignment, "workspace_other"), want: ErrWorkspaceUnavailable},
		{name: "changed revision", assignment: withAssignmentRevision(assignment, 2), want: ErrConfigurationMismatch},
		{name: "changed digest", assignment: withAssignmentDigest(assignment, "sha256:"+strings.Repeat("b", 64)), want: ErrConfigurationMismatch},
		{name: "changed project", assignment: withAssignmentProject(assignment, "prj_other"), want: ErrConfigurationMismatch},
		{name: "changed feature", assignment: withAssignmentFeature(assignment, "fea_other"), want: ErrConfigurationMismatch},
		{name: "changed role", assignment: withAssignmentRole(assignment, workerhttp.RoleReviewer), want: ErrConfigurationMismatch},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := resolver.Resolve(t.Context(), test.assignment); !errors.Is(err, test.want) {
				t.Fatalf("resolve error = %v, want %v", err, test.want)
			}
		})
	}

	if err := os.Remove(workspace); err != nil {
		t.Fatalf("remove workspace: %v", err)
	}
	if _, err := resolver.Resolve(t.Context(), assignment); !errors.Is(err, ErrWorkspaceUnavailable) {
		t.Fatalf("missing directory error = %v, want ErrWorkspaceUnavailable", err)
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := resolver.Resolve(cancelled, assignment); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled resolve error = %v", err)
	}
}

func TestImmutableEnvironmentResolverRejectsUnsafeMaterializations(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	assignment := environmentTestAssignment()
	tests := []ImmutableEnvironmentResolverConfig{
		{},
		{
			AgentProfileID: assignment.AgentProfileID, WorkspaceRoot: root,
			Materializations: []MaterializedEnvironment{{
				Assignment: assignment, WorkingDirectory: outside,
			}},
		},
		{
			AgentProfileID: assignment.AgentProfileID, WorkspaceRoot: root,
			Materializations: []MaterializedEnvironment{{
				Assignment: assignment, WorkingDirectory: root,
				Variables: []string{"PATH=/one", "PATH=/two"},
			}},
		},
	}
	for index, config := range tests {
		if _, err := NewImmutableEnvironmentResolver(config); !errors.Is(err, ErrInvalidEnvironmentResolver) {
			t.Errorf("case %d error = %v, want ErrInvalidEnvironmentResolver", index, err)
		}
	}
}

func environmentTestAssignment() workerhttp.Assignment {
	return workerhttp.Assignment{
		AgentProfileID: "profile_test", ProjectID: "prj_test", FeatureID: "fea_test",
		Role: workerhttp.RoleCoder, WorkspaceID: "workspace_test", ConfigurationRevision: 1,
		MaterializationDigest: "sha256:" + strings.Repeat("a", 64),
	}
}

func withAssignmentProfile(assignment workerhttp.Assignment, profileID string) workerhttp.Assignment {
	assignment.AgentProfileID = profileID
	return assignment
}

func withAssignmentWorkspace(assignment workerhttp.Assignment, workspaceID string) workerhttp.Assignment {
	assignment.WorkspaceID = workspaceID
	return assignment
}

func withAssignmentRevision(assignment workerhttp.Assignment, revision int64) workerhttp.Assignment {
	assignment.ConfigurationRevision = revision
	return assignment
}

func withAssignmentDigest(assignment workerhttp.Assignment, digest string) workerhttp.Assignment {
	assignment.MaterializationDigest = digest
	return assignment
}

func withAssignmentProject(assignment workerhttp.Assignment, projectID string) workerhttp.Assignment {
	assignment.ProjectID = projectID
	return assignment
}

func withAssignmentFeature(assignment workerhttp.Assignment, featureID string) workerhttp.Assignment {
	assignment.FeatureID = featureID
	return assignment
}

func withAssignmentRole(assignment workerhttp.Assignment, role workerhttp.Role) workerhttp.Assignment {
	assignment.Role = role
	return assignment
}
